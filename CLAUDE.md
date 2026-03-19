# Spark

Single-binary Go pod orchestrator for GPU hosts. Accepts Kubernetes manifests and runs workloads via podman on a single node. Connects to NATS for messaging.

## Build / Test / Lint / Run

```bash
# Build
go build ./cmd/spark

# Test
go test ./... -race -timeout 120s

# Lint
go vet ./...
staticcheck ./...

# Run
./spark --nats nats://localhost:4222
```

## Constraints

- Go standard library only, except `github.com/nats-io/nats.go`.
- Podman, not Docker.
- Standard `flag` package for CLI flags.

## NATS Subjects

| Prefix | Purpose |
|---|---|
| `req.spark.*` | Request/reply |
| `evt.spark.*` | Events |
| `log.spark.*` | Logs |
| `heartbeat.spark.*` | Heartbeats |

## Supported K8s Manifest Kinds

Pod, Job, CronJob, Deployment, StatefulSet

## Commit Discipline

- Small, logical commits — one package per commit.
- Conventional Commits (e.g., `feat(runtime): add pod lifecycle hooks`).
- Rebase and merge (not squash, not merge commits).
