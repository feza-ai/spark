# Spark Development Log

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
