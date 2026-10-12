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
- **Security context** -- runAsUser, privileged, capabilities add/drop forwarded to podman (v1.5.0)
- **Manifest removal** -- file deletion stops pods, releases resources, unregisters cron jobs (v1.5.0)
- **CronJob registration on all paths** -- NATS, HTTP, and filesystem all register cron jobs (v1.5.0)
- **Stuck pod recovery** -- Scheduled and Preempted pods recovered after timeout (v1.5.0)
- **GPU count-based scheduling** -- `nvidia.com/gpu: N` allocates N device slots, separate from GPU memory (v1.6.0)
- **Liveness probes** -- exec and HTTP probes with configurable thresholds; reconciler restarts on failure (v1.6.0)
- **CronJob HTTP management** -- list, inspect, and unregister cron jobs via REST API (v1.6.0)
- **Node info endpoint** -- GPU model, device count, device IDs, CPU, memory, OS via HTTP (v1.6.0)

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
| `--shutdown-timeout` | `30s` | Max time to drain pods on shutdown (only with `--drain-on-shutdown`) |
| `--drain-on-shutdown` | `false` | Stop all running pods on shutdown. Off by default: pods keep running through restarts and upgrades, and the new process re-adopts them (state, scheduler quota, and cpuset assignments included) |
| `--reconcile-resources-interval` | `60s` | Resource reconciliation interval |
| `--log-format` | `text` | Log output format (text or json) |
| `--api-token-file` | *(empty)* | Path to file containing API bearer token |
| `--housekeeping-interval` | `1m` | Housekeeping loop interval |
| `--completed-pod-ttl` | `1h` | TTL after which Completed pods are reaped (0 disables) |
| `--failed-pod-ttl` | `24h` | TTL after which Failed pods are reaped (0 disables) |
| `--orphan-reap-ttl` | `1h` | TTL after which terminal-state orphan podman pods are reaped (0 disables) |
| `--image-prune-interval` | `24h` | Interval between `podman image prune -f` runs (0 disables) |
| `--pending-log-timeout` | `10m` | How long a Pending pod's `GET /logs` treats podman's "no such pod" as still-queued before reporting the resource shortfall instead of staying silent (issue #78) |
| `--host-load-sample-interval` | `15s` | Interval between `/proc/loadavg` samples used for utilization-aware CPU admission (issue #76) |
| `--cpu-overcommit-margin-millis` | `1000` | CPU millicores subtracted from the live headroom estimate before utilization-aware admission will admit a pod over the accounted ceiling |
| `--default-memory-request-mb` | `2048` | Memory request accounted for a container that declares neither a memory request nor a memory limit. `0` disables defaulting and accounts such a container at 0MB (issue #121) |
| `--live-memory-guard` | `true` | Refuse admission when real host memory minus the pod's request would fall below `--live-memory-reserve-mb`, even when the declared-request ledger has room |
| `--live-memory-reserve-mb` | `4096` | MB of real host memory the live memory guard never hands out |

### Memory admission

Two independent protections stand between a manifest and a node that runs
out of RAM. Both default on; see `docs/adr/015-undeclared-memory-admission.md`.

**Undeclared memory is accounted at a default.** A container that names no
memory request and no memory limit is charged `--default-memory-request-mb`
rather than 0MB. Without this, a node packed with such containers reports
its memory ledger as completely empty while it is full, and admission
believes it. An explicit `memory: "0"` is a declaration and is honored as
written; only silent absence is defaulted. Set the flag to `0` to restore
the previous behavior.

**Real host memory is checked at admission.** After the declared-request
ledger approves a pod, Spark reads `MemAvailable` from `/proc/meminfo` and
refuses the pod when admitting it would leave less than
`--live-memory-reserve-mb` actually free. The refusal appears on
`GET /api/v1/pods/{name}/events` with a reason beginning
`refused: live memory`. This guard only ever refuses: it can never admit a
pod the ledger rejects. On a host without `/proc/meminfo` it disables
itself and admission falls back to accounting alone.

Two counters on `/metrics` report how much the ledger is trusting a guess
and how often the host overrules it:
`spark_defaulted_memory_admissions_total` and
`spark_live_memory_refusals_total`.

### Overriding a flag that `spark.env` does not list

`deploy/spark.env` covers the settings the service unit passes explicitly.
Every other flag uses its built-in default. To change one on an installed
host, add a systemd drop-in rather than editing the unit file, which a
package upgrade replaces:

```bash
sudo systemctl edit spark
```

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/spark --live-memory-reserve-mb 16384
```

The empty `ExecStart=` is required: it clears the unit's own command before
the replacement is appended. Reproduce the flags from
`deploy/spark.service` that the host needs, then
`sudo systemctl restart spark`.

Per-pod TTL override is available via the `spark.feza.ai/ttl-after-finished` annotation (any value parseable by `time.ParseDuration`; `0s` disables cleanup for that pod).

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

### Get Pod Manifest

```bash
curl http://localhost:8080/api/v1/pods/myapp/manifest
```

Returns the exact manifest bytes that were submitted for this pod -- via `POST /api/v1/pods`, `req.spark.apply`, or the manifest directory watcher -- byte-for-byte, whether the original submission was YAML or JSON. Unlike Get Pod, this is not filtered by status: it works for a pod stuck in any state, including one whose reported status has diverged from its actual container. Useful for recreating a pod faithfully after such an incident, since Get Pod alone only returns a status summary.

### Apply Pod

```bash
curl -X POST http://localhost:8080/api/v1/pods \
  -H "Content-Type: application/yaml" \
  -d @pod.yaml
```

JSON is also accepted for the same endpoint and kinds:

```bash
curl -X POST http://localhost:8080/api/v1/pods \
  -H "Content-Type: application/json" \
  -d @pod.json
```

Accepts a Kubernetes manifest (YAML or JSON) in the request body. Supports Pod, Job, CronJob, Deployment, and StatefulSet kinds. A document that parses to nothing usable (empty, or missing a `kind`) is a `400 Bad Request`, never a silent `201` that creates nothing.

Both block-style and flow-style YAML collections are supported (`limits: { cpu: "1", memory: 512Mi }`, `env: [{ name: FOO, value: bar }]`). A malformed flow collection is a `400 Bad Request`, never a silently ignored value.

#### Restart semantics

`restartPolicy` applies per container, matching Kubernetes: when one container in a multi-container pod crashes, only that container is restarted in place (config and filesystem intact) — its siblings keep running. In-place restarts back off exponentially (10s doubling to a 5m cap) and do not count against a Job's `backoffLimit`, which governs whole-pod failures. Under `Never`, a crashed container stays down while the rest of the pod runs; the pod completes or fails once all containers have exited (first non-zero exit code wins).

Whole-pod restarts (a crash-looping single-container pod under `Always`, or `OnFailure` retries) back off on the same schedule — 10s doubling to a 5m cap, CrashLoopBackOff-style — and the schedule resets after the pod runs cleanly for 10 minutes. The pending event carries the current delay.

#### Image references

Docker Hub short names are qualified the way Kubernetes does it: `pgvector/pgvector:pg16` becomes `docker.io/pgvector/pgvector:pg16`, and bare names like `alpine:latest` become `docker.io/library/alpine:latest`. Refs whose first component is a registry (contains a dot or port, or is `localhost`) pass through untouched.

#### Resource quantities

- **CPU**: whole or fractional cores (`"2"`, `"0.5"`) or millicores (`"500m"`).
- **Memory**: plain bytes or a `Ki`/`Mi`/`Gi` (binary) / `K`/`M`/`G` (decimal) suffix, e.g. `512Mi`, `1.5Gi`. A lowercase `m` suffix is rejected — in Kubernetes quantity syntax it means *millibytes*, which is never what you want.
- **GPU**: `nvidia.com/gpu` takes a non-negative integer count.

A manifest containing an unparseable quantity is rejected with `400 Bad Request` naming the container and value — it is never silently treated as zero, since a zero-valued request would bypass admission control.

Per Kubernetes semantics, a request left unspecified defaults to the corresponding limit, so limits-only manifests are fully accounted by the scheduler.

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

### Node Info

```bash
curl http://localhost:8080/api/v1/node
```

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

### List CronJobs

```bash
curl http://localhost:8080/api/v1/cronjobs
```

```json
[
  {"name": "train-nightly", "schedule": "0 2 * * *", "next_run": "2026-03-21T02:00:00Z", "run_count": 14}
]
```

### Get CronJob

```bash
curl http://localhost:8080/api/v1/cronjobs/train-nightly
```

### Delete CronJob

```bash
curl -X DELETE http://localhost:8080/api/v1/cronjobs/train-nightly
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
  api/              HTTP REST API handlers (health, resources, node, pods, exec, logs, events, images, cronjobs, metrics, auth)
  bus/              NATS bus abstraction, protocol handlers, event/log publishers
  cron/             Cron expression parser and scheduled job trigger
  executor/         Podman interface: pod create, stop, exec, logs, image pull, stats, liveness probes
  gpu/              GPU detection (nvidia-smi), device enumeration, and system resource detection
  lifecycle/        Graceful shutdown coordinator with pod draining
  manifest/         K8s YAML parser (Pod, Job, CronJob, Deployment, StatefulSet) with ports, init containers, securityContext, livenessProbe
  metrics/          Prometheus metrics collector and text renderer
  reconciler/       Desired-state reconciliation loop, pod recovery, resource sync, liveness probe polling
  scheduler/        Resource-aware scheduling with priority preemption and GPU count-based device slot tracking
  state/            Pod state store (in-memory + SQLite WAL persistence) with source path tracking
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

## On-demand HTTP workloads

Spark can register an HTTP service without starting it, wake it when a request
arrives, and unload its containers after inactivity. Enable this per pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: model
  annotations:
    spark.feza.ai/on-demand: "true"
    spark.feza.ai/backend-port: "18080"
    spark.feza.ai/readiness-path: "/health"
    spark.feza.ai/idle-timeout: "5m"
    spark.feza.ai/startup-timeout: "5m"
    spark.feza.ai/max-requests: "64"
    spark.feza.ai/always-warm: "false"
spec:
  restartPolicy: Always
  containers:
    - name: server
      image: localhost:5000/model-server:latest
      ports:
        - containerPort: 8080
          hostPort: 18080
      resources:
        requests:
          cpu: "2"
          memory: "8Gi"
```

Replace the image, resource requests, ports, and readiness path with those of
your service. Its readiness endpoint must return **2xx only when it can serve
requests**, including loading any models. The backend port must match a TCP
`hostPort` in the manifest. Redirects do not indicate readiness.

Apply the manifest normally, then send application requests through Spark:

```bash
curl -X POST http://localhost:8080/workloads/model/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"Hello"}'
```

The gateway forwards this to `http://127.0.0.1:18080/v1/completions`. Use a
trailing slash for the backend root: `/workloads/model/`. Requests share a
single startup and wait for readiness, bounded by `startup-timeout`. Bodies
are streamed after readiness; responses, SSE, and WebSockets remain pinned
until their connections finish. `max-requests` caps waiting **and active**
requests per pod; excess requests receive 429 with `Retry-After`. A readiness
wait timeout returns 504; startup failure returns 503. Connection failures
return 502 and are not automatically replayed.

If Spark authentication is enabled, supply its bearer token as usual. Spark
consumes this Authorization header and does not pass the Spark token to the
application. Application authentication must use another header or cookie.

New pods report `dormant`. The first request activates normal scheduling;
`running` plus `ready: true` means the application can receive traffic.
`GET /api/v1/pods/{name}` also reports `onDemand` and `inFlight`. After
`idle-timeout` with no outstanding requests, Spark enters `stopping`, removes
the containers, releases resources, and returns to `dormant`. The manifest
and hostPath data remain registered. Container writable layers and emptyDir
volumes are recreated, so keep model caches and durable data on hostPath
volumes. Containers are restarted and models reloaded on the next request;
the first request pays this cold-start delay.

Timeout defaults are 5 minutes; request capacity defaults to 64. Both timeouts
must be positive durations. On-demand services require `restartPolicy: Always`
(the default when omitted). `always-warm: "true"` starts the service at apply
time and prevents idle eviction while retaining gateway readiness checks.
For Deployments and StatefulSets, put annotations under
`spec.template.metadata.annotations` and use the expanded pod name (for example
`model-0`) in the gateway URL. Jobs and CronJobs cannot use on-demand mode.

All application traffic must pass through the gateway for idle accounting.
Direct host-port requests and background work are invisible to it. Use
always-warm mode for services that continue processing after returning a
response. Existing priority preemption still applies, including to active
on-demand workloads. See [ADR 016](docs/adr/016-on-demand-http-workloads.md).

### TCP services

Use native client connections for PostgreSQL, BuildKit (including TLS/gRPC), or
another TCP service:

```yaml
metadata:
  annotations:
    spark.feza.ai/on-demand: "true"
    spark.feza.ai/activation: "tcp"
    spark.feza.ai/gateway-port: "5432"
    spark.feza.ai/backend-port: "15432"
    spark.feza.ai/idle-timeout: "5m"
    spark.feza.ai/readiness-container: "postgres"
    spark.feza.ai/readiness-command: '["pg_isready", "-U", "postgres"]'
spec:
  containers:
    - name: postgres
      ports:
        - containerPort: 5432
          hostPort: 15432
```

This fragment augments a complete manifest. Spark owns the public gateway port
and publishes the backend host port on loopback. Existing applications continue
to connect to port 5432. The optional readiness command must exit zero before
connections are forwarded; otherwise readiness checks that the backend accepts
TCP connections. Both directions are streamed without interpreting application
messages or TLS. Database/TLS authentication is unchanged. A connection, even
an idle pooled connection, prevents eviction until it closes. Capacity or
startup-timeout failures close the connection; clients should retry and allow
for cold-start latency. Spark restarts close gateway connections.

### GitHub Actions runners

Configure the host with `--github-token-file /etc/spark/github-token` (or
`SPARK_GITHUB_TOKEN_FILE` in the service environment). The file should be readable
only by the Spark service account and contain a GitHub token with Actions read
access to the configured repositories. Polling defaults to 30 seconds; set
`--github-poll-interval` to change it.

```yaml
metadata:
  annotations:
    spark.feza.ai/on-demand: "true"
    spark.feza.ai/activation: "github"
    spark.feza.ai/github-repository: "owner/repository"
    spark.feza.ai/github-runner-name: "gpu-runner-1"
    spark.feza.ai/github-labels: "self-hosted,Linux,ARM64,dgx"
    spark.feza.ai/worker-container: "runner"
    spark.feza.ai/idle-timeout: "5m"
```

Run the registered runner with `./run.sh --once`, and retain its registration
files on persistent storage. The configured runner name and labels must match
GitHub. A queued job wakes a matching runner; an executing job is protected
until the actual runner process exits. Spark then unloads the finished pod
(including sidecars) after the idle timeout, or immediately replaces it for new
queued work. Queue errors never authorize eviction based on stale observations.

A listening runner that has not yet completed a job stays protected. If its
triggering job was cancelled or claimed by another runner, it can remain warm
until a later job completes. This avoids interrupting jobs during the gap
between assignment and GitHub API updates. Jobs and CronJobs retain their
existing submission/schedule-driven lifecycle and run to completion.

### Retaining local volumes

Use explicit local Podman volumes for durable data across eviction:

```yaml
spec:
  volumes:
    - name: database
      persistentVolumeClaim:
        claimName: existing-postgres-volume
  containers:
    - name: postgres
      volumeMounts:
        - name: database
          mountPath: /var/lib/postgresql/data
```

`claimName` maps directly to an existing, explicitly named local Podman volume.
Create that volume before applying the manifest; Spark rejects startup if it is
missing, preventing silent replacement with an empty database. Back up databases
before migration. For image-created anonymous volumes, copy the data into an
explicitly created named volume before removing the original pod: referencing an
anonymous volume by its generated name does not make it durable. Named volumes
survive idle eviction.
See [ADR 017](docs/adr/017-connection-and-queue-activation.md).
