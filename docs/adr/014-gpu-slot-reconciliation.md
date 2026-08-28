# ADR 014: GPU Device-Slot Reconciliation

## Status
Accepted

## Date
2026-08-27

## Context

Issue #81: on the DGX (`gpu-max 1`), the scheduler reported 0 GPUs free
while the device was physically idle and no live pod held it. The host
was stuck in this state for 9 days -- a total GPU-scheduling outage,
since a single-GPU host has no other slot to fall back to.

Tracing the accounting paths (`internal/scheduler/resources.go`,
`internal/executor/podman.go`, `internal/reconciler/reconciler.go`,
`internal/api/pods_mutate.go`, `internal/bus/handler_delete.go`) found
three independent gaps that combine to produce and then permanently seal
in a leaked slot:

1. **`ResourceTracker.Allocate` had no self-heal for a GPU-to-GPU-less
   transition.** A GPU-count slot is reserved by writing
   `gpuAssignments[name]`. CPU/memory/GPU-count all live in one
   `allocations[name]` entry, replaced wholesale on every `Allocate`
   call for that name. But the GPU-device-slot map is a *separate*
   structure only ever written when the new request's `GPUCount`/
   `GPUMemoryMB` is greater than zero -- a request with neither present
   left a pre-existing `gpuAssignments[name]` entry completely
   untouched. A pod re-applied under the same name with its GPU request
   dropped therefore kept the previous incarnation's device slot
   forever, even though its CPU/memory accounting correctly reflected
   the new (GPU-less) request. This is the exact shape of the reported
   symptom: memory accounting free, GPU accounting stuck.

2. **The scheduler and the executor read different fields to decide
   "does this pod need a GPU".** Admission and accounting
   (`Schedule`/`Allocate`) sum `Requests.GPUCount`/`GPUMemoryMB`
   (`PodSpec.TotalRequests`). Device attachment
   (`buildRunArgs` in the executor) gated `--device nvidia.com/gpu=all`
   on `Limits.GPUCount`/`GPUMemoryMB` only. A manifest that sets
   `resources.requests.nvidia.com/gpu` without a `limits` block --  a
   valid, ordinary shape -- was admitted and billed for a device slot
   that the container never received. Separately, `PodSpec.GPUDevices`
   (documented as "runtime: assigned GPU device IDs, set by scheduler")
   was never actually populated by the reconciler, so
   `NVIDIA_VISIBLE_DEVICES` was dead code -- the executor had no way to
   learn which specific device the scheduler had in mind.

3. **The pod-delete paths (`DELETE /api/v1/pods/{name}` and the
   `req.spark.delete` NATS handler) release scheduler state and drop the
   store record together, but only when `executor.RemovePod` returns
   nil or a recognized "no such pod" error.** podman can report a
   different, unrecognized error (e.g. a network-cleanup warning) after
   having *already removed the pod*. Both handlers took that at face
   value and returned 500 with the store record and scheduler
   reservation left intact -- for a pod that no longer existed and,
   because nothing revisits a fully-processed delete request, would
   never be revisited again.

Any one of gap 1 or 3 alone can seal a leaked slot in permanently; gap 2
is what let a slot get reserved for a pod that was never actually using
a device in the first place, raising the odds of hitting 1 or 3 in
practice.

## Decision

Fix all three gaps, plus add a periodic safety net that does not depend
on correctly diagnosing every future variant of this class of bug:

1. **`Allocate` clears a stale `gpuAssignments[name]` when the new
   request needs no GPU.** Symmetric with how CPU/memory/GPU-count are
   already replaced wholesale on every call.

2. **Device attachment now checks `Requests` as well as `Limits`**, and
   the reconciler populates `PodSpec.GPUDevices` from
   `Scheduler.Tracker().AssignedGPUs(name)` at both `Schedule()` call
   sites (initial schedule and post-preemption retry), before
   `CreatePod`. The executor and scheduler now agree on which field
   decides "this pod needs a device", and the specific assigned device
   ID reaches the container via `NVIDIA_VISIBLE_DEVICES`.

3. **Both delete paths confirm actual pod state via a fresh
   `PodStatus` call before treating an unrecognized `RemovePod` error as
   fatal.** Only a confirmed "no such pod" response lets the delete
   proceed to release scheduler resources and drop the store record;
   any other outcome (the pod still reports a status, or the status
   check itself fails) keeps the existing conservative behavior --
   record and reservation intact, 500 returned. This avoids the
   alternative of unconditionally releasing on any `RemovePod` error,
   which would risk double-allocating resources still held by a pod
   that is, in fact, still alive.

4. **The housekeeper gains a fourth periodic responsibility**:
   `ReconcileGPUSlots` compares `ResourceTracker.GPUHolders()` (pod name
   -> assigned device IDs) against the Spark store's full pod list, and
   calls the new `ResourceTracker.ReleaseGPU(name)` for any holder name
   with no store record at all. `ReleaseGPU` is deliberately narrower
   than `RemovePod`/`Release`: it clears only the GPU device-slot
   assignment for that name, never CPU/memory/cores, so it cannot
   disturb accounting for a different, legitimately-live pod that
   happens to reuse the name. This is the backstop for any leak whose
   exact trigger isn't one of the three fixed above -- including one
   caused by an operator bypassing Spark's API entirely (e.g. a manual
   `podman pod rm`), which no amount of in-process request handling can
   fully anticipate.

5. **Slot ownership is now visible**: `GET /api/v1/node` gains
   `gpu_allocations: [{device, pod}, ...]`, sourced from the same
   `GPUHolders()` snapshot. A `spark_gpu_slots_reclaimed_total` counter
   on `/metrics` tracks how often the housekeeper backstop actually
   fires -- ideally always zero, but non-zero is now something an
   operator can see instead of days of `nvidia-smi`/`podman inspect`
   cross-referencing.

## Consequences

- Positive: a leaked GPU device slot is now caught and released
  automatically within one housekeeping interval, regardless of which
  code path caused it.
- Positive: scheduler admission and executor device attachment are
  driven by the same field (`Requests`), closing the class of bug where
  a pod holds a reservation for a device it never received.
- Positive: slot ownership is observable via `/api/v1/node` and
  `/metrics`, cutting the diagnosis time for a repeat of issue #81 from
  days to a single API call.
- Negative: `ReleaseGPU`'s narrow scope (GPU-only, name-matching) means
  it cannot repair a *live* pod whose GPU assignment was corrupted while
  its CPU/memory allocation stayed intact under the same name -- by
  design, since a broader release would risk clobbering a live pod's
  real accounting. Any such case is still visible via
  `spark_gpu_slots_reclaimed_total` staying at zero while
  `gpu_allocations` disagrees with `podman inspect`, which is now
  directly checkable instead of hidden.
- Negative: the executor still attaches the device via the blunt
  `--device nvidia.com/gpu=all` CDI selector rather than a per-device
  reference, even though `spec.GPUDevices` is now correctly populated
  with the specific assigned IDs. Precise per-device CDI attachment
  (needed for real multi-GPU isolation) is deferred -- this host has one
  GPU, so `=all` and "the one assigned device" are equivalent in
  practice today, but this remains a gap on a future multi-GPU host and
  is tracked as follow-up work, not fixed here.
