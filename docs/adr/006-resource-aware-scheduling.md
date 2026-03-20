# ADR 006: Resource-Aware Scheduling

## Status
Accepted

## Date
2026-03-18

## Context
The DGX Spark has specific hardware: NVIDIA GB10 GPU (128GB GPU memory), Grace CPU (10 cores), and 128GB system RAM. Multiple pods compete for these resources. Without resource awareness, spark would either serialize all GPU work (wasteful when jobs use only a fraction of GPU memory) or allow overcommit (OOM kills, GPU memory exhaustion).

Kubernetes uses resource requests and limits on containers to make scheduling decisions. Spark needs the same for a single node.

## Decision
Spark tracks three resource dimensions:

| Resource | K8s resource name | Detection method |
|----------|------------------|-----------------|
| GPU memory | nvidia.com/gpu (units: MiB or whole GPUs) | nvidia-smi |
| CPU | cpu (units: millicores) | runtime.NumCPU or /proc/cpuinfo |
| System RAM | memory (units: Mi, Gi) | /proc/meminfo or sysctl |

**Capacity detection:** On startup, spark probes the host and reports total allocatable resources (total minus a configurable system reserve, default 2 CPU cores + 4Gi RAM + 0 GPU memory).

**Scheduling algorithm:**
1. For each pending pod (sorted by priority, then FIFO):
   a. Sum resource requests of all containers in the pod spec.
   b. Check if requested resources fit within (allocatable - currently allocated).
   c. If yes, schedule the pod.
   d. If no, check if preemption can free enough resources (see ADR 005).
   e. If preemption cannot help, pod remains pending.

**Resource requests vs limits:**
- `requests` are used for scheduling decisions (guaranteed allocation).
- `limits` are enforced at runtime via podman flags: `--memory` for RAM, `--cpus` for CPU.
- GPU memory limits are best-effort (set via `CUDA_MEM_LIMIT` env var; true enforcement depends on the application).

**Whole GPU vs fractional GPU:**
- For v1, `nvidia.com/gpu: 1` means "this pod needs the GPU" and requests all GPU memory.
- Future: support fractional GPU via MIG or memory-based scheduling.

## Consequences
- Positive: Multiple non-GPU pods run concurrently without contention.
- Positive: GPU pods are scheduled based on actual availability, not a simple mutex.
- Positive: Resource requests in manifests are the same format as K8s -- portable.
- Negative: GPU memory accounting is approximate (nvidia-smi reports used memory, not reserved). Mitigated by conservative scheduling with a buffer.
- Negative: v1 treats GPU as a single indivisible resource. Sufficient for the DGX Spark's single GB10 GPU but limits concurrent GPU workloads.
