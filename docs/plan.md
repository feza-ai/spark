# Spark: Resolve Open GitHub Issues (v1.6.2)

## Status: In Progress

## Context

### Problem Statement

Three open issues on github.com/feza-ai/spark cover bugs that degrade correctness and operability on the DGX Spark host. None are feature requests; all are defects in existing wired functionality.

1. **Issue #12 -- DELETE leaves orphaned podman pod.** `DELETE /api/v1/pods/{name}` removes the pod from Spark's in-memory store and SQLite, but the underlying podman pod can keep running. The reconciler's `RecoverPods` logs `"orphaned pod discovered in podman"` every ~5s (internal/reconciler/reconciler.go:88) without reconciling. On a GPU-heavy workload the orphan keeps saturating the DGX, and recovery requires `ssh + podman pod rm -f`. Root cause: the delete handler calls `executor.StopPod` and `executor.RemovePod` as best-effort (errors ignored) and then deletes the store record unconditionally. If podman is under load, stop/rm can fail or time out and the store goes out of sync with reality.
2. **Issue #10 -- args/command containing `://` are silently dropped.** The custom YAML parser in `internal/manifest/yaml.go` walks list items and, on finding any `:` in the item content, treats the item as a map (line 121). For a list item like `- "nats://10.88.0.1:4222"`, this splits it into key=`"nats"`, value=`//10.88.0.1:4222`, which is then discarded because it is not a real scalar. YAML requires a `:` to be followed by whitespace or end-of-line to act as a map separator; the parser does not enforce this rule, nor does it treat quoted strings as atomic scalars.
3. **Issue #8 -- reconciler infinite retry when container start fails after successful pod create.** When `podman pod create` succeeds but `podman run` fails (e.g., missing volume mount), the next reconcile tick hits "already exists" and the fix from #7 removes and re-creates the pod. The same container-start error recurs, producing an infinite loop. There is no `backoffLimit` enforcement and no terminal failure state with the container start error surfaced in the API.

### Objectives

- Make `DELETE /api/v1/pods/{name}` idempotent and truthful: podman pod is gone before the Spark record is deleted, OR the API returns an error and the record stays so the client can retry.
- Make the reconciler kill orphaned podman pods it sees but has no record for.
- Make the YAML parser preserve scalar list items that contain `://`, and any `:` not followed by whitespace.
- Make container-start failures terminate on a bounded retry count with a clear error surfaced on the pod record.

### Non-Goals

- Full YAML 1.2 compliance. Fix the specific scalar/colon rule; do not rewrite the parser.
- Async delete. Keep DELETE synchronous; long stop times are acceptable as long as the response is truthful.
- Readiness probes, ConfigMap/Secret, or other v1.7.0 features.

### Constraints

- Go standard library only (plus `nats.go`, `modernc.org/sqlite`). ADR-001.
- Podman CLI only (no API socket). ADR-004.
- Conventional Commits, rebase-and-merge, one package per commit.

### Success Metrics

- Submit long-running pod, `DELETE` it, then `podman pod ls` on the DGX does NOT show the pod. Endpoint returns 200 only when podman confirms removal; returns 5xx with the Spark record preserved otherwise.
- Reconciler with an orphaned podman pod present removes the orphan within 2 reconcile ticks; no more infinite "orphaned pod discovered" warnings.
- Pod manifest with `command: ["echo", "nats://10.88.0.1:4222", "-port=8080"]` runs and prints all three arguments verbatim.
- Pod with a bad volume mount transitions to `Failed` after `backoffLimit` (default 3) retries with a human-readable reason field; reconciler stops retrying.
- `go test ./... -race -timeout 120s` and `go vet ./...` pass.

## Discovery Summary

### Current state

- Delete path: `internal/api/pods_mutate.go:65-96`. StopPod/RemovePod errors ignored; store.Delete runs unconditionally.
- Orphan detection: `internal/reconciler/reconciler.go:87-89` logs only.
- YAML list parse: `internal/manifest/yaml.go:121-146`. Any `:` in item content promotes the item to a map.
- Reconciler failure-after-create: `internal/reconciler/reconciler.go` + `resources.go`. Need to confirm current retry/backoff state and how container start errors are surfaced.

### Related prior work

- Issue #7 fix (already merged, commit 8f30f6d): removes stale pod before retry on "already exists".
- v1.6.0 delivered liveness probes and full wiring. Pod state machine already has `StatusFailed`.

Use case manifest: `.claude/scratch/usecases-manifest.json` (update after plan lands to track UC-051 orphan reconcile, UC-052 YAML colon scalars, UC-053 bounded container-start retry).

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | Synchronous, truthful DELETE | podman pod is gone OR API returns error and keeps the Spark record |
| D2 | Orphan reconciliation | Reconciler removes podman pods that have no Spark record |
| D3 | YAML scalar colon fix | List items with `://` or colons not followed by space preserved as scalar |
| D4 | Bounded container-start retry | backoffLimit honored; terminal failure with reason; no infinite retry |

### Out of Scope

- Async delete with a separate status endpoint.
- Rewriting the YAML parser.
- TCP probes, ConfigMap, Secret.

## Checkable Work Breakdown

### E54: Issue #12 -- DELETE leaves orphaned podman pod

- [x] T54.1 Make DELETE truthful about podman state  Owner: task-T54-1  Est: 45m  verifies: [UC-051]  Completed: 2026-04-15 (PR #14, 8578c09)
  - File: `internal/api/pods_mutate.go`.
  - Change `handleDeletePod` to: (1) call `executor.StopPod(ctx, name, grace)` and propagate error; (2) call `executor.RemovePod` and propagate error; (3) only on success, release scheduler resources and call `store.Delete`; (4) on error, return `500` with `{"deleted": false, "error": "..."}` and keep the store record so clients can retry.
  - Preserve 404 behavior when the pod is unknown.
  - Acceptance: unit test with stub executor returning error leaves store record intact and returns 5xx.

- [ ] T54.2 Reconcile orphans by removing them  Owner: TBD  Est: 45m  verifies: [UC-051]
  - File: `internal/reconciler/reconciler.go` around line 87.
  - When a podman pod is present but not in the store, call `executor.RemovePod(ctx, p.Name)` and log `"removed orphaned podman pod"`. On error, keep the existing warning so operators can page.
  - Gate behind a short grace window (e.g., ignore pods younger than one reconcile interval) to avoid races with in-flight `handleApplyPod` that has created the podman pod but not yet written the store record.
  - Acceptance: `go test -race ./internal/reconciler/` passes. New test: orphan present -> removed on next tick.

- [ ] T54.3 Tests and integration for delete truthfulness  Owner: TBD  Est: 30m  verifies: [UC-051]
  - Update `internal/api/pods_mutate_test.go`: success path, StopPod error path, RemovePod error path.
  - Add a reconciler test that simulates an orphan and asserts `executor.RemovePod` was called.
  - Acceptance: `go test -race ./internal/api/ ./internal/reconciler/` passes.

### E55: Issue #10 -- args/command with `://` silently dropped

- [x] T55.1 Fix list-item scalar vs map detection  Owner: task-T55-1  Est: 45m  verifies: [UC-052]  Completed: 2026-04-15 (PR #14, 50f7d7f)
  - File: `internal/manifest/yaml.go` around line 121.
  - Replace `strings.Index(itemContent, ":")` with a helper `findMapSeparator(s string) int` that returns -1 unless a `:` is followed by a space or end-of-string AND is not inside a quoted segment.
  - Apply the same helper in `parseYAMLLines` (line 42) for consistency so root/object keys behave the same way.
  - Leave quoted scalars (`"..."`, `'...'`) untouched.
  - Acceptance: new unit tests cover: `- "nats://10.88.0.1:4222"`, `- http://x/y`, `- key: value`, `- key:novalue` (still treated as map per YAML spec? no -- per YAML the `:` must be followed by space; this should be scalar. Document the choice with a comment.).

- [x] T55.2 Regression tests end-to-end through manifest.Parse  Owner: task-T55-1  Est: 30m  verifies: [UC-052]  Completed: 2026-04-15 (folded into T55.1 commit 50f7d7f; parse_test.go covers the issue #10 manifest end-to-end)
  - Depends on: T55.1.
  - Add test in `internal/manifest/yaml_test.go` (or `parse_test.go`) that parses the Pod manifest from issue #10 and asserts the container's `Command` slice equals `["echo", "hello", "nats://10.88.0.1:4222", "-port=8080"]`.
  - Add a block-style variant.
  - Acceptance: `go test -race ./internal/manifest/` passes.

### E56: Issue #8 -- infinite retry on container start failure

- [x] T56.1 Surface container start error on pod record  Owner: task-T56-1  Est: 45m  verifies: [UC-053]  Completed: 2026-04-15 (PR #14, 93871c4 + c3219cc)
  - Files: `internal/reconciler/reconciler.go`, `internal/state/sqlite.go` (if Reason column is missing, add it; otherwise reuse).
  - When the reconciler starts a pod and container start fails, set `rec.Status = Failed` with `Reason` containing the container-start stderr, increment a `StartAttempts` counter.
  - Acceptance: reconciler unit test with stub executor returning container-start error observes `StatusFailed` and a populated reason.

- [ ] T56.2 Honor backoffLimit before terminal failure  Owner: TBD  Est: 60m  verifies: [UC-053]
  - Depends on: T56.1.
  - Parse `spec.backoffLimit` for Pod/Job (already done for Jobs? verify in `manifest/job.go`). Default to 3 for Pods.
  - Reconciler: while `StartAttempts < backoffLimit` and container start fails, clean up podman pod (reuse #7 fix), bump counter, reschedule on next tick. When `StartAttempts == backoffLimit`, transition to `Failed` terminally and stop retrying.
  - Add exponential backoff between attempts (minimum 5s, cap 60s) to avoid hot-looping on transient errors.
  - Acceptance: reconciler test: 3 failures -> Failed; no 4th attempt observed over >=3 reconcile ticks.

- [ ] T56.3 Expose start attempts and reason via API  Owner: TBD  Est: 30m  verifies: [UC-053]
  - Depends on: T56.1, T56.2.
  - Update `GET /api/v1/pods/{name}` JSON response to include `startAttempts` and `reason` when non-empty.
  - Update `GET /api/v1/pods/{name}/events` emission to include a `ContainerStartFailed` event per attempt and a `PodFailed` event on terminal failure.
  - Acceptance: `go test -race ./internal/api/` passes with new fields asserted.

### E57: Integration, docs, release

- [ ] T57.1 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T54.*, T55.*, T56.*.
  - `go build ./...`, `go vet ./...`, `go test ./... -race -timeout 120s`, `staticcheck ./...`.

- [ ] T57.2 Update docs and changelog  Owner: TBD  Est: 20m  verifies: [infrastructure]
  - Depends on: T57.1.
  - `docs/design.md`: delete semantics, orphan reconciliation, bounded retry.
  - `docs/devlog.md`: append v1.6.2 entry describing root causes and fixes.
  - README: document new API response fields (`startAttempts`, `reason`).

- [ ] T57.3 Close issues with links to merged commits  Owner: TBD  Est: 10m  verifies: [infrastructure]
  - Depends on: T57.2, PRs merged by release-please.
  - Comment on #8, #10, #12 with commit SHAs and close.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: DELETE truthfulness | T54.1, T54.2, T54.3 | API handler + reconciler orphan removal + tests |
| B: YAML colon scalar | T55.1, T55.2 | Parser fix + regression tests |
| C: Container-start retry | T56.1, T56.2, T56.3 | Reason field + backoffLimit + API surface |

Tracks A, B, C are independent and touch disjoint packages. T57.* serializes.

### Waves

### Wave 1: Parallel fixes (3 agents)
- [ ] T54.1 Make DELETE truthful about podman state
- [ ] T55.1 Fix list-item scalar vs map detection
- [ ] T56.1 Surface container start error on pod record

### Wave 2: Follow-ups per track (3 agents)
- [ ] T54.2 Reconcile orphans by removing them
- [ ] T55.2 Regression tests end-to-end through manifest.Parse
- [ ] T56.2 Honor backoffLimit before terminal failure

### Wave 3: Remaining follow-ups (2 agents)
- [ ] T54.3 Tests and integration for delete truthfulness
- [ ] T56.3 Expose start attempts and reason via API

### Wave 4: Integration (3 agents)
- [ ] T57.1 Run full test suite and lint
- [ ] T57.2 Update docs and changelog
- [ ] T57.3 Close issues

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Issue #12 fixed | T54.1-T54.3 | DELETE is truthful and orphans auto-removed |
| M2 | Issue #10 fixed | T55.1-T55.2 | `://` args round-trip through manifest parse |
| M3 | Issue #8 fixed | T56.1-T56.3 | Bounded retry, terminal Failed, reason surfaced |
| M4 | Shipped | T57.1-T57.3 | Green CI, release-please PR merged, issues closed |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | Synchronous DELETE blocks client on saturated host | Medium | Medium | Keep 10s grace then force-remove; document in issue that DELETE may take up to ~15s under load |
| R2 | YAML helper regresses existing map parsing | High | Low | Comprehensive tests covering all known manifest shapes before merge |
| R3 | backoffLimit default of 3 is aggressive for transient errors | Low | Low | Exponential backoff between attempts; operators can override in manifest |
| R4 | Orphan removal races with in-flight apply | Medium | Low | Grace window of one reconcile interval before treating as orphan |

## Operating Procedure

- Each commit: one package directory, Conventional Commits, rebase-and-merge.
- Add tests alongside each code change.
- Run `go vet ./...` and `go test ./... -race -timeout 120s` locally before PR.
- release-please produces the version bump (expect v1.6.2) and tag after merges.
- Reply to each GitHub issue with the PR link after merge; do not close until tagged release is live.

## Progress Log

### 2026-04-15: Change Summary
- Trimmed completed v1.6.0 plan; knowledge preserved in `docs/devlog.md` (2026-03-20 entry) and existing ADRs.
- New plan targets three open issues: #12 (orphaned podman pod on DELETE), #10 (args/command with `://` dropped), #8 (infinite retry on container-start failure).
- 11 tasks across 4 waves, 3 parallel tracks. No new ADRs -- all fixes are within existing decisions.

## Hand-off Notes

- All three issues are present on `main` as of commit `819529c`.
- DGX host for manual verification: `http://192.168.86.250:8080`. Submit manifests via API; do not ssh in. See `CLAUDE.md` for workflow.
- Reconciler tick is ~5s; tests should set short intervals via constructor options rather than sleeping.
- YAML parser is a hand-rolled recursive descent in `internal/manifest/yaml.go`. Do not swap in a third-party library -- ADR-001 forbids it.
- Pod state machine: `Pending -> Running -> {Succeeded, Failed}`. `Failed` is terminal; do not reset to `Pending` once reached by backoff exhaustion.

## Appendix

- Issue #8: https://github.com/feza-ai/spark/issues/8
- Issue #10: https://github.com/feza-ai/spark/issues/10
- Issue #12: https://github.com/feza-ai/spark/issues/12
- Related merged fix (#7): commit 8f30f6d (remove stale pod before retry).
