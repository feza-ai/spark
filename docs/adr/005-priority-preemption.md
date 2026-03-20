# ADR 005: Priority-Based Preemption

## Status
Accepted

## Date
2026-03-18

## Context
The DGX Spark has finite resources: 1 GPU with 128GB memory, 64GB system RAM, and a Grace CPU. With a dozen or more pods competing for resources, some workloads are more important than others. An emergency model retrain triggered by drift detection must not wait behind a low-priority backtest that is consuming the GPU.

Kubernetes supports priority and preemption via PriorityClass resources. Spark needs the same capability for a single node.

## Decision
Spark implements priority-based preemption:

1. **Every pod has a priority.** Set via `spec.priorityClassName` in the manifest. Default is "normal" (value 1000). Lower numeric value = higher priority.

2. **Scheduling checks resources first.** When a pod is submitted, spark checks if the requested resources (GPU memory, CPU, RAM) are available. If yes, the pod starts immediately.

3. **Preemption triggers when resources are insufficient.** If a pod cannot be scheduled due to resource constraints, spark identifies candidate victims:
   - Only pods with strictly lower priority (higher numeric value) are candidates.
   - Spark selects the minimum set of victims whose freed resources satisfy the pending pod.
   - Among equal candidates, prefer the most recently started (least work lost).
   - Victims receive SIGTERM, then SIGKILL after terminationGracePeriodSeconds.

4. **Non-preemptible pods.** Pods with annotation `spark.feza.ai/preemptible: "false"` are never evicted. Use for critical infrastructure (NATS, registry).

5. **Preempted pods are re-queued** at their original priority if their restartPolicy is Always or OnFailure. Jobs (restartPolicy: Never) are marked failed with reason "Preempted".

## Consequences
- Positive: Emergency workloads always get resources quickly.
- Positive: Low-priority work (backtests, experiments) uses idle resources without blocking critical work.
- Positive: Consistent with K8s preemption semantics -- same mental model.
- Negative: Preempted jobs lose progress. Mitigated by checkpointing at the application level (not spark's responsibility).
- Negative: Thrashing risk if priorities are misconfigured (high-priority pod preempts, fails, low-priority restarts, gets preempted again). Mitigated by backoff on repeated preemption of the same pod.
