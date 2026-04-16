# Spark: Resolve Open GitHub Issue #28 (Automatic Housekeeping)

## Status: Planned

## Context

### Problem Statement

Issue #28: completed and failed pods, orphaned podman containers, and
dangling images accumulate indefinitely until an operator manually
deletes them. On a single-node DGX with shared GPU and disk, this drift
consumes resources and clutters the pod list.

Spark already has a `--pod-retention` flag (default 168h) and a prune
loop in `cmd/spark/main.go` that calls `store.Prune` every 10 minutes
to remove old in-memory + SQLite records. But it does NOT remove the
underlying podman pods/containers. And it treats completed and failed
pods identically (same TTL). The reconciler's orphan-detection loop
(added for #12) warns about orphans but only removes them after an
initial 30s grace window -- it does not reap long-standing orphans in
terminal state.

### Objectives

- Completed pods auto-deleted (Spark record + podman pod) after a
  configurable TTL (default 1h).
- Failed pods auto-deleted after a longer TTL (default 24h) so
  operators can inspect logs before cleanup.
- Orphaned podman containers in terminal state reaped after a
  configurable TTL (default 1h). Running orphans are warned, not killed.
- `podman image prune -f` runs on a configurable interval (default 24h).
- Metrics emitted for all cleanup actions.
- TTL of 0 disables the corresponding cleanup (opt-out).
- Per-pod TTL override via `spark.feza.ai/ttl-after-finished` annotation.

### Non-Goals

- Automatic cleanup of running pods (that is preemption, already handled).
- Log rotation for pod logs (podman handles this).
- Cleaning up host-level files outside podman's control.

### Constraints

- Go standard library only (plus nats.go, modernc.org/sqlite). ADR-001.
- Podman CLI only (no API socket). ADR-004.
- Conventional Commits, rebase-and-merge, one package per commit.
- The existing `--pod-retention` flag continues to work but is superseded
  by the new granular TTL flags for completed/failed pods. If both are
  set, the more specific flag wins.

### Success Metrics

- `go test ./... -race -timeout 120s` passes.
- DGX validation: submit a pod, wait for completion, verify it
  auto-deletes within the configured TTL. Verify `GET /metrics` includes
  the new housekeeping counters.
- Issue #28 closes via PR merge with `fixes #28`.

## Discovery Summary

Existing code paths relevant to this work:

- `cmd/spark/main.go:287-307`: prune loop runs every 10 min, calls
  `store.Prune(retention)` and `sqlStore.PruneBefore(cutoff)`. Removes
  records only; does NOT call `executor.RemovePod`.
- `internal/reconciler/reconciler.go`: orphan loop discovers podman pods
  not in the store. Currently warns + removes after 30s grace
  (`orphanGrace`). Only handles the case where Spark record is missing
  entirely; does not handle the case where Spark record exists but the
  pod is in terminal state and expired.
- `internal/executor/podman.go`: has `RemovePod`, `StopPod`, `ListPods`.
  No `ImagePrune` method yet.
- `internal/manifest/types.go`: PodSpec has `Annotations map[string]string`
  -- need to verify this field exists or add it for the TTL annotation.
- `internal/metrics/collector.go`: Collector pattern with SetXxx/Collect.

## Scope and Deliverables

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | Pod TTL cleanup | Completed pods deleted after `--completed-pod-ttl`; failed pods after `--failed-pod-ttl`; both Spark record and podman pod removed |
| D2 | Per-pod TTL annotation | `spark.feza.ai/ttl-after-finished` annotation overrides the global TTL |
| D3 | Orphan container reap | Terminal orphans removed after `--orphan-reap-ttl`; running orphans warned only |
| D4 | Image prune | `podman image prune -f` runs on `--image-prune-interval` |
| D5 | Housekeeping metrics | `spark_pods_reaped_total`, `spark_images_pruned_total`, `spark_housekeeping_last_run_seconds` |
| D6 | PR merged, CI green, issue #28 closed | PR state MERGED, main green |
| D7 | DGX smoke test | Pod auto-deletes within TTL; metrics visible |

## Checkable Work Breakdown

### E1: Issue #28 -- Automatic housekeeping

#### Wave 1: Core TTL + executor (parallel, 4 agents)

- [ ] T1.1 Add `--completed-pod-ttl` and `--failed-pod-ttl` flags to
  `cmd/spark/main.go`  Owner: TBD  Est: 30m  verifies: [UC-024]
  - Declare two new `flag.Duration` flags alongside the existing
    `--pod-retention`. Default: 1h completed, 24h failed.
  - Thread the parsed values into a new `HousekeepingConfig` struct
    (define in `internal/reconciler/` or a new `internal/housekeeping/`).
  - Do NOT implement the sweep logic yet -- just flags + config struct.
  - Acceptance: `go build ./...` clean; `spark --help` shows the new
    flags; unit test asserts default values.

- [ ] T1.2 Parse `spark.feza.ai/ttl-after-finished` annotation in
  manifest parser  Owner: TBD  Est: 45m  verifies: [UC-024]
  - Verify `PodSpec.Annotations` exists in `internal/manifest/types.go`.
    If not, add `Annotations map[string]string` and parse it from the
    YAML metadata block in `parsePod` / `parsePodFromMap`.
  - Add a helper `func (p *PodSpec) TTLAfterFinished() time.Duration`
    that reads the annotation, parses it as a Go duration string, and
    returns 0 if absent or unparseable.
  - Tests: annotation present -> correct duration; absent -> 0; malformed -> 0.
  - Acceptance: `go test ./internal/manifest/ -race` passes.

- [ ] T1.3 Add `ImagePrune(ctx) (int, error)` to executor interface
  Owner: TBD  Est: 30m  verifies: [UC-024]
  - Add method to the `Executor` interface and implement in
    `PodmanExecutor`: runs `podman image prune -f` and parses the
    number of pruned images from output.
  - Add to stub executor (returns 0, nil).
  - Test: stub returns expected value.
  - Acceptance: `go test ./internal/executor/ -race` passes.

- [ ] T1.4 Add `--orphan-reap-ttl` and `--image-prune-interval` flags
  Owner: TBD  Est: 30m  verifies: [UC-024]
  - Two more flags in `cmd/spark/main.go`. Default: orphan-reap-ttl=1h,
    image-prune-interval=24h. 0 disables.
  - Thread into the same `HousekeepingConfig` struct from T1.1.
  - Acceptance: `go build ./...` clean; flags show in help.

#### Wave 2: Sweep logic (parallel, 3 agents)

Depends on: T1.1, T1.2, T1.3, T1.4.

- [ ] T2.1 Implement pod TTL sweep in reconciler  Owner: TBD  Est: 90m
  verifies: [UC-024]
  - In the reconciler's tick (or a new dedicated sweep function called
    from the prune loop), iterate pods in terminal state (completed,
    failed). For each:
    - Compute effective TTL: per-pod annotation wins; else use global
      completed/failed TTL.
    - If `time.Since(pod.FinishedAt) > effectiveTTL && effectiveTTL > 0`:
      call `executor.StopPod` + `executor.RemovePod`, then remove from
      store + SQLite. Emit event. Increment counter.
  - Replace or augment the existing prune loop in `main.go` so the
    podman cleanup is integrated (don't just prune records).
  - Tests: pod expired -> removed; pod within TTL -> kept; TTL=0 ->
    never removed; annotation overrides global; failed vs completed
    use different TTLs.
  - Acceptance: `go test ./internal/reconciler/ -race` passes.

- [ ] T2.2 Implement orphan reap in reconciler  Owner: TBD  Est: 60m
  verifies: [UC-024]
  - Extend the existing orphan loop: after the 30s grace window, check
    if the orphan is in a terminal state (Exited/Stopped). If terminal
    AND `time.Since(firstSeen) > orphanReapTTL`: run
    `executor.RemovePod`. If still running: log WARN but do NOT kill.
  - Tests: terminal orphan past TTL -> removed; terminal orphan within
    TTL -> warned; running orphan -> warned only.
  - Acceptance: `go test ./internal/reconciler/ -race` passes.

- [ ] T2.3 Implement image prune loop  Owner: TBD  Est: 45m
  verifies: [UC-024]
  - New goroutine in `main.go` (or extend existing prune loop): on each
    tick of `--image-prune-interval`, call `executor.ImagePrune(ctx)`.
    Log count. Increment counter. 0 interval disables.
  - Tests: verify the loop calls ImagePrune on tick; verify 0 disables.
  - Acceptance: `go build ./...` clean; `go test ./... -race` passes.

#### Wave 3: Metrics + deploy (parallel, 2 agents)

Depends on: T2.1, T2.2, T2.3.

- [ ] T3.1 Add housekeeping metrics to Collector  Owner: TBD  Est: 45m
  verifies: [UC-024]
  - `spark_pods_reaped_total{reason="ttl_completed|ttl_failed|orphan"}` (counter)
  - `spark_images_pruned_total` (counter)
  - `spark_housekeeping_last_run_seconds` (gauge, Unix timestamp)
  - Wire: reconciler increments counters; Collector reads and renders.
  - Tests: render with synthetic values; assert metric lines.
  - Acceptance: `go test ./internal/metrics/ -race` passes.

- [ ] T3.2 Wire `--completed-pod-ttl`, `--failed-pod-ttl`,
  `--orphan-reap-ttl`, `--image-prune-interval` into deploy/spark.env
  and deploy/spark.service  Owner: TBD  Est: 20m  verifies: [infrastructure]
  - Add env vars with defaults. Thread into ExecStart.
  - Acceptance: spark.service references all four flags.

#### Wave 4: Ship + validate (sequential, 1 agent)

Depends on: Wave 3.

- [ ] T4.1 Open PR, wait for CI, rebase-merge  Owner: TBD  Est: 30m
  verifies: [infrastructure]
  - Branch: `feat/housekeeping-ttl`.
  - PR body: link to issue #28, summarise the TTL model.
  - `fixes #28` in the PR body.
  - Acceptance: PR MERGED, main green, issue #28 auto-closes.

- [ ] T4.2 Cut release via release-please  Owner: TBD  Est: 15m
  verifies: [infrastructure]
  - Merge release-please PR. DGX auto-upgrade picks it up.
  - Acceptance: GitHub release published.

- [ ] T4.3 DGX smoke test  Owner: TBD  Est: 30m  verifies: [UC-024]
  - Set `SPARK_COMPLETED_POD_TTL=2m` in spark.env. Restart.
  - Submit a quick pod (`sleep 5`). Wait 3 min. Verify the pod record
    and podman container are gone.
  - `curl /metrics | grep spark_pods_reaped_total` shows the counter.
  - Acceptance: pod auto-deleted; metrics visible.

#### Cross-cutting

- [ ] T5.1 `go vet ./...` and `staticcheck ./...` clean  Owner: TBD
  Est: 10m  verifies: [infrastructure]

- [ ] T5.2 `go test ./... -race -timeout 120s` clean  Owner: TBD
  Est: 10m  verifies: [infrastructure]

## Parallel Work

| Track | Tasks | Sync point |
|-------|-------|------------|
| A: CLI flags + config | T1.1, T1.4 | feeds Wave 2 |
| B: Manifest annotation | T1.2 | feeds T2.1 |
| C: Executor | T1.3 | feeds T2.3 |
| D: Reconciler sweep | T2.1, T2.2 | feeds Wave 3 |
| E: Image prune | T2.3 | feeds Wave 3 |
| F: Metrics + deploy | T3.1, T3.2 | feeds Wave 4 |
| G: Ship | T4.1-T4.3 | sequential |

### Waves

### Wave 1: Foundations (4 agents)
- [ ] T1.1 CLI flags + HousekeepingConfig struct
- [ ] T1.2 Annotation parser + TTLAfterFinished helper
- [ ] T1.3 Executor ImagePrune method
- [ ] T1.4 Orphan-reap-ttl + image-prune-interval flags

### Wave 2: Sweep logic (3 agents)
- [ ] T2.1 Pod TTL sweep in reconciler
- [ ] T2.2 Orphan reap extension
- [ ] T2.3 Image prune loop

### Wave 3: Metrics + deploy (2 agents)
- [ ] T3.1 Housekeeping metrics in Collector
- [ ] T3.2 deploy/spark.env + spark.service wiring

### Wave 4: Ship + validate (1 agent)
- [ ] T4.1 PR, CI, merge
- [ ] T4.2 release-please
- [ ] T4.3 DGX smoke test

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Foundations | T1.1-T1.4 | Flags declared, annotation parsed, ImagePrune implemented |
| M2 | Sweep logic | T2.1-T2.3 | Terminal pods auto-deleted, orphans reaped, images pruned |
| M3 | Observable | T3.1-T3.2 | Metrics emitted, deploy wired |
| M4 | Shipped | T4.1-T4.3 | PR merged, released, DGX validated |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | Pod TTL sweep races with manual DELETE from API | Low | Medium | The existing delete path is idempotent (RemovePod handles "no such pod"). The sweep uses the same path. |
| R2 | Orphan reap kills a legitimate non-Spark container | Medium | Low | Only reap terminal-state orphans (Exited/Stopped). Running orphans are warned, never killed. |
| R3 | Image prune removes an image still needed by a pending pod | Medium | Low | `podman image prune -f` only removes dangling (untagged) images. Tagged images used by pods are retained. |
| R4 | Per-pod annotation TTL of "0" is ambiguous (disable or "immediately"?) | Low | Low | Document: 0 means "use global default". To disable cleanup for a specific pod, use a very large value. |

## Operating Procedure

- One PR for all housekeeping work. `fixes #28` in the body.
- Rebase-and-merge (not squash, not merge commits).
- Tests added in the same commit as the implementation they cover.
- `go vet ./...` and `staticcheck ./...` after every code change.
- `go test ./... -race -timeout 120s` before each push.
- Ship via release-please; DGX auto-upgrade timer picks it up.

## Progress Log

### 2026-04-16: Plan created
- One open issue: #28 (automatic housekeeping).
- Existing code: prune loop removes records but not podman pods; orphan
  loop warns but does not reap terminal orphans after the 30s grace
  window. No image prune capability.
- Plan: 4 waves, 12 tasks, max 4 parallel agents in Wave 1.

## Hand-off Notes

- Issue #28 body contains the full spec with observed incident,
  proposed features, and acceptance criteria. Treat it as the spec.
- The existing `--pod-retention` flag (168h) and prune loop in main.go
  remain for backward compatibility. The new TTL flags are more granular
  and operate at the podman level (not just records).
- The reconciler already has the orphan-detection infrastructure
  (`orphanFirstSeen`, `orphanGrace`). T2.2 extends it rather than
  replacing it.
- Executor interface needs `ImagePrune` added (T1.3). The stub executor
  in tests must implement it.
- DGX at 192.168.86.250, v1.10.1, auto-upgrade active (15-min timer).

## Appendix

- Issue #28: https://github.com/feza-ai/spark/issues/28
- Prior housekeeping-related fixes: #12 (DELETE orphan, v1.7.0),
  #13 (scheduler stuck, v1.8.1).
