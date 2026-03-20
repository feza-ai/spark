# Spark: GPU Resource Model, Liveness Probes, and CronJob Management (v1.6.0)

## Status: In Progress

## Context

### Problem Statement

Spark v1.5.0 has 46 wired use cases across 13 packages with zero broken wiring paths. However, three gaps limit its utility as ML infrastructure for Feza's Wolf trading system and Zerfoo inference workloads on the DGX:

1. **GPU resource model is semantically broken.** `parseGPU()` in `manifest/job.go` converts `nvidia.com/gpu: 1` (a device count) directly into `GPUMemoryMB = 1`. The scheduler then compares this 1 MB value against the total GPU memory (273,066 MB on DGX Spark). Every GPU request trivially fits, making resource tracking meaningless for multi-GPU workloads. The `GPUMemoryMB` field in `ResourceList` conflates device count and memory. This must be separated: `nvidia.com/gpu: N` should allocate N device slots, tracked as `GPUCount`, distinct from memory-based scheduling.

2. **No liveness probes.** A deadlocked inference service (Zerfoo) or stalled trading microservice (Wolf) that does not exit will appear as "Running" to Spark indefinitely. The reconciler only checks if the podman process is alive, not whether the application is healthy. Without liveness probes, there is no automated recovery for stuck-but-running containers. K8s manifests with `livenessProbe` specifications are silently ignored.

3. **No CronJob HTTP management.** CronJobs can only be submitted via NATS, HTTP POST, or filesystem. There is no way to list registered cron jobs, inspect their next-run time, or unregister them via the HTTP API. Operators must read the manifest directory or query NATS directly.

4. **No node info endpoint.** `GET /api/v1/resources` returns CPU/memory/GPU memory totals but not GPU model, device count, device IDs, or OS info. ML workload operators need a `GET /api/v1/node` endpoint to see the full hardware picture for scheduling decisions.

### Objectives

1. Fix the GPU resource model: separate device count from memory, track `GPUCount` in `ResourceList` and the scheduler.
2. Add liveness probe support: parse probe specs from manifests, poll running containers, restart on failure.
3. Add CronJob HTTP management endpoints: list, inspect, unregister.
4. Add a node info HTTP endpoint for GPU/hardware observability.

### Non-Goals

- Readiness probes (no service discovery or load balancing in single-node Spark; deferred to v1.7.0).
- envFrom / ConfigMap / Secret support (requires new package and manifest kinds; deferred to v1.7.0).
- TCP socket probes (exec and HTTP probes cover ML workload needs; TCP deferred).
- Multi-node scheduling or node affinity.

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (CGO_ENABLED=0). See ADR-001.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns. See ADR-009.
- Standard `flag` package for CLI flags.
- Podman CLI only (no podman API socket).
- Liveness probes execute via `podman exec` (exec probes) or stdlib `net/http` (HTTP probes).

### Success Metrics

- `nvidia.com/gpu: 2` in a manifest allocates 2 device slots, not 2 MB of GPU memory.
- A pod with `livenessProbe: {exec: {command: ["cat", "/tmp/healthy"]}}` restarts when the probe fails.
- `curl localhost:8080/api/v1/cronjobs` returns a JSON list of registered cron jobs with next-run times.
- `curl localhost:8080/api/v1/node` returns GPU model, device count, device IDs, CPU, memory.
- `go test ./... -race -timeout 120s` passes with all new tests.

## Discovery Summary

**Current architecture** (from docs/design.md):
- 13 packages, 46 use cases (all WIRED after v1.5.0).
- HTTP API: 12 endpoints + auth middleware + Prometheus metrics on :8080.
- NATS: 4 request/reply subjects + events + logs + heartbeat.
- Filesystem: manifest directory watcher with SHA-256 change detection.

**Gaps discovered**:
- UC-047: GPU count-based scheduling (resource model conflates count and memory).
- UC-048: Liveness probe polling (no health check for running containers).
- UC-045: CronJob HTTP management (no list/inspect/unregister endpoints).
- UC-050: Node info HTTP endpoint (no hardware detail exposed).

**Use case manifest**: .claude/scratch/usecases-manifest.json (50 total: 46 WIRED, 4 PLANNED).

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | GPU resource model fix | GPUCount field in ResourceList, parseGPU returns count, scheduler allocates by count |
| D2 | Liveness probe support | Parse livenessProbe from manifests, reconciler polls probes, restart on failure |
| D3 | CronJob HTTP management | GET /api/v1/cronjobs, GET /api/v1/cronjobs/{name}, DELETE /api/v1/cronjobs/{name} |
| D4 | Node info endpoint | GET /api/v1/node returns GPU model, device count, device IDs, CPU, memory, OS |

### Out of Scope

- Readiness probes
- envFrom / ConfigMap / Secret support
- TCP socket probes
- Multi-node scheduling

## Checkable Work Breakdown

### E49: GPU Resource Model Fix

- [ ] T49.1 Add GPUCount to ResourceList and refactor parseGPU  Owner: TBD  Est: 45m  verifies: [UC-047]
  - Add `GPUCount int` field to `manifest.ResourceList` in `internal/manifest/types.go`.
  - Update `parseGPU()` in `internal/manifest/job.go` to set `GPUCount` instead of `GPUMemoryMB`.
  - Update `TotalRequests()` to sum `GPUCount` across containers.
  - Create tests: `nvidia.com/gpu: 2` produces `GPUCount=2, GPUMemoryMB=0`.
  - Acceptance: `go test -race ./internal/manifest/` passes.

- [ ] T49.2 Update scheduler to track GPU count  Owner: TBD  Est: 45m  verifies: [UC-047]
  - Update `scheduler.Resources` to include `GPUCount int`.
  - Update `ResourceTracker.Allocate` to check `GPUCount` against `gpuMax` and available device slots.
  - Update `ResourceTracker.Release` to free device slots by count.
  - Keep `GPUMemoryMB` for future memory-based scheduling but do not use it for count-based allocation.
  - Create tests: allocate 2 GPUs, verify 2 device slots consumed. Exceed gpuMax, verify rejection.
  - Acceptance: `go test -race ./internal/scheduler/` passes.

- [ ] T49.3 Update reconciler and heartbeat for GPU count  Owner: TBD  Est: 30m  verifies: [UC-047]
  - Update `ResourceReconciler` to use `GPUCount` for drift detection.
  - Update heartbeat payload to include `gpuCount` alongside `gpuMemoryMB`.
  - Update `reconciler/resources.go` to compare allocated GPU count vs actual.
  - Create tests.
  - Acceptance: `go test -race ./internal/reconciler/` passes.

### E50: Liveness Probe Support

- [ ] T50.1 Add ProbeSpec to ContainerSpec and parse from YAML  Owner: TBD  Est: 30m  verifies: [UC-048]
  - Add to `internal/manifest/types.go`:
    ```
    type ProbeSpec struct {
        Exec                *ExecProbe
        HTTPGet             *HTTPGetProbe
        InitialDelaySeconds int
        PeriodSeconds       int  // default 10
        FailureThreshold    int  // default 3
        TimeoutSeconds      int  // default 1
    }
    type ExecProbe struct { Command []string }
    type HTTPGetProbe struct { Path string; Port int }
    ```
  - Add `LivenessProbe *ProbeSpec` field to `ContainerSpec`.
  - Update `parseContainer` in `parse.go` / `job.go` to extract `livenessProbe` from YAML.
  - Create tests: parse exec probe, HTTP probe, missing probe (nil), default values.
  - Acceptance: `go test -race ./internal/manifest/` passes.

- [ ] T50.2 Add probe executor methods  Owner: TBD  Est: 45m  verifies: [UC-048]
  - Add to `internal/executor`:
    - `ExecProbe(ctx, podName, containerName, command) (exitCode int, err error)` -- runs `podman exec` with timeout.
    - `HTTPProbe(ctx, port int, path string, timeout time.Duration) error` -- makes HTTP GET to localhost:port/path.
  - Both methods return nil on success, error on failure.
  - Create tests with stubs.
  - Acceptance: `go test -race ./internal/executor/` passes.

- [ ] T50.3 Add probe poller to reconciler  Owner: TBD  Est: 60m  verifies: [UC-048]
  - Depends on: T50.1, T50.2.
  - In `internal/reconciler`, add a `probePoller` that:
    1. For each Running pod with a LivenessProbe, tracks consecutive failures.
    2. After `InitialDelaySeconds`, polls every `PeriodSeconds`.
    3. On `FailureThreshold` consecutive failures, calls `executor.StopPod`, then sets pod to `StatusPending` (triggering restart via reconciler).
    4. Resets failure count on successful probe.
  - The poller runs as part of `reconcileRunning` -- check probes for each running pod on each reconcile tick.
  - Track probe state per pod in a `map[string]*probeState` on the Reconciler struct.
  - Create tests: probe passes (no restart), probe fails N times (restart triggered), initial delay respected.
  - Acceptance: `go test -race ./internal/reconciler/` passes.

### E51: CronJob HTTP Management

- [ ] T51.1 Add List method to CronScheduler  Owner: TBD  Est: 30m  verifies: [UC-045]
  - Add `CronJobStatus` struct to `internal/cron/scheduler.go`:
    ```
    type CronJobStatus struct {
        Name     string
        Schedule string
        LastRun  time.Time
        NextRun  time.Time
        RunCount int
    }
    ```
  - Add `List() []CronJobStatus` method to `CronScheduler`.
  - Add `Get(name string) (CronJobStatus, bool)` method.
  - Create tests.
  - Acceptance: `go test -race ./internal/cron/` passes.

- [ ] T51.2 Add CronJob HTTP endpoints  Owner: TBD  Est: 30m  verifies: [UC-045]
  - Depends on: T51.1.
  - Create `internal/api/cronjobs.go`:
    - `GET /api/v1/cronjobs` -- lists all registered cron jobs as JSON array.
    - `GET /api/v1/cronjobs/{name}` -- returns single cron job detail (404 if not found).
    - `DELETE /api/v1/cronjobs/{name}` -- unregisters cron job (calls cronSched.Unregister).
  - Extend the CronRegisterer interface in api to include List/Get/Unregister, or define a broader CronManager interface.
  - Register routes in server.go.
  - Create tests.
  - Acceptance: `go test -race ./internal/api/` passes.

### E52: Node Info Endpoint

- [ ] T52.1 Add node info HTTP endpoint  Owner: TBD  Est: 30m  verifies: [UC-050]
  - Create `internal/api/node.go`:
    - `GET /api/v1/node` returns JSON:
      ```json
      {
        "hostname": "dgx-spark",
        "os": "linux",
        "arch": "arm64",
        "cpu_cores": 72,
        "memory_total_mb": 131072,
        "gpu_model": "NVIDIA GH200",
        "gpu_count": 1,
        "gpu_device_ids": [0],
        "gpu_memory_mb": 131072
      }
      ```
  - Server needs access to `gpu.GPUInfo` and `gpu.SystemInfo` (pass at construction or store on Server struct).
  - Register route in server.go.
  - Create tests.
  - Acceptance: `go test -race ./internal/api/` passes.

### E53: Integration Wiring and Verification

- [ ] T53.1 Wire GPU count into main.go and reconciler  Owner: TBD  Est: 30m  verifies: [UC-047, UC-048, UC-045, UC-050]
  - Depends on: T49.1, T49.2, T49.3, T50.1, T50.2, T50.3, T51.1, T51.2, T52.1.
  - Update `cmd/spark/main.go`:
    - Pass GPUCount through to scheduler.
    - Pass GPUInfo and SystemInfo to api.NewServer for node endpoint.
    - Wire CronManager interface to api.NewServer.
  - Acceptance: `go build ./...` and `go vet ./...` pass.

- [ ] T53.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T53.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Acceptance: All tests pass, zero lint warnings.

- [ ] T53.3 Update design docs for v1.6.0  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T53.2.
  - Update docs/design.md: GPU count model, liveness probes, CronJob endpoints, node endpoint.
  - Update README.md: new endpoints documentation.
  - Acceptance: Docs accurately reflect current system.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: GPU Model | T49.1, T49.2, T49.3 | Resource model fix across manifest, scheduler, reconciler |
| B: Probes | T50.1, T50.2, T50.3 | Probe types, executor methods, reconciler poller |
| C: CronJob Mgmt | T51.1, T51.2 | CronScheduler List + HTTP endpoints |
| D: Node Info | T52.1 | Node info HTTP endpoint |

Sync point: T53.1 requires all tracks to complete before integration wiring.

### Waves

### Wave 1: Independent Components (8 agents)
- [ ] T49.1 Add GPUCount to ResourceList and refactor parseGPU  verifies: [UC-047]
- [ ] T49.2 Update scheduler to track GPU count  verifies: [UC-047]
- [ ] T50.1 Add ProbeSpec to ContainerSpec and parse from YAML  verifies: [UC-048]
- [ ] T50.2 Add probe executor methods  verifies: [UC-048]
- [ ] T51.1 Add List method to CronScheduler  verifies: [UC-045]
- [ ] T51.2 Add CronJob HTTP endpoints  verifies: [UC-045]
- [ ] T52.1 Add node info HTTP endpoint  verifies: [UC-050]
- [ ] T49.3 Update reconciler and heartbeat for GPU count  verifies: [UC-047]

### Wave 2: Dependent Components (1 agent)
- [ ] T50.3 Add probe poller to reconciler  verifies: [UC-048]

Note: T50.3 depends on T50.1 (probe types) and T50.2 (probe executor). T51.2 depends on T51.1 but can run in Wave 1 if assigned to the same agent sequentially.

### Wave 3: Integration and Verification (3 agents)
- [ ] T53.1 Wire GPU count into main.go and reconciler  verifies: [UC-047, UC-048, UC-045, UC-050]
- [ ] T53.2 Run full test suite and lint  verifies: [infrastructure]
- [ ] T53.3 Update design docs for v1.6.0  verifies: [infrastructure]

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | GPU model correct | T49.1, T49.2, T49.3 | nvidia.com/gpu:2 allocates 2 device slots |
| M2 | Liveness probes functional | T50.1, T50.2, T50.3 | Failed probe restarts container |
| M3 | CronJob management | T51.1, T51.2 | GET /api/v1/cronjobs returns registered jobs |
| M4 | Wired, tested, documented | T53.1, T53.2, T53.3 | Full integration passes, docs updated |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | Changing GPUMemoryMB semantics breaks existing manifests | High | Medium | Keep GPUMemoryMB field but stop populating it from parseGPU. Existing manifests using nvidia.com/gpu will now use GPUCount. No behavior change for manifests not specifying GPU. |
| R2 | Liveness probe polling adds overhead to reconciler loop | Medium | Low | Probes only checked for pods with LivenessProbe defined. Skip pods without probes. Poll interval >= 10s. |
| R3 | HTTP probe to container localhost may fail due to network namespace | Medium | Medium | Containers in podman pods share the pod network. Use the pod's published port for HTTP probes, or use exec probes as the default recommendation. |
| R4 | CronJob List() races with Register/Unregister | Low | Medium | CronScheduler already uses a mutex. List() acquires the same lock. |

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
- Probe tests use stub executor (no podman dependency in CI).
- GPU tests verify count-based allocation with mock device IDs.

## Progress Log

### 2026-03-20: Plan created
- Trimmed completed v1.5.0 plan. v1.5.0 knowledge preserved in docs/devlog.md (2026-03-20 entry) and docs/design.md.
- Updated use case manifest: UC-037 through UC-046 marked WIRED. Added UC-047 through UC-050 as PLANNED.
- Created plan for v1.6.0: GPU resource model fix, liveness probes, CronJob HTTP management, node info endpoint.
- 12 tasks across 3 waves. 8 agents in Wave 1, 1 in Wave 2, 3 in Wave 3.

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.5.0 is complete: all 46 use cases WIRED. Zero broken wiring paths.
- This plan adds four features: GPU count model, liveness probes, CronJob HTTP management, node info endpoint.
- GPU model fix: `parseGPU()` currently returns raw count into `GPUMemoryMB`. Must add `GPUCount` field and use it instead.
- Liveness probes: exec probes use `podman exec`, HTTP probes use stdlib `net/http`. Reconciler polls on each 5s tick.
- CronJob management: `CronScheduler` needs `List()` and `Get()` methods. Three new HTTP routes.
- Node info: expose `gpu.GPUInfo` and `gpu.SystemInfo` via `GET /api/v1/node`.
- CI/CD is operational: GitHub Actions with build/test/vet/staticcheck + release-please + goreleaser.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU, 1 GPU 128GB unified memory).

### Completed Versions

| Version | Features | Use Cases |
|---------|----------|-----------|
| v1.0.0 | Core orchestrator: podman executor, NATS bus, manifest parser, reconciler, scheduler, watcher | UC-001 through UC-010 |
| v1.1.0 | SQLite persistence, pod recovery, retention pruning | UC-011 through UC-017 |
| v1.2.0 | HTTP REST API, priority preemption, CronJob, Deployment, StatefulSet | UC-018 through UC-025 |
| v1.3.0 | Prometheus metrics, HTTP auth, pod logs/events, JSON logging, emptyDir volumes | UC-026 through UC-031 |
| v1.4.0 | Pod exec, port mapping, init containers, GPU device assignment, image management | UC-032 through UC-036 |
| v1.5.0 | Wiring integrity: CronJob registration on all paths, manifest removal, delete releases resources, restart counter, stuck pod recovery, StreamPodLogs cleanup, Ki suffix, securityContext passthrough | UC-037 through UC-046 |
