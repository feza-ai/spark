# Spark — Roadmap

## Shipped

- **#81 — GPU reservation leak** (device-slot counter never released on a GPU-to-GPU-less re-`Allocate`, a `Requests`-only GPU pod never getting a device, and both delete paths trusting a non-"no such pod" `RemovePod` error at face value). PR #82, merged 2026-08-28. Released as v1.17.0, deployed and confirmed live on `aitopatom-bfc8` (`GET /healthz` → `1.17.0`). Owner: this session. Root cause and fix: `docs/adr/014-gpu-slot-reconciliation.md`, `docs/devlog.md`.
- **#76 — utilization-aware CPU overcommit.** Triggered by a live capacity incident: `GET /api/v1/resources` showed 95% CPU / 92% memory allocated while GPU sat at 0% and ~20 same-priority GitHub Actions runner pods held most of the reservation. PR #83, merged 2026-08-28 (rebased onto post-#81 main; conflicts in reconciler.go, scheduler/resources.go, metrics/collector.go, cmd/spark/main.go, devlog.md all resolved additively). 5-minute-load-average fallback admission when accounted CPU is full, safety margin exposed as `--cpu-overcommit-margin-millis`, plus fixing `SavePod`'s `INSERT OR REPLACE` silently cascade-deleting a pod's event history. Memory/GPU accounting untouched by design. Released as v1.18.0, deployed (`GET /healthz` → `1.18.0`). Owner: this session. Root cause and fix: `docs/adr/013-utilization-aware-admission.md`, `docs/devlog.md`. **Caveat: the overcommit bypass itself has not been live-triggered and observed** (`docs/plan.md` T1.6) — deployment is confirmed, the behavior under real phantom-CPU-saturation is not yet.
- **#85 — GPU-assigned pods with a `command` override fail to start** (`invalid reference format`). Found live 2026-08-28 while verifying #81's fix on the DGX. Root cause: `injectGPUDevices`'s positional-scan flag-skip-list was missing `--entrypoint` (and `--cpuset-cpus`/`--user`/`--cap-add`/`--cap-drop`), so it misread `--entrypoint`'s own value token as the image position and spliced the GPU env var in between. PR #86: removed `injectGPUDevices` entirely, `buildRunArgs` now emits `NVIDIA_VISIBLE_DEVICES` inline alongside the rest of `container.Env`, closing the whole "which flags take a value" bug class rather than one instance of it. Merged 2026-08-28 (rebased a second time onto post-#83 main to resolve a `docs/roadmap.md`-only conflict). Released as v1.18.0, deployed, and live-verified — the exact repro manifest that previously failed with "invalid reference format" now starts successfully with the GPU device attached. Owner: this session.
- **#75 — `backoffLimit: 0` never reached terminal `Failed`** on repeated `CreatePod` failure (image pull error, rejected `securityContext`, resource contention) — retried forever with exponential backoff instead of failing on the first attempt (Kubernetes Job semantics: `backoffLimit: 0` = zero retries). Root cause: `reconcilePending`'s terminal-failure check at both `CreatePod`-failure call sites (initial-schedule path and the post-preemption retry path) was guarded by `pod.Spec.BackoffLimit > 0 &&`, so `BackoffLimit == 0` disabled the check entirely instead of satisfying it on the first failure (`1 > 0` is true). Fix drops that guard; `reconcileScheduled`'s separate staleness/orphan-recovery guard and `reconcileRunning`'s container-exit retry comparison are untouched. PR #96, merged 2026-08-29, `de7b90e`. **Caveat: not yet live-verified on the DGX** (queued in the coordinator's centralized live-verify pass) — issue #75 reopened pending that (was auto-closed by the PR's "Fixes #75" line; Operating Procedure requires closing only after live verification).
- **#78 — a legitimately-Pending pod's `/logs` reported "podman has lost the pod"** (500) instead of distinguishing "queued, awaiting resources" from "actually lost" — `handlePodLogs` forwarded podman's raw "no such pod" error verbatim without checking the pod's own recorded status, so CI callers polling logs during normal admission queueing saw the same error shape as a genuinely vanished pod. Fix: a new `pendingLogTimeout` (default 10m, `--pending-log-timeout` / `Server.SetPendingLogTimeout`) — within it, `/logs` on a `Pending` pod returns `200` empty; past it, `503` naming the real shortfall pulled from the pod's existing event history (reuses the reconciler's `PendingWatchdog` events, no new persisted field or schema change). `/events` already surfaced the shortfall; gained regression coverage only. PR #97, merged 2026-08-29, `13042f7`. Issue #78 correctly left open by the PR (no auto-close keyword) — live-verify queued in the coordinator's centralized pass; the 503/timeout-exceeded path is unit-tested only (would need a ~10min wait or a disposable instance with a short timeout to trigger live).

## In flight (PRs open)

Wave 1 dispatched via `/apply --pool` 2026-08-28 (loop). 10 dispatch units,
each in an isolated worktree, claims held via `refs/claims/*`. Kazi lane
(default, no `lane: agent` marker) unless noted.

- T1.6 — live-verify CPU overcommit bypass (issue #76). Owner: pool wave 1.
- T2.2-T2.3 — issue #73 (quoted-scalar `command` mangling). Owner: pool wave 1.
- T2.4-T2.5 — issue #77 (silent instant-complete). Owner: pool wave 1. `lane: agent`.
- T2.9 — issue #88 (DELETE/stop hang). Owner: pool wave 1. `lane: agent`.
- T3.1-T3.5 — issue #74 (JSON POST ingestion gap). Owner: pool wave 1.
- T4.1-T4.2 — issue #71 (DELETE phantom record on cgroup race). Owner: pool wave 1.
- T4.7-T4.8 — issue #80 quick win 1 (`GET /pods/{name}/manifest`). Owner: pool wave 1.
- T5.1-T5.4 — issue #79 (preemption anti-thrash starvation). Owner: pool wave 1. `lane: agent` for T5.2.

Deferred to next pool run (not claimed, no blocking dependents this wave):
T2.6 (flag-skip-list regression coverage), T4.9 (#80 signedness bug).

## Discovered, not yet fixed

- **#88 — `DELETE /api/v1/pods/{name}` on a GPU-attached pod hung for ~7 minutes**, found live 2026-08-28 during #85's live verification. `podman pod stop` was retried three times by Spark without completing; a `podman` child process parented directly by Spark's own PID (`2476433`) became a zombie (exited but never reaped); an unrelated, unfiltered `sudo podman pod ps` also hung host-wide for the same ~7-minute window (confirmed via a second SSH session, `timeout 8 sudo podman pod ps` returned exit 124). Self-resolved on its own — the pod reached `status: "completed"` without a `spark.service` restart, and a follow-up `sudo podman pod ps` returned normally afterward. Not yet root-caused; tracked as `docs/plan.md` T2.9. Owner: unassigned.

## Planned

Full triage in `docs/plan.md` (created 2026-08-28, covers all untriaged
open issues as of that date). Summary by epic:

- **E1 (#76)** — shipped, see above; T1.6 (live-trigger the overcommit bypass) still outstanding.
- **E2 (#85, #73, #77, #88)** — executor `command`/entrypoint/lifecycle fragility, same code path family (`buildRunArgs`, formerly also `injectGPUDevices`; and `StopPod`/`RemovePod`) in `internal/executor/podman.go`. #85 shipped, see above. #73 (long quoted scalar mangled), #77 (pod completes instantly, container spec silently dropped), and #88 (stop/delete hang, newly discovered) remain, sequenced after #85 (already merged) — none of the three block each other.
- **E3 (#74)** — `POST /api/v1/pods` with a JSON body returns `201 {"pods":null}` and creates nothing; likely the hand-rolled YAML parser silently mis-parsing JSON. Independent, no blockers.
- **E4 (#71, #75, #78, #80 quick wins)** — DELETE phantom record on cgroup race (#71); `backoffLimit: 0` never reaching terminal Failed (#75); pending-vs-lost pod status distinction (#78); `GET /api/v1/pods/{name}/manifest` endpoint and a `gpu 0 > -1 free` signedness bug (#80). Was blocked on #76 merging (shares reconciler.go) -- unblocked now that PR #83 is merged.
- **E5 (#79)** — preemption anti-thrash cap silently starves a high-priority pod; the actual cap-vs-starvation trade-off is a real design call, flagged `lane: agent` rather than pre-decided. Was blocked on #76 merging (shares scheduler.go) -- unblocked now that PR #83 is merged.
- **E6 (outline)** — #80's harder root cause (pod reports `pending` while its container is alive and serving under crash-restart resource pressure). Not yet understood well enough to decompose; planning task triggers once E4 lands.
- **E7 (outline)** — #47, unified-memory host OOM protection (admission headroom reserve + usage-based alerting). Planning task triggers once E1 (shipped) and E5 land.

## Blocked

- None.
