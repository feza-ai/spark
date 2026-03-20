# Spark: Observability, Security, and Operational Maturity (v1.3.0)

## Status: In Progress

## Context

### Problem Statement

Spark v1.2.0 is a fully functional pod orchestrator with NATS messaging, HTTP REST API, SQLite persistence, resource reconciliation, and graceful shutdown. All 25 use cases are wired and verified. However, it has five operational gaps:

1. **No Prometheus metrics.** There is no /metrics endpoint for Prometheus scraping. GPU utilization, pod failure rates, scheduling throughput, and resource pressure cannot be monitored via Grafana or trigger alerts. On a production DGX running ML training jobs, blind spots in GPU and pod health are unacceptable.

2. **No HTTP authentication.** The HTTP API accepts requests from any network client without credentials. Any process that can reach port 8080 can apply, delete, or inspect pods. For a GPU host running trading model training, this is a security gap.

3. **No pod logs via HTTP.** Container logs are only accessible through NATS (log.spark.{pod}). Debugging a failed training job requires a NATS client library. Operators need `curl` access to pod logs for quick triage.

4. **No pod events via HTTP.** Pod lifecycle events (scheduled, running, failed, preempted) are stored in SQLite and published over NATS, but there is no HTTP endpoint to query event history. Operators cannot audit pod lifecycle without a NATS client or direct SQLite access.

5. **No structured log output.** Spark uses slog with the default text handler. Log aggregation systems (CloudWatch, ELK, Loki) require JSON-formatted logs for structured querying. The log format is not configurable.

6. **EmptyDir volumes not functional.** The manifest parser accepts emptyDir volumes but the executor does not create temporary directories for them. Pods that need scratch space (model checkpoints, intermediate data) cannot use emptyDir.

### Objectives

1. Add a Prometheus-compatible /metrics endpoint using stdlib-only text exposition format.
2. Add bearer token authentication middleware to protect mutating and query endpoints.
3. Add HTTP endpoints for pod logs (tail and streaming) and pod events (history query).
4. Add configurable JSON log output for structured log aggregation.
5. Wire emptyDir volumes to podman tmpfs mounts with automatic cleanup.

### Non-Goals

- Per-user or role-based authorization (single shared token is sufficient for single-node).
- Histogram-based latency metrics (gauges and counters only for v1.3.0).
- WebSocket streaming (SSE is sufficient for log follow).
- Multi-node metric aggregation.
- Token rotation without restart.

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (CGO_ENABLED=0). See ADR-001.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns. See ADR-009.
- Prometheus text exposition format v0.0.4 implemented without client_golang. See ADR-010.
- Bearer token auth read from file at startup. See ADR-011.
- Standard `flag` package for CLI flags.
- Podman CLI only.

### Success Metrics

- `curl localhost:8080/metrics` returns valid Prometheus text format with node and pod metrics.
- `curl -H "Authorization: Bearer <token>" localhost:8080/api/v1/pods` returns pod list; without header returns 401.
- `curl localhost:8080/api/v1/pods/NAME/logs?tail=50` returns last 50 lines of pod logs.
- `curl localhost:8080/api/v1/pods/NAME/events` returns JSON event history.
- `./spark --log-format json` outputs JSON-structured log lines.
- EmptyDir volumes create tmpfs mounts that are cleaned up on pod removal.
- `go test ./... -race -timeout 120s` passes with all new tests.

## Discovery Summary

**Current architecture** (from docs/design.md):
- Interfaces: NATS (req.spark.*) + HTTP API (6 endpoints on :8080) + filesystem watcher.
- HTTP server: `internal/api/server.go` registers routes on `http.ServeMux`. No middleware chain -- handlers are registered directly.
- Logs: NATS only via `bus.LogStreamer` calling `executor.StreamLogs`. No HTTP log endpoint.
- Events: stored in SQLite `events` table (pod_name, time, type, message). Published over NATS. No HTTP query.
- Logging: `log/slog` with default text handler. No configuration.
- Volumes: `manifest.VolumeSpec` has `HostPath` and `EmptyDir` fields. Executor `buildRunArgs` maps hostPath volumes but ignores emptyDir (creates invalid mount with empty source path).

**Key interfaces to extend**:
- `internal/api/server.go`: add /metrics, /api/v1/pods/{name}/logs, /api/v1/pods/{name}/events routes.
- `internal/api/server.go`: wrap mux with auth middleware.
- New `internal/metrics` package: metric collection and Prometheus text rendering.
- `internal/executor/podman.go`: add `PodLogs(ctx, name, tail)` method and emptyDir tmpfs support.
- `internal/state/sqlite.go`: add `ListEvents(podName, since)` query.
- `cmd/spark/main.go`: add --api-token-file, --log-format flags.

**Use cases affected**:
- UC-026 (Prometheus metrics): new observability use case.
- UC-027 (HTTP auth): new security use case.
- UC-028 (pod logs via HTTP): extends UC-013 log streaming to HTTP.
- UC-029 (pod events via HTTP): extends UC-012 event publishing to HTTP.
- UC-030 (structured logging): operational improvement.
- UC-031 (emptyDir volumes): fixes incomplete volume support.

Decision rationale: docs/adr/010-prometheus-metrics.md, docs/adr/011-http-bearer-token-auth.md

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | Prometheus metrics endpoint | GET /metrics returns valid text exposition format with node, pod, scheduler, and HTTP metrics |
| D2 | HTTP bearer token auth | Middleware rejects unauthenticated requests with 401; /healthz and /metrics exempt |
| D3 | Pod logs via HTTP | GET /api/v1/pods/{name}/logs returns text/plain with ?tail=N and ?follow=true (SSE) |
| D4 | Pod events via HTTP | GET /api/v1/pods/{name}/events returns JSON event array with ?since= filter |
| D5 | Structured JSON logging | --log-format json switches slog to JSONHandler |
| D6 | EmptyDir volume support | EmptyDir volumes map to tmpfs mounts, cleaned up on pod removal |

### Out of Scope

- Role-based access control or per-user tokens
- Prometheus histogram metrics
- WebSocket log streaming
- Multi-node metric federation
- Token rotation without restart
- Container exec (pod exec into running containers)

## Checkable Work Breakdown

### E27: Prometheus Metrics

- [x] T27.1 Create metrics collector and text renderer in internal/metrics  Owner: TBD  Est: 60m  verifies: [UC-026]
  - Create `internal/metrics/collector.go`:
    - `type Collector struct` with `store *state.PodStore`, `tracker *scheduler.ResourceTracker`, `scheduler *scheduler.Scheduler`.
    - `NewCollector(store, tracker, scheduler) *Collector`.
    - `Collect() []MetricFamily`: gathers all metrics at call time. Returns structured data.
  - Create `internal/metrics/types.go`:
    - `type MetricFamily struct { Name, Help, Type string; Metrics []Metric }`.
    - `type Metric struct { Labels map[string]string; Value float64 }`.
  - Metrics to collect:
    - `spark_node_cpu_total_millis` (gauge): total CPU from ResourceTracker.
    - `spark_node_cpu_available_millis` (gauge): available CPU.
    - `spark_node_memory_total_mb` (gauge): total memory.
    - `spark_node_memory_available_mb` (gauge): available memory.
    - `spark_node_gpu_memory_total_mb` (gauge): total GPU memory.
    - `spark_node_gpu_memory_available_mb` (gauge): available GPU memory.
    - `spark_pods_total` (gauge, label: status): count of pods by status (Pending, Running, Completed, Failed).
    - `spark_pod_restarts_total` (counter, label: pod): total restarts per pod.
    - `spark_scheduling_attempts_total` (counter): total Schedule() calls.
    - `spark_preemptions_total` (counter): total preemption events.
  - Create `internal/metrics/render.go`:
    - `Render(families []MetricFamily) []byte`: produces Prometheus text exposition format.
    - Format: `# HELP name description\n# TYPE name type\nname{labels} value\n`.
    - Escape label values per Prometheus spec (backslash, double-quote, newline).
  - Create `internal/metrics/collector_test.go`:
    - TestCollect_PodCounts: add pods with various statuses, verify spark_pods_total values.
    - TestCollect_Resources: set tracker values, verify resource gauge values.
    - TestRender_Format: verify output matches Prometheus text format exactly.
    - TestRender_LabelEscaping: verify special characters in label values are escaped.
  - Acceptance: `go test -race ./internal/metrics/` passes. Rendered output parses with promtool if available.

- [x] T27.2 Add scheduling and preemption counters to scheduler  Owner: TBD  Est: 30m  verifies: [UC-026]
  - Add atomic counters to `scheduler.Scheduler`:
    - `scheduleAttempts int64`: incremented on every `Schedule()` call.
    - `preemptionCount int64`: incremented on every successful preemption.
  - Add getter methods: `ScheduleAttempts() int64`, `PreemptionCount() int64`.
  - Increment `scheduleAttempts` at the top of `Schedule()`.
  - Increment `preemptionCount` in the preemption execution path.
  - Create tests in `internal/scheduler/scheduler_test.go`:
    - TestScheduleAttempts: call Schedule() N times, verify counter.
    - TestPreemptionCount: trigger preemptions, verify counter.
  - Acceptance: `go test -race ./internal/scheduler/` passes. Counters are thread-safe.

- [x] T27.3 Add /metrics HTTP handler in internal/api  Owner: TBD  Est: 30m  verifies: [UC-026]
  - Add `internal/api/metrics.go`:
    - `GET /metrics` handler: calls `collector.Collect()`, renders with `metrics.Render()`, writes `text/plain; version=0.0.4; charset=utf-8`.
  - Extend `api.Server` struct to hold `*metrics.Collector`.
  - Update `NewServer` to accept collector (or nil if metrics disabled).
  - Register route: `mux.HandleFunc("GET /metrics", s.handleMetrics)`.
  - Create `internal/api/metrics_test.go`:
    - TestMetricsEndpoint: GET /metrics, verify 200, verify Content-Type, verify output contains expected metric names.
    - TestMetricsEndpoint_Unauthenticated: verify /metrics is accessible without auth token (like /healthz).
  - Acceptance: `go test -race ./internal/api/` passes. `curl /metrics` returns valid Prometheus text.

### E28: HTTP Authentication

- [x] T28.1 Create bearer token auth middleware in internal/api  Owner: TBD  Est: 45m  verifies: [UC-027]
  - Create `internal/api/auth.go`:
    - `type AuthMiddleware struct { token string }`.
    - `NewAuthMiddleware(token string) *AuthMiddleware`.
    - `Wrap(next http.Handler) http.Handler`: checks `Authorization: Bearer <token>` header.
    - If token matches, call next.ServeHTTP.
    - If token is empty string (no auth configured), call next.ServeHTTP (passthrough).
    - If token does not match or header is missing, return 401 with `{"error":"unauthorized"}`.
    - Exempt paths: /healthz, /metrics (checked by path prefix before token validation).
  - Create `internal/api/auth_test.go`:
    - TestAuth_ValidToken: request with correct Bearer token, verify 200.
    - TestAuth_InvalidToken: request with wrong token, verify 401 JSON.
    - TestAuth_MissingHeader: request without Authorization header, verify 401 JSON.
    - TestAuth_HealthzExempt: GET /healthz without token, verify 200.
    - TestAuth_MetricsExempt: GET /metrics without token, verify 200.
    - TestAuth_Disabled: empty token string, all requests pass through.
  - Acceptance: `go test -race ./internal/api/` passes. Auth middleware correctly gates access.

- [x] T28.2 Wire auth middleware into server  Owner: TBD  Est: 15m  verifies: [UC-027]
  - Depends on: T28.1.
  - Update `api.NewServer` signature to accept an optional token string parameter.
  - If token is non-empty, wrap `s.mux` with `AuthMiddleware.Wrap()` in `ServeHTTP()`.
  - Update `cmd/spark/main.go`:
    - Add `--api-token-file` flag (string, default empty): path to file containing the API token.
    - If set, read file contents at startup, trim whitespace, pass to `api.NewServer`.
    - If file does not exist or is unreadable, log error and exit.
  - Update existing `api.NewServer` call sites in tests to pass empty token.
  - Acceptance: `go build ./...` succeeds. `--api-token-file /path/to/token` enables auth.

### E29: Pod Logs via HTTP

- [x] T29.1 Add PodLogs method to executor  Owner: TBD  Est: 30m  verifies: [UC-028]
  - Add to `executor.Executor` interface: `PodLogs(ctx context.Context, name string, tail int) ([]byte, error)`.
  - Implement in `PodmanExecutor`:
    - Run `podman pod logs --tail N NAME` (if tail > 0) or `podman pod logs NAME` (all logs).
    - Return combined stdout/stderr output as bytes.
  - Add `StreamPodLogs(ctx context.Context, name string, tail int) (io.ReadCloser, error)`:
    - Run `podman pod logs --follow --tail N NAME`.
    - Return the stdout pipe of the running command.
    - Caller is responsible for closing the reader (which kills the process).
  - Create tests in `internal/executor/podman_test.go`:
    - TestPodLogs_Tail: verify correct args passed to podman (stub).
    - TestStreamPodLogs_Args: verify --follow flag included.
  - Acceptance: `go test -race ./internal/executor/` passes.

- [x] T29.2 Add pod logs HTTP endpoint in internal/api  Owner: TBD  Est: 45m  verifies: [UC-028]
  - Depends on: T29.1.
  - Create `internal/api/pods_logs.go`:
    - `GET /api/v1/pods/{name}/logs` handler.
    - Query params: `?tail=N` (default 100), `?follow=true` (default false).
    - Non-follow mode:
      1. Verify pod exists in store (404 if not).
      2. Call `executor.PodLogs(ctx, name, tail)`.
      3. Return response with Content-Type: text/plain.
    - Follow mode:
      1. Verify pod exists and is Running (400 if not running).
      2. Set headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`.
      3. Call `executor.StreamPodLogs(ctx, name, tail)`.
      4. Read lines from the reader, write as SSE `data:` events.
      5. Flush after each event.
      6. Stop when reader closes (pod exits) or client disconnects (context cancelled).
  - Register route: `mux.HandleFunc("GET /api/v1/pods/{name}/logs", s.handlePodLogs)`.
  - Create `internal/api/pods_logs_test.go`:
    - TestPodLogs_Tail: apply pod, GET /logs?tail=10, verify text/plain response.
    - TestPodLogs_NotFound: GET /logs for nonexistent pod, verify 404.
    - TestPodLogs_DefaultTail: GET /logs without ?tail, verify default 100 passed to executor.
  - Acceptance: `go test -race ./internal/api/` passes. `curl /api/v1/pods/NAME/logs?tail=50` returns log lines.

### E30: Pod Events via HTTP

- [x] T30.1 Add ListEvents query to SQLiteStore  Owner: TBD  Est: 30m  verifies: [UC-029]
  - Add to `state.SQLiteStore`:
    - `ListEvents(podName string, since time.Time) ([]Event, error)`.
    - `type Event struct { PodName string; Time time.Time; Type string; Message string }`.
    - SQL: `SELECT pod_name, time, type, message FROM events WHERE pod_name = ? AND time >= ? ORDER BY time ASC`.
    - If since is zero value, return all events for the pod.
  - Create tests in `internal/state/sqlite_test.go`:
    - TestListEvents_All: save 3 events, list all, verify count and order.
    - TestListEvents_Since: save events at different times, filter by since, verify only recent events returned.
    - TestListEvents_NoPod: list events for nonexistent pod, verify empty slice (not error).
  - Acceptance: `go test -race ./internal/state/` passes.

- [x] T30.2 Add pod events HTTP endpoint in internal/api  Owner: TBD  Est: 30m  verifies: [UC-029]
  - Depends on: T30.1.
  - Create `internal/api/pods_events.go`:
    - `GET /api/v1/pods/{name}/events` handler.
    - Query params: `?since=RFC3339` (optional time filter).
    - Verify pod exists in store (404 if not).
    - Call `sqlStore.ListEvents(name, since)`.
    - Return JSON: `{"events":[{"time":"...","type":"...","message":"..."},...]}`
    - If ?since is malformed, return 400.
  - Register route: `mux.HandleFunc("GET /api/v1/pods/{name}/events", s.handlePodEvents)`.
  - Create `internal/api/pods_events_test.go`:
    - TestPodEvents_All: apply pod, save events, GET /events, verify JSON response.
    - TestPodEvents_Since: filter by ?since, verify only matching events.
    - TestPodEvents_NotFound: GET /events for nonexistent pod, verify 404.
    - TestPodEvents_InvalidSince: malformed ?since, verify 400.
  - Acceptance: `go test -race ./internal/api/` passes.

### E31: Structured JSON Logging

- [x] T31.1 Add --log-format flag and JSON handler setup  Owner: TBD  Est: 30m  verifies: [UC-030]
  - Update `cmd/spark/main.go`:
    - Add `--log-format` flag (string, default "text"): "text" or "json".
    - Before any slog calls, configure the default logger:
      - If "json": `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))`.
      - If "text": keep default (no change).
      - If other value: log error "unknown log format" and exit.
    - Move flag parsing and log setup to the very top of main(), before any other initialization.
  - Create `cmd/spark/main_test.go` (or test via integration):
    - TestLogFormatJSON: verify JSON handler produces valid JSON lines.
    - TestLogFormatText: verify text handler produces human-readable output.
  - Acceptance: `./spark --log-format json` outputs JSON log lines to stderr. `go build ./...` succeeds.

### E32: EmptyDir Volume Support

- [x] T32.1 Add emptyDir tmpfs mount support to executor  Owner: TBD  Est: 45m  verifies: [UC-031]
  - Update `executor.buildRunArgs` in `internal/executor/podman.go`:
    - When a VolumeMount references a volume with `EmptyDir: true`:
      - Use `--mount type=tmpfs,destination=<mountPath>` instead of `--volume`.
      - If ReadOnly is true on the mount, add `,ro` option.
    - HostPath volumes continue to use `--volume` as before.
  - Update `executor.PodmanExecutor.StopPod` and `RemovePod`: no change needed since tmpfs mounts are automatically cleaned up by podman when the container is removed.
  - Create tests in `internal/executor/podman_test.go`:
    - TestBuildRunArgs_EmptyDir: verify tmpfs mount args for emptyDir volume.
    - TestBuildRunArgs_EmptyDirReadOnly: verify ro flag on tmpfs mount.
    - TestBuildRunArgs_HostPath: verify existing hostPath behavior unchanged.
    - TestBuildRunArgs_MixedVolumes: pod with both hostPath and emptyDir volumes.
  - Acceptance: `go test -race ./internal/executor/` passes. EmptyDir volumes produce correct podman args.

### E33: Integration Wiring

- [x] T33.1 Wire metrics, auth, logs, events, log format, and emptyDir into main.go  Owner: TBD  Est: 45m  verifies: [UC-026, UC-027, UC-028, UC-029, UC-030, UC-031]
  - Depends on: T27.1, T27.2, T27.3, T28.1, T28.2, T29.1, T29.2, T30.1, T30.2, T31.1, T32.1.
  - Wire metrics collector into api.Server.
  - Wire auth middleware token into api.Server.
  - Ensure all new routes are registered.
  - Verify emptyDir volumes work through the full apply path.
  - Update `deploy/spark.service` to include new flags in documentation comments.
  - Acceptance: Spark starts with all new features. `go build ./...` and `go vet ./...` pass.

- [x] T33.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T33.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Acceptance: All tests pass, zero lint warnings.

- [ ] T33.3 Update README and design docs for v1.3.0  Owner: TBD  Est: 30m  verifies: [infrastructure]
  - Depends on: T33.2.
  - Update README.md:
    - Add new CLI flags (--api-token-file, --log-format) to flags table.
    - Add /metrics endpoint documentation.
    - Add /api/v1/pods/{name}/logs and /api/v1/pods/{name}/events documentation.
    - Add authentication section with Bearer token usage example.
  - Update docs/design.md:
    - Add internal/metrics to package layout.
    - Add /metrics, /logs, /events to Interfaces section.
    - Note auth middleware in HTTP section.
  - Update CHANGELOG.md with v1.3.0 release notes if desired.
  - Acceptance: README accurately describes all v1.3.0 features.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: Prometheus Metrics | T27.1, T27.2, T27.3 | Metrics collector, counters, HTTP handler |
| B: HTTP Authentication | T28.1, T28.2 | Auth middleware and wiring |
| C: Pod Logs via HTTP | T29.1, T29.2 | Executor logs method and HTTP handler |
| D: Pod Events via HTTP | T30.1, T30.2 | SQLite query and HTTP handler |
| E: Structured Logging | T31.1 | Log format flag |
| F: EmptyDir Volumes | T32.1 | Tmpfs mount support |

Sync point: T33.1 requires all tracks to complete before wiring.

### Waves

### Wave 1: Independent Components (8 agents)
- [x] T27.1 Create metrics collector and text renderer in internal/metrics  verifies: [UC-026]
- [x] T27.2 Add scheduling and preemption counters to scheduler  verifies: [UC-026]
- [x] T28.1 Create bearer token auth middleware in internal/api  verifies: [UC-027]
- [x] T29.1 Add PodLogs method to executor  verifies: [UC-028]
- [x] T30.1 Add ListEvents query to SQLiteStore  verifies: [UC-029]
- [x] T31.1 Add --log-format flag and JSON handler setup  verifies: [UC-030]
- [x] T32.1 Add emptyDir tmpfs mount support to executor  verifies: [UC-031]
- [x] T27.3 Add /metrics HTTP handler in internal/api  verifies: [UC-026]

### Wave 2: HTTP Handlers and Wiring (4 agents)
- [x] T28.2 Wire auth middleware into server  verifies: [UC-027]
- [x] T29.2 Add pod logs HTTP endpoint in internal/api  verifies: [UC-028]
- [x] T30.2 Add pod events HTTP endpoint in internal/api  verifies: [UC-029]
- [x] T33.1 Wire metrics, auth, logs, events, log format, and emptyDir into main.go  verifies: [UC-026, UC-027, UC-028, UC-029, UC-030, UC-031]

### Wave 3: Verification (2 agents)
- [x] T33.2 Run full test suite and lint  verifies: [infrastructure]
- [ ] T33.3 Update README and design docs for v1.3.0  verifies: [infrastructure]

Note: T27.3 depends on T27.1 (needs collector) but can run in Wave 1 if T27.1 completes first or if the agent runs them sequentially. T28.2 depends on T28.1. T29.2 depends on T29.1. T30.2 depends on T30.1. T33.1 depends on all Wave 1 and Wave 2 tasks.

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Metrics collector and renderer tested | T27.1, T27.2, T27.3 | Prometheus text format output verified |
| M2 | Auth middleware tested | T28.1, T28.2 | Bearer token validation working, exempt paths verified |
| M3 | Pod logs endpoint tested | T29.1, T29.2 | Tail and streaming modes return correct output |
| M4 | Pod events endpoint tested | T30.1, T30.2 | Event query with time filtering returns correct JSON |
| M5 | Wired, tested, documented | T33.1, T33.2, T33.3 | Full integration passes, README updated |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | Prometheus text format compliance issues | Medium | Low | Test with promtool lint if available. Comprehensive unit tests with known-good format samples. |
| R2 | SSE log streaming leaks goroutines on client disconnect | High | Medium | Use request context cancellation to detect disconnect. Close podman logs process on context cancel. |
| R3 | Auth middleware bypass via path manipulation | High | Low | Use exact path matching for exempt routes. No prefix matching beyond /healthz and /metrics. |
| R4 | Large pod log output causes memory pressure | Medium | Medium | Default tail=100 limits output. Streaming mode uses io.Copy to pipe without buffering. |
| R5 | SQLite ListEvents query slow on large event tables | Low | Low | Events are pruned with pod retention. Add index on (pod_name, time) if needed. |
| R6 | EmptyDir tmpfs exhausts memory on DGX | Low | Low | tmpfs uses memory. Pods using emptyDir should declare memory requests that account for tmpfs usage. |

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
- Auth tests verify both enabled and disabled (empty token) modes.
- Metrics output tests verify exact Prometheus text format compliance.

## Progress Log

### 2026-03-19: Plan created
- Trimmed completed v1.2.0 plan. v1.2.0 knowledge already preserved in docs/design.md and docs/adr/009-http-api.md.
- Created plan for v1.3.0: observability (metrics, logs, events), security (auth), operational (JSON logging, emptyDir).
- Created ADR 010 (docs/adr/010-prometheus-metrics.md): stdlib-only Prometheus text format.
- Created ADR 011 (docs/adr/011-http-bearer-token-auth.md): bearer token auth with file-based token.
- Updated use case manifest: UC-018 through UC-025 marked WIRED. Added UC-026 through UC-031 as PLANNED.
- 14 tasks across 3 waves. 8 agents in Wave 1 (parallel), 4 in Wave 2 (parallel), 2 in Wave 3 (parallel).

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.2.0 is complete: HTTP API, resource reconciliation, graceful shutdown. All 25 use cases are WIRED.
- This plan adds six features: Prometheus metrics, HTTP auth, pod logs/events via HTTP, JSON logging, emptyDir volumes.
- Key constraint: stdlib only. Prometheus text format is hand-rendered (no client_golang). See ADR-010.
- Auth is bearer token from file (--api-token-file). If flag not set, auth is disabled. See ADR-011.
- Pod logs use `podman pod logs` for tail mode and `podman pod logs --follow` for SSE streaming.
- Pod events query the existing SQLite `events` table. A new `ListEvents` method is needed on SQLiteStore.
- EmptyDir volumes map to `--mount type=tmpfs,destination=PATH` in podman. No host directory needed.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU).

## Appendix

### Prometheus Text Exposition Format Example

```
# HELP spark_node_cpu_total_millis Total CPU capacity in millicores
# TYPE spark_node_cpu_total_millis gauge
spark_node_cpu_total_millis 20000

# HELP spark_pods_total Number of pods by status
# TYPE spark_pods_total gauge
spark_pods_total{status="Running"} 3
spark_pods_total{status="Pending"} 1
spark_pods_total{status="Completed"} 12
spark_pods_total{status="Failed"} 0
```

### Bearer Token Authentication Example

```bash
# Create token file
echo "my-secret-token" > /etc/spark/api-token

# Start spark with auth enabled
./spark --api-token-file /etc/spark/api-token

# Authenticated request
curl -H "Authorization: Bearer my-secret-token" http://localhost:8080/api/v1/pods

# Unauthenticated health check (always allowed)
curl http://localhost:8080/healthz
```

### SSE Log Streaming Example

```bash
# Stream logs (Server-Sent Events)
curl -N http://localhost:8080/api/v1/pods/myapp/logs?follow=true&tail=10
# data: 2026-03-19T10:00:01Z Starting training epoch 1...
# data: 2026-03-19T10:00:02Z Loading dataset from /data/train.csv...
```
