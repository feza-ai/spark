# Spark: Resolve Open GitHub Issue #13 (scheduler create retry)

## Status: Complete (merged to main, issue #13 closed; DGX smoke test deferred)

## Context

### Problem Statement

Issue #13: when `podman pod create` fails during `reconcilePending` (for example `signal: killed` from a context-deadline hit or a podman daemon hiccup), the pod is stored with `StatusScheduled` but the underlying podman pod is missing. The subsequent reconcile loop calls `podman pod inspect` on every tick and fails with `no such pod`, logging the error but never transitioning the pod back to `StatusPending`. The pod is stuck indefinitely, only unblocked by `DELETE /api/v1/pods/{name}` followed by a fresh `POST`.

Prior work already on main (this session): issues #8, #10, #12 resolved in v1.7.x. Relevant infrastructure now present: `StartAttempts` and `LastAttemptAt` on `PodRecord`, exponential backoff between retries (5s, 10s, 20s, 40s, cap 60s), `BackoffLimit` parsing on Pod spec, terminal `StatusFailed` transition after exceeding the limit, `SetClock` on Reconciler for deterministic tests. Issue #13 is the final missing hop: recognize "podman says no such pod" as a signal to re-queue, not a transient error.

### Objectives

- After `podman pod create` fails, the reconciler must retry on a subsequent tick (subject to backoff and `BackoffLimit`) without requiring the client to DELETE and re-POST.
- Differentiate `no such pod` (podman authoritative: missing) from transient inspect errors (daemon flake: leave state alone).
- Preserve the existing backoff/terminal-failure machinery added in T56.x; #13 plugs into it rather than duplicating it.

### Non-Goals

- Changing reconciler tick interval, scheduler behavior, or the executor API surface.
- Async create or separate retry endpoint.
- Handling partial-create states beyond "pod missing in podman".

### Constraints

- Go standard library only (plus `nats.go`, `modernc.org/sqlite`). ADR-001.
- Podman CLI only (no podman API socket). ADR-004.
- Conventional Commits, rebase-and-merge, one package per commit.
- Tests use `SetClock` + stub executor, no real podman.

### Success Metrics

- Repro case: pod in `StatusScheduled` whose podman pod is missing transitions back to `StatusPending` on the next reconcile tick after the staleness window, and `CreatePod` fires on the subsequent tick.
- Transient inspect errors (not "no such pod") leave the pod in `StatusScheduled`.
- Pod that has already exceeded `BackoffLimit` transitions to `StatusFailed` terminally instead of spinning in the Pending <-> Scheduled <-> missing loop.
- `go test ./... -race -timeout 120s` passes.
- Issue #13 closes via PR merge with `fixes #13` in the commit message.

## Discovery Summary

### Existing implementation

A local worktree at `/private/tmp/spark-scheduler-fix` (branch `fix/scheduler-create-retry`) already contains the implementation. Commit: `98c10bd` "fix(reconciler): retry create when scheduled pod is missing in podman".

Diff shape:
- `internal/reconciler/reconciler.go`: +51/-11 lines.
- `internal/reconciler/reconciler_test.go`: +134/-0 lines.

Changes:
1. New `isNoSuchPod(err)` helper: case-insensitive substring match for `"no such pod"`.
2. `reconcileScheduled` switches on the inspect error:
   - nil -> proceed to Running/stale logic.
   - `no such pod` -> treat as `Running: false`, log `scheduled pod missing in podman`, continue to staleness check.
   - Any other error -> log and return (leave state alone).
3. `scheduledStaleness` reduced from 30s to 10s so the next reconcile tick (5s) can act.
4. Pre-reset `BackoffLimit` check: if `pod.StartAttempts > pod.Spec.BackoffLimit` (and limit > 0), call `terminateAfterStartFailure` instead of resetting to Pending.
5. On reset to Pending: emit a `ScheduledRetry` event and include `attempts` in the log.
6. `r.now()` used throughout the scheduled path for deterministic tests.

Tests added:
- `TestReconcileScheduled_MissingPodAfterStalenessResetsToPending`
- `TestReconcileScheduled_MissingPodWithinStalenessKeepsScheduled`
- `TestReconcileScheduled_TransientInspectErrorKeepsScheduled`
- `TestReconcileScheduled_BackoffLimitExceededTransitionsToFailed`

Validation already run locally on the branch: `go build`, `go vet`, `go test ./... -race -timeout 120s` all PASS across 12 packages.

### Migration need

The existing worktree is outside the repo at `/private/tmp/spark-scheduler-fix`. Per the user directive "Work in git worktrees", new work in this session uses `.claude/worktrees/`. The existing branch is fine; we only need to push it and open the PR. No need to relocate the worktree or rebase.

## Scope and Deliverables

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | `fix/scheduler-create-retry` branch pushed to origin | Branch visible on GitHub, tests green in CI |
| D2 | PR opened, CI green, merged into main via rebase | PR state MERGED, main green |
| D3 | Issue #13 closed via `fixes #13` in PR body/commit | Issue state CLOSED linked to the merge commit |

## Checkable Work Breakdown

### E58: Issue #13 -- Scheduler stuck in "scheduled" after create failure

- [x] T58.1 Detect missing pod in `reconcileScheduled` and re-queue to Pending  Owner: user  Est: 60m  verifies: [UC-054]  Completed: 2026-04-15 (local commit 98c10bd on fix/scheduler-create-retry)
  - Already implemented on the local branch. See commit 98c10bd for details.
  - Acceptance: `go test -race ./internal/reconciler/` passes, including four new `TestReconcileScheduled_*` cases.

- [x] T58.2 Push branch to origin and open PR  Completed: 2026-04-15 (PR #18)
  - From the existing worktree: `git push -u origin fix/scheduler-create-retry`.
  - `gh pr create --base main --head fix/scheduler-create-retry --title "fix(reconciler): retry create when scheduled pod is missing in podman"` with a body that references issue #13 and summarizes the change.
  - Acceptance: PR URL returned, CI starts running.

- [x] T58.3 Wait for CI to pass  Completed: 2026-04-15 (build pass 2m9s)
  - Depends on: T58.2.
  - `gh pr checks <pr> --watch`. Zero failures required.
  - If CI fails, read logs, fix locally, push, repeat. Cap at 3 attempts before escalating.

- [x] T58.4 Rebase-merge PR and sync main  Completed: 2026-04-15 (commit a35ac4e; issue #13 auto-closed)
  - Depends on: T58.3.
  - `gh pr merge <pr> --rebase --delete-branch`.
  - `git checkout main && git pull origin main`. Verify `git log --oneline -3` shows the merged commit.
  - Acceptance: PR state MERGED, issue #13 auto-closes from `fixes #13`.

- [ ] T58.5 Smoke test against DGX Spark  Owner: coordinator  Est: 15m  verifies: [UC-054]
  - Depends on: T58.4.
  - Wait for release-please to open the version bump PR; merge it to cut a new tag.
  - On the DGX host, update the Spark binary to the new version. Submit a throwaway pod manifest, verify `/api/v1/pods/<name>` eventually reaches Running.
  - Kill the podman pod manually (`podman pod rm -f <name>`) while Spark holds a `StatusScheduled` record; verify that within 15s the pod resets to Pending, retries create, and reaches Running.
  - Acceptance: reproducer is gone, manual kill recovers automatically.

- [x] T58.6 Clean up the non-standard worktree path  Completed: 2026-04-15 (removed /private/tmp/spark-scheduler-fix)
  - Depends on: T58.4.
  - After merge and branch deletion: `git worktree remove /private/tmp/spark-scheduler-fix`.
  - Future fixes use `.claude/worktrees/<name>`.
  - Acceptance: `git worktree list` no longer shows `/private/tmp/spark-scheduler-fix`.

## Parallel Work

Only one independent unit of work remains. No parallelism benefit.

### Waves

### Wave 1: Ship (1 agent)
- [x] T58.2 Push branch to origin and open PR  Completed: 2026-04-15 (PR #18)
- [x] T58.3 Wait for CI to pass  Completed: 2026-04-15 (build pass 2m9s)
- [x] T58.4 Rebase-merge PR and sync main  Completed: 2026-04-15 (commit a35ac4e; issue #13 auto-closed)
- [x] T58.6 Clean up the non-standard worktree path  Completed: 2026-04-15 (removed /private/tmp/spark-scheduler-fix)

Wave 1 is strictly sequential because each step depends on the prior.

### Wave 2: Validate on DGX (1 agent)
- [ ] T58.5 Smoke test against DGX

Wave 2 runs after release-please produces a new tag and the DGX is updated.

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | PR merged | T58.2-T58.4 | Main contains the fix; issue #13 closed |
| M2 | Worktree tidy | T58.6 | `/private/tmp/spark-scheduler-fix` removed |
| M3 | DGX verified | T58.5 | Reproducer no longer occurs on DGX |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | `scheduledStaleness` reduction from 30s to 10s triggers premature retries on a transiently slow podman daemon | Medium | Low | The staleness check only fires AFTER inspect returns `no such pod`. A slow daemon returns timeout, not `no such pod`, and hits the transient-error branch which leaves state alone. |
| R2 | Case-insensitive substring match for `no such pod` collides with a future podman error message | Low | Low | If podman ever emits a different message, test suite catches the regression. Add to integration test matrix in a future task. |
| R3 | Pod exceeds BackoffLimit via scheduled-miss path but Reason is empty | Low | Low | Fix already provides a fallback reason string `"pod missing in podman after <staleness>"`. |
| R4 | CI pre-existing flake fails the PR | Medium | Low | Full test suite passes locally. If CI flakes, `gh run rerun` on the specific job. |

## Operating Procedure

- One PR per issue; `fixes #13` in the body.
- Rebase-merge (not squash, not merge commits). Per CLAUDE.md.
- Do not push to remote DGX host directly; ship via release-please + tag + manual update.
- On merge, release-please should open a version-bump PR. Review and merge it to cut a tag.

## Progress Log

### 2026-04-15: Change Summary
- Trimmed completed v1.6.2 plan (issues #8, #10, #12). Knowledge preserved in `docs/devlog.md` (2026-04-15 entry written during prior /apply session).
- New plan targets only remaining open issue #13. The code change is already implemented on local branch `fix/scheduler-create-retry` (commit 98c10bd) and tests pass.
- Remaining work is shipping: push, PR, CI, merge, release, DGX smoke test.
- No new ADRs: fix stays within existing decisions (ADR-001 stdlib-only, ADR-004 podman CLI).

## Hand-off Notes

- Branch `fix/scheduler-create-retry` exists locally at `/private/tmp/spark-scheduler-fix` with commit `98c10bd`. If the worktree is missing, checkout the branch in a fresh worktree under `.claude/worktrees/fix-scheduler-create-retry` and re-apply the commit.
- The commit message already contains a clear summary of the change and includes `Fixes #13`. When creating the PR, use this as the body.
- DGX smoke test is the only step requiring real hardware. All other steps are CI + local.
- Prior context: plan trimmed earlier in this session produced the devlog entry at the top of `docs/devlog.md`. Consult it for the context around #8, #10, #12 fixes that this plan builds on.

## Appendix

- Issue #13: https://github.com/feza-ai/spark/issues/13
- Related merged fixes: PRs #14, #16, #17 (issues #8, #10, #12).
- Fix commit: local `98c10bd` on branch `fix/scheduler-create-retry`.
