# Spark — Roadmap

## Shipped

- **#81 — GPU reservation leak** (device-slot counter never released on a GPU-to-GPU-less re-`Allocate`, a `Requests`-only GPU pod never getting a device, and both delete paths trusting a non-"no such pod" `RemovePod` error at face value). PR #82, merged 2026-08-28. Released as v1.17.0, deployed and confirmed live on `aitopatom-bfc8` (`GET /healthz` → `1.17.0`). Owner: this session. Root cause and fix: `docs/adr/014-gpu-slot-reconciliation.md`, `docs/devlog.md`.
- **#76 — utilization-aware CPU overcommit.** Triggered by a live capacity incident: `GET /api/v1/resources` showed 95% CPU / 92% memory allocated while GPU sat at 0% and ~20 same-priority GitHub Actions runner pods held most of the reservation. PR #83, merged 2026-08-28 (rebased onto post-#81 main; conflicts in reconciler.go, scheduler/resources.go, metrics/collector.go, cmd/spark/main.go, devlog.md all resolved additively). 5-minute-load-average fallback admission when accounted CPU is full, safety margin exposed as `--cpu-overcommit-margin-millis`, plus fixing `SavePod`'s `INSERT OR REPLACE` silently cascade-deleting a pod's event history. Memory/GPU accounting untouched by design. Owner: this session. Root cause and fix: `docs/adr/013-utilization-aware-admission.md`, `docs/devlog.md`.

## In flight (PRs open)

- **#85 — GPU-assigned pods with a `command` override fail to start** (`invalid reference format`). Found live 2026-08-28 while verifying #81's fix on the DGX. Root cause: `injectGPUDevices`'s positional-scan flag-skip-list was missing `--entrypoint` (and `--cpuset-cpus`/`--user`/`--cap-add`/`--cap-drop`), so it misread `--entrypoint`'s own value token as the image position and spliced the GPU env var in between. PR #86 (`fix/85-gpu-entrypoint-env-conflict`, worktree `/Volumes/BuildOffload/spark-wt-issue85`): removed `injectGPUDevices` entirely, `buildRunArgs` now emits `NVIDIA_VISIBLE_DEVICES` inline alongside the rest of `container.Env`, closing the whole "which flags take a value" bug class rather than one instance of it. Live DGX check not performed — the deployed binary (v1.17.0) predates this unmerged fix; recommended once released, same as #81. Owner: this session.

## Planned

Full triage in `docs/plan.md` (created 2026-08-28, covers all untriaged
open issues as of that date). Summary by epic:

- **E1 (#76)** — shipped, see above.
- **E2 (#85, #73, #77)** — executor `command`/entrypoint fragility, same root code path (`buildRunArgs`, formerly also `injectGPUDevices`) in `internal/executor/podman.go`. #85 in flight (PR #86, see above). #73 (long quoted scalar mangled) and #77 (pod completes instantly, container spec silently dropped) sequenced after #85 merges.
- **E3 (#74)** — `POST /api/v1/pods` with a JSON body returns `201 {"pods":null}` and creates nothing; likely the hand-rolled YAML parser silently mis-parsing JSON. Independent, no blockers.
- **E4 (#71, #75, #78, #80 quick wins)** — DELETE phantom record on cgroup race (#71); `backoffLimit: 0` never reaching terminal Failed (#75); pending-vs-lost pod status distinction (#78); `GET /api/v1/pods/{name}/manifest` endpoint and a `gpu 0 > -1 free` signedness bug (#80). Was blocked on #76 merging (shares reconciler.go) -- unblocked now that PR #83 is merged.
- **E5 (#79)** — preemption anti-thrash cap silently starves a high-priority pod; the actual cap-vs-starvation trade-off is a real design call, flagged `lane: agent` rather than pre-decided. Was blocked on #76 merging (shares scheduler.go) -- unblocked now that PR #83 is merged.
- **E6 (outline)** — #80's harder root cause (pod reports `pending` while its container is alive and serving under crash-restart resource pressure). Not yet understood well enough to decompose; planning task triggers once E4 lands.
- **E7 (outline)** — #47, unified-memory host OOM protection (admission headroom reserve + usage-based alerting). Planning task triggers once E1 (shipped) and E5 land.

## Blocked

- None.
