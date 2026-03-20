# ADR 004: Kubernetes Manifest Compatibility

## Status
Accepted

## Date
2026-03-18

## Context
Wolf anticipates a dozen or more pods running on the DGX Spark, including long-running services (inference, data pipelines) and batch jobs (training, backtests). Writing custom manifest formats creates throwaway work -- if the DGX workloads ever move to a real Kubernetes cluster, all manifests would need rewriting.

Kubernetes pod and job manifests are well-documented, widely understood, and have mature tooling (kubectl, kustomize, helm). Supporting a subset of the K8s API surface lets spark users write manifests once and run them on either spark (single-node podman) or Kubernetes (multi-node) without changes.

## Decision
Spark reads a subset of Kubernetes YAML manifests. Supported resource kinds:

**Kind: Pod** -- long-running services (restartPolicy: Always) and one-shot containers.
**Kind: Job** -- batch workloads (restartPolicy: Never, with backoffLimit).
**Kind: CronJob** -- scheduled batch workloads (cron schedule, creates Job on each trigger).

Supported pod spec fields:
- `metadata.name`, `metadata.namespace` (used as podman pod name prefix)
- `metadata.labels`, `metadata.annotations`
- `spec.containers[].name`, `image`, `command`, `args`, `env`, `volumeMounts`
- `spec.containers[].resources.requests` and `resources.limits` (cpu, memory, nvidia.com/gpu)
- `spec.volumes[]` (hostPath and emptyDir only for v1)
- `spec.restartPolicy` (Always, OnFailure, Never)
- `spec.priorityClassName` (maps to spark's priority system)
- `spec.terminationGracePeriodSeconds`

Unsupported fields are ignored with a warning log, not rejected. This allows manifests authored for full K8s to run on spark without modification (minus features spark does not implement).

Priority classes are defined in spark's config file, not as K8s PriorityClass resources:
```yaml
priorityClasses:
  emergency: 0    # preempts everything
  critical: 100
  high: 500
  normal: 1000    # default
  low: 2000
  idle: 10000     # preempted by anything
```
Lower value = higher priority (consistent with K8s convention).

## Consequences
- Positive: Manifests are portable between spark and Kubernetes.
- Positive: Engineers already know the format. No learning curve.
- Positive: Existing K8s tooling (kustomize, yq) works on spark manifests.
- Positive: Migration to K8s requires zero manifest changes.
- Negative: Spark must parse K8s YAML, which is verbose. Mitigated by using only the subset we need, not importing the full K8s API machinery.
- Negative: Users may expect unsupported K8s features to work. Mitigated by warning logs for ignored fields.
