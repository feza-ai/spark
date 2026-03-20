# Spark

Single-binary Go pod orchestrator for GPU hosts. Accepts Kubernetes manifests and runs workloads via Podman on a single node. Connects to NATS for messaging.

## Features

- **Kubernetes manifest support** -- Pod, Job, CronJob, Deployment, StatefulSet
- **NATS messaging** -- apply, delete, get, list pods over request/reply subjects
- **HTTP REST API** -- health checks, resource queries, and pod CRUD (v1.2.0)
- **Resource-aware scheduling** -- CPU, memory, and GPU tracking with allocatable limits
- **Priority-based preemption** -- lower-priority pods evicted to make room for higher-priority work
- **SQLite state persistence** -- WAL-mode database for crash recovery (v1.1.0)
- **Pod re-discovery** -- recovers running pods from Podman after restart (v1.1.0)
- **Retention pruning** -- automatic cleanup of completed/failed pods (v1.1.0)
- **Resource reconciliation** -- periodic sync of actual vs requested CPU/memory/GPU usage (v1.2.0)
- **Graceful shutdown** -- configurable drain timeout with ordered teardown (v1.2.0)
- **GPU detection** -- NVIDIA GPU support with unified memory fallback (GB10)
- **CronJob scheduling** -- cron expression parsing with Allow/Forbid/Replace concurrency policies
- **Directory watcher** -- manifest hot-loading with SHA-256 change detection
- **Heartbeat publishing** -- periodic node status over NATS
- **Event and log streaming** -- lifecycle events and container logs published over NATS
- **Prometheus metrics** -- /metrics endpoint with node, pod, and scheduler metrics (v1.3.0)
- **HTTP authentication** -- bearer token auth with configurable token file (v1.3.0)
- **Pod logs via HTTP** -- tail and SSE streaming of pod logs (v1.3.0)
- **Pod events via HTTP** -- lifecycle event history with time filtering (v1.3.0)
- **Structured JSON logging** -- configurable log format for aggregation (v1.3.0)
- **EmptyDir volumes** -- tmpfs-backed scratch volumes for pods (v1.3.0)
- **Pod exec** -- execute commands inside running containers via HTTP (v1.4.0)
- **Container port mapping** -- expose container ports to the host via podman --publish (v1.4.0)
- **Init containers** -- sequential initialization containers before main containers (v1.4.0)
- **GPU device assignment** -- per-pod GPU device isolation via NVIDIA_VISIBLE_DEVICES (v1.4.0)
- **Image management** -- list and pull container images via HTTP API (v1.4.0)

## Quick Start

```bash
go build ./cmd/spark
./spark --nats nats://localhost:4222
```

Spark will detect system resources (CPU, memory, GPU), connect to NATS, and begin watching `/etc/spark/manifests` for Kubernetes manifests.

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--nats` | `nats://localhost:4222` | NATS server URL |
| `--node-id` | hostname | Node identifier |
| `--manifest-dir` | `/etc/spark/manifests` | Directory to watch for manifests |
| `--gpu-max` | `1` | Max concurrent GPU pods |
| `--heartbeat-interval` | `10s` | Heartbeat publish interval |
| `--reconcile-interval` | `5s` | Reconciliation loop interval |
| `--system-reserve-cpu` | `2000` | CPU millicores reserved for system |
| `--system-reserve-memory` | `4096` | MB of RAM reserved for system |
| `--state-db` | `/var/lib/spark/state.db` | Path to SQLite database file |
| `--pod-retention` | `168h` | Retention period for completed/failed pods |
| `--http-addr` | `:8080` | HTTP listen address |
| `--shutdown-timeout` | `30s` | Max time to drain pods on shutdown |
| `--reconcile-resources-interval` | `60s` | Resource reconciliation interval |
| `--log-format` | `text` | Log output format (text or json) |
| `--api-token-file` | *(empty)* | Path to file containing API bearer token |

## HTTP API

All endpoints are served on the address specified by `--http-addr` (default `:8080`).

### Health Check

```bash
curl http://localhost:8080/healthz
```

```json
{"status": "ok"}
```

### List Resources

```bash
curl http://localhost:8080/api/v1/resources
```

```json
{
  "total": {"cpu_millis": 20000, "memory_mb": 131072, "gpu_memory_mb": 131072},
  "reserved": {"cpu_millis": 2000, "memory_mb": 4096, "gpu_memory_mb": 0},
  "allocated": {"cpu_millis": 4000, "memory_mb": 8192, "gpu_memory_mb": 65536},
  "available": {"cpu_millis": 14000, "memory_mb": 118784, "gpu_memory_mb": 65536}
}
```

### List Pods

```bash
curl http://localhost:8080/api/v1/pods
```

```json
[
  {"name": "myapp", "status": "Running", "created_at": "2026-03-19T10:00:00Z"}
]
```

### Get Pod

```bash
curl http://localhost:8080/api/v1/pods/myapp
```

Returns the full pod record including spec, status, events, and timestamps.

### Apply Pod

```bash
curl -X POST http://localhost:8080/api/v1/pods \
  -H "Content-Type: application/yaml" \
  -d @pod.yaml
```

Accepts a Kubernetes manifest (YAML) in the request body. Supports Pod, Job, CronJob, Deployment, and StatefulSet kinds.

### Delete Pod

```bash
curl -X DELETE http://localhost:8080/api/v1/pods/myapp
```

```json
{"deleted": "myapp"}
```

### Prometheus Metrics

```bash
curl http://localhost:8080/metrics
```

Returns node and pod metrics in Prometheus text exposition format (v0.0.4).

### Pod Logs

```bash
curl http://localhost:8080/api/v1/pods/myapp/logs?tail=50
```

Returns the last 50 lines of pod logs as `text/plain`. Use `?follow=true` for SSE streaming.

### Pod Events

```bash
curl http://localhost:8080/api/v1/pods/myapp/events
```

Returns pod lifecycle events as JSON. Use `?since=2026-03-19T00:00:00Z` to filter by time.

### Pod Exec

```bash
curl -X POST http://localhost:8080/api/v1/pods/myapp/exec \
  -H "Content-Type: application/json" \
  -d '{"command":["ls","-la"]}'
```

```json
{"stdout":"total 0\ndrwxr-xr-x ...","stderr":"","exit_code":0}
```

### List Images

```bash
curl http://localhost:8080/api/v1/images
```

### Pull Image

```bash
curl -X POST http://localhost:8080/api/v1/images/pull \
  -H "Content-Type: application/json" \
  -d '{"image":"localhost:5000/mymodel:latest"}'
```

## Authentication

When `--api-token-file` is set, all HTTP endpoints except `/healthz` and `/metrics` require a bearer token:

```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/api/v1/pods
```

Without the header, requests return 401 Unauthorized. If `--api-token-file` is not set, authentication is disabled.

## NATS Subjects

| Subject | Purpose |
|---------|---------|
| `req.spark.apply` | Apply a pod manifest (request/reply) |
| `req.spark.delete` | Delete a pod (request/reply) |
| `req.spark.get` | Get pod status (request/reply) |
| `req.spark.list` | List all pods (request/reply) |
| `evt.spark.event.{pod}` | Pod lifecycle events |
| `log.spark.{pod}` | Container log streaming |
| `heartbeat.spark.{node}` | Node heartbeat with resource usage |

## Deployment

The `deploy/` directory contains everything needed to run Spark on a DGX or similar GPU host:

| File | Purpose |
|------|---------|
| `setup-dgx.sh` | Full DGX setup: installs NATS, Spark, and systemd services |
| `setup-registry.sh` | Sets up a local OCI registry on port 5000 |
| `spark.service` | Systemd unit for Spark |
| `nats-server.service` | Systemd unit for NATS |
| `registry.service` | Systemd unit for local OCI registry |
| `spark.env` | Environment variables for the Spark service |
| `install.sh` | Binary installation script |
| `nfpm/` | Deb packaging configuration |

### DGX Deployment

```bash
ssh ndungu@192.168.86.250
sudo bash deploy/setup-dgx.sh
```

This installs Spark and NATS as systemd services, configures the manifest directory, and starts both services.

## Local OCI Registry

Spark uses a local OCI registry at `localhost:5000` to store and serve container images, avoiding remote pulls during workload execution.

```bash
# Set up the registry
sudo bash deploy/setup-registry.sh

# Push an image
podman build -t myapp:latest .
podman tag myapp:latest localhost:5000/myapp:latest
podman push localhost:5000/myapp:latest
```

Reference images in pod manifests with the `localhost:5000` prefix:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: myapp
spec:
  containers:
    - name: myapp
      image: localhost:5000/myapp:latest
```

## Architecture

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

## Build / Test / Lint

```bash
# Build
go build ./cmd/spark

# Test
go test ./... -race -timeout 120s

# Lint
go vet ./...
staticcheck ./...
```

## Constraints

- Go standard library only, except `github.com/nats-io/nats.go` and `modernc.org/sqlite`.
- Podman, not Docker.
- Standard `flag` package for CLI flags.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns.

## Architecture Decisions

| ADR | Title |
|-----|-------|
| [001](docs/adr/001-go-standard-library-only.md) | Go standard library only |
| [002](docs/adr/002-nats-protocol-design.md) | NATS protocol design |
| [003](docs/adr/003-local-oci-registry.md) | Local OCI registry |
| [004](docs/adr/004-k8s-manifest-compatibility.md) | K8s manifest compatibility |
| [005](docs/adr/005-priority-preemption.md) | Priority preemption algorithm |
| [006](docs/adr/006-resource-aware-scheduling.md) | Resource-aware scheduling |
| [007](docs/adr/007-ubuntu-deb-packaging.md) | Ubuntu deb packaging |
| [008](docs/adr/008-sqlite-state-persistence.md) | SQLite state persistence |
| [009](docs/adr/009-http-api.md) | HTTP API design |
| [010](docs/adr/010-prometheus-metrics.md) | Prometheus metrics via stdlib |
| [011](docs/adr/011-http-bearer-token-auth.md) | HTTP bearer token authentication |

## License

MIT License. See [LICENSE](LICENSE) for details.
