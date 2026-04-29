# Spark Development Log

## 2026-04-29: Issue #37 phantom-running pods leak resources

**Type:** finding
**Tags:** reconciler, executor, podman, issue-37, resource-leak

**Problem:** A pod stayed in `phase: running` in the Spark API after its podman pod had been removed (silent exit, OOM, or manual `podman pod rm`). The pod's CPU/GPU/memory quota was held forever; subsequent pods needing the same resources queued indefinitely. Workaround was a manual `DELETE /api/v1/pods/<name>`.

**Root cause:** `internal/executor/podman.go` `PodStatus` runs `podman pod inspect <name>`. When the pod has been removed entirely, that command exits non-zero and `PodStatus` returns an error. `internal/reconciler/reconciler.go` `reconcileRunning` treated every `PodStatus` error as transient (`slog.Error` + return), so the state machine never transitioned. A primitive `isNoSuchPod(err)` already existed and was used by `reconcileScheduled`; `reconcileRunning` simply did not consult it.

**Fix:** In `reconcileRunning`, switch on the error class. If `isNoSuchPod(err)`, synthesize `Status{Running: false, ExitCode: -1}`, append a `lost` event, and fall through to the existing exited-pod transition path (release scheduler resources and apply restart policy). All other errors keep the existing log-and-return behaviour. Test: `TestIssue37_NoSuchPodRecoversResources` injects a `no such pod` error from a stub executor and asserts the pod ends up Failed with the resources released and a `lost` event recorded.

**Impact:** A pod whose podman backing has gone away is detected within one reconcile tick (~5s) and its quota is released. No new dependencies, no API change, parallel to the existing `reconcileScheduled` recovery path.

## 2026-04-28: Issue #32 silent-pending investigation (T1.3)

**Type:** investigation
**Tags:** reconciler, scheduler, issue-32, watchdog, silent-pending

**Problem:** Issue #32: a high-priority GPU pod (`ztensor102-v17-r2`, GPU=1, priorityClassName=high, restartPolicy=Never) sat in `status: pending` for 20+ minutes with `events: []`, `startAttempts: 0`, empty `reason`. The user asked whether `reconcileOnce` could fail to reach `reconcilePending` for a "logically pending" pod -- e.g. because a prior failed attempt had left the record in `Scheduled` state.

**Reproduction:** New file `internal/reconciler/reconciler_issue32_test.go`. Three tests:

1. `TestIssue32_PendingPodReachesReconcilePending` -- submit the exact issue manifest as a `PodSpec` literal into a fresh `PodStore`, run `reconcileOnce` once, assert `Status == Running` and `CreatePod` was called. Passes -- the natural Pending state DOES reach `reconcilePending`.
2. `TestIssue32_StatusGatesReconcilePath` -- table-driven over `Pending|Scheduled|Running|Completed|Failed|Preempted`. Only `Pending` triggers a `CreatePod` (proxy for "reached `reconcilePending`"). Confirmed: every other status takes a different switch arm; none silently re-routes a pending-equivalent pod into `reconcilePending`.
3. `TestIssue32_SchedulerPendingIsSilent` -- give the scheduler too little CPU to fit the request and no preemption candidates; `Schedule()` returns `Action=Pending`. Reconciler reaches `reconcilePending` and hits the `case scheduler.Pending` arm.

**Root cause (pre-fix):** This is NOT a "wrong status routes around `reconcilePending`" bug. `reconcileOnce`'s switch is exhaustive over the states it cares about, and `Apply` always inserts new pods as `StatusPending`. The defect was INSIDE `reconcilePending`: when `Schedule()` returned `Action=Pending`, the only side effect was `slog.Debug(...)`. From an API consumer's perspective this was indistinguishable from "the reconciler never ran" -- exactly the user-reported symptom.

**Fix:** T1.2 (PR #34, this same wave) addresses the visibility half: the `case scheduler.Pending` arm now calls `updateStatus(name, StatusPending, "awaiting-resources: "+reason)` and `store.AddEvent(name, "PendingWatchdog", msg)`. The `ScheduleResult.Reason` field plumbs the scheduler's shortfall description (e.g. "no preemption candidates; shortfall: gpu 1 > 0 free") to the reconciler. After this Wave 1 lands, the issue's symptom (silent forever-pending) is impossible by construction.

**Outstanding:** the second-order question -- WHY did `Schedule()` return `Pending` for a pod that fit free capacity in the original repro -- remains. Likely candidate: `scheduler.go:89` skips candidates with `pod.Priority <= spec.Priority`, so if the running CPU-only pod's `Priority` was equal to (or lower numeric than) the new pod's, it was skipped during preemption-candidate gathering. Tracked as FU1.3b in the plan.

**Impact:** `reconcileOnce` cannot fail to reach `reconcilePending` for a `Pending` pod -- confirmed by test. The watchdog (T1.2) is sufficient to mitigate the user-visible symptom; the scheduler-accounting audit (FU1.3b) is a separate follow-up.

## 2026-04-16: Issue #22 cpuset pinning shipped (v1.9.0-v1.10.1), DGX validated

**Type:** finding
**Tags:** v1.9.0, v1.10.0, v1.10.1, cpuset, issue-22, deployment, auto-upgrade

**Problem:** Issue #22: pods with integer CPU limits still saturated all 20 DGX cores because Spark only passed `--cpus` (CFS quota) to podman, not `--cpuset-cpus` (core pinning). This caused 40+ minutes of 100% packet loss and SSH banner timeouts during Wolf CrossAsset GPU training.

**Root cause:** `internal/executor/podman.go` emitted only `--cpus=N.0` which is a cumulative time quota, not a CPU set restriction. Container threads could land on any core including those handling network IRQs and sshd.

**Fix:** Five waves across PRs #23, #24, #25: scheduler tracks per-pod core assignments mirroring the GPU device-slot pattern; executor emits `--cpuset-cpus`; SQLite persists assignments for restart recovery; `--system-reserve-cores` flag excludes host cores; `/api/v1/node` exposes the partition; admission rejects oversize pods; three new Prometheus metrics added. ADR-012 documents the decision. v1.9.0 released with the code; v1.10.0 added the deploy/spark.env wiring; v1.10.1 added version in /healthz + auto-upgrade conffile fix.

**Impact:** DGX validated: pod with limits.cpu=4 pinned to cpuset-cpus=2-5, oversize pod rejected 400, /healthz returns version=1.10.1. Auto-upgrade pipeline verified end-to-end (push -> release-please -> GoReleaser .deb -> DGX timer -> dpkg install -> restart). The `--force-confold` fix prevents future conffile prompt failures in non-interactive upgrades.

## 2026-04-15: Issue #13 fix shipped, v1.8.1 deployed, auto-upgrade live

**Type:** finding
**Tags:** v1.8.1, reconciler, issue-13, deployment, auto-upgrade

**Problem:** Issue #13 -- when `podman pod create` failed during `reconcilePending`, the pod was stored with `StatusScheduled` but the underlying podman pod was missing. Subsequent reconcile ticks called `podman pod inspect`, got `no such pod`, logged the error, and never re-queued. The pod stuck indefinitely until the client issued `DELETE` + fresh `POST`.

**Root cause:** `reconcileScheduled` returned on any inspect error without distinguishing `no such pod` (authoritative: missing) from transient daemon flakes.

**Fix:** Added `isNoSuchPod(err)` helper. `reconcileScheduled` now switches on inspect error: `nil` -> proceed; `no such pod` -> treat as not-running and continue to staleness check; other errors -> log and return. `scheduledStaleness` reduced from 30s to 10s so the next 5s tick can act. Pre-reset `BackoffLimit` check transitions to `StatusFailed` instead of resetting to `Pending` when attempts exceed the limit. Four new `TestReconcileScheduled_*` cases cover missing-after-staleness, missing-within-staleness, transient-error, and backoff-limit-exceeded.

**Impact:** PR #18 merged (commit `a35ac4e`), issue #13 auto-closed. v1.8.1 released and deployed to DGX `192.168.86.250`. Auto-upgrade infrastructure (script + systemd timer at 15-minute interval) installed and active on production DGX, so future Spark releases self-deploy without manual intervention.

## 2026-04-15: Resolve open GitHub issues (#8, #10, #12)

**Type:** finding
**Tags:** reconciler, api, manifest, yaml, state, issue-8, issue-10, issue-12

**Problem:**
- #12: `DELETE /api/v1/pods/{name}` removed the Spark store record even when podman failed to stop/remove the pod, leaving orphaned podman pods that saturated the DGX (GPU/CPU). The reconciler logged `orphaned pod discovered` every ~5s forever without acting.
- #10: Args/command values containing `://` (e.g. `nats://host:port`) were silently dropped by the hand-rolled YAML list parser because any `:` in an item was treated as a map separator.
- #8: When `podman pod create` succeeded but container start failed (e.g., missing volume), the reconciler looped infinitely retrying. No `backoffLimit` enforcement, no terminal failure state, no visibility of the container-start error.

**Root cause:**
- #12: `handleDeletePod` called `executor.StopPod`/`RemovePod` as best-effort (errors ignored) then unconditionally removed the store record. Reconciler's orphan branch was log-only.
- #10: `parseYAMLList` used `strings.Index(item, ":")`. YAML requires `:` to be followed by whitespace or EOL to act as a map separator; the parser did not enforce this and did not preserve quoted strings atomically.
- #8: No fields existed on `PodRecord` to track failure state. Reconciler kept rescheduling failed pods without a ceiling.

**Fix:**
- #12: `handleDeletePod` returns 500 + `deleted:false` and preserves the store record when podman stop/remove fails. "no such pod" treated as success. Reconciler now actively removes orphans after a 30s grace window via `orphanFirstSeen` tracking.
- #10: New `findMapSeparator` helper tracks quote state and only treats `:` as a map separator when followed by space/tab/EOL. Applied at both list-item and root parse sites.
- #8: Added `Reason`, `StartAttempts`, `LastAttemptAt` to `state.PodRecord` with idempotent SQLite column migration. Parsed `spec.backoffLimit` (default 3, `0` disables retry). Reconciler enforces exponential backoff (5s, 10s, 20s, 40s, cap 60s) and transitions to `StatusFailed` terminally when `StartAttempts > BackoffLimit`. Both `startAttempts` and `reason` surface on `GET /api/v1/pods/{name}`.

**Impact:**
- 7 new commits on main (PRs #14 and #16) plus T56.3 API surface in wave-3-apis.
- 304 tests pass with -race; no new linter findings.
- Wire check: DELETE handler, reconciler.RecoverPods, reconciler.reconcilePending, YAML parser, and GET /api/v1/pods/{name} all exercise the new paths.

## 2026-03-20: v1.6.0 GPU Count Model, Liveness Probes, CronJob Management, Node Info

**Type:** finding
**Tags:** v1.6.0, gpu, probes, cronjob, node-info

**Problem:** GPU resource model conflated device count and memory (parseGPU set GPUMemoryMB=1 for nvidia.com/gpu:1). No liveness probes for stuck containers. No HTTP management of cron jobs. No hardware detail endpoint.
**Root cause:** GPUMemoryMB was the only GPU field; parseGPU wrote device count into it. Liveness probes, cron management, and node info were not yet implemented.
**Fix:** Delivered 12 tasks across 3 parallel waves (8+1+3 agents):
- GPU count model: added GPUCount field to ResourceList, refactored parseGPU, updated scheduler to track device slots separately from memory, added heartbeat gpuCount field, reconciler GPU count drift detection.
- Liveness probes: ProbeSpec (exec, HTTP) parsed from manifests, ExecProbe/HTTPProbe executor methods, reconciler polls probes on each tick respecting InitialDelaySeconds/PeriodSeconds/FailureThreshold, restarts on threshold breach.
- CronJob HTTP management: CronScheduler.List()/Get() methods, GET /api/v1/cronjobs, GET /api/v1/cronjobs/{name}, DELETE /api/v1/cronjobs/{name}.
- Node info: GET /api/v1/node returns hostname, OS, arch, CPU cores, memory, GPU model/count/device IDs/memory.
**Impact:** 50 use cases (UC-001 through UC-050, excluding deferred UC-044 and UC-049). 48 WIRED, 2 PLANNED. 13 packages, 304 tests pass with -race. HTTP API now has 16 endpoints + auth + metrics.

## 2026-03-20: v1.5.0 Wiring Integrity and Reconciler Hardening

**Type:** finding
**Tags:** v1.5.0, wiring, reconciler, cron, securityContext, manifest-removal

**Problem:** Post-v1.4.0 audit found 7 broken wiring paths (CronJob registration on NATS/HTTP, manifest removal no-op, delete not releasing scheduler resources, restart counter never incremented, stuck Scheduled/Preempted pods, StreamPodLogs zombie processes) plus missing Ki suffix parsing and securityContext support.
**Root cause:** N/A -- audit-driven fix delivery.
**Fix:** Delivered 15 tasks across 3 parallel waves (10+2+3 agents):
- CronJob registration on all ingestion paths (NATS, HTTP, filesystem).
- Manifest file removal stops pods, releases resources, unregisters cron jobs.
- HTTP and NATS delete releases scheduler resources immediately.
- Restart counter increments on reconciler re-queue.
- Stuck StatusScheduled (30s timeout) and StatusPreempted pods recovered.
- StreamPodLogs reaps child processes via cmdReadCloser wrapping cmd.Wait().
- parseMemory Ki/K suffix support added.
- SecurityContext (runAsUser, privileged, capabilities add/drop) parsed and forwarded to podman.
- Source path tracking in state store for manifest-to-pod association.
**Impact:** 46 use cases (UC-001 through UC-046) all WIRED. Zero broken use cases. 13 packages, all tests pass.

## 2026-03-20: v1.4.0 Container Operations and GPU Device Management

**Type:** finding
**Tags:** v1.4.0, exec, ports, init-containers, gpu-devices, images

**Problem:** v1.3.0 lacked pod exec, container port mapping, init containers, GPU device isolation, and image management API.
**Root cause:** N/A -- planned feature delivery.
**Fix:** Delivered 5 features across 15 tasks in 3 parallel waves (8+4+3 agents):
- Pod exec: POST /api/v1/pods/{name}/exec runs commands inside containers, returns stdout/stderr/exitCode JSON.
- Container port mapping: manifest ports parsed, mapped via `podman pod create --publish`.
- Init containers: parsed from initContainers field, run sequentially before main containers, fail-fast on non-zero exit.
- GPU device assignment: nvidia-smi device enumeration, scheduler tracks device slots, NVIDIA_VISIBLE_DEVICES env var replaces --device nvidia.com/gpu=all, --gpu-max enforced.
- Image management: GET /api/v1/images lists images, POST /api/v1/images/pull pulls by name:tag.
**Impact:** 36 use cases (UC-001 through UC-036) all WIRED. 13 packages, all tests pass. HTTP API now has 12 endpoints + auth + metrics.

## 2026-03-19: v1.3.0 Observability, Security, and Operational Maturity

**Type:** finding
**Tags:** v1.3.0, metrics, auth, logs, events, emptydir

**Problem:** v1.2.0 had no Prometheus metrics, no HTTP auth, no HTTP access to pod logs/events, no structured logging, and broken emptyDir volumes.
**Root cause:** N/A -- planned feature delivery.
**Fix:** Delivered 6 features across 14 tasks in 2 parallel waves (8+4 agents):
- Prometheus /metrics endpoint (stdlib text exposition format, no client_golang). ADR-010.
- Bearer token HTTP auth middleware (--api-token-file, /healthz and /metrics exempt). ADR-011.
- Pod logs via HTTP: GET /api/v1/pods/{name}/logs with ?tail=N and ?follow=true (SSE).
- Pod events via HTTP: GET /api/v1/pods/{name}/events with ?since=RFC3339 filter.
- Structured JSON logging: --log-format json switches slog to JSONHandler.
- EmptyDir volumes: mapped to podman --mount type=tmpfs,destination=PATH.
**Impact:** 31 use cases (UC-001 through UC-031) all WIRED. 13 packages, all tests pass. HTTP API now has 9 endpoints + auth + metrics.

## 2026-03-19: v1.1.0 Full System Verification

**Type:** finding
**Tags:** verification, v1.1.0, use-cases, wiring

**Problem:** Needed to verify all 17 use cases (UC-001 through UC-017) were fully wired end-to-end after v1.1.0 (SQLite persistence, pod recovery, retention pruning).
**Root cause:** N/A -- verification audit, not a bug.
**Fix:** N/A -- all 17 use cases confirmed WIRED. Zero gaps found.
**Impact:** Validated that v1.1.0 is production-ready. 10 packages, all tests pass with -race. Startup sequence verified (21 steps). Shutdown sequence verified (graceful with 10s wait). Wiring verified across all layers: CLI -> state -> scheduler -> executor -> NATS -> SQLite.

Key findings:
- All 10 packages compile and pass tests (watcher takes ~38s due to polling intervals).
- Pod recovery reconciles podman state with SQLite on restart.
- Retention pruning (10-min tick) cleans both in-memory store and SQLite.
- No orphaned code paths or unconnected handlers found.
