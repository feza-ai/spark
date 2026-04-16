# ADR 012: CPU Pinning via cpuset Partitioning

## Status
Accepted

## Date
2026-04-15

## Context

Spark passes manifest CPU limits to podman via `--cpus=N`, which is a CFS
quota: the container receives `N` CPU-seconds per wall-second of cumulative
runtime, distributed across whichever cores the kernel scheduler chooses.
It is not a CPU set restriction.

This caused a production incident on the DGX (issue #22, 2026-04-15): a
GPU training pod with `limits.cpu: "6"` on a 20-core host saturated all 20
cores under sustained load, starving `ksoftirqd` and `sshd` and producing
40+ minutes of 100% packet loss. CFS quotas alone cannot reserve specific
cores for the host's network IRQs and management daemons.

The existing `--system-reserve-cpu` flag reduces the scheduler's
allocatable millicores, but the reserve is never translated into any
container-side isolation. It is an accounting number only.

## Decision

Spark introduces explicit core-set partitioning in addition to the
existing CFS quota:

1. **Scheduler tracks core IDs**, not just millicores. `Resources` gains a
   `Cores []int` set; `ResourceTracker` gains a `coreAssignments` map
   that mirrors the existing `gpuAssignments` pattern. At startup the
   tracker computes `allocatableCores = host_cores \ system_reserve_cores`.
2. **System-reserved cores are excluded up-front** via a new
   `--system-reserve-cores` flag (default `0-1` on hosts with >= 4 cores,
   empty otherwise). Pods are never scheduled on these cores. The host's
   ksoftirqd, sshd, podman daemon, and Spark itself always retain budget.
3. **Pod admission allocates a contiguous core range.** When `limits.cpu`
   is an integer >= 1, the scheduler reserves that many unassigned cores
   from the allocatable set and records the assignment.
4. **Executor passes `--cpuset-cpus=<range>`** to podman alongside the
   existing `--cpus` flag. The cgroup v2 `cpuset.cpus` controller pins
   container threads to the assigned subset.
5. **Pods exceeding allocatable cores are rejected at admission**,
   not silently scheduled and then thrashed.

GPU-only pods that do not declare an explicit `cpu` limit still receive
a default cpuset covering the allocatable range minus the reserve, so an
unbounded Go/Python runtime co-resident with CUDA cannot regress this
pattern.

## Consequences

**Positive:**

- Network IRQs and SSH daemon retain dedicated cores under any pod load.
- Saturation incidents like issue #22 stop being a class of failure.
- Operators get a deterministic core map from `GET /api/v1/node` and can
  reason about which workloads share which cores.
- The model composes with existing GPU device-slot tracking; both use the
  same per-pod assignment pattern.

**Negative:**

- Per-pod CPU capacity drops from "any 6 cores via CFS quota" to "exactly
  these 6 cores." A workload that briefly bursts above its quota onto
  idle cores can no longer do so. This is a deliberate trade -- isolation
  beats bursting on a single-node orchestrator.
- The scheduler must reject a pod that fits in millicores but not in
  contiguous cores. This adds a new failure mode operators must understand.
- Linux-only: `cpuset.cpus` requires cgroup v2 with the `cpuset`
  controller enabled. macOS dev environments cannot exercise the pin.
  Tests use the stub executor; CI on Linux verifies real podman behaviour.
- Pre-existing `--system-reserve-cpu` flag continues to exist for
  backward compatibility but its value is now derived: when
  `--system-reserve-cores` is set, `system-reserve-cpu` is forced to
  `len(reserve_cores) * 1000`.
