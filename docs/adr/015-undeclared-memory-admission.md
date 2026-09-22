# ADR 015: Undeclared Memory Requests and the Live Memory Admission Guard

## Status
Accepted

## Date
2026-09-22

## Context

Memory admission is enforced strictly against
`available = allocatable - sum(declared requests)` (ADR 006), and that check
is correct: `canFitLocked` has no skip path for memory, and ADR 013
deliberately withheld from memory the live-load bypass it gave CPU.

Nothing, however, floors a container that declares *no* memory.
`parseResources` returned a zero `ResourceRequirements` for a container with
no `resources:` block, with no error, so such a container was accounted at
0MB for its entire life. `PodSpec.TotalRequests` summed those zeros plainly.
The result: the strictest check in the scheduler, applied to a number that
can be silently empty.

Live incident (issue #121). On a single-node GB10 (unified memory, 114372MB
allocatable), `GET /api/v1/resources` reported `available.memoryMB` equal to
the **entire** allocatable total while eight container workloads were
resident and actively building. Admission compared a 100GiB (102400MB)
render request against that figure, correctly found room, and admitted it.
Minutes later the host stopped servicing the network entirely -- no ICMP, no
SSH, an incomplete ARP entry -- observed independently from three machines.

That the declarations really were zero was measured before the incident, not
inferred from it: with exactly one 8GiB job running, `allocated.memoryMB`
read 8192 and the container cgroup confirmed `memory.max = 8589934592`;
deleting that job moved `allocated.memoryMB` to 0. Eight CI containers,
mid-build, declared 0MB between them.

This repository keeps re-learning one failure: a silent zero in the input
path.

- **#43** (closed) -- the ledger was right and admission ignored it.
- **#66** (closed) -- flow-style YAML maps were dropped, so a pod was
  admitted with zero requests. Its wording is the point here: *"silent zero
  is the failure mode this repo keeps re-learning ... unparseable input must
  be an error, never a default."* Absent input still defaulted to zero.
- **#76** (closed) -- the CPU analogue, and the interesting asymmetry.
  Declared CPU was treated as "an accounting fiction" and given a live-load
  fallback. Declared memory carries the identical fiction and had no
  compensation, by design (ADR 013), because memory exhaustion on this
  hardware is not recoverable: a driver-level OOM wedges NVRM and freezes
  the node.

The operational note from #121 is worth preserving: because
`available.memoryMB` stays near-full precisely when undeclared workloads are
what fill the node, **any runbook that gates on it passes in exactly the
situation it exists to catch.**

## Decision

Two changes, deliberately complementary. The first stops the ledger being
wrong; the second stops admission trusting it blindly.

### 1. Undeclared memory is accounted at a configured default

`parseResources` (`internal/manifest/job.go`) now records
`ResourceRequirements.MemoryRequestUndeclared` when a container declares
neither a memory request nor a memory limit. `Parse` applies
`WithDefaultMemoryRequestMB` to those containers after the whole document is
parsed, so `TotalRequests` -- the figure admission reads -- reflects the
default.

- **Declared means named by either side, including an explicit `"0"`.** An
  explicit zero is a statement about the container; silent absence is not.
  A valueless `memory:` key counts as undeclared, since it states no more
  than omitting the key does.
- **Defaulting runs after parsing, never inside it.** An unparseable
  quantity still fails loudly. This keeps the line #66 drew: absent input
  may be defaulted, malformed input must be rejected.
- **Existing manifests are not rejected.** Rejecting a container that
  declares no memory would stop every currently-running workload from being
  re-applied. A `--reject-undeclared-memory` mode is a follow-up, not this
  change.
- **Init containers are left alone**, because `TotalRequests` does not
  account them; defaulting them would change nothing the scheduler reads.

**Default: 2048MB**, via `--default-memory-request-mb`. Justification,
against the incident's own numbers (114372MB allocatable, eight undeclared
containers resident, a 102400MB request):

| default | accounted by 8 undeclared containers | ledger available | 102400MB request |
|--------:|-------------------------------------:|-----------------:|:-----------------|
| 0 (today) | 0 | 114372 | **admitted** -- the incident |
| 512 | 4096 | 110276 | admitted |
| 1024 | 8192 | 106180 | admitted |
| **2048** | **16384** | **97988** | **refused** |
| 4096 | 32768 | 81604 | refused |

2048MB is the smallest power-of-two default that would have refused the
incident's admission. Going higher buys margin at a real cost: this node's
steady state is roughly 20 CI runner pods, which at 4096 would lock away
81920MB of a 114372MB node -- reintroducing the phantom-ceiling starvation
ADR 013 exists to fix, on the one dimension that has no overcommit path.

**State the limit of this honestly: the default is pessimism, not
measurement.** Its adequacy in the incident depends on there having been
eight containers. Five undeclared containers at 2048 leave 104132MB
available and the same render is admitted again. A default cannot know what
a container will use. That is what the second change is for.

### 2. Live memory guard at admission

`Scheduler.HostMemorySource` + `SetHostMemory(src, reserveMB)`
(`internal/scheduler/scheduler.go`), implemented by
`cmd/spark/hostmem.go` reading `/proc/meminfo`'s `MemAvailable` --
the same source `internal/gpu/system.go` reads `MemTotal` from.

At both admission doors (the direct `CanFit` path and issue #76's
utilization-aware CPU path), once the accounted ledger has approved a pod,
the host itself is consulted. When `MemAvailable - request < reserveMB`,
the pod is refused with `ScheduleResult.Err = ErrLiveMemoryExhausted` and a
Reason beginning `refused: live memory`, so `/events` distinguishes "the
host is genuinely full right now" from an ordinary accounted no-fit. Those
are different operator actions: wait, versus free a reservation.

Three properties hold by construction:

- **Subtractive only.** The guard runs *after* the ledger approves and can
  only turn a yes into a no. It never admits anything the ledger rejects, so
  `TestSchedule_UtilizationAwareAdmission_NeverBypassesMemory` and ADR 013's
  memory asymmetry are preserved.
- **It guards the overcommit path too.** Issue #76's CPU bypass must not
  become a way around it.
- **No reading never refuses.** A `HostMemorySource` reporting `ok=false`
  (no `/proc/meminfo`, e.g. a macOS dev host) disables the guard for that
  call. An unreadable host is not evidence of an exhausted one.

It is placed at admission rather than before preemption on purpose: a
high-priority pod must still be able to preempt its way onto a full node,
and the freed memory is then visible to the guard on the retry.

**Reading is on demand, not sampled.** `hostLoadAdapter` samples on a ticker
because a load average is a trailing statistic and has no choice. Memory can
collapse in seconds, and admitting a 100GiB pod against a 15-second-old
reading is the failure this guard exists to stop. A procfs read costs
microseconds and happens once per admission attempt.

**Reserve: 4096MB**, via `--live-memory-reserve-mb`, matching
`--system-reserve-memory`'s default. It is a separate flag rather than a
reuse of that one because the two answer different questions: the system
reserve is subtracted from the ledger's total once at startup, while this is
a floor under *real* free memory at each admission. On a unified-memory host
where a driver OOM takes the whole node down, an operator will want this one
higher without inflating the accounting reserve.

The guard defaults **on** (`--live-memory-guard=true`). It makes admission
depend on live host state -- the same manifest may be admitted or refused
depending on what else is running -- which is the intended behavior and is
documented as such.

### 3. Parser configuration is threaded, not global

`Parse` takes variadic `ParseOption`s. The alternative -- a package-level
default in `manifest` set once at startup -- was rejected: it is mutable
global state that tests would race on, and it hides which ingestion path
configured what. All three ingestion paths (NATS, HTTP, the directory
watcher) are passed the same options from `main`, so a manifest is accounted
identically however it arrives.

### 4. The flags are not wired into `deploy/spark.service`

`deploy/install.sh` writes `spark.env` only when the file does not already
exist, so an upgraded host keeps its old copy. A new `${SPARK_...}`
reference in `ExecStart` would expand to an empty argument on every existing
deployment, `flag` would fail to parse it, and the service would crash-loop
on restart. The last several flags added to this binary
(`--pending-log-timeout`, `--host-load-sample-interval`,
`--cpu-overcommit-margin-millis`, the housekeeping TTLs) were added the same
way: built-in defaults, overridden per host with a systemd drop-in. The
README documents the drop-in.

That is why both defaults are chosen to be the values a host should actually
run with, rather than conservative no-ops an operator is expected to raise.

### 5. Visibility

- `spark_defaulted_memory_admissions_total` -- pods admitted carrying a
  memory request Spark supplied rather than the manifest declaring one. A
  climbing figure means the ledger rests on a guess.
- `spark_live_memory_refusals_total` -- admissions the ledger approved and
  real host memory did not.
- A WARN log, once per pod, naming the pod and the containers that declared
  no memory, plus what the pod is being accounted at.
- Startup logs state both settings explicitly, including a WARN when either
  protection is switched off.

`GET /api/v1/resources` is unchanged: splitting its totals into declared
versus defaulted would require the `ResourceTracker` to carry provenance per
allocation, which is more surface than the question justifies while the
metric and the WARN log answer it.

## Consequences

**Positive:**

- The #121 shape cannot recur silently. Undeclared containers charge the
  ledger, and a ledger that is still wrong is caught by the host itself.
- The distinct error means the events feed says *why* a pod is pending.
  "refused: live memory" and "no preemption candidates; shortfall: memory
  102400MB > 11972MB free" are different diagnoses.
- Both protections have complete kill switches
  (`--default-memory-request-mb=0`, `--live-memory-guard=false`), and with
  both off the scheduler behaves exactly as it did before this ADR.

**Negative:**

- **Undeclared pods now consume ledger capacity they may not use.** A node
  running many small undeclared containers has less accounted memory to hand
  out than before, and memory has no utilization-aware overcommit path to
  compensate. This is the accepted trade: on this hardware, being wrong
  toward pessimism costs throughput, and being wrong toward optimism costs
  the host.
- **Admission becomes host-state-dependent.** The same manifest, submitted
  twice, can be admitted and then refused. Reproducibility of scheduling
  decisions is reduced in exchange for not freezing the node.
- **A pod persisted before this change keeps its zero.** Defaulting happens
  at parse time, so specs already in SQLite are adopted at 0MB after an
  upgrade until they are re-applied. The live guard covers the gap in the
  meantime; this is a transitional inaccuracy in the ledger, not in the
  guard.
- **The default's adequacy is coincidental to container count**, as the
  table above shows. Treat `spark_defaulted_memory_admissions_total` as a
  prompt to fix the manifests, not as a solved problem.
- `/proc/meminfo` is Linux-only. On a macOS dev host the guard never
  engages and admission falls back to pure accounting -- intentional
  degradation, matching ADR 013's treatment of `/proc/loadavg`.

## Non-goals

- **Rejecting manifests that declare no memory.** Considered and deferred:
  it would break every currently-running workload on re-apply. A follow-up
  flag can make it opt-in, and issue #121's "stricter variant" notes it is
  defensible on a unified-memory node.
- **Usage-based tracking and alerting** (issue #47 proposals 1 and 2):
  sampling per-pod `podman stats` into the reconciliation loop and emitting
  NATS events at a node-wide usage threshold. This ADR implements only
  proposal 3, and only for memory.
- **A headroom reserve as a fraction of allocatable** (#47 proposal 1). The
  live guard's reserve is a floor under real free memory, which is a
  different and, on this hardware, a more direct protection than a
  percentage withheld from a ledger that can be wrong.
- **Negative `available.cpuMillis`** (#119) is untouched; ADR 013 point 4
  explains why CPU is deliberately not floored.
- **Init container accounting.** `TotalRequests` has never summed init
  containers; changing that is a separate accounting decision.
