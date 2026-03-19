# Spark Architecture

Single-binary Go pod orchestrator for single-node GPU hosts. Reads K8s-compatible manifests and runs workloads via podman.

## Package Layout

```
cmd/spark/          Entry point: flags, startup, signal handling
internal/
  api/              HTTP REST API handlers (health, resources, pod CRUD)
  bus/              NATS bus abstraction, protocol handlers, event/log publishers
  cron/             Cron expression parser and scheduled job trigger
  executor/         Podman interface: pod create, stop, logs, image pull, stats
  gpu/              GPU detection (nvidia-smi) and system resource detection
  lifecycle/        Graceful shutdown coordinator with pod draining
  manifest/         K8s YAML parser (Pod, Job, CronJob, Deployment, StatefulSet)
  reconciler/       Desired-state reconciliation loop, pod recovery, resource sync
  scheduler/        Resource-aware scheduling with priority preemption
  state/            Pod state store (in-memory + SQLite WAL persistence)
  watcher/          Manifest directory poller (SHA-256 change detection)
```

## Data Flow

1. Manifest arrives via NATS (`req.spark.apply`), HTTP API (`POST /api/v1/pods`), or filesystem watcher.
2. Parser extracts PodSpec(s) from the manifest kind.
3. Scheduler checks resource availability; preempts lower-priority pods if needed.
4. Executor creates podman pod(s) on the shared `spark-net` network.
5. State store tracks desired vs actual state.
6. Reconciler (5s loop) restarts crashed services and retries failed jobs.
7. Events, logs, and heartbeats publish over NATS.

## Key Invariants

- Resource accounting: allocated = sum of running pod requests. Available = allocatable - allocated.
- Preemption: only lower-priority pods are evictable. Equal priority never preempts. Anti-thrash: 3 preemptions in 5 minutes skips the victim.
- Reconciler never stops service pods on spark shutdown. Service pods persist in podman across restarts.
- All pods join `spark-net` for name-based pod-to-pod communication.
- CronJob concurrency policies: Allow (always create), Forbid (skip if active), Replace (delete active, create new).

## Interfaces

- **HTTP**: GET /healthz, GET /api/v1/resources, GET /api/v1/pods, GET /api/v1/pods/{name}, POST /api/v1/pods, DELETE /api/v1/pods/{name}
- **NATS**: req.spark.{apply,delete,get,list}, evt.spark.{event}.{pod}, log.spark.{pod}, heartbeat.spark.{node}
- **Filesystem**: manifest directory watch (poll every 5s, SHA-256 checksums)
- **Podman CLI**: pod create, pod stop, pod rm, pod logs, pod stats, image exists, image pull, network create

## Decisions

See docs/adr/ for rationale:
- ADR-001: Go standard library only (except nats.go)
- ADR-002: NATS protocol design
- ADR-003: Local OCI registry
- ADR-004: K8s manifest compatibility (subset, not full API)
- ADR-005: Priority preemption algorithm
- ADR-006: Resource-aware scheduling
- ADR-007: Ubuntu deb packaging via goreleaser and nfpm
- ADR-008: SQLite state persistence (modernc.org/sqlite, WAL mode)
- ADR-009: HTTP API design (net/http.ServeMux, Go 1.22+ patterns)
