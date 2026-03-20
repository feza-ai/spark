# ADR 011: HTTP Bearer Token Authentication

## Status
Accepted

## Date
2026-03-19

## Context
The HTTP API (v1.2.0) has no authentication. Any process on the network can apply, delete, or query pods. For a production GPU host running ML workloads, unauthenticated access to pod management is a security risk.

Full OAuth2/JWT is overengineered for a single-node orchestrator with a small number of API clients. A simple shared secret is sufficient.

## Decision
Add bearer token authentication as HTTP middleware. The token is read from a file specified by --api-token-file flag. If the flag is not set, authentication is disabled (backwards compatible).

- All endpoints except GET /healthz require the Authorization header: `Authorization: Bearer <token>`.
- Missing or invalid tokens receive 401 Unauthorized with `{"error":"unauthorized"}`.
- The /healthz endpoint remains unauthenticated so load balancers and monitoring can check health without credentials.
- Token file is read once at startup. To rotate, restart spark.

## Consequences
- Positive: Simple security boundary with no new dependencies.
- Positive: Backwards compatible -- existing deployments without --api-token-file continue to work.
- Negative: No per-user or per-role authorization. All authenticated clients have full access.
- Negative: Token rotation requires restart. Acceptable for single-node use.
