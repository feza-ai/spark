# Spark: Triage and Resolve Unplanned, Untriaged GitHub Issues

## Status: Active. E1 shipped except the live-verify task (T1.6). E2's #85 slice shipped; #73/#77 and the newly discovered #88 remain. E3-E5 ready to dispatch. E6-E7 are outline epics pending investigation/design.

## Context

### Problem statement

The DGX capacity investigation on 2026-08-28 (see docs/roadmap.md and
docs/devlog.md) led to fixing issues #76 and #81, and finding a new one
(#85) live on the shared host. A full sweep of `feza-ai/spark`'s open
issues at that point showed 9 more untriaged reports with no plan
covering them, spanning 2026-07-10 through 2026-08-20. This plan
triages all of them into sequenced, executable work.

### Objectives

- Every open, untriaged issue on `feza-ai/spark` has an owning epic in
  this plan, with a fidelity tier reflecting how well understood it is.
- Frontier epics (well-scoped, no blocking investigation) are
  decomposed to executable tasks with acceptance criteria and, since
  kazi is on PATH, `acc:` predicates.
  Harder epics that need investigation or a design call are outline
  epics with a single planning task, not guessed at now.
- Epics that touch the same files as issue #76's still-open PR #83 are
  sequenced after it merges, so this plan does not repeat the merge
  conflict already hit once this session between PR #82 and PR #83.

### Non-goals

- Re-litigating already-shipped work (#37, #81) or already-dispatched
  work already covered by its own agent brief (#76's PR #83, #85's PR
  once opened) beyond tracking their remaining steps here.
- A full product-wide use case re-catalog. `.claude/scratch/usecases-manifest.json`
  covers only the pod-lifecycle/admission surface these 9 issues touch.
- Resolving the #79 anti-thrash-cap trade-off or the #80 root cause at
  planning time -- both need engineering judgment during execution,
  flagged inline rather than decided here.

### Constraints and assumptions

- Go standard library only, except `github.com/nats-io/nats.go`. Podman,
  not Docker. Standard `flag` package for CLI flags (see project
  CLAUDE.md).
- Single-binary, single-node orchestrator on a shared DGX GB10 host
  that also runs live CI runners and real workloads -- every fix needs
  a red-then-green test, not just a green test, and live verification
  before being called done.
- kazi is on PATH (`/opt/homebrew/bin/kazi`): every engineering task
  below carries an `acc:` predicate line.
- `/apply --pool` executes this plan; up to 10 agents in parallel, one
  isolated git worktree per task. Two agents are already running
  outside this plan's dispatch (issue #76 rebase, issue #85 build) --
  do not additionally dispatch new agents against files those two
  touch until they merge (see Risk Register R1).

### Success metrics

- All 9 previously-untriaged issues (#47, #71, #73, #74, #75, #77,
  #78, #79, #80) are either closed with a merged, released,
  live-verified PR, or have an outline epic with a completed planning
  pass ready for the next `/apply` wave.
- `go test ./... -race -timeout 120s` stays green throughout; no
  regression in the 14-package suite.
- Zero new merge conflicts between epics in this plan (verified by
  sequencing overlapping-file epics after their blockers merge).

## Discovery Summary

Engineering work. Discovery drew on: `gh issue list`/`gh issue view`
for all 9 issues' full bodies (several already contain a root-cause
analysis or a suggested fix written by the issue author); direct
reading of `docs/design.md`, `docs/adr/001-014`, and `docs/devlog.md`;
and this session's own hands-on work fixing #76/#81/#85 in the same
codebase (scheduler, reconciler, executor, state, housekeeper, api
packages).

Use case manifest: `.claude/scratch/usecases-manifest.json`, 12 use
cases (UC-101 through UC-112), covering pod ingestion (YAML/JSON),
GPU scheduling, command-override execution, delete, backoff-to-failure,
pending-vs-lost status, preemption fairness, crash-restart recovery,
and unified-memory OOM protection. 2 WIRED-and-healthy (UC-104, UC-112,
both hardened/in-progress this session), 1 already fixed this session
(GPU pod scheduling, UC-104), 8 BROKEN, 1 PLANNED (UC-110), 1 PLANNED
outline (UC-111).

Key findings that shape the plan's structure:

1. **A recurring root-cause family**: three of the nine issues (#73,
   #77, and the #85 fix already in flight) all trace to the same
   `internal/executor/podman.go` function pair (`buildRunArgs` /
   `injectGPUDevices`) that translates a manifest's `command`/`args`
   into a podman invocation. #85's fix (in progress) replaces the
   fragile two-pass, positional-scan approach with a single inline
   pass. #73 and likely #77 should land *after* #85 merges, both to
   reuse its fix and to avoid a third conflict in the same file this
   session.
2. **A second recurring family**: #74 (JSON POST silently no-ops) is
   very likely the hand-rolled YAML parser (`internal/manifest`)
   attempting to parse JSON as YAML and silently returning a
   zero-value result instead of erroring -- the same "silently
   defaults instead of erroring" pattern the devlog's 2026-03-20
   standing lesson already named for four earlier bugs (#43, #44,
   #52, #66) and the flow-style-map fix in 66df4af. Confirm at
   implementation time; do not assume the exact mechanism without
   reading `internal/manifest/parse.go`'s content-type dispatch (or
   lack of one) first.
3. **File overlap with the in-flight #76 PR (#83)**: `#79` touches
   `internal/scheduler/scheduler.go`, and `#71`/`#75`/`#78`/`#80`
   all touch `internal/reconciler/reconciler.go` -- both files #76
   already modified. Sequencing these after #76 merges avoids
   repeating the #82/#83 conflict from earlier this session.
4. **#80 is two problems, not one**: a well-scoped, independent
   addition (`GET /api/v1/pods/{name}/manifest`, UC-110) and a small
   isolated signedness bug (`gpu 0 > -1 free` in the shortfall
   message), both frontier; plus a genuinely open state-divergence
   root cause (pod reports `pending` while its container is alive and
   serving) that is not yet understood well enough to decompose --
   that piece is its own outline epic (E6).
5. **#47 is an enhancement, not a bug**, and shares conceptual
   territory (admission headroom, host-level thresholds) with #76's
   utilization-aware admission work landing right now. Sequenced after
   E1 and E5 so it can build on both rather than duplicate reasoning
   about live-headroom checks.

## Scope and Deliverables

In scope: all 9 untriaged issues (#47, #71, #73, #74, #75, #77, #78,
#79, #80), plus tracking the 2 already-in-flight issues (#76, #85)
through to merge since this plan's sequencing depends on them.

Out of scope: any issue not yet filed; UI/browser surfaces (Spark has
none); non-DGX deployment targets.

| ID | Deliverable | Acceptance |
|----|-------------|------------|
| D1 | Issue #76 merged, live | PR #83 rebased onto post-#81 main, CI green, merged, released, DGX confirms overcommit admission |
| D2 | Issue #85 merged, live | PR opened and reviewed, merged, released, DGX confirms a GPU+command-override pod starts |
| D3 | Issue #73 fixed | Long quoted scalar in `command` survives to the container unmangled |
| D4 | Issue #77 root-caused and fixed or scoped | Silent instant-complete container-spec-drop either fixed or converted to a well-scoped follow-up |
| D5 | Issue #74 fixed | JSON POST creates a pod identically to YAML, or rejects with 400 |
| D6 | Issue #71 fixed | DELETE never leaves a phantom record on a podman stop-then-rm race |
| D7 | Issue #78 fixed | Pending-for-resources is distinguishable from a genuinely lost pod via `/logs` and `/events` |
| D8 | Issue #75 fixed | `backoffLimit: 0` reaches terminal `Failed` on the first `CreatePod` error |
| D9 | Issue #80 quick wins shipped | `GET /api/v1/pods/{name}/manifest` exists; the `gpu 0 > -1 free` signedness bug is fixed |
| D10 | Issue #79 fixed | A high-priority pod is not silently starved behind more than 3 lower-priority victims; the event message names the real shortfall |
| D11 | Issue #80's state-divergence root cause | Investigated and either fixed or handed off as a well-scoped follow-up plan |
| D12 | Issue #47 designed and either shipped or handed off | Admission headroom reserve + usage-based OOM alerting either implemented or handed off as an executable epic |

## Checkable Work Breakdown

### E1: Utilization-aware CPU admission (issue #76)

Acceptance: PR #83 merged to main, released, and the DGX confirms a
CPU-bound pod schedules via the overcommit bypass when accounted CPU is
full but real load shows headroom.
fidelity: executable

- [x] T1.1 Utilization-aware CPU admission implemented (`Scheduler.HostLoadSource`, `CanFitIgnoringCPU`, `AllocateOverCommittingCPU`), `--cpu-overcommit-margin-millis`/`--host-load-sample-interval` flags, `requested` field on `GET /api/v1/pods/{name}`, `spark_cpu_overcommit_admissions_total` metric, `SavePod` upsert fix (was silently wiping event history via `INSERT OR REPLACE` cascade-delete). Owner: spark-fix-76  Est: -  verifies: [UC-112]  (completed 2026 08 28)
- [x] T1.2 Judgment call (load window / safety margin) reviewed and confirmed; recorded in docs/adr/013-utilization-aware-admission.md. Owner: this session  Est: -  verifies: [UC-112]  (completed 2026 08 28)
- [x] T1.3 Rebase `fix/76-utilization-aware-overcommit` onto post-#81 main; resolve conflicts in `cmd/spark/main.go`, `docs/devlog.md`, `internal/metrics/collector.go`, `internal/metrics/collector_test.go`, `internal/reconciler/reconciler.go`, `internal/scheduler/resources.go`, `internal/scheduler/resources_test.go` -- both sides' changes must survive; the GPU stale-assignment clear from #81 must end up inside the new shared `allocate(name, req, skipCPUCheck)` helper. Owner: spark-fix-76  Est: 45m  verifies: [UC-112, infrastructure]  acc: [gh pr view 83 reports mergeable=MERGEABLE and go test ./... -race -timeout 120s -count=1 is green on the rebased branch]  kind: agent  (completed 2026 08 28 -- verified independently by reading the merged `allocate` helper directly)
- [x] T1.4 Re-verify full suite after rebase (build/vet/staticcheck/test), confirm CI green on PR #83, merge (rebase, not squash). Owner: this session  Est: 15m  verifies: [infrastructure]  acc: [gh pr view 83 --json state shows MERGED]  (completed 2026 08 28 -- PR #83 MERGED)
- [x] T1.5 Merge the resulting release-please PR; watch the tag-triggered Release workflow to completion; trigger the DGX auto-upgrade timer; confirm `/healthz` reports the new version. Owner: this session  Est: 15m  verifies: [infrastructure]  acc: [curl http://192.168.86.250:8080/healthz reports the version tagged by this release]  (completed 2026 08 28 -- /healthz reports {"status":"ok","version":"1.18.0"})
- [ ] T1.6 Live verify: with the node CPU-saturated by idle reservations, submit a pod whose request exceeds accounted headroom but fits real load; confirm it schedules via the overcommit path and `spark_cpu_overcommit_admissions_total` increments. Owner: TBD  Est: 20m  verifies: [UC-112]  acc: [a pod submitted under phantom CPU saturation reaches status=running and CPUOvercommitAdmissions()>0 is observable via /metrics]  (not yet performed -- v1.18.0 is deployed but the overcommit bypass itself has not been live-triggered and observed)
- [x] T1.7 Update docs/roadmap.md (move E1 from In flight to Shipped); close issue #76. Owner: this session  Est: 10m  delivers: [docs/roadmap.md updated, issue #76 closed]  (completed 2026 08 28 -- issue #76 auto-closed by PR #83)

### E2: Executor command/entrypoint fragility (issues #85, #73, #77)

Acceptance: a GPU-assigned pod with a multi-token `command` starts
reliably; a `command` containing a long quoted scalar with nested
quotes reaches the container unmangled; #77's silent-drop failure is
either fixed or root-caused into its own scoped follow-up.
fidelity: executable

- [x] T2.1 (issue #85) Rebuild GPU env-injection inline in `buildRunArgs` instead of the `injectGPUDevices` positional-scan post-pass; remove the flag-skip-list class of bug entirely. Owner: spark-fix-85  Est: -  verifies: [UC-103]  acc: [a pod requesting nvidia.com/gpu with a multi-token command starts successfully and podman receives an intact image reference]  (completed 2026 08 28)
- [x] T2.1a Review and independently verify #85's PR (build/vet/staticcheck/test, read the diff, confirm live if the agent didn't already). Owner: this session  Est: 20m  verifies: [UC-103]  acc: [go test ./... -race -timeout 120s -count=1 green on the PR branch, confirmed independently not just via the agent's self-report]  (completed 2026 08 28 -- rebased onto post-#83 main by this session after a docs/roadmap.md conflict, full suite re-run green)
- [x] T2.1b Merge PR (rebase), merge the release-please PR, deploy, confirm live on DGX. Owner: this session  Est: 20m  verifies: [infrastructure]  acc: [curl http://192.168.86.250:8080/healthz reports the new version]  (completed 2026 08 28 -- PR #86 MERGED, /healthz reports 1.18.0, live repro manifest that previously failed with "invalid reference format" now starts successfully with the GPU device attached)
- [x] T2.1c Close issue #85. Owner: this session  Est: 5m  delivers: [issue #85 closed]  (completed 2026 08 28 -- auto-closed by PR #86)
- [ ] T2.9 (issue #88, newly discovered 2026 08 28) `DELETE /api/v1/pods/{name}` on a GPU-attached pod hung for ~7 minutes during T2.1b's live verification -- `podman pod stop` was retried three times, a `podman` child process parented by spark's own PID became a zombie (exited but never reaped), and an unrelated, unfiltered `sudo podman pod ps` also hung host-wide for the same window. Self-resolved without a `spark.service` restart; not yet root-caused. Investigate `StopPod`/`RemovePod`/`StreamPodLogs` in `internal/executor/podman.go` for a wedged-context or lock-contention path with no timeout of its own. Owner: TBD  Est: 60m  verifies: [UC-105]  acc: [a targeted test or repro demonstrates the specific lock/timeout gap, and a fix bounds the stop/delete path's own wait so it cannot block host-wide podman pod-enumeration commands]  lane: agent
- [ ] T2.2 (issue #73) Reproduce with the exact manifest from the issue (a ~900-char `command[2]` with nested double quotes); trace where the argument is lost between `state.db`'s stored `spec_json` (already confirmed correct) and the actual podman invocation -- likely the same JSON-array `--entrypoint` encoding path #2.1 touches. Write a failing test first. Owner: TBD  Est: 45m  verifies: [UC-103]  acc: [the exact repro command from issue #73 reaches the container as a single intact argv element, verified via a test that execs a real echo/printf and checks output]  blocked-by: [T2.1b]
- [ ] T2.3 (issue #73) Fix and add regression test using the exact reported script content (nested quotes, ~900 chars). Owner: TBD  Est: 30m  verifies: [UC-103]  acc: [go test ./internal/executor/... -race passes a new test built from the #73 repro manifest]  deps: [T2.2]
- [ ] T2.4 (issue #77) Investigate: reproduce the "pod completes instantly, zero container startup" sequence with the exact manifest from the issue (2-element `command`+`args`, `restartPolicy: Always`); determine whether it shares root cause with #73/#85 (same buildRunArgs path) or is a distinct intermittent failure (the issue notes the identical manifest shape worked once earlier in the same session, suggesting non-determinism, not a hard parse bug). Owner: TBD  Est: 60m  verifies: [UC-103]  acc: [either a reliable repro is found and asserted in a test, or the investigation produces a written root-cause hypothesis with specific next steps in docs/devlog.md]  blocked-by: [T2.1b]  lane: agent
- [ ] T2.5 (issue #77) Fix if root-caused in T2.4; otherwise write up findings and open a narrower follow-up issue with what was ruled out. Owner: TBD  Est: 45m  verifies: [UC-103]  deps: [T2.4]  lane: agent
- [ ] T2.6 Also add regression coverage for the flag-skip-list's other gaps that #2.1's inline rewrite should have fixed as a side effect: `--cpuset-cpus`, `--user`, `--cap-add`, `--cap-drop` combined with a GPU device. Owner: TBD  Est: 20m  verifies: [UC-104]  acc: [go test ./internal/executor/... -race covers all four flags combined with a GPU-assigned pod and passes]  deps: [T2.1b]
- [ ] T2.7 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s -count=1` across all of E2's changes combined. Owner: TBD  Est: 15m  verifies: [infrastructure]  acc: [exit code 0 on all three commands]  deps: [T2.3, T2.5, T2.6]
- [ ] T2.8 Open PR(s) for #73/#77 fixes (can be one PR or two depending on whether #77 shares the fix); merge (rebase), release, deploy, live-verify on DGX with the exact reported repro manifests; close #73 and #77 (or #77's narrower follow-up). Owner: TBD  Est: 30m  verifies: [infrastructure]  deps: [T2.7]

### E3: Manifest JSON ingestion gap (issue #74)

Acceptance: `POST /api/v1/pods` with `Content-Type: application/json`
either creates the pod identically to the YAML path, or rejects with
400 -- never a silent 201 that creates nothing.
fidelity: executable

- [ ] T3.1 Read `internal/api/pods_mutate.go`'s `handleApplyPod` and `internal/manifest/parse.go` to find where JSON bodies are (mis)handled -- confirm or refute the hypothesis that the hand-rolled YAML parser is silently accepting JSON as a degenerate/empty YAML document rather than erroring. Owner: TBD  Est: 30m  verifies: [UC-102]  acc: [a written root-cause note names the exact function and line where a JSON body produces a zero-value ParseResult without an error]
- [ ] T3.2 Fix: either make the parser JSON-aware (JSON is a YAML subset; the fix may be as small as detecting `{` as the first non-whitespace byte and routing to `encoding/json`), or make `handleApplyPod` reject unparseable/empty-result manifests with 400 instead of 201. Prefer making JSON actually work, since it is documented as supported. Owner: TBD  Est: 45m  verifies: [UC-102]  acc: [POST /api/v1/pods with Content-Type: application/json and a valid pod body returns 201 with a non-null pods array, and GET /api/v1/pods/{name} finds it]  deps: [T3.1]
- [ ] T3.3 Regression test: the exact JSON manifest shape from the issue, asserting a real pod is created and independently a malformed JSON body gets 400, not 201. Owner: TBD  Est: 30m  verifies: [UC-102]  acc: [go test ./internal/api/... -race covers both the valid-JSON-creates-pod and invalid-JSON-rejects-with-400 cases]  deps: [T3.2]
- [ ] T3.4 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s -count=1`. Owner: TBD  Est: 15m  verifies: [infrastructure]  acc: [exit code 0 on all three]  deps: [T3.3]
- [ ] T3.5 Open PR; merge (rebase); release; deploy; live-verify with a real JSON POST against the DGX; close issue #74. Owner: TBD  Est: 30m  verifies: [infrastructure]  deps: [T3.4]

### E4: Pod lifecycle status and DELETE reconciliation (issues #71, #75, #78, #80 quick wins)

Acceptance: DELETE never leaves a phantom record on a podman
stop-then-rm race; `backoffLimit: 0` reaches terminal `Failed` on the
first `CreatePod` error; a pending-for-resources pod is distinguishable
from a genuinely lost one; `GET /api/v1/pods/{name}/manifest` exists;
the `gpu 0 > -1 free` signedness bug is fixed.
fidelity: executable
blocked-by: [T1.4] (shares `internal/reconciler/reconciler.go` with E1's PR #83 -- do not branch until #76 merges, to avoid repeating the #82/#83 conflict)

- [ ] T4.1 (issue #71) In the DELETE path (`internal/api/pods_mutate.go`, and the equivalent NATS handler), classify "cgroup slice not loaded" / already-gone-cgroup errors from `podman pod rm` the same way `isNoSuchPod` is already classified elsewhere -- desired end state (pod gone) is already true, proceed with store/scheduler cleanup instead of returning 500 and aborting. Add a bounded retry on `rm` to also cover the race window itself. Owner: TBD  Est: 45m  verifies: [UC-105]  acc: [a DELETE that hits a simulated 'cgroup slice not loaded' error still returns deleted:true and the store record and scheduler reservation are gone]
- [ ] T4.2 (issue #71) Regression test using a fake executor that returns the exact "cgroup: Unit machine-libpod_pod_<id>.slice not loaded" error text on first `RemovePod` call. Owner: TBD  Est: 20m  verifies: [UC-105]  acc: [go test ./internal/api/... -race (and the equivalent bus test) cover this exact error string]  deps: [T4.1]
- [ ] T4.3 (issue #75) In `internal/reconciler/reconciler.go`'s `reconcilePending` (and its `Preempting` twin), treat `BackoffLimit == 0` as "fail on first CreatePod error" instead of "no limit enforced" -- drop the `> 0` guard or special-case `== 0` to call `terminateAfterStartFailure` immediately. Owner: TBD  Est: 30m  verifies: [UC-106]  acc: [a Job with backoffLimit: 0 whose CreatePod always fails reaches status=failed after exactly one attempt, not an unbounded retry loop]
- [ ] T4.4 (issue #75) Regression test: fake executor whose CreatePod always errors, backoffLimit: 0, assert status transitions to Failed after attempt 1. Owner: TBD  Est: 20m  verifies: [UC-106]  acc: [go test ./internal/reconciler/... -race covers this exact case]  deps: [T4.3]
- [ ] T4.5 (issue #78) Distinguish "pending, awaiting resources" from "podman has lost the pod" in the logs endpoint -- do not treat "no such pod" from `podman pod logs` as fatal while the pod's own status is still Pending; add a `pendingTimeout` config so callers can tell "still queued" from "gave up"; surface the shortfall message in the error returned to HTTP callers instead of a generic "lost" message. Owner: TBD  Est: 45m  verifies: [UC-107]  acc: [GET /api/v1/pods/{name}/logs on a pod that is status=pending due to resource shortfall returns an empty/not-started response, never an error implying the pod vanished]
- [ ] T4.6 (issue #78) Regression test: a pod pending on resource shortfall, assert /logs does not error and /events surfaces the shortfall reason. Owner: TBD  Est: 20m  verifies: [UC-107]  acc: [go test ./internal/api/... -race covers this]  deps: [T4.5]
- [ ] T4.7 (issue #80, quick win 1) Add `GET /api/v1/pods/{name}/manifest` returning the exact submitted manifest bytes (or an equivalent structured form), sourced from the existing `spec_json`/source-path tracking already in `state.PodRecord`. Owner: TBD  Est: 30m  verifies: [UC-110]  acc: [GET /api/v1/pods/{name}/manifest returns the manifest that was POSTed for that pod, byte-equivalent modulo whitespace]
- [ ] T4.8 (issue #80, quick win 1) API test for the new endpoint (found pod, not-found pod). Owner: TBD  Est: 15m  verifies: [UC-110]  acc: [go test ./internal/api/... -race covers both cases]  deps: [T4.7]
- [ ] T4.9 (issue #80, quick win 2) Fix the `gpu 0 > -1 free` signedness bug in the shortfall-describing code (`internal/scheduler`'s `describeShortfall` or equivalent free-GPU computation) -- a negative "free" value should never be possible or displayed. Owner: TBD  Est: 20m  verifies: [UC-108, UC-109]  acc: [a shortfall message never displays a negative free-resource value; a targeted test asserts the free-GPU computation floors at 0]
- [ ] T4.10 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s -count=1` across all of E4's changes. Owner: TBD  Est: 15m  verifies: [infrastructure]  acc: [exit code 0 on all three]  deps: [T4.2, T4.4, T4.6, T4.8, T4.9]
- [ ] T4.11 Open PR; merge (rebase); release; deploy; live-verify each of the four fixes on DGX; close issues #71, #75, #78. Owner: TBD  Est: 40m  verifies: [infrastructure]  deps: [T4.10]

### E5: Preemption fairness (issue #79)

Acceptance: a high-priority pod is not silently starved behind more
than 3 lower-priority victims; the event message names the real
shortfall rather than a misleading "evicting N candidates".
fidelity: executable
blocked-by: [T1.4] (shares `internal/scheduler/scheduler.go` with E1's PR #83)

- [ ] T5.1 Reproduce: a high-priority pod behind more than 3 lower-priority, non-thrashed pods; confirm it never schedules due to the anti-thrash cap, and confirm the misleading event message. Write the failing test first. Owner: TBD  Est: 30m  verifies: [UC-108]  acc: [a test with 4+ eligible lower-priority victims and a high-priority pending pod shows Pending forever under current code]
- [ ] T5.2 Fix: this needs a real design call, not a mechanical patch -- raising or removing the per-victim anti-thrash cap trades starvation-prevention for the original thrash-prevention the cap exists for. Do not silently pick a resolution; if the fix isn't obvious from the issue alone (e.g. cap per-pod-being-scheduled rather than global, or extend the anti-thrash window instead of the count), stop and report the trade-off rather than guessing. Also fix the event message to name the real shortfall instead of "evicting N candidates" when the cap is what's blocking, not availability. Owner: TBD  Est: 60m  verifies: [UC-108]  acc: [the T5.1 test goes green without introducing a preemption-storm regression test failure]  deps: [T5.1]  lane: agent
- [ ] T5.3 Regression test using the exact issue scenario (max 3/pod cap, 4+ candidates, high-priority pod). Owner: TBD  Est: 20m  verifies: [UC-108]  deps: [T5.2]
- [ ] T5.4 `go vet ./... && staticcheck ./... && go test ./... -race -timeout 120s -count=1`. Owner: TBD  Est: 15m  verifies: [infrastructure]  acc: [exit code 0 on all three]  deps: [T5.3]
- [ ] T5.5 Open PR; merge (rebase); release; deploy; live-verify; close issue #79. Owner: TBD  Est: 30m  verifies: [infrastructure]  deps: [T5.4]

### E6: Pod state divergence under crash-restart pressure (issue #80, root cause)

fidelity: outline

Intent: a long-lived pod that crashes and is blocked from restarting by
resource contention can leave its actual container alive and serving
while Spark reports it `pending` -- the restart-teardown-then-wait
ordering in the reconciler needs deeper investigation than this plan
can respsonsibly script upfront (the issue's own author was not able to
pin the exact divergence point). Exit criteria for the planning task:
a concrete failing test that reproduces the divergence, and a named
fix location in `internal/reconciler/reconciler.go`.

- [ ] T6.0 PLAN: expand E6 to executable fidelity (informed by E4's reconciler-area fixes landing first)  Owner: pool  Est: 1h  kind: plan  delivers: [docs/plan.md E6 at fidelity: executable]  deps: [T4.11]  acc: [parse_plan.py sees E6 with >= 3 tasks, every task carries acceptance criteria, deps resolve, fidelity flipped to executable]

### E7: Unified-memory host OOM protection (issue #47)

fidelity: outline

Intent: an admission headroom reserve (configurable fraction of
allocatable memory, on top of `--system-reserve-memory`) plus
usage-based alerting via the resource-reconciliation loop, so a driver
OOM (`NV_ERR_NO_MEMORY`) that hard-freezes the host is caught before it
happens rather than after. Conceptually adjacent to E1's utilization-
aware admission (both consult live usage, not just requests) and E5's
preemption fairness (both are admission-policy changes) -- sequencing
the planning pass after both land lets it reuse their patterns instead
of inventing a third. Exit criteria for the planning task: a specific
headroom-percentage default, a specific alert threshold and NATS
subject/payload shape, and a decision on whether "refuse new admissions
above usage threshold" (the issue's optional item 3) is in scope for
the first cut.

- [ ] T7.0 PLAN: expand E7 to executable fidelity (informed by E1 and E5 landing)  Owner: pool  Est: 1h  kind: plan  delivers: [docs/plan.md E7 at fidelity: executable]  deps: [T1.4, T5.5]  acc: [parse_plan.py sees E7 with >= 3 tasks, every task carries acceptance criteria, deps resolve, fidelity flipped to executable]

## Parallel Work (optimize for up to 10 concurrent agents)

Two agents are already running outside this plan's own dispatch:
`spark-fix-76` (T1.3, the rebase) and `spark-fix-85` (T2.1). Do not
double-dispatch against those task IDs.

| Track | Epics | Depends on |
|-------|-------|------------|
| Track A: admission/scheduler | E1 (T1.3-T1.7) | none (in progress) |
| Track B: executor fragility | E2 (T2.1a onward) | T2.1 (in progress) |
| Track C: manifest ingestion | E3 | none -- fully independent, can start immediately |
| Track D: pod lifecycle/reconciler | E4 | T1.4 (E1 merged) |
| Track E: preemption fairness | E5 | T1.4 (E1 merged) |
| Track F: outline planning passes | E6, E7 | T4.11 (E6); T1.4 + T5.5 (E7) |

### Waves

```
Wave 1: Independent frontier work (3 agents)
- [ ] T3.1 Investigate JSON ingestion gap          verifies: [UC-102]
- [ ] T3.2-T3.5 (sequential within E3, 1 agent carries the epic)
- [ ] T2.2 Investigate #73 mangled-arg root cause   verifies: [UC-103]  blocked-by: [T2.1b]
- [ ] T2.4 Investigate #77 silent-drop root cause    verifies: [UC-103]  blocked-by: [T2.1b]
(T2.2 and T2.4 start once T2.1b -- the #85 PR merge -- completes; track separately from Wave 1's clock if #85 merges later)

Wave 2: E1 and E2 close-out (agent count depends on what remains open)
- [ ] T1.3-T1.7 (E1 rebase through close, spark-fix-76 continues)
- [ ] T2.1a-c, T2.3, T2.5-T2.8 (E2 close-out, up to 3 agents: verify/merge #85, fix #73, fix/scope #77)

Wave 3: E4 and E5, once E1 (T1.4) merges (up to 8 agents)
- [ ] T4.1-T4.2 issue #71 (1 agent)
- [ ] T4.3-T4.4 issue #75 (1 agent)
- [ ] T4.5-T4.6 issue #78 (1 agent)
- [ ] T4.7-T4.8 issue #80 manifest endpoint (1 agent)
- [ ] T4.9 issue #80 signedness bug (1 agent)
- [ ] T4.10-T4.11 E4 close-out (1 agent, after the above 5 converge)
- [ ] T5.1-T5.4 issue #79 (1 agent, lane: agent for T5.2's design call)
- [ ] T5.5 E5 close-out (1 agent)

Wave 4: Outline planning passes, once their triggers complete (2 agents)
- [ ] T6.0 PLAN: expand E6                          deps: [T4.11]
- [ ] T7.0 PLAN: expand E7                          deps: [T1.4, T5.5]
```

Dependency-minimization notes: T3.x (E3) has zero dependency on
anything else in this plan and should saturate a slot immediately.
Within E4, T4.1/T4.3/T4.5/T4.7/T4.9 are mutually independent (different
functions, same file in most cases -- file overlap is not a dependency
per the isolated-worktree model) and can run as 5 parallel agents in
Wave 3 rather than sequentially.

## Timeline and Milestones

| ID | Milestone | Depends on | Exit criteria |
|----|-----------|------------|----------------|
| M1 | Issue #76 shipped | T1.3-T1.7 | PR #83 merged, released, DGX confirms overcommit admission live |
| M2 | Issue #85 (+ #73, #77) shipped | T2.1-T2.8 | All three executor-fragility issues closed with live-verified fixes, or #77 handed off as a scoped follow-up |
| M3 | Issue #74 shipped | T3.1-T3.5 | JSON POST creates pods identically to YAML, live-verified |
| M4 | Issues #71, #75, #78, #79, #80-quick-wins shipped | T4.1-T4.11, T5.1-T5.5 | All five live-verified and closed |
| M5 | E6 and E7 promoted to executable | T6.0, T7.0 | Both epics carry >= 3 fully-decomposed tasks each, ready for a follow-on `/apply` wave |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | A new epic branches before E1 (#76/PR #83) merges and re-hits the #82/#83-style conflict in reconciler.go/scheduler.go | M | M | E4 and E5 are explicitly `blocked-by: [T1.4]`; do not dispatch them early even if agent capacity is free |
| R2 | #73/#77's fix duplicates or conflicts with #85's in-flight rewrite of the same function | M | M | E2's T2.2/T2.4 are `blocked-by: [T2.1b]` (the #85 PR merge), not just noted as related |
| R3 | #79's anti-thrash-cap fix (T5.2) is a genuine design trade-off, not a mechanical patch; guessing wrong reintroduces preemption thrashing this cap was built to prevent | M | M | T5.2 is `lane: agent` with explicit instruction to stop and report rather than pick a resolution unilaterally |
| R4 | #77's root cause may not be findable within the estimated 60m (issue author already tried and called it "not yet root-caused") | M | M | T2.4/T2.5 accept a written root-cause-so-far handoff as a valid outcome, not only a fix |
| R5 | #47's OOM protection is safety-critical (unified-memory driver freeze has already happened twice per the issue) but is deliberately deferred to an outline epic | H | L (deferred, not ignored) | E7's planning trigger (T1.4, T5.5) is set so it is re-evaluated promptly once its prerequisites land, not indefinitely parked |
| R6 | Running many parallel agents against a shared production DGX risks the same capacity pressure this whole triage thread started from | M | L | Operating Procedure below caps concurrent live-verify steps; prefer sequential live-verification on the DGX even when the code-writing waves are parallel |

## Operating Procedure

Definition of done for every task in this plan (all must hold):

1. A red-then-green test exists for the fix (a test that fails against
   the pre-fix code and passes after) -- not just a green test written
   after the fact.
2. `go vet ./...`, `staticcheck ./...`, and `go test ./... -race -timeout 120s -count=1` (the full suite, not just the touched packages) are clean.
3. PR opened, reviewed (independently re-verify build/vet/test/diff --
   do not record a teammate's self-report as fact), merged via rebase
   (not squash, not merge commit).
4. If the repo's release-please PR fires, merge it and watch the
   tag-triggered Release workflow (goreleaser) to completion -- the
   merge-commit-triggered run completes earlier and does not indicate
   asset readiness.
5. DGX auto-upgrade timer triggered (`ssh ndungu@192.168.86.250 'sudo systemctl start spark-auto-upgrade.service'`), `/healthz` confirms the new version.
6. Live-verified on the DGX with the exact repro from the issue where
   practical; if a live repro is not safely practical on a shared
   production host, say so explicitly and rely on the automated test
   plus an API-level check instead of skipping verification silently.
7. Clean up any throwaway verification pod completely; confirm
   `/api/v1/resources` and `/api/v1/node`'s `gpu_allocations` are back
   to baseline.
8. Issue closed with links to the PR, release, and live-verification
   evidence.
9. `docs/roadmap.md` updated (Shipped line: item, owner, date, PR#).

Never commit files from separate directories in the same commit. Small,
logical, Conventional Commits, one package per commit where practical.
No Claude/Anthropic attribution anywhere.

## Progress Log

- 2026 08 28: Change summary -- new plan created. Trimmed the prior
  plan (issue #37, shipped v1.13.1, already fully captured in
  docs/devlog.md and docs/design.md's reconciler invariants) and
  replaced it with this one. Cataloged all 9 untriaged open issues
  (#47, #71, #73, #74, #75, #77, #78, #79, #80) plus the 2 already
  in-flight (#76, #85) into 7 epics (E1-E7). Use case manifest written
  to `.claude/scratch/usecases-manifest.json` (UC-101 through UC-112).
  No new ADRs from this planning pass (no new architectural decisions
  were made; #79's and #47's design questions are deliberately deferred
  to execution time). `docs/roadmap.md`'s Planned section updated to
  reference this plan.
- 2026 08 28 (later same day): E1 (#76) fully shipped except T1.6's live
  overcommit trigger, which remains outstanding -- PR #83 merged, v1.18.0
  released and confirmed live (`/healthz` -> 1.18.0), issue #76 closed.
  E2's #85 slice fully shipped -- PR #86 merged (rebased once more, onto
  post-#83 main, to resolve a docs/roadmap.md-only conflict), same
  v1.18.0 release, live-verified with the exact repro manifest from the
  issue. While live-verifying #85, discovered and filed issue #88 (a
  ~7-minute DELETE hang plus a host-wide `podman pod ps` stall, self
  resolved, root cause not yet found) -- added as T2.9, blocking nothing
  else in E2 since #73/#77 do not touch the stop/delete path. Both merged
  worktrees (`spark-wt-issue76`, `spark-wt-issue85`) and their branches
  removed.

## Hand-off Notes

- Repo: `feza-ai/spark`. DGX endpoint: `http://192.168.86.250:8080`
  (`ssh ndungu@192.168.86.250`).
- Two agents already running as of this plan (not part of `/apply`'s
  own dispatch): `spark-fix-76` (rebasing PR #83), `spark-fix-85`
  (building the fix for #85, PR not yet opened as of this plan).
- kazi is on PATH; every engineering task above carries an `acc:` line
  for `/apply`'s kazi lane to derive a predicate from at dispatch time
  (ADR 015 in the kazi plugin, not this repo).
- Existing ADRs relevant to this batch: docs/adr/005 (priority
  preemption, relevant to E5), docs/adr/006 (resource-aware scheduling,
  relevant to E1/E5/E7), docs/adr/013 (utilization-aware admission,
  E1), docs/adr/014 (GPU slot reconciliation, relevant precedent for
  E2's executor fixes).
- The devlog's 2026-03-20 "standing lesson" (a parser/state-mapper
  that silently defaults instead of erroring) is directly relevant to
  E2 and E3 -- read it before touching either.

## Appendix

- `.claude/scratch/usecases-manifest.json` -- UC-101 through UC-112.
- Issues: https://github.com/feza-ai/spark/issues/47, /71, /73, /74,
  /75, /76, /77, /78, /79, /80, /85.
- In-flight PRs as of this plan: #83 (issue #76).
- Related code: `internal/scheduler/` (E1, E5, E7), `internal/executor/podman.go` (E2),
  `internal/manifest/` (E3), `internal/reconciler/reconciler.go` and
  `internal/api/pods_mutate.go` (E4), `internal/housekeeper/` (E6/E7
  candidates for the alerting/reconciliation loop).
