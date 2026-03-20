# ADR 002: NATS Protocol Design

## Status
Accepted (updated 2026-03-18)

## Date
2026-03-18

## Context
Spark is a single-node pod orchestrator that manages pods via podman. Remote clients (wolf, zerfoo, or any NATS-connected service) need to apply manifests, delete pods, query status, and receive logs and lifecycle events.

Wolf's existing scheduler uses NATS request-reply on subjects like `req.scheduler.submit`. Spark defines its own subject namespace that is generic and aligned with K8s operations (apply, delete, get, list) rather than job-specific operations (submit, dequeue).

## Decision
Use the following NATS subject hierarchy:

**Request-reply (synchronous):**
- `req.spark.apply` -- apply a K8s manifest (Pod, Job, Deployment, StatefulSet). Payload: raw YAML bytes. Reply: list of pod names and initial status.
- `req.spark.delete` -- delete a pod by name. Reply: confirmation.
- `req.spark.get` -- get full pod details (spec, status, events, resources). Reply: pod record.
- `req.spark.list` -- list pods with optional status filter. Reply: pod list.

**Lifecycle events (asynchronous publish):**
- `evt.spark.scheduled.{pod_name}` -- pod scheduled for execution
- `evt.spark.started.{pod_name}` -- pod containers running
- `evt.spark.completed.{pod_name}` -- pod completed successfully
- `evt.spark.failed.{pod_name}` -- pod failed
- `evt.spark.preempted.{pod_name}` -- pod evicted by higher-priority pod
- `evt.spark.restarted.{pod_name}` -- pod restarted by reconciler
- `evt.spark.deleted.{pod_name}` -- pod deleted by user

**Log streaming:**
- `log.spark.{pod_name}` -- stdout/stderr lines from running pod

**Heartbeat (periodic publish):**
- `heartbeat.spark.{node_id}` -- node capacity, resource availability, pod counts

All payloads are JSON. Manifests are sent as raw YAML bytes in the apply request.

## Consequences
- Positive: Operations align with K8s vocabulary (apply, delete, get, list) -- familiar to users.
- Positive: Any NATS client can manage pods without importing spark code.
- Positive: Lifecycle events enable monitoring, alerting, and audit logging by subscribers.
- Negative: JSON over NATS has no schema enforcement. Mitigated by validation in handlers.
- Negative: YAML parsing happens server-side, so malformed YAML errors are returned asynchronously. Acceptable for an infrastructure tool.
