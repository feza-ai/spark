# Spark — Roadmap

## Shipped

- **#81 — GPU reservation leak** (device-slot counter never released on a GPU-to-GPU-less re-`Allocate`, a `Requests`-only GPU pod never getting a device, and both delete paths trusting a non-"no such pod" `RemovePod` error at face value). PR #82, merged 2026-08-28. Released as v1.17.0, deployed and confirmed live on `aitopatom-bfc8` (`GET /healthz` → `1.17.0`). Owner: this session. Root cause and fix: `docs/adr/014-gpu-slot-reconciliation.md`, `docs/devlog.md`.

## In flight (PRs open)

- **#76 — utilization-aware CPU overcommit.** Triggered by a live capacity incident: `GET /api/v1/resources` showed 95% CPU / 92% memory allocated while GPU sat at 0% and ~20 same-priority GitHub Actions runner pods held most of the reservation. Fix in progress on `fix/76-utilization-aware-overcommit` (worktree `/Volumes/BuildOffload/spark-wt-issue76`): 5-minute-load-average fallback admission when accounted CPU is full, safety margin exposed as `--cpu-overcommit-margin-millis` (ruling given 2026-08-28), plus fixing `AddEvent`-sourced events never persisting to SQLite (admission-failure history survives a restart). Memory/GPU accounting untouched by design. Owner: this session.

## Planned

- **#85 — GPU-assigned pods with a `command` override fail to start** (`invalid reference format`). Found live 2026-08-28 while verifying #81's fix on the DGX: `injectGPUDevices`'s flag-skip-list is missing `--entrypoint` (and `--cpuset-cpus`/`--user`/`--cap-add`/`--cap-drop`), so it misreads `--entrypoint`'s own value token as the image position and splices the GPU env var in between. Pre-existing (346319c), unrelated to #76/#81. Any real GPU job that sets `command` hits this on every start attempt. Not yet started.

## Blocked

- None.

## Carried over from the 2026-07-10 handover (not started this session)

- **#71** — DELETE fails on a podman stop→rm race (`cgroup slice not loaded`), returns 500, leaves a phantom store record that can resurrect the workload across the next upgrade. Fix sketch on the issue.
- **#47** — unified-memory host protection: admission headroom reserve + usage-based OOM alerting.
