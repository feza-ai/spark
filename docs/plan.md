# Spark: HTTP API, Resource Reconciliation, and Graceful Shutdown (v1.2.0)

## Status: Complete

## Context

### Problem Statement

Spark v1.1.0 is a fully functional pod orchestrator with NATS messaging, SQLite persistence, and crash recovery. However, it has three operational gaps:

1. **No HTTP API.** All pod management requires a NATS client library. Health checks, simple scripting, and monitoring integration require an HTTP endpoint that any tool (curl, browsers, Prometheus) can access.

2. **No resource reconciliation.** The scheduler tracks *requested* resources but never compares them to *actual* usage. If a pod uses more or less than requested, the scheduler's model drifts from reality. nvidia-smi and podman both provide actual usage data that is currently ignored after startup.

3. **No graceful shutdown.** On SIGTERM, spark cancels all goroutines and exits after a fixed 10-second wait. Running pods are not stopped or drained. There is no configurable timeout, no option to drain pods before exit, and the HTTP server (once added) needs graceful shutdown via `http.Server.Shutdown()`.

### Objectives

1. Add an HTTP REST API for pod management, health checks, and resource queries using `net/http` (stdlib only).
2. Periodically reconcile the scheduler's resource model with actual podman and nvidia-smi data.
3. Implement graceful shutdown: stop accepting work, drain running pods, close all connections cleanly.

### Non-Goals

- Authentication/authorization on HTTP endpoints (deferred to v1.3.0).
- Multi-node coordination or service mesh.
- WebSocket or SSE streaming over HTTP.
- Prometheus /metrics endpoint (deferred to v1.3.0).

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (CGO_ENABLED=0). See ADR-001 and ADR-008.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns. No third-party routers. See ADR-009.
- Podman CLI only (no programmatic API).
- Standard `flag` package for CLI flags.

### Success Metrics

- `curl localhost:8080/healthz` returns `{"status":"ok"}` with HTTP 200.
- `curl localhost:8080/api/v1/pods` returns JSON pod list matching NATS `req.spark.list`.
- `curl -X POST localhost:8080/api/v1/pods -d @pod.yaml` applies a pod.
- Resource reconciliation logs discrepancies between requested and actual CPU/memory/GPU usage.
- On SIGTERM, spark drains running pods (up to `--shutdown-timeout`) before exiting.
- `go test ./... -race -timeout 120s` passes with all new tests.

## Discovery Summary

**Current architecture** (from docs/design.md):
- Interfaces: NATS only (`req.spark.{apply,delete,get,list}`) + filesystem watcher. No HTTP.
- Resource tracking: `scheduler.ResourceTracker` tracks allocations per pod based on *requested* resources. Never updated with actual usage after initial allocation.
- Shutdown: `cmd/spark/main.go` lines 265-283 -- catches SIGINT/SIGTERM, stops log streams, cancels context, waits 10s (hardcoded), closes NATS. Does not stop managed pods.
- Executor: `PodmanExecutor` has `CreatePod`, `StopPod`, `PodStatus`, `RemovePod`, `ListPods`. No per-pod resource query.
- GPU: `gpu.Detect()` runs nvidia-smi at startup only. `gpu.GPUInfo` has `MemoryUsedMB` and `UtilizationPercent` fields already.

**Key interfaces to extend**:
- New `internal/api` package: HTTP handlers wrapping store, scheduler, executor.
- `executor.Executor`: add `PodStats(ctx, name)` for per-pod resource usage from podman.
- `gpu.Detect()`: already returns current usage; call periodically for reconciliation.
- `scheduler.ResourceTracker`: add `UpdateAllocation(name, actual)` to correct drift.
- `cmd/spark/main.go`: add `--http-addr`, `--shutdown-timeout` flags; wire HTTP server and graceful shutdown.

**Use cases affected**:
- UC-001 through UC-004 gain HTTP equivalents (UC-018 through UC-021).
- UC-005 (resource scheduling) improved by reconciliation (UC-024).
- UC-022 (health check) and UC-023 (resource endpoint) are new observability use cases.
- UC-025 (graceful shutdown) affects all running pods on exit.

Decision rationale: docs/adr/009-http-api.md

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | HTTP API server | Health, resource, apply, delete, get, list endpoints via net/http |
| D2 | Resource reconciliation | Periodic podman/nvidia-smi queries update scheduler's resource model |
| D3 | Graceful shutdown | Configurable drain timeout, pod stop, clean connection closure |
| D4 | Main.go wiring | New flags, HTTP server start, reconciliation loop, shutdown sequence |

### Out of Scope

- HTTP authentication/authorization
- Prometheus /metrics endpoint
- WebSocket or SSE streaming
- Multi-node coordination

## Checkable Work Breakdown

### E23: HTTP API Handlers

- [x] T23.1 Create health check and resource endpoints in internal/api  Owner: TBD  Est: 45m  verifies: [UC-022, UC-023]
  - Create `internal/api/server.go`:
    - `type Server struct` with `store *state.PodStore`, `tracker *scheduler.ResourceTracker`, `executor executor.Executor`, `priorityClasses map[string]int`, `sqlStore *state.SQLiteStore`, `mux *http.ServeMux`.
    - `NewServer(store, tracker, executor, priorityClasses, sqlStore) *Server` -- creates mux, registers all routes.
    - `ServeHTTP(w, r)` -- delegates to mux (implements http.Handler).
  - Create `internal/api/health.go`:
    - `GET /healthz` handler: returns `{"status":"ok"}` with HTTP 200.
  - Create `internal/api/resources.go`:
    - `GET /api/v1/resources` handler: calls `tracker.Available()` and `tracker.Allocated()`.
    - Response: `{"allocatable":{"cpuMillis":N,"memoryMB":N,"gpuMemoryMB":N},"allocated":{...},"available":{...}}`.
  - Create `internal/api/server_test.go`:
    - TestHealthz: GET /healthz returns 200 with `{"status":"ok"}`.
    - TestResources: GET /api/v1/resources returns correct JSON with allocatable/allocated/available.
    - Use `httptest.NewServer` for all tests.
  - Acceptance: `go test -race ./internal/api/` passes. Health and resource endpoints return correct JSON.

- [x] T23.2 Create pod list and get endpoints in internal/api  Owner: TBD  Est: 45m  verifies: [UC-020, UC-021]
  - Create `internal/api/pods_query.go`:
    - `GET /api/v1/pods` handler: calls `store.List(status)`. Reads optional `?status=` query param.
    - Response: `{"pods":[{"name":"x","status":"running","priority":1000},...]}` (same schema as NATS ListResponse).
    - `GET /api/v1/pods/{name}` handler: calls `store.Get(name)`.
    - Response: same schema as NATS GetResponse (name, status, priority, timestamps, restarts, events).
    - 404 if pod not found: `{"error":"pod not found: NAME"}`.
  - Create `internal/api/pods_query_test.go`:
    - TestListPods: apply two pods, GET /api/v1/pods, verify both in response.
    - TestListPodsFilterStatus: apply pods with different statuses, filter by ?status=running.
    - TestGetPod: apply a pod, GET /api/v1/pods/NAME, verify full detail.
    - TestGetPodNotFound: GET /api/v1/pods/nonexistent, verify 404.
  - Acceptance: `go test -race ./internal/api/` passes. List and get return correct JSON matching NATS schemas.

- [x] T23.3 Create pod apply and delete endpoints in internal/api  Owner: TBD  Est: 60m  verifies: [UC-018, UC-019]
  - Create `internal/api/pods_mutate.go`:
    - `POST /api/v1/pods` handler: reads request body as YAML, calls `manifest.Parse()`, applies each pod to store, persists to SQLite.
    - Response (201): `{"pods":[{"name":"x","status":"pending"},...]}` (same schema as NATS ApplyResponse).
    - 400 on parse error: `{"error":"message"}`.
    - `DELETE /api/v1/pods/{name}` handler: calls `store.Get(name)`, `executor.StopPod()`, `executor.RemovePod()`, `store.Delete()`.
    - Response (200): `{"name":"x","deleted":true}`.
    - 404 if not found: `{"name":"x","deleted":false,"error":"pod not found"}`.
  - Create `internal/api/pods_mutate_test.go`:
    - TestApplyPod: POST valid YAML, verify 201 and pod appears in store.
    - TestApplyInvalidYAML: POST garbage, verify 400.
    - TestDeletePod: apply pod, DELETE, verify removed from store.
    - TestDeletePodNotFound: DELETE nonexistent, verify 404.
    - Use a stub executor that tracks calls (no real podman).
  - Acceptance: `go test -race ./internal/api/` passes. Apply and delete work correctly.

### E24: Resource Reconciliation

- [x] T24.1 Add PodStats to executor for per-pod resource queries  Owner: TBD  Est: 45m  verifies: [UC-024]
  - Add to `executor.Executor` interface: `PodStats(ctx context.Context, name string) (PodResourceUsage, error)`.
  - Define `type PodResourceUsage struct { CPUPercent float64; MemoryMB int }`.
  - Implement in `PodmanExecutor`: run `podman pod stats --no-stream --format json NAME`, parse JSON.
  - Parse the stats output: sum CPU% and memory usage across containers in the pod.
  - Create tests in `internal/executor/podman_test.go`:
    - TestPodStatsParseOutput: test JSON parsing with sample podman stats output.
    - Use a stub command runner to avoid requiring podman in CI.
  - Acceptance: `go test -race ./internal/executor/` passes. PodStats returns correct values from sample output.

- [x] T24.2 Create resource reconciliation loop in internal/reconciler  Owner: TBD  Est: 60m  verifies: [UC-024]
  - Add `type ResourceReconciler struct` in `internal/reconciler/resources.go`:
    - Fields: `store *state.PodStore`, `executor executor.Executor`, `tracker *scheduler.ResourceTracker`, `interval time.Duration`.
    - `NewResourceReconciler(store, executor, tracker, interval) *ResourceReconciler`.
    - `Run(ctx context.Context)`: tick loop at interval.
  - ReconcileOnce logic:
    1. Call `gpu.Detect()` to get current GPU usage. Log if UtilizationPercent > 90 or MemoryUsedMB diverges from allocated GPU by >20%.
    2. For each Running pod in store: call `executor.PodStats(ctx, name)`.
    3. Compare actual memory usage to requested MemoryMB. If actual > requested * 1.5, log warning "pod exceeds requested memory".
    4. Call `tracker.UpdateAllocation(name, actualResources)` to correct drift.
  - Add `UpdateAllocation(name string, actual manifest.ResourceList)` to `ResourceTracker`:
    - Updates the allocation map entry for the pod with actual values.
    - Only updates if the pod exists in the allocation map.
  - Create `internal/reconciler/resources_test.go`:
    - TestResourceReconciler_NoDiscrepancy: actual matches requested, no warnings.
    - TestResourceReconciler_MemoryExceeded: actual > 1.5x requested, verify warning logged.
    - TestResourceReconciler_GPUDrift: GPU usage diverges >20%, verify warning logged.
    - TestResourceReconciler_UpdatesTracker: verify tracker allocation updated with actual values.
    - Use stub executor and in-memory tracker.
  - Acceptance: `go test -race ./internal/reconciler/` passes. Reconciler detects discrepancies and updates tracker.

### E25: Graceful Shutdown

- [x] T25.1 Create shutdown coordinator  Owner: TBD  Est: 60m  verifies: [UC-025]
  - Create `internal/lifecycle/shutdown.go`:
    - `type ShutdownCoordinator struct` with `store *state.PodStore`, `executor executor.Executor`, `scheduler *scheduler.Scheduler`, `timeout time.Duration`.
    - `NewShutdownCoordinator(store, executor, scheduler, timeout) *ShutdownCoordinator`.
    - `Drain(ctx context.Context) error`:
      1. List all Running pods from store.
      2. Create a context with `timeout` deadline.
      3. For each Running pod: call `executor.StopPod(ctx, name, gracePeriod)` concurrently.
      4. Wait for all stops to complete or timeout.
      5. For pods still running after timeout: call `executor.RemovePod(ctx, name)` (force kill).
      6. Update store status to Completed/Failed as appropriate.
      7. Release scheduler resources for all drained pods.
    - `StopAccepting()`: sets an atomic flag that the store can check.
  - Add `SetReadOnly(bool)` to `state.PodStore`:
    - When true, `Apply()` returns without modifying state (rejects new pods).
    - Does not affect `UpdateStatus` or `Delete` (shutdown needs these).
  - Create `internal/lifecycle/shutdown_test.go`:
    - TestDrain_AllPodsStop: 3 running pods, all stop within timeout. Verify all marked completed.
    - TestDrain_TimeoutForceKill: 1 pod doesn't stop within timeout. Verify force-removed.
    - TestDrain_NoPods: empty store. Drain returns immediately.
    - TestStopAccepting: set read-only, verify Apply is rejected.
    - Use stub executor with configurable delays.
  - Acceptance: `go test -race ./internal/lifecycle/` passes. Drain stops all pods or force-kills after timeout.

### E26: Integration Wiring

- [x] T26.1 Wire HTTP server, resource reconciliation, and graceful shutdown into main.go  Owner: TBD  Est: 60m  verifies: [UC-018, UC-019, UC-020, UC-021, UC-022, UC-023, UC-024, UC-025]
  - Depends on: T23.1, T23.2, T23.3, T24.1, T24.2, T25.1.
  - Add flags to `cmd/spark/main.go`:
    - `--http-addr` (string, default `:8080`): HTTP listen address.
    - `--shutdown-timeout` (duration, default `30s`): max time to drain pods on shutdown.
    - `--reconcile-resources-interval` (duration, default `60s`): resource reconciliation interval.
  - Startup sequence additions (after step 11, before step 12):
    - Create `api.Server` with store, tracker, executor, priorityClasses, sqlStore.
    - Create `http.Server{Addr: *httpAddr, Handler: apiServer}`.
    - Start HTTP server in a goroutine: `go httpServer.ListenAndServe()`.
    - Create `ResourceReconciler` and start in a goroutine.
  - Shutdown sequence changes (replace current steps 15 onward):
    1. Receive signal.
    2. Stop accepting new pods: `store.SetReadOnly(true)`.
    3. Shut down HTTP server: `httpServer.Shutdown(ctx)`.
    4. Stop log streams.
    5. Cancel context (stops reconciler, watcher, heartbeat, cron).
    6. Drain running pods: `shutdownCoordinator.Drain(ctx)`.
    7. Close SQLite.
    8. Disconnect NATS.
  - Acceptance: Spark starts with HTTP server on :8080. `curl /healthz` works. On SIGTERM, pods are drained before exit. `go build ./...` and `go vet ./...` pass.

- [x] T26.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T26.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Run `staticcheck ./...` if available.
  - Acceptance: All tests pass, zero lint warnings.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: HTTP Handlers | T23.1, T23.2, T23.3 | HTTP API in internal/api |
| B: Resource Reconciliation | T24.1, T24.2 | PodStats + reconciliation loop |
| C: Graceful Shutdown | T25.1 | Shutdown coordinator in internal/lifecycle |

Sync point: T26.1 requires all tracks to complete before wiring.

### Waves

### Wave 1: Core Components (6 agents)
- [x] T23.1 Create health check and resource endpoints in internal/api  verifies: [UC-022, UC-023]
- [x] T23.2 Create pod list and get endpoints in internal/api  verifies: [UC-020, UC-021]
- [x] T23.3 Create pod apply and delete endpoints in internal/api  verifies: [UC-018, UC-019]
- [x] T24.1 Add PodStats to executor for per-pod resource queries  verifies: [UC-024]
- [x] T24.2 Create resource reconciliation loop in internal/reconciler  verifies: [UC-024]
- [x] T25.1 Create shutdown coordinator  verifies: [UC-025]

### Wave 2: Integration (2 agents, sequential)
- [x] T26.1 Wire HTTP server, resource reconciliation, and graceful shutdown into main.go  verifies: [UC-018, UC-019, UC-020, UC-021, UC-022, UC-023, UC-024, UC-025]
- [x] T26.2 Run full test suite and lint  verifies: [infrastructure]

Note: T24.2 depends on T24.1 (needs PodStats interface). These can be run by the same agent sequentially in Wave 1, or T24.2 can start once T24.1 merges. All Wave 2 tasks depend on Wave 1 completion.

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | HTTP handlers complete | T23.1, T23.2, T23.3 | All 6 endpoints tested and passing |
| M2 | Resource reconciliation complete | T24.1, T24.2 | PodStats + reconciler loop tested |
| M3 | Graceful shutdown complete | T25.1 | Drain + force-kill tested |
| M4 | Wired and tested | T26.1, T26.2 | Full integration passes, all endpoints work |
| M5 | Release v1.2.0 | M4 | Tag cut, .deb packages published on GitHub Releases |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | `podman pod stats` JSON format varies between versions | Medium | Low | Parse defensively. Test with sample output from DGX podman version. |
| R2 | Resource reconciliation creates noise (frequent log warnings) | Low | Medium | Use thresholds (>20% drift for GPU, >1.5x for memory) to filter noise. |
| R3 | Graceful shutdown races with reconciler creating new pods | Medium | Medium | SetReadOnly on store before cancelling context. Reconciler checks read-only. |
| R4 | HTTP server port conflicts with other services | Low | Low | Configurable via `--http-addr`. Default 8080 is standard for APIs. |
| R5 | Go 1.22+ ServeMux method routing not available | High | Low | Project requires Go 1.22+. Verify in go.mod. Fallback: manual method checking in handlers. |
| R6 | PodStats returns stale data for rapidly changing workloads | Low | Medium | Reconciliation interval (60s) is intentionally slow. Scheduler uses requested values for admission; actual values are advisory. |

## Operating Procedure

### Definition of Done
- `go build ./...` succeeds.
- `go vet ./...` reports zero warnings.
- `go test ./... -race -timeout 120s` passes.
- All new functions have unit tests.
- Each commit touches one package directory.
- Conventional Commits format.

### Review and QA
- Rebase and merge (not squash, not merge commits).
- Each commit is a small, logical unit touching one package.
- HTTP tests use `httptest.NewServer` (no real network binding).
- Executor tests use stub command runner (no podman dependency in CI).

## Progress Log

### 2026-03-19: Plan created
- Trimmed completed v1.1.0 plan. Knowledge preserved in docs/adr/008-sqlite-state-persistence.md and docs/design.md.
- Created plan for v1.2.0: HTTP API, resource reconciliation, graceful shutdown.
- Created ADR 009 (docs/adr/009-http-api.md): net/http with Go 1.22+ ServeMux, no third-party routers.
- Updated use case manifest with 8 new use cases (UC-018 through UC-025).
- 8 tasks across 2 waves. 6 agents in Wave 1 (parallel), 2 in Wave 2 (sequential).

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.1.0 is complete: SQLite persistence, pod recovery, retention pruning. All 17 use cases are WIRED.
- This plan adds three features: HTTP API, resource reconciliation, graceful shutdown.
- Key constraint: `net/http` only, no third-party routers. Go 1.22+ `ServeMux` supports `GET /path/{param}` patterns.
- The HTTP API is a thin transport layer. All business logic is in existing packages (store, scheduler, executor).
- Resource reconciliation adds `PodStats` to the executor interface and a new `UpdateAllocation` method to `ResourceTracker`.
- Graceful shutdown adds `internal/lifecycle` package. The `SetReadOnly` flag on PodStore prevents new pods during drain.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU).
