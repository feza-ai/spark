# Spark: State Persistence and Crash Recovery (v1.1.0)

## Status: Complete (2026-03-19)

## Context

### Problem Statement

Spark v1.0.0 stores all pod state in memory. If spark crashes or is restarted, all state is lost: pod records, event history, resource allocations, and retry counts. Running pods survive in podman but become invisible to spark -- the scheduler cannot account for their resource usage, the reconciler cannot manage their lifecycle, and operators lose all event history.

This is the most critical gap in spark v1.0.0. Without persistence, spark cannot be trusted to manage long-running workloads on the DGX.

### Objectives

1. Persist pod state to SQLite (WAL mode) so spark survives restarts without data loss.
2. Re-discover running podman pods on startup and reconcile with persisted state.
3. Prune completed/failed pods and old events to prevent unbounded storage growth.
4. Maintain zero-downtime for existing NATS protocol -- persistence is transparent to clients.

### Non-Goals

- HTTP API (deferred to v1.2.0).
- Resource reconciliation with actual podman/nvidia-smi data (deferred to v1.2.0).
- Graceful shutdown of managed pods (deferred to v1.2.0).
- Multi-node state replication or distributed consensus.

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (pure Go, CGO_ENABLED=0). See docs/adr/008-sqlite-state-persistence.md.
- Podman CLI only (no programmatic API).
- SQLite WAL mode for crash safety. No file-based JSON/GOB (not crash-safe).
- Schema must support the full PodRecord structure: spec (JSON), status, timestamps, restarts, retry count, events.

### Success Metrics

- After `kill -9` of spark, restart recovers all pod records and event history from SQLite.
- After restart, running pods in podman are re-discovered and re-registered in the scheduler with correct resource allocations.
- Completed pods older than the retention threshold are automatically pruned.
- `go test ./... -race -timeout 120s` passes with all new tests.
- Binary size increase is under 20MB (modernc.org/sqlite overhead).

## Discovery Summary

**Current architecture** (from docs/design.md):
- State store: `internal/state/store.go` -- in-memory `map[string]*PodRecord` with RWMutex.
- Executor: `internal/executor/podman.go` -- CreatePod, StopPod, PodStatus, RemovePod. No ListPods.
- Reconciler: `internal/reconciler/reconciler.go` -- 5s tick loop over stored pods. No recovery path.
- Startup: `cmd/spark/main.go` -- creates empty PodStore, registers handlers, starts loops.

**Use cases affected**:
- All 15 existing use cases are WIRED and working. This phase adds crash resilience to all of them.
- UC-003 (resource-aware scheduling) is directly affected: scheduler must re-account for recovered pods.
- UC-005 (desired-state reconciliation) is directly affected: reconciler must handle pods discovered from podman that are not in the store.

**Key interfaces to extend**:
- `state.PodStore` -- add Checkpoint (save all to SQLite), Recover (load all from SQLite), Prune (delete old records).
- `executor.Executor` -- add ListPods (query podman for all running pods).
- `reconciler.Reconciler` -- add RecoverPods (startup routine that reconciles podman state with store).
- `cmd/spark/main.go` -- add `--state-db` and `--pod-retention` flags, wire persistence and recovery.

Decision rationale: docs/adr/008-sqlite-state-persistence.md

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | SQLite persistence layer | PodStore saves/loads all state to/from SQLite with WAL mode |
| D2 | Pod re-discovery | Executor.ListPods queries podman; Reconciler.RecoverPods reconciles on startup |
| D3 | Retention pruning | Completed/failed pods older than threshold are auto-deleted |
| D4 | Main.go wiring | New flags, persistence init, recovery on startup, prune in reconciler loop |

### Out of Scope

- HTTP API
- Resource reconciliation with actual podman/nvidia-smi data
- Graceful shutdown of managed pods
- State migration tooling (v1.1.0 starts fresh; existing in-memory state is not migrated)

## Checkable Work Breakdown

### E19: SQLite State Persistence

- [x] T19.1 Create SQLite persistence layer in internal/state  Owner: TBD  Est: 90m  verifies: [UC-003, UC-005]
  - Run `go get modernc.org/sqlite` to add dependency.
  - Create `internal/state/sqlite.go`:
    - `type SQLiteStore struct` with `*sql.DB` field.
    - `OpenSQLite(path string) (*SQLiteStore, error)` -- open DB, set WAL mode, create tables.
    - Schema: `CREATE TABLE pods (name TEXT PRIMARY KEY, spec_json TEXT NOT NULL, status TEXT NOT NULL, started_at TEXT, finished_at TEXT, restarts INTEGER DEFAULT 0, retry_count INTEGER DEFAULT 0)`.
    - Schema: `CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, pod_name TEXT NOT NULL REFERENCES pods(name) ON DELETE CASCADE, time TEXT NOT NULL, type TEXT NOT NULL, message TEXT)`.
    - `SavePod(rec PodRecord) error` -- upsert pod row (INSERT OR REPLACE). Serialize spec to JSON.
    - `SaveEvent(podName string, event PodEvent) error` -- insert event row.
    - `LoadAll() (map[string]*PodRecord, error)` -- read all pods and their events, return map.
    - `DeletePod(name string) error` -- delete pod and cascade events.
    - `PruneBefore(cutoff time.Time) (int, error)` -- delete completed/failed pods with finished_at before cutoff.
    - `Close() error`.
  - Create `internal/state/sqlite_test.go`:
    - TestOpenSQLite: open in-memory DB, verify tables exist.
    - TestSavePodAndLoadAll: save a pod, load all, verify round-trip.
    - TestSaveEvent: save events, load, verify order and content.
    - TestDeletePod: save, delete, verify cascade.
    - TestPruneBefore: save old and new pods, prune, verify only old removed.
    - Use `:memory:` for tests (no temp files needed).
  - Acceptance: `go test -race ./internal/state/` passes. Round-trip save/load preserves all PodRecord fields.

### E20: Pod Re-discovery

- [x] T20.1 Add ListPods to executor interface and podman implementation  Owner: TBD  Est: 45m  verifies: [UC-003, UC-005]
  - Add to `executor.Executor` interface: `ListPods(ctx context.Context) ([]PodInfo, error)`.
  - Define `type PodInfo struct { Name string; Running bool }`.
  - Implement in `PodmanExecutor`: run `podman pod ls --format json`, parse JSON array.
  - Each object has `Name` and `Status` fields. Map `Status == "Running"` to `Running: true`.
  - Filter out the infra pod (name contains "infra") if present.
  - Create tests in `internal/executor/podman_test.go`:
    - TestListPodsParseOutput: test JSON parsing with sample podman output.
    - Use a stub command runner (inject exec.Command) to avoid requiring podman in CI.
  - Acceptance: `go test -race ./internal/executor/` passes. ListPods returns correct PodInfo from sample output.

- [x] T20.2 Add RecoverPods to reconciler  Owner: TBD  Est: 60m  verifies: [UC-003, UC-005]
  - Add method `RecoverPods(ctx context.Context) error` to `Reconciler`.
  - Logic:
    1. Call `executor.ListPods(ctx)` to get all podman pods.
    2. For each podman pod:
       a. Check if it exists in `store` (by name).
       b. If it exists in store and status is Running: no action (already tracked).
       c. If it exists in store but status is not Running: update status to Running, re-register in scheduler.
       d. If NOT in store: log warning "orphaned pod discovered" -- do not auto-adopt (operator must re-apply manifest).
    3. For each pod in store with status Running:
       a. Check if it exists in podman (by name from ListPods result).
       b. If NOT in podman: update status to Failed ("pod not found in podman after restart").
  - Create tests in `internal/reconciler/reconciler_test.go`:
    - TestRecoverPods_PodInBothStoreAndPodman: verify no-op.
    - TestRecoverPods_PodInStoreNotPodman: verify marked Failed.
    - TestRecoverPods_PodInPodmanNotStore: verify logged as orphan, not adopted.
    - TestRecoverPods_StoreStatusMismatch: verify status corrected.
    - Use stub executor returning controlled ListPods results.
  - Acceptance: `go test -race ./internal/reconciler/` passes. Recovery correctly reconciles store vs podman state.

### E21: Retention and Cleanup

- [x] T21.1 Add Prune method to PodStore  Owner: TBD  Est: 30m  verifies: [UC-005]
  - Add `Prune(olderThan time.Duration) int` to `PodStore`.
  - Delete pods where status is Completed or Failed and FinishedAt is before `time.Now().Add(-olderThan)`.
  - Return count of pruned pods.
  - Create tests in `internal/state/store_test.go`:
    - TestPrune_RemovesOldCompleted: add completed pod with old FinishedAt, prune, verify removed.
    - TestPrune_KeepsRecent: add completed pod with recent FinishedAt, prune, verify kept.
    - TestPrune_KeepsRunning: add running pod, prune, verify kept regardless of age.
  - Acceptance: `go test -race ./internal/state/` passes. Prune removes only old terminal pods.

### E22: Integration Wiring

- [x] T22.1 Wire persistence, recovery, and pruning into main.go  Owner: TBD  Est: 60m  verifies: [UC-003, UC-005]
  - Depends on: T19.1, T20.1, T20.2, T21.1.
  - Add flags to `cmd/spark/main.go`:
    - `--state-db` (string, default `/var/lib/spark/state.db`): path to SQLite database file.
    - `--pod-retention` (duration, default `168h` / 7 days): retention for completed/failed pods.
  - Startup sequence changes (insert between steps 4 and 5 in current main.go):
    - Open SQLite: `sqlStore, err := state.OpenSQLite(*stateDB)`.
    - Load persisted state: `persisted, err := sqlStore.LoadAll()`. Populate PodStore with loaded records.
    - After scheduler created (step 5): call `rec.RecoverPods(ctx)` to reconcile podman vs store.
    - Re-register recovered Running pods in scheduler's resource tracker.
  - Persistence hooks:
    - After `store.UpdateStatus`: call `sqlStore.SavePod(record)` and `sqlStore.SaveEvent(name, event)`.
    - After `store.Apply`: call `sqlStore.SavePod(record)`.
    - After `store.Delete`: call `sqlStore.DeletePod(name)`.
    - Implement via a wrapper or callback on PodStore (add `OnChange func(name string, rec PodRecord)` field).
  - Prune integration:
    - Add prune tick in reconciler loop (every 10 minutes): call `store.Prune(retention)` then `sqlStore.PruneBefore(cutoff)`.
  - Shutdown: call `sqlStore.Close()` before NATS disconnect.
  - Acceptance: Spark starts, applies a pod, is killed with SIGKILL, restarts, and recovers the pod from SQLite. `go build ./...` and `go vet ./...` pass.

- [x] T22.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T22.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Run `staticcheck ./...` if available.
  - Acceptance: All tests pass, zero lint warnings.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: SQLite Layer | T19.1 | Persistence CRUD in internal/state |
| B: Pod Discovery | T20.1 | ListPods in internal/executor |
| C: Recovery Logic | T20.2 | RecoverPods in internal/reconciler |
| D: Retention | T21.1 | Prune in internal/state |

Sync point: T22.1 requires all tracks to complete before wiring.

### Waves

### Wave 1: Core Components (4 agents)
- [x] T19.1 Create SQLite persistence layer in internal/state  verifies: [UC-003, UC-005]
- [x] T20.1 Add ListPods to executor interface and podman implementation  verifies: [UC-003, UC-005]
- [x] T20.2 Add RecoverPods to reconciler  verifies: [UC-003, UC-005]
- [x] T21.1 Add Prune method to PodStore  verifies: [UC-005]

### Wave 2: Integration (2 agents)
- [x] T22.1 Wire persistence, recovery, and pruning into main.go  verifies: [UC-003, UC-005]
- [x] T22.2 Run full test suite and lint  verifies: [infrastructure]

Note: T22.2 depends on T22.1 but both are in Wave 2 because T22.2 runs after T22.1 completes (sequential within the wave). If running with agents, T22.2 waits for T22.1.

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Core components complete | T19.1, T20.1, T20.2, T21.1 | All four packages have passing tests |
| M2 | Wired and tested | T22.1, T22.2 | Spark persists state, recovers from crash, prunes old pods |
| M3 | Release v1.1.0 | M2 | Tag cut, .deb packages published on GitHub Releases |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | modernc.org/sqlite binary size overhead (~15MB) | Low | Certain | Acceptable for a server binary. Document in release notes. |
| R2 | Podman pod ls JSON format varies between versions | Medium | Low | Parse defensively. Test with sample output from DGX podman version. |
| R3 | SQLite WAL file grows unbounded if prune is delayed | Medium | Low | Prune every 10 min. PRAGMA wal_checkpoint(TRUNCATE) after prune. |
| R4 | Race between NATS handler writes and SQLite persistence | Medium | Medium | SQLite in WAL mode supports concurrent readers + single writer. Serialize writes via PodStore mutex. |
| R5 | RecoverPods re-registers pods with stale resource values | Medium | Medium | Use persisted spec.TotalRequests() for resource registration. Log discrepancies. |

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
- SQLite tests use `:memory:` (no temp files, no cleanup).
- Executor tests use stub command runner (no podman dependency in CI).

## Progress Log

### 2026-03-19: All tasks complete
- All 6 tasks (T19.1, T20.1, T20.2, T21.1, T22.1, T22.2) implemented and passing.
- `go build ./...`, `go vet ./...`, and `go test ./... -race -timeout 120s` all pass.
- SQLite persistence, pod recovery, retention pruning, and main.go wiring all integrated.
- 10 packages tested, zero failures, zero lint warnings.

### 2026-03-19: Plan created
- Trimmed completed E18 (.deb packaging) from plan. Knowledge preserved in docs/adr/007-ubuntu-deb-packaging.md.
- Created plan for v1.1.0: state persistence, pod recovery, retention pruning.
- Created ADR 008 (docs/adr/008-sqlite-state-persistence.md): modernc.org/sqlite chosen over JSON files, GOB, and C-based SQLite.
- 6 tasks across 2 waves. 4 agents in Wave 1 (parallel), 2 in Wave 2 (sequential).

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.0.0 is released and deployed. All 15 use cases are WIRED. .deb packaging via GoReleaser is in place.
- This plan adds crash resilience: SQLite persistence, podman pod re-discovery, retention pruning.
- Key constraint: modernc.org/sqlite is pure Go (CGO_ENABLED=0). This is critical for the cross-compilation pipeline.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU).
- The PodStore interface is stable. Persistence is added as a wrapper/callback, not a rewrite.
- Executor interface gains one new method (ListPods). Existing methods are unchanged.
- Reconciler gains one new method (RecoverPods). The existing Run loop is unchanged.
