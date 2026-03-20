# Spark: Wiring Integrity and Reconciler Hardening (v1.5.0)

## Status: Complete

## Context

### Problem Statement

Spark v1.4.0 has 36 wired use cases across 13 packages. A deep audit of the codebase reveals 7 broken wiring paths and 2 operational defects that undermine reliability for production ML workloads on the DGX:

1. **CronJob registration broken on NATS and HTTP.** The NATS `req.spark.apply` handler and the HTTP `POST /api/v1/pods` handler both parse CronJobs from manifests but never call `cronSched.Register()`. CronJobs submitted via NATS or HTTP are silently accepted but never scheduled. Only the filesystem watcher path registers cron jobs.

2. **Manifest file removal is a no-op.** The watcher emits a `Removed` event when a manifest file is deleted, but `main.go:308` only logs a message. Pods created from the removed manifest continue running. Cron jobs remain registered. There is no way to undeploy workloads by removing their manifest files.

3. **HTTP/NATS delete does not release scheduler resources.** The `handleDeletePod` HTTP handler and the NATS delete handler call `StopPod` and `RemovePod` but never call `scheduler.RemovePod()` or `tracker.Release()`. Deleted pods leave their CPU/memory/GPU allocations permanently consumed until the next Spark restart.

4. **Restart counter never incremented.** `PodRecord.Restarts` is declared, persisted in SQLite, surfaced in the API, and emitted as `spark_pod_restarts_total` in Prometheus metrics, but it is never incremented. The reconciler re-queues pods to `StatusPending` on restart but does not call any increment function. The metric always reports 0.

5. **StatusScheduled and StatusPreempted pods stuck forever.** The reconciler's `reconcileOnce()` only handles `StatusPending` and `StatusRunning`. A pod that reaches `StatusScheduled` (between scheduling and creation) and then has its `CreatePod` call panic or timeout remains stuck. A preempted pod stays in `StatusPreempted` indefinitely with no path back to `StatusPending`.

6. **StreamPodLogs process not Wait()-ed.** `StreamPodLogs` starts a `podman pod logs --follow` process but does not call `cmd.Wait()` after the context closes. The process may become a zombie until the Go runtime garbage-collects it.

7. **parseMemory missing Ki suffix.** `parseMemory()` handles Gi, Mi, G, and M suffixes but not Ki. A manifest using `512Ki` parses as 0 MB. This is an uncommon unit for ML workloads but breaks K8s compatibility.

### Objectives

1. Fix all 7 broken wiring paths so that CronJobs, deletes, restarts, and stuck pods work correctly.
2. Fix the 2 operational defects (zombie processes, memory parsing).
3. Add securityContext passthrough for privileged ML containers.
4. Reach zero broken use cases in the manifest.

### Non-Goals

- envFrom / configMapRef / secretRef support (v1.6.0 -- requires new ConfigMap/Secret store).
- CronJob HTTP management endpoints (v1.6.0 -- depends on stable cron registration first).
- Liveness/readiness probes (significant new subsystem, not a wiring fix).
- GPU count-to-memory normalization (requires design decision on resource model; tracked separately).

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (CGO_ENABLED=0). See ADR-001.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns. See ADR-009.
- Standard `flag` package for CLI flags.
- Podman CLI only (no podman API socket).

### Success Metrics

- `go test ./... -race -timeout 120s` passes with all new tests.
- CronJob submitted via `nats pub req.spark.apply` triggers on schedule.
- CronJob submitted via `curl -X POST /api/v1/pods` triggers on schedule.
- Deleting a manifest file from the watch directory stops the corresponding pod.
- `curl -X DELETE /api/v1/pods/myapp` releases resources visible in `GET /api/v1/resources`.
- Pod restart counter increments on reconciler re-queue, visible in `GET /api/v1/pods/{name}` and `/metrics`.
- Preempted pod returns to Pending on next reconcile pass.
- `podman ps` shows no zombie `podman pod logs` processes after SSE stream disconnects.

## Discovery Summary

**Current architecture** (from docs/design.md):
- 13 packages, 36 use cases (all WIRED prior to this audit).
- HTTP API: 12 endpoints + auth middleware + Prometheus metrics on :8080.
- NATS: 4 request/reply subjects + events + logs + heartbeat.
- Filesystem: manifest directory watcher with SHA-256 change detection.

**Gaps discovered** (7 broken, 2 defects):
- UC-037: NATS apply ignores CronJobs (never calls Register).
- UC-038: HTTP apply ignores CronJobs (never calls Register).
- UC-039: Watcher Removed event is a no-op.
- UC-040: Delete handlers do not release scheduler resources.
- UC-041: Restarts counter never incremented.
- UC-042: StatusScheduled/StatusPreempted pods stuck forever.
- UC-046: StreamPodLogs zombie processes.
- parseMemory missing Ki suffix (breaks K8s compat).
- securityContext not parsed (needed for privileged ML containers).

**Use case manifest**: .claude/scratch/usecases-manifest.json (46 total: 36 WIRED, 7 BROKEN, 3 PLANNED).

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | CronJob registration on all ingestion paths | CronJobs register via NATS, HTTP, and filesystem |
| D2 | Manifest removal triggers pod deletion | Removing a file stops pods and unregisters cron jobs |
| D3 | Delete releases scheduler resources | Resources freed immediately on delete |
| D4 | Restart counter works | Restarts increments on re-queue, visible in API and metrics |
| D5 | Stuck pod recovery | StatusScheduled and StatusPreempted pods recover |
| D6 | StreamPodLogs cleanup | No zombie processes after SSE disconnect |
| D7 | parseMemory Ki support | 512Ki parses as 0 MB (0.5 MB, rounds to 0), 1024Ki parses as 1 MB |
| D8 | securityContext passthrough | runAsUser, privileged, capabilities forwarded to podman |

### Out of Scope

- envFrom / configMapRef / secretRef support
- CronJob HTTP management endpoints
- Liveness/readiness probes
- GPU count-to-memory normalization

## Checkable Work Breakdown

### E40: CronJob Registration Fixes

- [x] T40.1 Wire CronJob registration into NATS apply handler  Owner: TBD  Est: 30m  verifies: [UC-037]
  - Update `internal/bus/handler_apply.go` `RegisterApplyHandler` to accept a `cronSched` parameter.
  - After iterating CronJobs, call `cronSched.Register(cj)` for each.
  - Update `main.go` to pass `cronSched` to `RegisterApplyHandler`.
  - Create tests: NATS apply with CronJob manifest calls Register.
  - Acceptance: `go test -race ./internal/bus/` passes. CronJob registered.

- [x] T40.2 Wire CronJob registration into HTTP apply handler  Owner: TBD  Est: 30m  verifies: [UC-038]
  - Add `cronSched` field to `api.Server` struct.
  - Update `handleApplyPod` to call `cronSched.Register(cj)` for each CronJob in `result.CronJobs`.
  - Update `NewServer` to accept and store `cronSched`.
  - Update `main.go` to pass `cronSched` when creating the API server.
  - Create tests: HTTP POST with CronJob manifest calls Register.
  - Acceptance: `go test -race ./internal/api/` passes.

### E41: Manifest Removal Handling

- [x] T41.1 Implement manifest removal handler in main.go  Owner: TBD  Est: 45m  verifies: [UC-039]
  - In the watcher `Removed` case in `main.go`:
    1. Track which pods and cron jobs came from each manifest file path (add a `sourcePath` field to PodRecord or maintain a map in main.go).
    2. On Removed event: look up pods by source path, call `executor.StopPod`, `executor.RemovePod`, `store.Delete`, `scheduler.RemovePod` for each.
    3. Unregister cron jobs from `cronSched` by name.
  - Add `Unregister(name string)` method to `cron.Scheduler` if it does not exist.
  - Create tests: manifest removal stops pods and unregisters cron jobs.
  - Acceptance: `go test -race ./internal/cron/` and `go build ./cmd/spark` pass.

- [x] T41.2 Add source path tracking to state store  Owner: TBD  Est: 30m  verifies: [UC-039]
  - Add `SourcePath string` field to `state.PodRecord`.
  - Set it when a pod is applied from the watcher (pass the file path through to `store.Apply`).
  - Add `ListBySourcePath(path string) []PodRecord` query method to `PodStore`.
  - Persist `SourcePath` in SQLite schema (add column, update SavePod/LoadPods).
  - Create tests for `ListBySourcePath`.
  - Acceptance: `go test -race ./internal/state/` passes.

### E42: Delete Releases Scheduler Resources

- [x] T42.1 Release scheduler resources on HTTP delete  Owner: TBD  Est: 30m  verifies: [UC-040]
  - Update `handleDeletePod` in `internal/api/pods_mutate.go` to call `s.scheduler.RemovePod(name)` after stopping the pod.
  - Add `scheduler` field to `api.Server` struct (or use an interface with RemovePod method).
  - Update `NewServer` to accept and store the scheduler reference.
  - Update `main.go` to pass the scheduler when creating the API server.
  - Create tests: delete pod, verify RemovePod called on scheduler.
  - Acceptance: `go test -race ./internal/api/` passes.

- [x] T42.2 Release scheduler resources on NATS delete  Owner: TBD  Est: 30m  verifies: [UC-040]
  - Update `internal/bus/handler_delete.go` `RegisterDeleteHandler` to accept a scheduler parameter.
  - Call `scheduler.RemovePod(name)` after stopping the pod.
  - Replace `context.TODO()` with a proper context from the bus handler.
  - Update `main.go` to pass the scheduler to `RegisterDeleteHandler`.
  - Create tests: NATS delete calls scheduler.RemovePod.
  - Acceptance: `go test -race ./internal/bus/` passes.

### E43: Restart Counter Fix

- [x] T43.1 Increment restart counter in reconciler  Owner: TBD  Est: 30m  verifies: [UC-041]
  - In `reconcileRunning` in `internal/reconciler/reconciler.go`:
    - When re-queuing a pod to `StatusPending` (policy Always or OnFailure), call `r.store.IncrementRestarts(pod.Spec.Name)`.
  - Add `IncrementRestarts(name string)` method to `state.PodStore` if it does not exist (distinct from `IncrementRetry`).
  - The method should increment `PodRecord.Restarts` and persist to SQLite.
  - Create tests: reconciler restart increments counter, verify via store.Get.
  - Acceptance: `go test -race ./internal/reconciler/` and `go test -race ./internal/state/` pass.

### E44: Stuck Pod Recovery

- [x] T44.1 Handle StatusScheduled and StatusPreempted in reconciler  Owner: TBD  Est: 45m  verifies: [UC-042]
  - In `reconcileOnce` in `internal/reconciler/reconciler.go`, add cases for:
    - `StatusScheduled`: check if the pod exists in podman. If running, update to `StatusRunning`. If not found, reset to `StatusPending` to retry scheduling.
    - `StatusPreempted`: reset to `StatusPending` so the scheduler can re-evaluate. The preempted pod will be re-scheduled when resources become available.
  - Add a staleness check: if a pod has been in `StatusScheduled` for more than 30 seconds, force reset to `StatusPending`.
  - Create tests: scheduled pod reset, preempted pod reset, stale scheduled pod timeout.
  - Acceptance: `go test -race ./internal/reconciler/` passes.

### E45: StreamPodLogs Process Cleanup

- [x] T45.1 Add cmd.Wait() to StreamPodLogs  Owner: TBD  Est: 15m  verifies: [UC-046]
  - In `internal/executor/logs.go` `StreamPodLogs`:
    - After the context is done or the scanner loop exits, call `cmd.Wait()` to reap the child process.
    - Use a deferred call or a goroutine that waits on context cancellation.
  - Create test: verify that after cancelling the context, the command process is properly waited on.
  - Acceptance: `go test -race ./internal/executor/` passes.

### E46: parseMemory Ki Suffix

- [x] T46.1 Add Ki suffix support to parseMemory  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - In `internal/manifest/yaml.go` `parseMemory`:
    - Add case for `Ki` suffix: parse value, divide by 1024 to get MB (e.g., 1024Ki = 1 MB, 512Ki = 0 MB).
    - Add case for `K` suffix: parse value, divide by 1000 to get MB.
  - Create tests: 1024Ki=1, 512Ki=0, 2048Ki=2, 1K=0, 2000K=2.
  - Acceptance: `go test -race ./internal/manifest/` passes.

### E47: Security Context Passthrough

- [x] T47.1 Add SecurityContext to ContainerSpec and parse from YAML  Owner: TBD  Est: 30m  verifies: [UC-043]
  - Add `SecurityContext` struct to `internal/manifest/types.go`:
    ```go
    type SecurityContext struct {
        RunAsUser    int
        RunAsNonRoot bool
        Privileged   bool
        AddCaps      []string // capabilities.add
        DropCaps     []string // capabilities.drop
    }
    ```
  - Add `SecurityContext *SecurityContext` field to `ContainerSpec`.
  - Update `parseContainer` in `parse.go` to extract `securityContext` from YAML.
  - Create tests: parse securityContext with all fields, partial fields, missing.
  - Acceptance: `go test -race ./internal/manifest/` passes.

- [x] T47.2 Wire security context into executor buildRunArgs  Owner: TBD  Est: 30m  verifies: [UC-043]
  - Depends on: T47.1.
  - In `internal/executor/podman.go` `buildRunArgs`:
    - If `SecurityContext.RunAsUser` is set, add `--user <uid>`.
    - If `SecurityContext.Privileged` is true, add `--privileged`.
    - For each cap in `AddCaps`, add `--cap-add <cap>`.
    - For each cap in `DropCaps`, add `--cap-drop <cap>`.
  - Create tests: verify podman args for each securityContext field.
  - Acceptance: `go test -race ./internal/executor/` passes.

### E48: Integration Wiring and Verification

- [x] T48.1 Update main.go for all new wiring  Owner: TBD  Est: 30m  verifies: [UC-037, UC-038, UC-039, UC-040, UC-041, UC-042, UC-043, UC-046]
  - Depends on: T40.1, T40.2, T41.1, T41.2, T42.1, T42.2, T43.1, T44.1, T45.1, T46.1, T47.1, T47.2.
  - Update `cmd/spark/main.go`:
    - Pass `cronSched` to `RegisterApplyHandler` and `api.NewServer`.
    - Pass `scheduler` to `api.NewServer` and `RegisterDeleteHandler`.
    - Implement manifest removal handler in watcher callback.
  - Acceptance: `go build ./...` and `go vet ./...` pass.

- [x] T48.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T48.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Acceptance: All tests pass, zero lint warnings.

- [x] T48.3 Update design docs for v1.5.0  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T48.2.
  - Update docs/design.md: note wiring fixes, securityContext support.
  - Update README.md if any new user-facing behavior.
  - Acceptance: Docs accurately reflect current system.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: CronJob Registration | T40.1, T40.2 | Wire cron scheduler into NATS and HTTP apply |
| B: Manifest Removal | T41.1, T41.2 | Handle file deletion in watcher |
| C: Delete Resources | T42.1, T42.2 | Release scheduler allocations on delete |
| D: Reconciler Fixes | T43.1, T44.1 | Restart counter + stuck pod recovery |
| E: Executor Fixes | T45.1 | StreamPodLogs process cleanup |
| F: Parser Fixes | T46.1, T47.1 | Ki suffix + securityContext parsing |
| G: Security Context | T47.2 | Wire securityContext into executor (depends on T47.1) |

Sync point: T48.1 requires all tracks to complete before integration wiring.

### Waves

### Wave 1: Independent Fixes (10 agents)
- [x] T40.1 Wire CronJob registration into NATS apply handler  verifies: [UC-037]
- [x] T40.2 Wire CronJob registration into HTTP apply handler  verifies: [UC-038]
- [x] T41.2 Add source path tracking to state store  verifies: [UC-039]
- [x] T42.1 Release scheduler resources on HTTP delete  verifies: [UC-040]
- [x] T42.2 Release scheduler resources on NATS delete  verifies: [UC-040]
- [x] T43.1 Increment restart counter in reconciler  verifies: [UC-041]
- [x] T44.1 Handle StatusScheduled and StatusPreempted in reconciler  verifies: [UC-042]
- [x] T45.1 Add cmd.Wait() to StreamPodLogs  verifies: [UC-046]
- [x] T46.1 Add Ki suffix support to parseMemory  verifies: [infrastructure]
- [x] T47.1 Add SecurityContext to ContainerSpec and parse from YAML  verifies: [UC-043]

### Wave 2: Dependent Components (2 agents)
- [x] T41.1 Implement manifest removal handler in main.go  verifies: [UC-039]
- [x] T47.2 Wire security context into executor buildRunArgs  verifies: [UC-043]

Note: T41.1 depends on T41.2 (source path tracking). T47.2 depends on T47.1 (securityContext types).

### Wave 3: Integration and Verification (3 agents)
- [x] T48.1 Update main.go for all new wiring  verifies: [UC-037, UC-038, UC-039, UC-040, UC-041, UC-042, UC-043, UC-046]
- [x] T48.2 Run full test suite and lint  verifies: [infrastructure]
- [x] T48.3 Update design docs for v1.5.0  verifies: [infrastructure]

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | CronJob registration works on all paths | T40.1, T40.2 | CronJob registers via NATS, HTTP, and filesystem |
| M2 | Delete path complete | T41.1, T41.2, T42.1, T42.2 | Delete releases resources, manifest removal stops pods |
| M3 | Reconciler hardened | T43.1, T44.1 | Restart counter works, stuck pods recover |
| M4 | Wired, tested, documented | T48.1, T48.2, T48.3 | Full integration passes, docs updated |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | Adding cronSched to NATS/HTTP handlers changes function signatures | Low | High | Keep the interface narrow: pass only the Register method, not the full scheduler. |
| R2 | Source path tracking requires SQLite schema migration | Medium | High | Add column with ALTER TABLE ADD COLUMN. SQLite supports this without full migration. |
| R3 | Manifest removal race with watcher poll interval | Low | Medium | Use store lookup by source path. If path was re-added between polls, the new manifest will re-apply. |
| R4 | StatusPreempted -> Pending creates scheduling thrash | Medium | Low | Only reset to Pending once per reconcile cycle. The scheduler's anti-thrash logic (3 preemptions in 5 min) prevents loops. |

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
- Bus tests use mock bus (no NATS dependency in CI).
- Reconciler tests use stub executor and store.

## Progress Log

### 2026-03-20: v1.5.0 complete
- Wave 1 (10 tasks): All independent fixes merged. CronJob registration, source path tracking, scheduler resource release, restart counter, stuck pod recovery, StreamPodLogs cleanup, Ki suffix, securityContext parsing.
- Wave 2 (2 tasks): Manifest removal handler and securityContext executor wiring merged.
- Wave 3 (3 tasks): Wired cronSched and scheduler into NATS/HTTP handlers in main.go. Full test suite passes. Design docs updated.
- All 15 tasks (T40.1--T48.3) complete. All 7 broken wiring paths fixed. 2 operational defects resolved. securityContext passthrough added.

### 2026-03-20: Plan created
- Audited codebase post-v1.4.0 delivery. Found 7 broken wiring paths and 2 operational defects.
- Updated use case manifest: UC-032 through UC-036 marked WIRED. Added UC-037 through UC-046 (7 BROKEN, 3 PLANNED).
- Created plan for v1.5.0: 15 tasks across 3 waves (10+2+3 agents).
- Prioritized wiring integrity over new features. Non-goals: envFrom, CronJob HTTP endpoints, probes.

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.4.0 is complete: pod exec, port mapping, init containers, GPU device assignment, image management. All 36 use cases WIRED.
- This plan fixes 7 broken wiring paths discovered during a post-v1.4.0 audit.
- The core issue: NATS and HTTP apply handlers parse CronJobs but never register them. Delete handlers do not release scheduler resources. Reconciler does not handle stuck states.
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
