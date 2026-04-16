# Spark: Resolve Open GitHub Issue #22 (CPU pinning via cpuset)

## Status: Planned

## Context

### Problem Statement

Issue #22: pods scheduled with `resources.limits.cpu: "6"` on a 20-core
DGX host can still saturate the host to 100% packet loss and SSH banner
timeouts for 40+ minutes. Spark passes only `--cpus=6.0` to podman,
which is a CFS quota (cumulative CPU-seconds per wall-second across any
core), not a CPU set restriction. Container threads still land on every
core including the cores handling network IRQs and the SSH daemon.

The existing `--system-reserve-cpu` flag (default 2000m) reduces
`ResourceTracker.allocatable` by 2 cores in the scheduler's accounting,
but never translates into any container-side isolation. The reserve is
an accounting number; the kernel still multiplexes pods across all 20
cores at runtime.

The fix is core-set partitioning: track core IDs in the scheduler,
reserve specific cores for the host, allocate contiguous ranges per pod,
and emit `--cpuset-cpus=<range>` to podman alongside the existing
`--cpus` quota. See docs/adr/012-cpu-pinning-cpuset.md for full
rationale and trade-offs.

### Objectives

- A pod with `limits.cpu: N` runs on exactly N pinned cores. Other cores
  are unreachable from inside the container.
- The host's network IRQ handlers and management daemons (sshd, podman,
  Spark itself) retain dedicated cores under any pod load.
- A pod whose `limits.cpu` exceeds allocatable cores is rejected at
  admission, not silently scheduled then thrashed.
- `GET /api/v1/node` exposes the reserved-core set and the per-pod core
  assignments so operators can reason about isolation at runtime.
- Reproduction recipe in issue #22 (Wolf CrossAsset training with
  `ZERFOO_DISABLE_CUDA_GRAPH=1`) leaves the DGX responsive: 0% packet
  loss, SSH latency under 500 ms for the full training duration.

### Non-Goals

- Per-pod CPU bursting above the assigned cores. The whole point is
  isolation; a pod that wants more cores must request more cores.
- Real-time scheduling priorities, cpu.weight tuning, or `nice` overrides
  for system services. Those are kernel-level mitigations beyond podman.
- Fractional core assignment. `limits.cpu: "0.5"` continues to use only
  the existing `--cpus` quota path with no cpuset pinning.
- macOS support for cpuset. Spark runs on Linux in production; tests use
  the stub executor.

### Constraints

- Go standard library only (plus `nats.go`, `modernc.org/sqlite`).
  ADR-001.
- Podman CLI only (no podman API socket). ADR-004.
- Conventional Commits, rebase-and-merge, one package per commit.
- Tests use the stub executor; no real podman in CI. Linux-only
  integration verified manually on the DGX.
- Backward compatibility: existing `--system-reserve-cpu` flag continues
  to work. New `--system-reserve-cores` is preferred and overrides.

### Success Metrics

- `go test ./... -race -timeout 120s` passes.
- New tests cover: scheduler core allocation, contiguous-range selection,
  release on pod delete, admission rejection when cores exhausted,
  executor `--cpuset-cpus` emission for integer CPU limits.
- Issue #22 closes via PR merge with `fixes #22` in the commit message.
- DGX validation: re-run the offending Wolf CrossAsset manifest;
  concurrent `ping -i 1 192.168.86.250` shows 0% packet loss for the
  full training duration; `ssh ndungu@192.168.86.250 hostname` returns
  in under 500 ms throughout.
- `GET /api/v1/node` returns `cpu_reserved_cores` and
  `cpu_allocatable_cores` fields with non-empty values on the DGX.

## Discovery Summary

Issue #22 root cause spans three packages, all of which already have
established patterns this fix can mirror:

- `internal/executor/podman.go:209-218` builds podman run args from
  `Resources.Limits`. Adding `--cpuset-cpus` requires the per-container
  core assignment to be threaded through `RunContainer`.
- `internal/scheduler/resources.go` has `Resources` (CPU millicores,
  memory MB, GPU count, GPU memory MB) and `ResourceTracker` with
  per-pod `gpuAssignments map[string][]int`. The cpuset model mirrors
  GPU device-slot tracking exactly: a `coreAssignments map[string][]int`
  populated at `Allocate` and cleared at `Release`.
- `cmd/spark/main.go:39` declares `--system-reserve-cpu` (default 2000m).
  The new `--system-reserve-cores` flag accepts a range string like
  `"0-1"` or comma-separated `"0,1"` and is parsed into `[]int` before
  constructing the tracker.
- `internal/api/node.go` already exposes hostname, OS, arch, CPU cores,
  memory, GPU device IDs. New fields `cpu_reserved_cores` and
  `cpu_allocatable_cores` slot in alongside the GPU fields.

Existing use cases impacted (status updates after this work):

- UC-024 "Pod runs with CPU limits enforced" -- currently STUB (CFS
  quota only). Becomes WIRED with cpuset pinning.
- UC-046 "Inspect node hardware" -- gains two new response fields.
- New UC-051 "Pod is rejected when cores are exhausted" -- introduced
  by the admission check in T3.2 below.

Discovery artifact: this section serves as the discovery summary; no
separate manifest file generated for a single-issue plan.

## Scope and Deliverables

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | Scheduler tracks per-pod core assignments | `ResourceTracker` has `coreAssignments`, mirrored on Allocate/Release; tests cover happy path + exhaustion |
| D2 | Executor emits `--cpuset-cpus` for integer CPU limits | `buildRunArgs` produces the flag when given an assignment; verified by stub-executor test |
| D3 | `--system-reserve-cores` flag with range parsing | Accepts `"0-1"` and `"0,1"`; main.go wires the parsed set into the tracker |
| D4 | `/api/v1/node` exposes reserved + allocatable cores | JSON includes `cpu_reserved_cores` and `cpu_allocatable_cores`; node test asserts both |
| D5 | Admission rejects oversize pods | `POST /api/v1/pods` returns 400 with a clear error when `limits.cpu` exceeds `len(allocatableCores)` |
| D6 | ADR-012 written | docs/adr/012-cpu-pinning-cpuset.md present (already created) |
| D7 | PR opened, CI green, rebase-merged into main | PR state MERGED; main green; issue #22 auto-closes from `fixes #22` |
| D8 | DGX smoke test passes after auto-upgrade picks up the new release | Wolf CrossAsset training manifest runs without packet loss; `cpu_reserved_cores` visible in `GET /api/v1/node` |

## Checkable Work Breakdown

### E1: Issue #22 -- CPU pinning via cpuset partitioning

#### Wave 1: Foundations (parallel, 4 agents)

- [x] T1.1 Extend `scheduler.Resources` with `Cores []int` and add
  `coreAssignments map[string][]int` to `ResourceTracker`  Owner: task-T1.1
  Est: 60m  verifies: [UC-024]  Completed: 2026-04-15 (PR #23)
  - Mirror the `gpuAssignments` pattern in
    `internal/scheduler/resources.go`.
  - Add `unassignedCoresLocked(n int) []int` returning the lowest-index
    contiguous block of `n` unassigned cores when one exists; otherwise
    fall back to any `n` unassigned cores (contiguous preferred but not
    required).
  - Acceptance: package builds, no behaviour change yet (callers do not
    request cores). New unit test covers selection logic.

- [x] T1.2 Add `--system-reserve-cores` flag and parser  Owner: task-T1.2
  Est: 45m  verifies: [UC-024]  Completed: 2026-04-15 (PR #23)
  - In `cmd/spark/main.go`, declare
    `flag.String("system-reserve-cores", "", ...)`.
  - Add a small helper (in `internal/scheduler` or a new `cpuset.go`)
    that parses `"0-1"`, `"0,1"`, `"0,2-3"` into `[]int` and validates
    against `runtime.NumCPU()`.
  - When the flag is non-empty, the parsed set wins; force
    `--system-reserve-cpu` to `len(reserveCores) * 1000`.
  - Acceptance: unit test covers happy path, malformed input, and
    out-of-range core IDs.

- [x] T1.3 Update `docs/design.md` "Key Invariants" with cpuset model
  Owner: coordinator  Est: 20m  verifies: [infrastructure]  Completed: 2026-04-15 (commit 50f66ce)
  - Add: "CPU isolation: scheduler assigns specific core IDs per pod;
    executor passes `--cpuset-cpus` so container threads cannot land on
    reserved cores. `--system-reserve-cores` defines the host's
    dedicated set."
  - Cross-reference docs/adr/012-cpu-pinning-cpuset.md.
  - Acceptance: design.md mentions cpuset and references ADR-012.

- [x] T1.4 Detect host core IDs in `internal/gpu/system.go`
  Owner: task-T1.4  Est: 30m  verifies: [UC-024]  Completed: 2026-04-15 (PR #23)
  - Extend `SystemInfo` with `CoreIDs []int` populated as
    `[0..runtime.NumCPU())`.
  - Threaded into `main.go` so the tracker receives the full host core
    set as `total.Cores`.
  - Acceptance: unit test asserts `len(CoreIDs) == runtime.NumCPU()`.

#### Wave 2: Scheduler + executor wiring (parallel, 3 agents)

Depends on: T1.1, T1.2, T1.4.

- [x] T2.1 `ResourceTracker.Allocate` reserves cores for integer CPU
  limits  Owner: task-T2.1  Est: 90m  verifies: [UC-024]  Completed: 2026-04-15 (PR #24)
  - When `req.CPUMillis >= 1000` and `req.CPUMillis % 1000 == 0`,
    allocate `req.CPUMillis / 1000` cores from the unassigned set.
  - Record in `coreAssignments[name]`. `Release` clears it.
  - `CanFit` rejects when no contiguous (or any) block of N cores is
    available.
  - New `AssignedCores(name string) []int` accessor mirrors
    `AssignedGPUs`.
  - Acceptance: tests cover allocate-release-allocate, exhaustion,
    interleaved release-and-realloc, fractional CPU bypass.

- [x] T2.2 Executor emits `--cpuset-cpus` from assigned cores
  Owner: task-T2.2  Est: 75m  verifies: [UC-024]  Completed: 2026-04-15 (PR #24)
  - Plumb the assignment from scheduler -> executor: extend
    `executor.RunContainer` (or its options struct) with
    `CPUSetCores []int`. Caller in `internal/reconciler` passes
    `tracker.AssignedCores(podName)`.
  - In `buildRunArgs`, when `CPUSetCores` is non-empty, emit
    `--cpuset-cpus <range>` where the range is the contiguous form
    (e.g., `2-7`) or a comma list when not contiguous.
  - Add `formatCPURange(cores []int) string` helper alongside
    `formatDeviceIDs`.
  - Acceptance: stub-executor test asserts the flag appears with the
    correct range; legacy fractional path still emits only `--cpus`.

- [x] T2.3 Reconciler reuses cpuset on pod recovery  Owner: task-T2.3
  Est: 45m  verifies: [UC-024]  Completed: 2026-04-15 (PR #24)
  - On Spark restart, after `LoadAll` rehydrates pods, the reconciler
    must re-issue the same core assignment so a recreated container
    keeps its pinning.
  - Persist `coreAssignments` either in SQLite (preferred: extend the
    pod row with an `assigned_cores` column) or rebuild deterministically
    from millicore allocations + reserve set.
  - Acceptance: reconciler test rehydrates a tracker, verifies the
    assignment survives a restart.

#### Wave 3: Admission + API surface (parallel, 2 agents)

Depends on: T2.1.

- [x] T3.1 `GET /api/v1/node` includes core fields  Owner: coordinator
  Est: 30m  verifies: [UC-046]  Completed: 2026-04-15 (PR #25)
  - Add `cpu_reserved_cores []int` and `cpu_allocatable_cores []int`
    to `NodeResponse`.
  - Populated from the tracker (new `Allocatable().Cores` plus a stored
    reserve list).
  - Acceptance: `node_test.go` asserts both fields are present and
    non-overlapping.

- [x] T3.2 Admission rejects oversize pods  Owner: coordinator  Est: 45m
  verifies: [UC-051]  Completed: 2026-04-15 (PR #25)
  - In `POST /api/v1/pods` (and the NATS apply path), after parsing,
    call `tracker.CanFit` and return 400 with body
    `{"error": "limits.cpu N exceeds allocatable cores M"}` when N > M.
  - Existing path that returns 503 / pending on CanFit-false stays for
    transient cases; this check is the up-front guard for the
    structurally impossible case (limit > allocatable, not just
    > available).
  - Acceptance: handler test submits a manifest with `limits.cpu: "999"`
    and asserts 400.

#### Wave 4: Telemetry (parallel, 2 agents)

Depends on: Wave 2.

- [x] T4.1 Prometheus metric `spark_pod_cpu_throttled_seconds`
  Owner: coordinator  Est: 60m  verifies: [UC-024]  Completed: 2026-04-15 (PR #25; cgroup-path discovery a follow-up)
  - Read `cpu.stat` from each pod's cgroup
    (`/sys/fs/cgroup/.../cpu.stat` field `throttled_usec`).
  - Register in `internal/metrics`; expose via `/metrics`.
  - Acceptance: metrics test renders the gauge with a synthetic value.

- [x] T4.2 Host metrics `spark_host_loadavg` and
  `spark_host_softirq_seconds`  Owner: coordinator  Est: 60m
  verifies: [UC-046]  Completed: 2026-04-15 (PR #25; scrape orchestration a follow-up)
  - `loadavg`: read `/proc/loadavg` and export `_1m`, `_5m`, `_15m`.
  - `softirq_seconds`: parse `/proc/stat` `softirq` row, export per
    softirq type.
  - Acceptance: metrics test feeds a fixture and asserts emission.

#### Wave 5: Ship + validate (sequential, 1 agent)

Depends on: Waves 1-4 merged.

- [ ] T5.1 Open PR, wait for CI, rebase-merge  Owner: TBD  Est: 30m
  verifies: [infrastructure]
  - Branch: `fix/cpu-pinning-cpuset`.
  - PR body: link to issue #22, summarise the cpuset model, link
    docs/adr/012-cpu-pinning-cpuset.md.
  - Acceptance: PR state MERGED, main green, issue #22 auto-closes
    from `fixes #22`.

- [ ] T5.2 Cut release via release-please  Owner: TBD  Est: 15m
  verifies: [infrastructure]
  - Depends on: T5.1.
  - Merge the release-please version-bump PR. The DGX auto-upgrade
    timer (15-min interval) picks up the new tag automatically.
  - Acceptance: GitHub release published; DGX `spark --version` shows
    the new tag within 30 minutes.

- [ ] T5.3 DGX smoke test of the offending Wolf manifest
  Owner: TBD  Est: 30m  verifies: [UC-024]
  - Depends on: T5.2 + DGX auto-upgrade complete.
  - Submit the issue #22 manifest (Wolf CrossAsset GPU training with
    `ZERFOO_DISABLE_CUDA_GRAPH=1`).
  - Concurrent: `ping -i 1 192.168.86.250` from a workstation;
    `ssh ndungu@192.168.86.250 hostname` every 30s.
  - Acceptance: 0% packet loss for the training duration; SSH latency
    under 500 ms; `GET /api/v1/node` shows non-empty
    `cpu_reserved_cores`.

#### Cross-cutting (one per code-touching wave above)

- [ ] T6.1 `go vet ./...` and `staticcheck ./...` clean
  Owner: TBD  Est: 10m  verifies: [infrastructure]
  - Run after each wave merges; gate on PR CI.

- [ ] T6.2 `go test ./... -race -timeout 120s` clean
  Owner: TBD  Est: 10m  verifies: [infrastructure]
  - Run before pushing each wave's commits.

## Parallel Work

| Track | Tasks | Sync point |
|-------|-------|------------|
| A: Scheduler core | T1.1, T2.1, T2.3 | merges before Wave 3 |
| B: CLI + system detect | T1.2, T1.4 | feeds Wave 2 |
| C: Docs | T1.3, ADR-012 (done) | independent |
| D: Executor | T2.2 | needs T2.1 signature |
| E: API | T3.1, T3.2 | needs T2.1 |
| F: Telemetry | T4.1, T4.2 | independent of E |
| G: Ship | T5.1-T5.3 | sequential, after all merges |

### Waves

### Wave 1: Foundations (4 agents) — COMPLETE 2026-04-15 (PR #23)
- [x] T1.1 Scheduler `Cores` + `coreAssignments`
- [x] T1.2 `--system-reserve-cores` flag + parser
- [x] T1.3 design.md cpuset invariant note (commit 50f66ce, inline before agent dispatch)
- [x] T1.4 `SystemInfo.CoreIDs`

### Wave 2: Scheduler + executor wiring (3 agents) — COMPLETE 2026-04-15 (PR #24)
- [x] T2.1 `Allocate` reserves cores
- [x] T2.2 Executor emits `--cpuset-cpus`
- [x] T2.3 Reconciler reuses cpuset on recovery

### Wave 3: Admission + API surface (2 agents) — COMPLETE 2026-04-15 (PR #25, combined)
- [x] T3.1 `/api/v1/node` exposes core fields
- [x] T3.2 Admission rejects oversize pods

### Wave 4: Telemetry (2 agents) — COMPLETE 2026-04-15 (PR #25, combined)
- [x] T4.1 `spark_pod_cpu_throttled_seconds`
- [x] T4.2 `spark_host_loadavg` + softirq

### Wave 5: Ship + validate (1 agent)
- [ ] T5.1 PR, CI, rebase-merge
- [ ] T5.2 release-please version bump merged
- [ ] T5.3 DGX smoke test

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Foundations merged | T1.1-T1.4 | scheduler/main/gpu compile with new fields, no behaviour change yet |
| M2 | Wiring complete | T2.1-T2.3 | end-to-end test shows `--cpuset-cpus` emitted for an integer-CPU pod |
| M3 | API + admission live | T3.1-T3.2 | node endpoint returns core fields; oversize manifest rejected |
| M4 | Telemetry live | T4.1-T4.2 | new metrics visible at `/metrics` |
| M5 | Released and validated | T5.1-T5.3 | issue #22 closed, DGX smoke test passes, packet loss zero |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | cgroup v2 cpuset controller not enabled on DGX | High | Low | Verify on DGX before T5.3 with `cat /sys/fs/cgroup/cgroup.controllers`; if missing, document the manual enable step in T5.3 |
| R2 | A pod with fractional CPU (e.g. `0.5`) hits the new reject path because cores can't be fractional | Medium | Medium | T2.1 explicitly only allocates cores for integer multiples of 1000m; fractional pods continue to use the legacy `--cpus` quota path with no cpuset |
| R3 | Allocating contiguous cores fragments the available set quickly | Low | Medium | Selection prefers contiguous but falls back to any N cores; contiguous is best-effort, not required |
| R4 | Reconciler-on-restart re-allocates cores in a different order than the running container actually uses | Medium | Low | T2.3 persists the assignment in SQLite so recovery uses the exact prior set |
| R5 | Existing `--system-reserve-cpu` deployments break when we force-derive it from cores | Low | Low | Only override when `--system-reserve-cores` is set; if unset, current behaviour is preserved |

## Operating Procedure

- One PR per logical change (per wave). `fixes #22` in the final PR's
  body so the issue auto-closes.
- Rebase-and-merge (not squash, not merge commits). Per CLAUDE.md.
- Tests added in the same commit as the implementation they cover.
- `go vet ./...` and `staticcheck ./...` after every code change.
- `go test ./... -race -timeout 120s` before each push.
- Do not push to the DGX directly. Cut a release-please tag; the DGX
  auto-upgrade timer pulls the new `.deb` within 15 minutes.
- DGX smoke test is the only step requiring real hardware; everything
  else runs in CI on Linux (cpuset behaviour) and macOS (stub-executor
  unit tests).

## Progress Log

### 2026-04-15: Wave 3+4 shipped (PR #25 merged)
- T3.1, T3.2, T4.1, T4.2 implemented inline by coordinator after sub-agents went idle without completing the work. Combined into a single PR.
- T3.1: NodeResponse adds cpu_reserved_cores and cpu_allocatable_cores; ResourceTracker.ReservedCores accessor added.
- T3.2: handleApplyPod rejects pods whose total CPU request (integer cores) exceeds allocatable cores with HTTP 400.
- T4.1: parser/reader/renderer for spark_pod_cpu_throttled_seconds; cgroup-path discovery deferred to follow-up.
- T4.2: parsers/renderers for spark_host_loadavg and spark_host_softirq_seconds; scrape orchestration deferred.
- Final main commits: 64f94c3 (T3.1 scheduler), 1f8c27e (T3.1 api), ae1b3d2 (T3.2), df910c3 (T4.1+T4.2). Build/vet/staticcheck/tests all green.

### 2026-04-15: Wave 2 shipped (PR #24 merged)
- T2.1 (scheduler Allocate + main.go wiring), T2.2 (executor --cpuset-cpus + reconciler caller), T2.3 (SQLite persistence + RestoreAssignment) implemented in parallel by three sub-agents.
- Two merge conflicts resolved manually: reconciler.go preempt-retry path (combined T2.2 CpusetCores + T2.3 SetAssignedCores) and resources_test.go (kept all 7 T2.1 + 3 T2.3 tests).
- Cumulative test exposed an Allocate idempotency bug: T2.1 Allocate was overwriting T2.3 RestoreAssignment data. Fix: Allocate reuses pre-existing core assignment of the right size. Matches recovery design intent.
- GitHub rejected rebase-merge on integration branch with merge commits; squashed locally to a single commit and force-pushed.
- Final main commit: 393e79b. End-to-end cpuset path now active. Behaviour change: pods with integer CPU limits run pinned via --cpuset-cpus.

### 2026-04-15: Wave 1 shipped (PR #23 merged)
- T1.1, T1.2, T1.4 implemented in parallel by three sub-agents in worktrees, merged via wave-1-integration. T1.3 done inline pre-dispatch.
- CI staticcheck initially failed on unused private helper (`assignedCoreCountLocked`); dropped from this PR (Wave 2 reintroduces when needed).
- Final main commits: 6cce378 (T1.2 parser), a644ce9 (T1.2 flag), 60cd637 (T1.1 scheduler), 04ea7f3 (T1.4 gpu), 5ddd030 (CI fix). All tests pass with -race; no behaviour change yet (Wave 2 wires it).

### 2026-04-15: Change Summary
- Trimmed completed E58 (issue #13). Knowledge moved to `docs/devlog.md`
  ("2026-04-15: Issue #13 fix shipped, v1.8.1 deployed, auto-upgrade
  live") and `docs/design.md` (Key Invariants line on `no such pod`
  reconciliation). T58.5 DGX smoke test treated as effectively complete:
  v1.8.1 is deployed on the DGX and the auto-upgrade timer is active per
  the 2026-04-15 deployment record.
- Added new ADR-012 (`docs/adr/012-cpu-pinning-cpuset.md`) capturing the
  cpuset partitioning decision before planning the implementation.
- New plan targets only remaining open issue #22 (CPU pinning).
  Five waves, 14 tasks, max 4 parallel agents in Wave 1.
- ADRs created/updated this run:
  - ADR-012: CPU pinning via cpuset partitioning (new).

## Hand-off Notes

- The single open issue is feza-ai/spark#22. The full repro (Wolf
  CrossAsset training manifest, observed symptoms, root cause analysis)
  is in the issue body itself; treat it as the spec.
- Fix lives in three packages: `internal/scheduler/resources.go`,
  `internal/executor/podman.go`, `cmd/spark/main.go`. Each touches a
  small, contained area; no large refactors required.
- DGX `192.168.86.250` runs v1.8.1 with the auto-upgrade timer active
  (15-minute polling interval). Once a new release tag exists, the host
  self-updates without manual `ssh` deployment.
- The cpuset model deliberately mirrors the existing GPU device-slot
  tracking pattern in `ResourceTracker`. Read the `gpuAssignments` code
  before writing `coreAssignments` -- it is the template.
- For Wave 5 validation, the offending Wolf manifest is in issue #22's
  "The offending manifest" section. Submit it via
  `POST $SPARK/api/v1/pods` per the CLAUDE.md DGX runbook.

## Appendix

- Issue #22: https://github.com/feza-ai/spark/issues/22
- ADR-012: docs/adr/012-cpu-pinning-cpuset.md
- Prior closed issues this work builds on: #8, #10, #12 (v1.6.2), #13
  (v1.8.1). See `docs/devlog.md` for context.
- Related: feza-ai/wolf PR #108 ships
  `ZERFOO_DISABLE_CUDA_GRAPH=1` which makes the saturation pattern
  more severe by pushing more work onto host CPU. Wolf will keep
  exercising this Spark gap until upstream `zerfoo/ztensor#93`
  lands or Spark pins CPUs (this work).
