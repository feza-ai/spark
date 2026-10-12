# ADR 017: Connection and queue activation

Date: 2026-09-29
Status: Accepted

## Decision

Extend ADR 016's lifecycle to TCP services and GitHub Actions runner pods.
Activation is explicit (`http`, `tcp`, or `github`); resource utilization is
never treated as proof a workload has no work.

TCP services retain a stable public gateway port. Containers publish a separate
backend host port on loopback. The gateway waits for normal admission and
readiness, then forwards an opaque bidirectional byte stream, including TLS.
The lease lasts until both directions finish; half-close is forwarded. Existing
connections prevent idle eviction. Native database/TLS authentication remains
at the backend. HTTP bearer authentication does not apply to raw TCP.

Readiness can use a command in a declared container; otherwise TCP readiness is
a successful backend connection. HTTP readiness remains an application 2xx
response. Client timeouts must accommodate cold startup. Control-plane restarts
close proxy connections; clients must reconnect.

GitHub demand is derived from queued jobs, including queued jobs inside running
workflows. Match all requested job labels against a runner's configured labels,
prefer already waiting listeners, and allocate at most one free runner per job.
An in-progress job is never a signal to start a replacement runner, because a
new process cannot resume an assigned GitHub job. Credentials come from a host
token file and never enter the workload's environment through this controller.
Pagination failures, authentication errors, and network failures invalidate the
observation; stale queue data cannot authorize eviction.

Queue metadata alone is insufficient to kill a runner safely: assignment can
race a REST read. Runner pods use `run.sh --once`. Only the actual worker
container's exit proves its job has finished. The reconciler then retains any
sidecars until the idle deadline, or replaces the finished pod immediately if
another queued job needs it. A live listener is protected even when the queue
looks empty. If a queued job is cancelled or claimed elsewhere before this
listener accepts it, the listener can remain warm until it completes a later
job. This deliberate conservative case avoids failing a newly assigned job.

Batch render jobs remain submission-driven and run to completion. Jobs and
CronJobs do not use service-style idle eviction.

## Data retention and rollout

Map `persistentVolumeClaim.claimName` to an explicitly named local Podman volume.
This is a single-node volume reference, not a Kubernetes PV provisioner. Reuse
existing volume identities for database and BuildKit data so recreation does
not substitute empty anonymous volumes. For databases, take a backup, retain
the original manifests, and verify volume identities and the database identifier
after recreation.

Persistent runner registration files must survive recreation. Migrate only
idle runners, preserving existing startup scripts and job hooks while changing
execution to one job per activation. A runner with no usable registration
requires a fresh GitHub registration token for its first setup.
