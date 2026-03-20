# ADR-009: HTTP API with net/http

## Status

Accepted

## Context

Spark v1.0.0 and v1.1.0 expose only a NATS interface for pod management. NATS is ideal for event-driven messaging and pub/sub, but requires clients to install and configure a NATS client library. Many operational tasks (health checks, resource queries, quick pod management) are better served by a simple HTTP API that any tool (curl, browsers, monitoring systems) can access.

An HTTP API also enables:
- Load balancer health checks (Kubernetes liveness/readiness probes if spark is ever nested).
- Integration with monitoring systems (Prometheus, Grafana) that expect HTTP endpoints.
- Simple scripting without NATS client dependencies.

## Decision

Add an HTTP REST API using Go's `net/http` standard library. No third-party routers (gin, echo, chi) -- consistent with ADR-001.

### Routing

Use `http.NewServeMux` with method-aware patterns (Go 1.22+):
- `GET /healthz` -- health check
- `GET /api/v1/resources` -- resource usage
- `POST /api/v1/pods` -- apply manifest (same as NATS req.spark.apply)
- `DELETE /api/v1/pods/{name}` -- delete pod (same as NATS req.spark.delete)
- `GET /api/v1/pods/{name}` -- get pod (same as NATS req.spark.get)
- `GET /api/v1/pods` -- list pods (same as NATS req.spark.list)

### Request/Response Format

- Content-Type: application/json for all API endpoints.
- Apply endpoint accepts YAML (same as NATS) in the request body, returns JSON.
- Error responses use a standard `{"error": "message"}` envelope.
- HTTP status codes: 200 OK, 201 Created, 400 Bad Request, 404 Not Found, 500 Internal Server Error.

### Configuration

- `--http-addr` flag, default `:8080`.
- Server starts after NATS connection and store initialization.
- Graceful shutdown via `http.Server.Shutdown()`.

### Architecture

The HTTP handlers reuse the same store, scheduler, and executor instances as the NATS handlers. No new business logic -- HTTP is a thin transport layer over existing operations.

```
HTTP Request --> net/http Mux --> internal/api handlers --> store/scheduler/executor
NATS Request --> bus handlers                           --> store/scheduler/executor
```

## Alternatives Considered

1. **chi/echo/gin router**: Violates ADR-001 (stdlib-only). Go 1.22 `ServeMux` has method-aware routing, which is sufficient.
2. **gRPC**: Requires protobuf dependency. Overkill for a single-node orchestrator.
3. **NATS-only**: Requires NATS client for all consumers. Not suitable for health checks or simple curl-based operations.

## Consequences

- New `internal/api` package with HTTP handlers.
- `cmd/spark/main.go` gains `--http-addr` flag and HTTP server startup.
- HTTP server must shut down gracefully alongside other components.
- API versioned under `/api/v1/` for future compatibility.
