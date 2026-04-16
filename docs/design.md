# Spark Architecture

Single-binary Go pod orchestrator for single-node GPU hosts. Reads K8s-compatible manifests and runs workloads via podman.

## Package Layout

```
cmd/spark/          Entry point: flags, startup, signal handling
internal/
  api/              HTTP REST API handlers (health, resources, node, pod CRUD, exec, logs, events, metrics, images, cronjobs, auth)
  bus/              NATS bus abstraction, protocol handlers, event/log publishers
  cron/             Cron expression parser and scheduled job trigger
  metrics/          Prometheus metrics collector and text renderer
  executor/         Podman interface: pod create, exec, stop, logs, image list/pull, stats, port mapping, init containers, liveness probes (exec/HTTP)
  gpu/              GPU detection (nvidia-smi), device ID enumeration, and system resource detection
  lifecycle/        Graceful shutdown coordinator with pod draining
  manifest/         K8s YAML parser (Pod, Job, CronJob, Deployment, StatefulSet) with ports, init containers, securityContext, livenessProbe
  reconciler/       Desired-state reconciliation loop, pod recovery, resource sync, stuck pod recovery (StatusScheduled/StatusPreempted), liveness probe polling
  scheduler/        Resource-aware scheduling with priority preemption, GPU count-based device slot tracking
  state/            Pod state store (in-memory + SQLite WAL persistence) with source path tracking
  watcher/          Manifest directory poller (SHA-256 change detection)
```

## Data Flow

1. Manifest arrives via NATS (`req.spark.apply`), HTTP API (`POST /api/v1/pods`), or filesystem watcher.
2. Parser extracts PodSpec(s) from the manifest kind.
3. Scheduler checks resource availability (CPU, memory, GPU devices); preempts lower-priority pods if needed.
4. Executor runs init containers sequentially, then creates main containers in podman pod(s) on the shared `spark-net` network with port mappings.
5. State store tracks desired vs actual state.
6. Reconciler (5s loop) restarts crashed services and retries failed jobs. Recovers stuck StatusScheduled/StatusPreempted pods. Tracks restart counts. Polls liveness probes (exec/HTTP) for running pods; restarts on failure threshold.
7. Events, logs, and heartbeats publish over NATS.
8. Manifest file removal triggers pod stop, resource release, and cron job unregistration.

## Key Invariants

- Resource accounting: allocated = sum of running pod requests. Available = allocatable - allocated.
- Preemption: only lower-priority pods are evictable. Equal priority never preempts. Anti-thrash: 3 preemptions in 5 minutes skips the victim.
- Reconciler never stops service pods on spark shutdown. Service pods persist in podman across restarts.
- All pods join `spark-net` for name-based pod-to-pod communication.
- CronJob concurrency policies: Allow (always create), Forbid (skip if active), Replace (delete active, create new).
- GPU device isolation: scheduler assigns specific device IDs via `NVIDIA_VISIBLE_DEVICES`; `--gpu-max` limits concurrent GPU pods.
- Init containers run sequentially to completion before main containers start. Any init failure aborts the pod.
- Container ports declared in manifests are mapped via `podman pod create --publish`.
- SecurityContext: runAsUser, privileged, capabilities (add/drop) are forwarded to podman container args.
- Delete (HTTP, NATS, manifest removal) releases scheduler resources immediately.
- CronJob registration works on all ingestion paths: NATS apply, HTTP apply, filesystem watcher.
- StreamPodLogs properly reaps child processes via cmd.Wait() on context cancellation.
- GPU resource model: `nvidia.com/gpu: N` allocates N device slots (GPUCount), distinct from GPU memory. Scheduler tracks count-based allocation.
- Liveness probes: exec probes run via `podman exec`; HTTP probes via stdlib `net/http`. Reconciler polls on each tick respecting InitialDelaySeconds, PeriodSeconds, FailureThreshold.
- CronJob HTTP management: GET /api/v1/cronjobs (list), GET /api/v1/cronjobs/{name} (detail), DELETE /api/v1/cronjobs/{name} (unregister).
- Node info: GET /api/v1/node exposes hostname, OS, arch, CPU cores, memory, GPU model, GPU count, device IDs, GPU memory.
- Reconciler treats `no such pod` from `podman pod inspect` on a `StatusScheduled` record as authoritative (pod is missing). After `scheduledStaleness` (10s) the record transitions back to `StatusPending`, respecting `BackoffLimit`. Transient inspect errors leave the record alone.

## Interfaces

- **HTTP**: GET /healthz, GET /metrics, GET /api/v1/resources, GET /api/v1/node, GET /api/v1/pods, GET /api/v1/pods/{name}, GET /api/v1/pods/{name}/logs, GET /api/v1/pods/{name}/events, POST /api/v1/pods, POST /api/v1/pods/{name}/exec, DELETE /api/v1/pods/{name}, GET /api/v1/images, POST /api/v1/images/pull, GET /api/v1/cronjobs, GET /api/v1/cronjobs/{name}, DELETE /api/v1/cronjobs/{name}. Bearer token auth middleware (optional via --api-token-file; /healthz and /metrics exempt).
- **NATS**: req.spark.{apply,delete,get,list}, evt.spark.{event}.{pod}, log.spark.{pod}, heartbeat.spark.{node}
- **Filesystem**: manifest directory watch (poll every 5s, SHA-256 checksums)
- **Podman CLI**: pod create (--publish), pod stop, pod rm, pod logs, pod stats, exec, run (init containers), image exists, image pull, image list (--format json), network create

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
- ADR-010: Prometheus metrics via stdlib (text exposition format, no client_golang)
- ADR-011: HTTP bearer token authentication (file-based token, exempt /healthz and /metrics)
