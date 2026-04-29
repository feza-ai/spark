# Spark: Resolve Open GitHub Issue #32 (Stuck-Pending GPU Pod Watchdog + HostPath Validation)

## Status: Planned

## Context

### Problem Statement

Issue #32: a Pod submitted with `priorityClassName: high` and
`nvidia.com/gpu: "1"` stayed in `status: pending` for 20+ minutes with
`startAttempts: 0`, `reason: ""`, and `events: []`. The reconciler
never logged any reconciliation activity for the pod even though
the cluster had ample free capacity. A prior submission of the same
pod with flow-style YAML had failed with `podman run train: exit
status 125: Error: host directory cannot be empty` (a podman-side
message that does not name the offending volume).

Three asks from the issue:

1. Root-cause why the reconciler skipped the pending pod.
2. Add a stuck-in-pending watchdog so silent pending becomes
   impossible -- always emit an event.
3. Replace the opaque `host directory cannot be empty` failure with
   a Spark-side admission error that names the volume whose hostPath
   is empty.

### Objectives

- Reconcile every pending pod on every tick, or emit a pending-watchdog
  event explaining why it cannot run (e.g. `awaiting-resources`,
  `awaiting-preemption`, `admission-error: ...`).
- Reject manifests with empty `hostPath.path` at parse/admission time
  with a message naming the volume (`volume "data": hostPath.path is
  empty`), before the manifest reaches the executor.
- Existing tests stay green; new tests cover the three asks.

### Non-Goals

- No change to scheduler scoring or preemption policy.
- No new priority class semantics.
- No retroactive recovery of pods already wedged in pending.

### Constraints

- Go standard library only (plus existing `nats.go`).
- Single binary, single node.
- Backwards-compatible API: existing `/api/v1/pods/{name}` event
  schema unchanged; new events use the same shape.

### Success Metrics

- Repro from the issue (block-style YAML, GPU=1, priority=high) either
  schedules or emits a watchdog event within one reconcile interval.
- Submitting a manifest with an empty `hostPath.path` returns 4xx with
  the volume name in the response body.
- `go test ./... -race` green.

## Discovery Summary

Engineering work. Existing relevant code:

- `internal/reconciler/reconciler.go` -- `reconcileOnce`,
  `reconcilePending`, status updates, OnStatusChange hook.
- `internal/scheduler/scheduler.go` -- `Pending`, `Preempting`,
  `Scheduled` outcomes for `Schedule()`.
- `internal/manifest/parse.go`, `internal/manifest/types.go` --
  `VolumeSpec{Name, HostPath, EmptyDir}` parsing.
- `internal/executor/podman.go` -- `buildRunArgs` consumes
  `vol.HostPath` directly with no empty check (the proximate cause of
  the cryptic podman error).
- `internal/api/` -- pod submission handler; emits/persists events.
- The "host directory cannot be empty" string is podman's, not ours;
  Spark currently passes through unvalidated.

The skipped-reconcile root cause is unknown but two suspects are most
likely:

a) The pod record's status was stuck in a non-`Pending` value (e.g.
   `Scheduled` after a prior failed attempt) so `reconcileOnce`'s
   switch never reached `reconcilePending`. The previous flow-style
   YAML submission errored out and the second submission may have
   inherited stale state.
b) Scheduler returned `Pending` but the reconciler took the silent
   `case scheduler.Pending` branch (reconciler.go:394) and fell
   through without emitting an event.

The watchdog task makes (b) impossible by construction: every
reconcilePending decision MUST emit at least one event. The
investigation task confirms or rules out (a).

## Scope and Deliverables

In scope:

- Pending watchdog: every reconcile of a Pending pod produces an event
  describing the outcome (scheduled, preempting, awaiting-resources,
  admission-error). No silent fall-throughs.
- HostPath validation at manifest parse time.
- Targeted unit + integration tests.

Out of scope:

- Scheduler algorithm changes.
- New API endpoints.
- Retro recovery for already-wedged pods.

| ID | Deliverable | Acceptance |
|----|-------------|------------|
| D1 | Pending-watchdog event in reconciler | Every Pending tick records an event; integration test asserts the event for a GPU pod that cannot fit |
| D2 | HostPath admission validation | Manifest with empty `hostPath.path` rejected at parse with volume name in error |
| D3 | Root-cause note for issue #32 | docs/devlog.md entry documenting the reproduction, finding, and fix |

## Checkable Work Breakdown

### Wave 1: Implementation (3 agents)

- [x] T1.1 Add hostPath validation in `internal/manifest/parse.go`. Reject empty `path` with `volume %q: hostPath.path is empty`. (PR #33, merged 2026-04-28.) Owner: TBD  Est: 45m  verifies: [issue-32-ask-3]
  - Acceptance: parse returns error naming the volume; existing parse tests still pass.
  - Subtasks: S1.1.1 unit tests in `parse_test.go` (valid hostPath, empty hostPath, missing hostPath when `emptyDir` not set).
- [x] T1.2 Add pending watchdog to `internal/reconciler/reconciler.go`. (PR #34, merged 2026-04-28.) In `reconcilePending`, every code path -- `Scheduled`, `Preempting`, `Pending`, error -- must call `updateStatus` with a non-empty message OR emit a dedicated event via the OnStatusChange hook. The `case scheduler.Pending` arm at reconciler.go:394 must record `awaiting-resources: <reason from scheduler>`. Owner: TBD  Est: 90m  verifies: [issue-32-ask-2]
  - Acceptance: a Pending pod that does not fit gets a status message every tick; integration test inspects the events list and asserts at least one watchdog event after one reconcile interval.
  - Subtasks: S1.2.1 extend `Schedule()` result with a `Reason string` (or read from existing fields) so the watchdog can quote why scheduling failed; S1.2.2 integration test in `reconciler_integration_test.go` covering a GPU=1 pending pod with no GPU available.
- [x] T1.3 Investigate the original repro (PR #35, merged 2026-04-28). Finding: `reconcileOnce` always reaches `reconcilePending` for a `Pending` pod; the silent path was inside `reconcilePending`'s `case scheduler.Pending` arm and is fixed by T1.2. FU1.3b filed for separate scheduler-accounting audit.: drop a fresh pod identical to the issue manifest into a unit-style fixture and assert the reconciler reaches `reconcilePending`. If we find a state where the reconciler does NOT reach `reconcilePending` for a Pending record, file the path and fix it in T2.x. Owner: TBD  Est: 60m  verifies: [issue-32-ask-1]
  - Acceptance: written finding in docs/devlog.md; if a code defect was found, a follow-up task is added; if not, the watchdog (T1.2) is documented as the sufficient mitigation.

#### Wave 1 follow-ups (from T1.3 Issue #32 investigation)

- [ ] FU1.3b Audit why the live scheduler returned `Pending` for
  Issue #32's pod despite ample free GPU/RAM/CPU. Likely candidate:
  `scheduler.go:89` skips candidates with `pod.Priority <= spec.Priority`,
  so if the running CPU-only pod's `Priority` was equal to (or lower
  numeric than) the new pod's, it was skipped during preemption-candidate
  gathering. Verify the priority class -> numeric mapping and the live
  `scheduler.pods` content during repro. Separate from T1.2 watchdog
  (visibility) -- this is correctness. Est: 90m.

### Wave 2: Test, lint, document, release (1 agent, sequential)

- [x] T2.1 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s` -- verified clean on main post-merge of PRs #33/#34/#35.
- [x] T2.2 Append a devlog.md entry: reproduce summary, root cause, fix description (done in PR #35, 2026-04-28).
- [ ] T2.3 Verify release-please cuts a tag (release PR #36 = v1.12.0 open); merge it; wait for the release artifact. Owner: TBD  Est: 15m  verifies: [infrastructure]
- [ ] T2.4 Deploy on DGX (`192.168.86.250`) and re-run the issue #32 repro: submit the pod manifest verbatim (block-style); confirm either it schedules or emits a watchdog event within one reconcile interval. Submit a deliberately-broken manifest with empty hostPath; confirm a 4xx response naming the volume. Capture `curl` output. Owner: TBD  Est: 30m  verifies: [issue-32-ask-1, issue-32-ask-2, issue-32-ask-3]
- [ ] T2.5 Close issue #32 with a comment linking the PR, the release tag, and the live verification output. Owner: TBD  Est: 10m  delivers: [closed issue #32]

## Parallel Work

| Track | Tasks | Notes |
|-------|-------|-------|
| A: manifest validation | T1.1 | Self-contained; touches only `internal/manifest/`. |
| B: reconciler watchdog | T1.2 | Touches `internal/reconciler/` and possibly `internal/scheduler/` for the reason field. |
| C: investigation | T1.3 | Read-only; can run alongside A and B. |

Sync point after Wave 1: all three tasks merge into a single PR
branch before Wave 2 (test/lint/release/verify).

## Timeline and Milestones

| Milestone | Exit Criteria | Member Tasks |
|-----------|---------------|--------------|
| M0 | Wave 1 implemented and unit-tested | T1.1, T1.2, T1.3 |
| M1 | CI green; PR merged; release published | T2.1, T2.2, T2.3 |
| M2 | DGX live verification passes; issue #32 closed | T2.4, T2.5 |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | T1.3 finds a deeper state-machine bug | M | M | Carve out as follow-up task; do not block T1.2 (watchdog already mitigates the user-visible symptom). |
| R2 | DGX unavailable for live verify | M | L | Hold T2.5 (issue close) until live verify passes; do not claim done without it. |
| R3 | Scheduler `Reason` field doesn't exist | L | M | Add a minimal field via S1.2.1; keep struct backwards-compatible. |

## Operating Procedure

- Definition of done: PR merged via rebase-and-merge, release-please
  tag cut, deployed to DGX, both live curl checks confirmed, issue
  #32 closed with evidence.
- Run `go vet ./...`, `staticcheck ./...`, and
  `go test ./... -race -timeout 120s` before opening PR.
- Small commits, one package per commit.
- Conventional Commit subjects.

## Progress Log

### 2026-04-28 -- Plan created

Replaced the prior Issue #28 housekeeping plan (since merged on main;
content recoverable from git) with a focused plan for Issue #32.
Three tasks in Wave 1 (hostPath validation, pending watchdog,
investigation), five sequential tasks in Wave 2 (verify, document,
release, live verify on DGX, close issue).

## Hand-off Notes

- Issue: https://github.com/feza-ai/spark/issues/32
- Repro manifest is in the issue body (block-style YAML).
- DGX endpoint: `http://192.168.86.250:8080`. Submit via
  `POST /api/v1/pods`. Pull image via `POST /api/v1/images/pull`.
- Existing reconciler integration test scaffolding lives in
  `internal/reconciler/reconciler_integration_test.go`.
- The `host directory cannot be empty` string comes from podman, not
  Spark; T1.1 cuts off the manifest before it reaches the executor.

## Appendix

- Related code:
  - `internal/manifest/parse.go`
  - `internal/manifest/types.go`
  - `internal/reconciler/reconciler.go`
  - `internal/scheduler/scheduler.go`
  - `internal/executor/podman.go`
- Issue link: feza-ai/spark#32.
