# Spark Development Log

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
