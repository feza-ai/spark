# Spark — Roadmap

## Shipped

- **#81 — GPU reservation leak** (device-slot counter never released on a GPU-to-GPU-less re-`Allocate`, a `Requests`-only GPU pod never getting a device, and both delete paths trusting a non-"no such pod" `RemovePod` error at face value). PR #82, merged 2026-08-28. Released as v1.17.0, deployed and confirmed live on `aitopatom-bfc8` (`GET /healthz` → `1.17.0`). Owner: this session. Root cause and fix: `docs/adr/014-gpu-slot-reconciliation.md`, `docs/devlog.md`.

## In flight (PRs open)

- **#76 — utilization-aware CPU overcommit.** Triggered by a live capacity incident: `GET /api/v1/resources` showed 95% CPU / 92% memory allocated while GPU sat at 0% and ~20 same-priority GitHub Actions runner pods held most of the reservation. Fix in progress on `fix/76-utilization-aware-overcommit` (worktree `/Volumes/BuildOffload/spark-wt-issue76`): 5-minute-load-average fallback admission when accounted CPU is full, safety margin exposed as `--cpu-overcommit-margin-millis` (ruling given 2026-08-28), plus fixing `AddEvent`-sourced events never persisting to SQLite (admission-failure history survives a restart). Memory/GPU accounting untouched by design. Owner: this session.

## In flight (PRs open), continued

- **#85 — GPU-assigned pods with a `command` override fail to start** (`invalid reference format`). Found live 2026-08-28 while verifying #81's fix on the DGX: `injectGPUDevices`'s flag-skip-list is missing `--entrypoint` (and `--cpuset-cpus`/`--user`/`--cap-add`/`--cap-drop`), so it misreads `--entrypoint`'s own value token as the image position and splices the GPU env var in between. Pre-existing (346319c), unrelated to #76/#81. Any real GPU job that sets `command` hits this on every start attempt. Agent `spark-fix-85` dispatched 2026-08-28, worktree `/Volumes/BuildOffload/spark-wt-issue85`, PR not yet opened.

## Planned

Full triage in `docs/plan.md` (created 2026-08-28, covers all untriaged
open issues as of that date). Summary by epic:

- **E1 (#76)** — rebase PR #83 onto post-#81 main (conflicts in reconciler.go, scheduler/resources.go, metrics/collector.go, cmd/spark/main.go, devlog.md), merge, release, deploy, live-verify.
- **E2 (#85, #73, #77)** — executor `command`/entrypoint fragility, same root code path (`buildRunArgs`/`injectGPUDevices`) in `internal/executor/podman.go`. #73 (long quoted scalar mangled) and #77 (pod completes instantly, container spec silently dropped) sequenced after #85 merges.
- **E3 (#74)** — `POST /api/v1/pods` with a JSON body returns `201 {"pods":null}` and creates nothing; likely the hand-rolled YAML parser silently mis-parsing JSON. Independent, no blockers.
- **E4 (#71, #75, #78, #80 quick wins)** — DELETE phantom record on cgroup race (#71); `backoffLimit: 0` never reaching terminal Failed (#75); pending-vs-lost pod status distinction (#78); `GET /api/v1/pods/{name}/manifest` endpoint and a `gpu 0 > -1 free` signedness bug (#80). Blocked on #76 merging (shares reconciler.go).
- **E5 (#79)** — preemption anti-thrash cap silently starves a high-priority pod; the actual cap-vs-starvation trade-off is a real design call, flagged `lane: agent` rather than pre-decided. Blocked on #76 merging (shares scheduler.go).
- **E6 (outline)** — #80's harder root cause (pod reports `pending` while its container is alive and serving under crash-restart resource pressure). Not yet understood well enough to decompose; planning task triggers once E4 lands.
- **E7 (outline)** — #47, unified-memory host OOM protection (admission headroom reserve + usage-based alerting). Planning task triggers once E1 and E5 land.

## Blocked

- None.
