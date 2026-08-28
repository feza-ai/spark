# ADR 013: Utilization-Aware CPU Admission

## Status
Accepted

## Date
2026-08-27

## Context

Admission is purely accounting-based (ADR 006): a pod is scheduled if its
requested resources fit within `allocatable - allocated`. There is no
distinction between a resource being *reserved* and a resource being
*used*. In practice this let idle reservations block real work as
effectively as busy ones.

Live incident (issue #76): the DGX's 20-core node reported
`cpuMillis: {allocated: 17150, allocatable: 18000}` (95%) while real load
average stayed under 1.0 (of 20 cores) the whole time -- roughly 20 GitHub
Actions CI runner pods, all `priority: 1000`, mostly idle between jobs. A
`cpu: 6` CI pod could not schedule and stayed Pending for ~2 days; the only
workaround was understating its request to fit the phantom ceiling.
Because admission is priority-blind to actual load, same-priority pods
never became preemption candidates either -- there was no fallback once
the accounting ceiling was hit.

GPU memory exhaustion and CPU contention are not equivalent failure modes
on this hardware. A GPU OOM kills the process outright. CPU contention
under CFS quotas degrades throughput but is recoverable -- the kernel
scheduler still time-slices fairly. This asymmetry is why this ADR treats
CPU differently from every other resource dimension.

## Decision

Admission consults live host load, not just accounting, but only for CPU:

1. **`ResourceTracker.CanFitIgnoringCPU` / `AllocateOverCommittingCPU`**
   (`internal/scheduler/resources.go`). The existing `CanFit`/`Allocate`
   logic is factored to take a `skipCPU` flag; every other dimension --
   memory, GPU count, GPU memory, and the cpuset whole-core-block check
   (ADR 012) -- is checked exactly as strictly as today. The cpuset check
   is never bypassed even by the CPU-ignoring path: a pinned core is a
   physical exclusivity guarantee, not an accounting fiction, so it isn't
   "soft" the way plain CPU-millis accounting is.
2. **`Scheduler.HostLoadSource` interface + `SetHostLoad`**
   (`internal/scheduler/scheduler.go`). `Schedule` tries the existing
   accounted `CanFit` first; if that fails, and `CanFitIgnoringCPU`
   succeeds, and a `HostLoadSource` is set and reports enough real
   headroom, the pod is admitted directly -- before priority-based
   preemption is even considered. `nil` (the default) disables the
   feature entirely, preserving today's behavior exactly.
3. **`cmd/spark/hostload.go`** implements `HostLoadSource` by sampling
   `/proc/loadavg`'s 5-minute average on a ticker (default 15s, via
   `--host-load-sample-interval`) and converting it to free CPU millicores
   against `sysInfo.CPUMillis`, minus a safety margin (default 1000m / one
   core, via `--cpu-overcommit-margin-millis`). The same sample also feeds
   `spark_host_loadavg` (ADR 010's dead wiring, now active).
4. **Accounted CPU is allowed to go negative** once overcommitted --
   `Available().CPUMillis` reflects the real state honestly rather than
   clamping at zero or hiding it. This is intentional: once a node is
   consciously overcommitting CPU, the accounted ceiling stops being a
   meaningful admission signal for CPU and the utilization check becomes
   primary for that dimension. Visibility, not correction, is the goal --
   see point 5.
5. **Visibility**: `GET /api/v1/pods/{name}` now returns `requested`
   (cpuMillis/memoryMB/gpuCount/gpuMemoryMB) so an operator investigating
   a pending pod can see what it asked for without cross-referencing the
   manifest. `spark_cpu_overcommit_admissions_total` counts how often the
   bypass engages. A `CPUOvercommitAdmitted` pod event records the reason
   each time it does.

### Load window and margin: not prescribed by the issue

Neither the issue nor any prior ADR specifies a formula for converting
"real load" into an admission decision. The choices here are a considered
default, not an authoritative answer:

- **5-minute load average**, not 1-minute or 15-minute: 1-minute is noisy
  enough to flap admission on a brief spike (spinning up preemption
  candidates and back down); 15-minute reacts too slowly to genuine
  sustained idle -- the exact case this ADR exists to fix.
- **1-core (1000m) safety margin**: covers the trailing average's inherent
  lag (load can rise between samples faster than a 5-minute average
  reflects it).

Both are operator-tunable flags, not hardcoded, specifically because they
are a judgment call rather than something the issue or prior design
dictated. Revisit if production data shows the bypass engaging on load
spikes it shouldn't, or failing to engage on sustained idle it should.

## Consequences

**Positive:**

- The issue #76 incident (idle-but-reserved CPU blocking real work
  indefinitely) cannot recur: a pod that fits real headroom is admitted
  even when accounting says no.
- Memory accounting is untouched -- `AllocateOverCommittingCPU` still
  rejects on memory exactly like `Allocate`. GPU accounting (count and
  memory) is likewise untouched; the cpuset core-block check (a physical
  exclusivity guarantee) is enforced identically in both paths.
- Operators get direct observability into both the gap
  (`GET /api/v1/pods/{name}` `requested` vs `GET /api/v1/resources`
  `allocated`/`available`) and the mechanism firing
  (`spark_cpu_overcommit_admissions_total`, `CPUOvercommitAdmitted`
  events).
- `nil` `HostLoadSource` is a complete kill switch: every existing
  scheduler test that doesn't call `SetHostLoad` behaves identically to
  before this ADR.

**Negative:**

- CPU overcommit means a pod admitted via this path can experience worse
  CPU throughput than its request implies if load rises between the last
  sample and the next. This is the accepted trade for CPU specifically
  (soft, recoverable) and explicitly not extended to memory or GPU.
- The load-average window and safety margin are a judgment call, not a
  value derived from the issue or measured in production yet. They may
  need tuning once real overcommit-admission data exists.
- `/proc/loadavg` is Linux-only; on a host where it's unavailable (e.g. a
  macOS dev environment), sampling fails silently (logged at Debug) and
  `HostLoadSource.AvailableCPUMillis` reports `ok=false` forever --
  utilization-aware admission simply never engages, falling back to pure
  accounting. This is intentional degradation, not a crash.
- `SavePod`'s pre-existing `INSERT OR REPLACE` was found, while
  implementing this ADR's event-visibility piece, to silently wipe a
  pod's entire persisted event history via FK cascade on every status
  change. Fixed alongside this ADR (a true `ON CONFLICT DO UPDATE`
  upsert) since it directly undermined the diagnostic visibility this ADR
  adds -- see docs/devlog.md 2026-08-27 for the full mechanism.

## Non-goals

- GPU device-slot accounting changes and the DELETE-path reservation-release
  bug are tracked separately (issue #81) and untouched here.
- A configurable flat CPU-overcommit factor (schedule against
  `requests * N`) was considered and explicitly rejected in favor of this
  utilization-aware approach, per the issue author's stated preference.
- Retaining admission-failure events past a pod's normal TTL once it
  reaches a terminal state is not addressed here: `Prune`/`PruneBefore`
  never touch Pending pods in the first place (only completed/failed pods
  past their TTL), so a still-pending pod's events were never subject to
  TTL pruning -- the loss mechanism was the `OnEvent`/`SavePod` bugs fixed
  alongside this ADR, not TTL policy.
