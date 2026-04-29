# Spark: Resolve Open GitHub Issue #37 (Phantom Running Pods Leak Resources)

## Status: Planned

## Context

### Problem Statement

Issue #37: a Pod stays in `phase: running` in Spark's API after the
underlying podman container has exited or been removed. The pod's
resource quota (CPU cores, GPU slot, memory) is held forever, so new
pods that need the same resources queue indefinitely.

Observed 2026-04-28 with `wolf-train-crossasset-gpu-...`: 6000m CPU
held forever after a silent container exit. Operator had to call
`DELETE /api/v1/pods/<name>` manually.

### Root cause (already located)

`internal/executor/podman.go:378` — `PodStatus` runs
`podman pod inspect <name>`. If the pod has been removed from podman
state entirely, `podman pod inspect` exits non-zero and `PodStatus`
returns an error.

`internal/reconciler/reconciler.go:417-421` — `reconcileRunning`
treats every error from `PodStatus` as a transient failure: logs and
returns. It never marks the pod as Failed, never releases its
resources. Subsequent reconcile ticks repeat the same logged error
forever.

The reconciler already has the right primitive: `isNoSuchPod(err)` at
reconciler.go:479 is used by `reconcileScheduled` to detect missing
pods and recover. The fix is to apply the same pattern to
`reconcileRunning`.

### Objectives

- A pod whose podman container has been removed (returns "no such
  pod" from `podman pod inspect`) is marked `Failed`, scheduler
  resources are released, and an event documents the recovery.
- Transient errors (network blips, podman daemon hiccups) still log
  and retry — do not regress that path.
- Existing tests stay green; new tests cover the no-such-pod recovery
  in `reconcileRunning`.

### Non-Goals

- DELETE API timeout on wedged pods (issue notes this as adjacent;
  tracked separately).
- Subscribing to `podman events` (suggested in the issue as a
  long-term option). Polling via `PodStatus` per tick is sufficient
  for now.
- Liveness probe changes.

### Constraints

- Go standard library only (plus existing `nats.go`).
- Single binary, single node.
- Backwards-compatible API: existing event schema unchanged.

### Success Metrics

- After a podman container is `podman pod rm -f`'d behind Spark's
  back, the next reconcile tick (within ~5s) marks the pod Failed and
  frees its resources.
- `go test ./... -race` green.
- DGX live verify: kill a pod's podman backing pod manually, observe
  the Spark API transition to Failed within one reconcile tick.

## Discovery Summary

Code already located:

- `internal/executor/podman.go:374-394` — `PodStatus`. Currently
  returns an error if `podman pod inspect` fails for any reason.
- `internal/reconciler/reconciler.go:415-467` — `reconcileRunning`.
  Currently bails on any `PodStatus` error.
- `internal/reconciler/reconciler.go:479-484` — `isNoSuchPod` helper.
  Already used by `reconcileScheduled`.
- `internal/reconciler/reconciler_test.go` — existing test scaffolding
  for reconciler with a fake executor.

Two implementation options:

a) **Reconciler-side** (preferred): in `reconcileRunning`, when
   `PodStatus` returns an error, check `isNoSuchPod(err)`. If true,
   treat as "container gone" and run the same exited-pod transition
   path. Otherwise, log + return as today.
b) Executor-side: change `PodStatus` to return
   `Status{Running: false, ExitCode: -1}, nil` for "no such pod".

(a) is preferred because it keeps the executor's contract simple
("error means I couldn't determine status"), it parallels the
existing `reconcileScheduled` pattern, and it keeps "the container
went missing" explicit in the reconciler logic.

## Scope and Deliverables

In scope:

- Detect "no such pod" in `reconcileRunning` and transition the pod
  to Failed (or apply restart policy).
- Emit a `lost` event with a clear reason when the recovery fires.
- Targeted unit test using a fake executor that returns a `no such
  pod` error.

Out of scope:

- DELETE timeout on wedged pods.
- `podman events` subscription.

| ID | Deliverable | Acceptance |
|----|-------------|------------|
| D1 | `reconcileRunning` recovers from missing podman pods | A pod whose `PodStatus` returns "no such pod" is marked Failed (or restarted per policy), resources released, event recorded |
| D2 | Targeted regression test | Test asserts the recovery; existing tests still pass |
| D3 | DGX live verify | Manually `podman pod rm -f` a Spark-managed pod; observe Spark API transition within one tick |

## Checkable Work Breakdown

### Wave 1: Implementation (1 agent, sequential)

- [ ] T1.1 In `internal/reconciler/reconciler.go` `reconcileRunning`, branch on `isNoSuchPod(err)` from the `PodStatus` error path: if true, run the same exited-pod transition path that today fires when `st.Running == false` (release scheduler resources, apply restart policy, emit event with reason `lost: podman pod missing`). For other errors, keep the existing log-and-return behavior. Owner: TBD  Est: 30m  verifies: [issue-37]
  - Acceptance: a `PodStatus` error matching `isNoSuchPod` causes the pod to transition to Failed (or Pending under a restart policy), resources are released, and an event is recorded.
  - Subtask: S1.1.1 unit test in `reconciler_test.go` that injects a fake executor returning `errors.New("no such pod")` from `PodStatus` and asserts the pod ends up Failed with the resources released.

### Wave 2: Test, document, release, deploy, verify, close

- [ ] T2.1 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s`. Owner: TBD  Est: 15m  verifies: [infrastructure]
- [ ] T2.2 Append a devlog.md entry documenting the root cause and fix for issue #37. Owner: TBD  Est: 15m  delivers: [docs/devlog.md entry]
- [ ] T2.3 Open PR; rebase-and-merge; verify release-please cuts a tag; wait for the release artifact. Owner: TBD  Est: 15m  verifies: [infrastructure]
- [ ] T2.4 Live verify on DGX: submit a small alpine sleep pod via Spark, then `ssh ndungu@192.168.86.250 'podman pod rm -f <name>'`, and confirm Spark transitions the pod to Failed within ~5s with `lost: podman pod missing` in events; resources show as freed. Owner: TBD  Est: 20m  verifies: [issue-37]
- [ ] T2.5 Close issue #37 with PR/tag/verification links. Owner: TBD  Est: 5m  delivers: [closed issue #37]

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | `isNoSuchPod` doesn't match the actual podman 4.x error string | M | L | Test against the real DGX error output; add additional substring matches if needed |
| R2 | Recovery races with a legitimate concurrent restart | L | L | The transition is via `updateStatus` which is the standard path; no new concurrency primitives introduced |

## Operating Procedure

- Definition of done: PR merged via rebase-and-merge, release-please
  tag cut, DGX upgraded, live verify confirmed, issue #37 closed.
- Run `go vet ./...`, `staticcheck ./...`, `go test ./... -race -timeout 120s` before opening PR.
- Conventional Commits.

## Hand-off Notes

- Issue: https://github.com/feza-ai/spark/issues/37
- Existing primitive: `isNoSuchPod` (reconciler.go:479).
- Existing parallel pattern: `reconcileScheduled` (reconciler.go:486+).
- DGX endpoint: `http://192.168.86.250:8080`.

## Appendix

- Related code:
  - `internal/executor/podman.go` — `PodStatus`
  - `internal/reconciler/reconciler.go` — `reconcileRunning`, `isNoSuchPod`, `reconcileScheduled`
  - `internal/reconciler/reconciler_test.go` — fake executor scaffolding
- Issue link: feza-ai/spark#37.
