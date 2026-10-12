# ADR 016: On-demand HTTP workloads

Date: 2026-09-28
Status: Accepted

## Decision

Add opt-in lazy startup and idle unloading for HTTP service pods. The manifest
and raw submitted bytes remain registered while containers are absent. New
on-demand pods start `dormant`; `always-warm` pods start `pending`.

Use Spark's existing authenticated HTTP listener as the activation gateway at
`/workloads/{pod-name}/{path...}`. A request holds a store lease while waiting for
startup and through the complete proxied response, including streams and
upgraded connections. Each pod limits the combined waiting and active requests.
The gateway strips its routing prefix and preserves the method, escaped path,
query, body, and application response. With Spark bearer auth enabled, it
consumes the Authorization header rather than forwarding the Spark credential.

The reconciler remains the single owner of activation and idle shutdown.
Requests notify its existing loop; simultaneous requests share one activation.
Normal resource admission, restart backoff, and priority preemption still apply.
A bounded HTTP readiness probe to the declared TCP hostPort must return 2xx
before traffic is forwarded. Readiness redirects are not followed. Readiness
is separate from liveness and is checked again after process recovery.

Idle eviction and request admission use the same store lock. An eviction can
begin only when no request holds the pod and the idle timeout has elapsed since
startup or the last request release. Its durable `stopping` phase is retried
until containers are removed; scheduler reservations are retained through
cleanup failures. A request arriving after eviction commits waits for cleanup
and the next activation. Successful cleanup transitions back to `dormant`.
A Spark restart re-adopts surviving stopping pods with their resource charges.
Runtime request counts and readiness reset on restart; surviving pods receive
a fresh idle window.

## Configuration and scope

Annotations configure on-demand mode, backend host port, readiness path, idle
and startup timeouts, request capacity, and an always-warm override. Validate
these on the shared manifest parse path, including YAML and JSON. Deployment
and StatefulSet annotations live on the pod template; routing is by expanded
pod name, not by a load-balanced controller service. Jobs and CronJobs cannot
opt in because HTTP demand is not their execution trigger.

The request startup timeout bounds queueing and readiness wait. It does not
limit an active response. Admission and startup failures are reported through
pod events; a failed startup returns 503, a full request capacity returns 429,
and a startup deadline returns 504. A cancelled caller releases its lease.
Workloads with no remaining callers are eligible for normal idle cleanup.

Clients must use the gateway for activity to be observable. Background work,
direct host-port traffic, or jobs accepted asynchronously after the response
need application-level activity integration or always-warm mode. Persistent
hostPath data survives unloading; writable container layers and emptyDir data
do not. Keeping images and model weights cached avoids downloads but model
initialization still contributes cold-start latency. No GPU process snapshot
or memory checkpointing is introduced.
