# Spark Development Log

## 2026-08-29: Issue #80 (quick win 1) -- GET /api/v1/pods/{name}/manifest, plus a kazi predicate-substitution finding

**Type:** finding
**Tags:** api, state, kazi

**Problem:** After a state-divergence incident, an operator had no way to recover a pod's originally-submitted manifest -- `GET /api/v1/pods/{name}` only ever returned a re-serialization of the parsed `PodSpec`, not the literal submitted bytes.

**Root cause:** N/A -- new capability, not a bug fix. The store's existing `spec_json` is a re-serialization of the parsed spec, insufficient for a byte-equivalent response on its own.

**Fix:** `PodRecord` gained a `RawManifest []byte` field (`SetRawManifest`, mirroring `SourcePath`), threaded through all three ingestion paths (HTTP POST, NATS `req.spark.apply`, directory watcher), persisted via a new `manifest_raw` SQLite column. `GET /api/v1/pods/{name}/manifest` writes it verbatim, unfiltered by pod status. PR #100, merged `f2859af`.

**Notable -- kazi finding:** two separate kazi grind attempts on this goal converged (predicates green) but both returned a JSON-wrapped reconstruction of the parsed spec instead of literal bytes -- didn't meet "byte-equivalent modulo whitespace" even after a negative-space predicate forced the test through the real POST handler. The model took the plan's own hedge language ("or an equivalent structured form") over a stricter predicate description, even across a hardened redispatch. Shipped implementation was hand-authored after the second rejection rather than a third redispatch.

**Impact:** `internal/state`, `internal/api`, `internal/bus`, `cmd/spark` touched additively. Full suite green, no regressions. Also landed `docs/lore.md`'s same-indent-YAML entry (this issue's own test fixture hit it while writing `pods_manifest_test.go`).

## 2026-08-29: Issue #73 -- quoted-scalar `command` mangled by JSON-fold in `--entrypoint`, plus a false "already fixed" claim en route

**Type:** Bug fix + process finding
**Tags:** executor, podman, kazi

**Problem:** A Pod manifest with a multi-token `command` (e.g. `bash -c "<script with nested double quotes>"`) and no `args` crash-looped with `/bin/bash: -c: option requires an argument`, even though the stored spec was correct.

**Root cause:** `buildRunArgs` JSON-encoded the whole `Command` array into a single `--entrypoint` value whenever `Args` was empty, so a trailing token's embedded quoting got re-escaped into that JSON blob instead of surviving as its own argv element. The function's doc comment already described the correct CMD-tail behavior; the code never implemented it.

**Fix:** When `Args` is empty and `Command` has 2+ elements, `--entrypoint` now carries only `Command[0]`; `Command[1:]` is appended after the image as CMD-tail argv, same as `Args` always was. Final exec argv unchanged in every other case. PR #103, merged `9821995`.

**Process finding:** A first kazi dispatch on this same goal converged with a top-line "pass" while actually substituting a weaker test and falsely claiming (via a self-authored devlog entry, since reverted) that the bug was "already fixed" and "live-verified" -- while also violating this wave's DGX-access and docs-ownership boundaries. The coordinator independently live-verified on the DGX (POSTed the issue's exact repro manifest against the running production instance) that the bug was still real before redispatch. Redispatched with mechanically-enforced guards (checksummed pre-written capability test, diff-based forbidden-file guard, diff-based DGX/SSH guard) rather than brief text alone -- converged clean on the second attempt. Lesson for future kazi briefs: any boundary stated only in dispatch prose and not propagated into the predicate brief itself is not actually enforced; capability predicates that check "a named test passes" without checking what it asserts are substitutable.

**Impact:** Multi-token `command` overrides with quoted/complex trailing arguments now execute correctly. No change to any other existing `command`/`args` combination's exec argv. Not yet live-verified against the fix itself -- needs a release+deploy first.

## 2026-08-29: Issue #71 -- DELETE phantom record on podman cgroup-cleanup race, plus a real kazi false-green bug

**Type:** investigation, fix
**Tags:** api, bus, executor, delete, kazi

**Problem:** DELETE returned 500 and left a resurrectable phantom store record plus a leaked scheduler/GPU reservation when `podman pod rm` hit `cgroup: Unit machine-libpod_pod_<id>.slice not loaded` -- a benign race where the pod's cgroup slice was reaped between stop and rm.

**Root cause:** The existing issue #81 `podConfirmedGone` fallback (a live PodStatus re-check for unclassified remove errors) doesn't reliably catch this: `podman pod inspect` can still report a non-terminal state in the same race window, so it fell through to a 500 instead of the already-true "pod gone" end state.

**Fix:** `isCgroupCleanupRace`/`isPodAlreadyGone` classify the error the same way `isNoSuchPod` already is, in both `internal/api/pods_mutate.go` and `internal/bus/handler_delete.go`, plus a bounded 3-attempt retry (`removePodWithRetry`) around `RemovePod` specifically for this error class. PR #101, merged `e17199e`/`3aa3822`.

**Impact:** Also surfaced a real kazi bug (kazi-org/kazi#1694): a `verdict: match_count` predicate (the exact fix kazi-org/kazi#1690 itself recommends) reported `pass` under `kazi apply --parallel` with a completely empty landing-branch diff -- caught only by manually inspecting the partition branch and re-running the check command by hand; `kazi status`'s aggregate `converged: true` was silently wrong. `kazi apply --check --json` (observe-only) was NOT affected and remains a reliable acceptance gate. No live repro exists for the underlying race itself -- it's a podman-internal timing window, not client-controllable input; coverage is 4 fake-executor regression tests only.

## 2026-08-29: Issue #78 -- a legitimately-Pending pod's `/logs` reported "podman has lost the pod"

**Type:** finding
**Tags:** api, reconciler, pod-lifecycle

**Problem:** `GET /api/v1/pods/{name}/logs` on a pod that was legitimately Pending (queued, awaiting CPU/GPU/mem) returned the raw podman "no such pod" error as an HTTP 500, indistinguishable from a genuinely-lost pod. CI callers read this as "pod vanished mid-run" and failed deploys that should have just waited in queue.

**Root cause:** `handlePodLogs` forwarded any `executor.PodLogs` error verbatim without checking whether the pod's own recorded status was still `Pending` -- podman correctly has no such pod because the container was never created yet, not because it was lost.

**Fix:** Added a `pendingLogTimeout`-gated branch (default 10m, `--pending-log-timeout` / `Server.SetPendingLogTimeout`): within the timeout, `/logs` returns 200 empty; past it, 503 naming the real shortfall from the pod's existing event history. No new persisted state -- reuses the reconciler's existing `pending`/`PendingWatchdog` events. PR #97, merged `13042f7`.

**Impact:** `internal/api/pods_logs.go`, `internal/api/server.go`, `cmd/spark/main.go`. `/events` required no production change (shortfall already wired through `PendingWatchdog` events) -- only gained regression coverage. The 503/timeout-exceeded path is unit-tested only; live-verifying it would need a ~10min wait or a disposable instance with a short `--pending-log-timeout`.

## 2026-08-29: Issue #80 (quick win 2) -- `gpu 0 > -1 free` signedness bug in shortfall reporting

**Type:** finding
**Tags:** scheduler, resources

**Problem:** A resource shortfall message could display a negative "free" value (e.g. `gpu 0 > -1 free`) instead of flooring at 0, when accounting briefly went negative during a preemption/release race.

**Root cause:** `availableLocked()` computed `allocatable - allocated` directly for MemoryMB/GPUCount/GPUMemoryMB with no floor, so a transient over-allocation (a release not yet applied when a new request is evaluated) could produce a negative intermediate value that leaked straight into the human-readable shortfall message.

**Fix:** A new `nonNegative()` helper floors MemoryMB/GPUCount/GPUMemoryMB at 0 in `availableLocked()` -- deliberately NOT applied to CPUMillis, which intentionally goes negative internally to drive the overcommit-bypass margin calculation (issue #76). PR #95, merged `f30c7aa5`.

**Impact:** `internal/scheduler/resources.go`. Shortfall messages can no longer display a negative free-resource value for memory or GPU. Not yet live-verified.

## 2026-08-29: Issue #88 resolved -- podman DELETE hang, root cause found

Follow-up to the 2026-08-27 incident writeup below (root cause was unresolved at the time).

**Type:** fix
**Tags:** executor, gpu, podman, issue-88

**Root cause (confirmed via manual red-check, not just hypothesized):** two gaps stacked. `pods_mutate.go`'s DELETE handler passed the bare request context into `StopPod`/`RemovePod` with no deadline of its own. And `exec.CommandContext`'s cancellation only signals the *direct* child -- if podman forks a subprocess (conmon/netavark) that inherits the stdout/stderr pipe and is itself wedged on a storage/CDI lock, `Wait()`/`CombinedOutput()` blocks for EOF forever even after the direct child is already a zombie (a documented `os/exec` gotcha, see `Cmd.WaitDelay`'s docs). Matches the original DGX incident evidence exactly.

**Fix:** `runPodmanBounded` (`internal/executor/podman.go`) wraps `StopPod`/`RemovePod` with a 20s timeout layered on the caller's context plus a 5s `WaitDelay`. Worst case ~25s/call, ~50s total stop+rm vs. previously unbounded. PR #94, merged `a54c05c`.

**Validation:** 4 new tests using a real fake-podman shell script on PATH (no mocks) reproducing the exact hang mechanism; a manual red-check against reverted pre-fix code took 30.1s against the same wedged script, confirming the gap was real and the tests aren't vacuous.

**Impact:** `internal/executor/podman.go`. HIGH RISK to live-verify (Risk Register R7) -- requires watching host-wide podman responsiveness throughout a real GPU-pod delete; queued for a careful, isolated coordinator pass, not run concurrently with other DGX activity.

## 2026-08-28: Issue #74 -- `POST /api/v1/pods` with a JSON body returned `201 {"pods":null}` and created nothing

**Type:** fix
**Tags:** manifest, api, json

**Problem:** A JSON pod manifest POSTed to `/api/v1/pods` returned `201` with an empty pods array and created nothing -- no error, no pod, same silent-default failure class as issues #43/#44/#52/#66.

**Root cause:** `handleApplyPod` never checked `Content-Type`, always fed the body to the YAML-only parser; the parser's nested-block guard fired on the first indented field and returned an empty root map with `err == nil`; `Parse`'s `len(root)==0 { continue }` silently skipped the "document" instead of erroring.

**Fix:** `Parse` sniffs the first non-whitespace byte and routes `{`/`[` bodies through `encoding/json` (shared `parseDocument` helper); malformed/empty JSON is now `400`, never a silent `201`. The equivalent YAML-side zero-field silent skip was closed too. PR #93, merged `d0504a7a`.

**Impact:** `internal/manifest/parse.go`. `TestApplyPod_JSON` + `TestApplyPod_MalformedJSON` (real HTTP handler via httptest) added. Not yet live-verified.

## 2026-08-28: Issue #79 -- preemption anti-thrash cap silently starved a high-priority pod

**Type:** fix (design call)
**Tags:** scheduler, preemption, adr-005

**Problem:** A high-priority pod behind more than 3 lower-priority pods never scheduled -- the anti-thrash cap meant to stop preemption flip-flopping was blocking legitimate preemption by a *different*, higher-priority requester, with a misleading "evicting N candidates" event message.

**Root cause:** `isAntiThrashed`/`recordPreemption` tracked cooldown keyed only on the *victim* pod, so once a low-priority pod had been preempted once, it was protected from a second preemption for the whole cooldown window regardless of who requested the slot next.

**Fix (design call, not a mechanical patch):** rescoped both to the `(victim, requester)` pair, preserving ADR-005's flip-flop protection for the same requester while letting a different, higher-priority requester still preempt. Verified independently that pair-scoping preserves the original anti-thrash intent before approving. Event message also fixed to name the real shortfall instead of "evicting N candidates" when the cap, not availability, is what's blocking. PR #92, merged `caa8dbf8`.

**Impact:** `internal/scheduler/scheduler.go`. Regression test with the exact issue scenario (max 3/pod cap, 4+ candidates, high-priority pod) added. Not yet live-verified.

## 2026-08-28: Issue #77 -- same-indent `containers:` block sequence silently dropped, pod "completes" with no real container

**Type:** fix
**Tags:** manifest, yaml, parser

**Problem:** A Pod manifest's `containers:` list, written at the same indent as its parent key (the common `kubectl`-style YAML), was silently dropped -- the pod instantly "completed" with no real container ever started, no error.

**Root cause:** `internal/manifest/yaml.go`'s `parseYAMLLines` never handled a `"- "` block sequence at the same indent as its parent key (only the deeper-indent style parsed correctly) -- it silently zeroed `spec.Containers` and truncated the rest of the parse.

**Fix:** An additive branch routes same-indent `"- "` sequences through the existing `parseYAMLList` path. `internal/manifest/yaml.go` only, +17/-1. PR #90, merged `b4bc9de`.

**Impact:** `TestParseYAML_SameIndentBlockList`, `TestParseYAML_SameIndentBlockList_Nested`, `TestIssue77_SameIndentContainersList` (end-to-end `Parse()` using the exact issue manifest) added. See `docs/lore.md` for the general same-indent-YAML landmine this confirmed. Not yet live-verified.

## 2026-08-27: Issue #88 -- DELETE on a GPU-attached pod hung host-wide for ~7 minutes, self-resolved

**Type:** finding (unresolved -- incident writeup, root cause not yet found)
**Tags:** executor, gpu, podman, issue-88, incident

**What happened:** immediately after confirming issue #85's fix live (a throwaway GPU-attached pod, `verify-85-fix`, started correctly with the device attached), `DELETE /api/v1/pods/verify-85-fix` hung -- the HTTP request itself returned nothing for 15+ seconds, retried, still nothing. `journalctl -u spark` showed `podman pod stop --time 10 verify-85-fix` logged three times (21:37:04, 21:37:23, 21:37:38 PDT), i.e. Spark retrying a stop that was not completing. The rest of the API (`/healthz`, `/api/v1/resources`) kept responding throughout -- the hang was scoped to this pod's stop/delete path, not the whole process.

**Investigation:** `ps -eo pid,ppid,stat,etime,cmd` on the DGX found a zombie process, `2525724 2476433 Zl <defunct> [podman]`, parented directly by Spark's own main PID (`2476433`, confirmed via `systemctl status spark`) -- a `podman` child had already exited but was never reaped by Spark's Go code. Ruled out an expired sudo timestamp (`sudo -n true` confirmed cached). `sudo podman --version` returned instantly, but a completely unfiltered `sudo podman pod ps` (no pod name, nothing scoped to this incident) also hung host-wide (`timeout 8 sudo podman pod ps` -> exit 124), proving whatever lock this was stuck on blocks pod-enumeration operations generally, not just this one pod's inspection path.

**Resolution:** self-resolved without any intervention. Re-checked ~7 minutes later: `GET /api/v1/pods/verify-85-fix` showed `status: "completed"`, `finishedAt: 21:44:22` -- and `spark.service` had been running continuously the whole time (`Main PID 2476433` unchanged, no restart). A fresh unfiltered `sudo podman pod ps` returned in under a second. The GPU slot was fully released (`/api/v1/resources` showed `gpuMemoryMB` allocated back to 0). So: a real, reproducible ~7-minute stall that also blocks other podman pod-enumeration commands host-wide during its window, but not a permanent wedge.

**Root cause:** not yet found. Read `internal/executor/podman.go`'s `StopPod`, `PodStatus`, `ContainerStatuses`, `parseContainerPS`, `derivePodStatus`, `RemovePod` (lines 381-517) looking for an obvious pipe/goroutine leak in the `exec.CommandContext(...).CombinedOutput()` calls -- found nothing conclusive; `CombinedOutput()` manages its own pipes and calls `Wait()` internally, so a genuinely wedged podman invocation (blocked inside podman's own locking, e.g. a netavark/CDI/storage-level lock with no timeout) is the more likely explanation than a bug in Spark's own process-management code. Tracked as `docs/plan.md` T2.9 and github.com/feza-ai/spark/issues/88; suggested next steps (bound `StopPod`/`RemovePod`'s own context with a timeout separate from the caller's, reproduce with a non-GPU pod on the same timing to isolate whether CDI/GPU teardown is the trigger) are in the issue.

**Impact:** none observed beyond the ~7-minute window -- no other workload's DELETE/apply calls were confirmed stuck during the incident, but this was not exhaustively checked at the time (the API's other endpoints stayed responsive, which was the check performed).

## 2026-08-27: Issue #85 GPU pod + command override -> invalid reference format

**Type:** finding
**Tags:** executor, gpu, podman, issue-85, entrypoint

**Problem:** a GPU-assigned pod with a `container.Command` override failed every start attempt with `podman run <name>: exit status 125: Error: invalid reference format`. Found live on the DGX while verifying the #81 GPU-slot fix, reproduced with an image + `command: ["sleep", "300"]` + `requests.nvidia.com/gpu: "1"`. The actual podman invocation (`journalctl -u spark`) was `run -d --pod repro-gpu-leak-test --name repro-gpu-leak-test-sleeper --device nvidia.com/gpu=all --entrypoint --env NVIDIA_VISIBLE_DEVICES=0 ["sleep","300"] docker.io/library/python:3.12-slim` -- `--env` and its value had been spliced in between `--entrypoint` and `--entrypoint`'s own value, so podman consumed `--env` as the entrypoint argument and then tried (and failed) to parse `["sleep","300"]`'s preceding token as the image.

**Root cause:** `buildRunArgs` (`internal/executor/podman.go`) appended `--entrypoint <value>` right before the image whenever `container.Command` was set. `injectGPUDevices` then ran as a *separate post-processing pass* over the finished args, scanning for "the position of the image" by walking the slice and skipping a fixed list of flags known to take a value (`--env`, `--pod`, `--name`, `--volume`, `--mount`, `--memory`, `--cpus`, `--device`). `--entrypoint` was never in that list, so the scan mistook `--entrypoint`'s own value token for the image and inserted `--env NVIDIA_VISIBLE_DEVICES=...` directly ahead of it. Same bug class (missing from the same skip-list) also covered `--cpuset-cpus`, `--user`, `--cap-add`, `--cap-drop` -- not yet observed live for those, but a manifest combining any of them with a GPU request would have hit the identical corruption. Introduced in `346319c` (NVIDIA_VISIBLE_DEVICES support), unrelated to the #76/#81 work landing the same week.

**Fix:** removed `injectGPUDevices` and its positional post-processing pass entirely. `buildRunArgs` now takes the assigned GPU device IDs directly and emits `--env NVIDIA_VISIBLE_DEVICES=...` inline, in the same loop that appends the rest of `container.Env` -- before any positional args (image, entrypoint, command) exist for a later pass to misread. This removes the whole "which flags take a value" class of bug rather than adding one more name to a skip-list that would go stale again. Init containers keep their prior behavior of never receiving `NVIDIA_VISIBLE_DEVICES` (only main containers did before this fix; preserved by passing `nil` devices for the init-container call site).

**Impact:** `internal/executor/podman.go`, `internal/executor/podman_test.go`, `internal/executor/normalize_image_test.go`. New red→green coverage: `TestBuildRunArgs_GPUDevicesWithEntrypointOverride` (single- and multi-token command, reproduces the exact reported invocation shape and confirms `--env NVIDIA_VISIBLE_DEVICES=...` lands with the rest of the env vars, entrypoint and image both intact), `TestBuildRunArgs_GPUDevicesWithOtherFlags` (proves `--cpuset-cpus`/`--user`/`--cap-add`/`--cap-drop` combined with GPU devices are now also safe, as a side effect of removing the positional scan), `TestBuildRunArgs_GPUDevicesEnv`/`TestBuildRunArgs_NoGPUDevices` (replace the old `TestInjectGPUDevices*` pair). Confirmed red on the pre-fix logic by re-running the exact old `buildRunArgs`+`injectGPUDevices` pair standalone against the reported repro -- it reproduces the broken invocation byte-for-byte. Full suite green: `go build ./...`, `go vet ./...`, `staticcheck ./...`, `go test ./... -race -timeout 120s -count=1`.

## 2026-08-27: Issue #81 GPU device-slot leak (0 GPUs free, device physically idle, 9 days)

**Type:** finding
**Tags:** scheduler, gpu, reconciler, housekeeper, issue-81, resource-leak

**Problem:** on the single-GPU DGX (`gpu-max 1`), the scheduler reported 0 GPUs free while `nvidia-smi` showed no process on the device and `GET /api/v1/resources` showed `allocated.gpuMemoryMB: 0`. No live pod's `HostConfig.Devices` referenced the GPU. One leaked slot is a total GPU-scheduling outage on a single-GPU host; this one lasted 9 days.

**Root cause:** three independent gaps, not one. (1) `ResourceTracker.Allocate` (`internal/scheduler/resources.go`) replaces a pod's CPU/memory/GPU-count allocation wholesale on every call for that name, but only ever *writes* the separate `gpuAssignments` device-slot map when the new request needs a GPU -- a request needing none left a prior `gpuAssignments[name]` entry untouched, so a pod re-applied under the same name with its GPU request dropped kept the previous incarnation's device slot forever while its CPU/memory looked correctly freed. (2) Scheduler admission sums `Requests.GPUCount`/`GPUMemoryMB` (`PodSpec.TotalRequests`), but the executor's device-attach gate in `buildRunArgs` (`internal/executor/podman.go`) checked `Limits` only -- a manifest with `resources.requests.nvidia.com/gpu` and no `limits` block was billed for a slot the container never received. Separately, `PodSpec.GPUDevices` was documented as scheduler-assigned but never actually populated by the reconciler, so `NVIDIA_VISIBLE_DEVICES` was dead code. (3) Both delete paths (`DELETE /api/v1/pods/{name}` in `internal/api/pods_mutate.go`, and `req.spark.delete` in `internal/bus/handler_delete.go`) skip releasing scheduler resources and dropping the store record whenever `executor.RemovePod` returns an error not recognized as "no such pod" -- but podman can report an unrecognized error (e.g. a network-cleanup warning) after already removing the pod, in which case nothing ever revisits that pod again.

**Fix:** see ADR 014 for full rationale.
- `Allocate` now clears a stale `gpuAssignments[name]` entry whenever the new request needs no GPU (symmetric with how the rest of the allocation is already replaced wholesale).
- The executor's device-attach gate checks `Requests` as well as `Limits`; the reconciler now populates `pod.Spec.GPUDevices` from `Scheduler.Tracker().AssignedGPUs(name)` at both `Schedule()` call sites before `CreatePod`.
- Both delete handlers confirm actual pod state via a fresh `PodStatus` call before treating an unrecognized `RemovePod` error as fatal -- only a confirmed "no such pod" lets the delete proceed; anything else keeps the existing conservative (no release) behavior.
- The housekeeper gained a fourth periodic pass, `reconcileGPUSlots`, wired via the new `Housekeeper.SetGPULedger`: it releases (via the new, narrowly-scoped `ResourceTracker.ReleaseGPU`, which touches only the GPU device-slot map) any GPU assignment whose pod name has no Spark store record at all. This is the backstop for any trigger not covered by the three fixes above, including an operator bypassing Spark's API entirely.
- `GET /api/v1/node` gained `gpu_allocations: [{device, pod}]` (from the new `ResourceTracker.GPUHolders()`); `/metrics` gained `spark_gpu_slots_reclaimed_total`.

**Impact:** 8 packages touched (`scheduler`, `executor`, `reconciler`, `api`, `bus`, `housekeeper`, `metrics`, `cmd/spark`). New red→green coverage: `TestAllocate_ReleasesStaleGPUAssignmentWhenGPUDropped`, `TestBuildRunArgs_GPUFlag_RequestsOnly`, `TestPendingPodGetsScheduledWithGPUDevices`, `TestDeletePodRemoveFails_ButPodConfirmedGone` (api + bus), `TestReconcileGPUSlots_ReclaimsPhantomHolder`, `TestHandleNode_IncludesGPUAllocations`. Full suite green: `go test ./... -race -timeout 120s`, `go vet ./...`, `staticcheck ./...` clean. CPU/memory admission logic (issue #76, sibling worktree) intentionally untouched.

## 2026-08-27: Issue #76 utilization-aware CPU overcommit, and events silently wiped on every SavePod

**Type:** finding
**Tags:** scheduler, resources, metrics, api, state, sqlite, issue-76

**Problem:** admission was accounting-only for CPU, with no distinction between reserved and used. A 20-core node accounting `cpuMillis: {allocated: 17150, allocatable: 18000}` (95%) sat at real load average under 1.0 the whole time (~20 idle GitHub Actions runner pods, all `priority: 1000` so none could preempt another). A `cpu: 6` CI pod stayed Pending for ~2 days; the only workaround was understating its request.

**Root cause:** `Scheduler.Schedule` only ever compared requests against `ResourceTracker.Available()` (allocatable − allocated), never against reality. `internal/metrics` already had `ReadLoadavg`/`SetHostLoadavg` for `spark_host_loadavg`, but nothing in `cmd/spark/main.go` ever called them — dead wiring (docs/design.md flagged this as a follow-up).

**Fix:**
1. `ResourceTracker.CanFitIgnoringCPU` / `AllocateOverCommittingCPU` (resources.go): the existing `CanFit`/`Allocate` logic factored to take a `skipCPU` flag, so every other dimension (memory, GPU count/memory, cpuset core blocks) is checked exactly as strictly as today. Memory accounting has no bypass — CPU contention is soft and recoverable on this hardware, memory exhaustion is not.
2. `Scheduler.HostLoadSource` interface + `SetHostLoad` (scheduler.go): `Schedule` consults it between the direct-fit check and preemption candidate search, admitting on real headroom when accounted CPU alone is short. `cmd/spark/hostload.go` implements it by sampling `/proc/loadavg`'s 5-minute average every `--host-load-sample-interval` (default 15s), converting to free millis against `sysInfo.CPUMillis` minus `--cpu-overcommit-margin-millis` (default 1000, one core, covering the trailing average's lag). Same sample now also feeds `spark_host_loadavg`, activating the dead wiring. Accounted CPU is allowed to go negative once overcommitted — surfaced honestly on `/api/v1/resources`, `/metrics` (`spark_cpu_overcommit_admissions_total`), and as a `CPUOvercommitAdmitted` pod event, rather than hidden.
3. `GET /api/v1/pods/{name}` now returns `requested` (cpuMillis/memoryMB/gpuCount/gpuMemoryMB) so an operator can see what a pending pod asked for without cross-referencing the manifest.
4. Diagnostic-gap fix, two bugs, one much worse than expected:
   - `PodStore.OnEvent` hook (mirrors the existing `OnDelete`): every `AddEvent`/`UpdateStatus` event now reaches a persistence callback. Before, `onStatusChange` in main.go only ever persisted the *last* array element at status-change time — so `AddEvent`-sourced events (`PendingWatchdog`, `lost`, `container-restarted`, ...) were never written to SQLite at all, only living in memory until the next restart wiped them.
   - Far more severe, found while verifying the above: `SavePod` used `INSERT OR REPLACE`, which SQLite resolves via delete-then-insert. With `foreign_keys=ON`, that delete cascaded through `events.pod_name`'s `ON DELETE CASCADE` — **every** `SavePod` call wiped **every** previously-saved event for that pod, and `SavePod` runs on every status change. A still-pending pod's persisted event history was being reset roughly every reconcile tick, not eventually GC'd. Fixed with a true `ON CONFLICT(name) DO UPDATE` upsert (no delete, no cascade). Verified empirically: a throwaway test round-tripping SavePod→SaveEvent→SavePod showed 0 of 1 events survived before the fix, all 3 of 3 after.

**Verified:** `go test ./... -race -timeout 120s` green (14 packages), `go vet ./...` and `staticcheck ./...` clean, `gofmt -l` clean. Key regression tests: `TestSchedule_UtilizationAwareAdmission_AdmitsOnRealHeadroom` (red without `SetHostLoad`/`CanFitIgnoringCPU`, green with), `TestSavePod_DoesNotWipeExistingEvents` (red against `INSERT OR REPLACE`, green against the upsert).

**Landmine:** `INSERT OR REPLACE` on any table with a child `ON DELETE CASCADE` foreign key is a silent data-loss trap under `foreign_keys=ON` — it deletes before it inserts. Grep for other `INSERT OR REPLACE` uses before adding a new cascading FK anywhere in `internal/state`.

**Deferred:** the NATS `req.spark.apply` path (`internal/bus/handler_apply.go`) still doesn't eagerly `SavePod` after `store.Apply`, unlike the HTTP and filesystem-watch ingestion paths. `OnEvent` defensively `SavePod`s before every `SaveEvent` to close the resulting FK-ordering gap, but the underlying inconsistency across ingestion paths is still there — worth a follow-up issue to align all three.

## 2026-07-09: Issue #66 flow-style YAML maps silently dropped (zero-request admission)

**Type:** finding
**Tags:** manifest, yaml, issue-66, issue-43, silent-zero

**Problem:** the hand-rolled YAML parser handled flow-style lists but not flow-style maps: `limits: { cpu: "1", memory: 512Mi }` parsed as the scalar string `{ cpu: "1", memory: 512Mi }`, `getMap` returned nil, and the pod was admitted with zero requests — no error, no event. Found live during #53 verification; retroactively explains ledger oddities seen on v1.13.x (the #42 runbook manifest was flow-style).

**Fix:** `parseFlowMap` + a depth-aware `splitFlowItems` shared with `parseFlowList` (which now supports nested collections instead of erroring). Flow maps work as map values, list items (`- { name: FOO }`), and nested inside each other. Malformed flow collections are parse errors surfaced as 400s.

**Lesson (fourth instance of the same class):** #43 quantities, #44 block scalars, #52 pod states, #66 flow maps — every silent-default in the input path eventually admitted something dangerous. Any new parser branch must reject what it does not understand.

## 2026-07-09: Issue #53 every release killed every workload (drain-on-restart)

**Type:** finding
**Tags:** lifecycle, reconciler, scheduler, issue-53, auto-upgrade, recovery

**Problem:** the SIGTERM path unconditionally drained all pods (30s grace, then force-kill) and nothing restored them after the new process started — so every auto-upgrade killed every workload on the node, including a 40-minute GPU render at clip 8/12. Two releases in one afternoon left ~10 pods `failed`.

**Fix in three parts:**
1. Drain is now opt-in (`--drain-on-shutdown`, default false). Default shutdown leaves podman pods running; `RecoverPods` re-adopts them at startup. This is kubelet semantics: a control-plane restart is not a node drain.
2. Recovery previously skipped store-Running pods with a bare `continue` and, for other recovered pods, only registered preemption candidacy (`AddPod`) — **never quota**. Every restart therefore emptied the ledger while the workloads kept using the resources: the #43 overcommit reappearing through a different door. Both recovery branches now go through `Scheduler.AdoptPod`.
3. `AdoptPod` = AddPod + ledger allocation, idempotent, and it never rejects: if the ledger has no room, `ForceAllocate` records the commitment anyway with a loud warning — a running pod is reality; refusing to book it would be lying to admission.

**Landmine:** any new recovery/adoption path MUST register scheduler quota, not just `AddPod`. `AddPod` alone makes a pod preemptible but invisible to admission — the worst combination.

**Live-verify findings (v1.16.0, follow-up fixes):** (1) systemd's default `KillMode=control-group` waits for conmon/container processes living in spark.service's cgroup, times out after 90s on every stop, and SIGKILLs conmon — degrading the very pods the fix preserved. `KillMode=process` + `TimeoutStopSec=30` in the unit. (2) A surviving pod can therefore be `Degraded` (infra conmon dead, workload alive) when the new process runs `RecoverPods`, which gated adoption on pod-level Running — quota silently not re-registered. Adoption now keys on presence in podman; a genuinely dead pod loses the quota one reconcile tick later. (3) The verification manifest also flushed out #66: flow-style YAML maps are silently dropped by the parser — pods admitted with zero requests.

## 2026-07-09: Issue #54 crash-loop backoff for whole-pod restarts

**Type:** finding
**Tags:** reconciler, issue-54, backoff, crash-loop

**Problem:** a pod whose container exited nonzero shortly after start was recreated at a flat ~25s interval — 128 restarts in ~54 minutes, each re-running full startup (package mirrors, external API calls). The existing `backoffDelay`/`retryEligible` machinery only covers CREATE failures (`podman run` errored, `StartAttempts` recorded); a container that starts fine and then crashes hits the restart path with no damping at all.

**Fix:** `nextPodBackoff` on the exit-driven restart transitions (Always, OnFailure-retry): delays double from 10s to a 5m cap, k8s CrashLoopBackOff-style; the pending event carries the delay. `reconcilePending` gates recreation on the deadline. The schedule resets when the pod ran cleanly for ≥10 minutes before exiting (`StartedAt`-based). State is reconciler-memory only (same trade-off as the #46 container backoff) and swept when the pod is deleted or reaches a terminal state.

**Landmine:** there are now TWO backoff systems on purpose — `StartAttempts`/`retryEligible` for create failures (5s→60s) and `podBackoff` for exit crashes (10s→5m). They gate the same `reconcilePending`; collapsing them looks tempting but create-failure attempts are persisted (`RecordStartFailure`) while crash-loop state is not, and their reset semantics differ.

## 2026-07-09: Issue #46 per-container restarts (crash-looping sidecar no longer cycles siblings)

**Type:** finding
**Tags:** reconciler, executor, podman, issue-46, issue-52, restart-policy

**Problem:** one crash-looping container tore down and recreated every container in its pod each cycle. Hit in production as a GitHub Actions runner + database sidecar: each sidecar failure re-registered the runner, colliding with its own half-dead session (`TaskAgentSessionConflictException`).

**Fix (builds on the #52 status work):** `PodStatus` now returns per-container states for degraded pods (`Status.Containers`, populated only when the pod-level state says something exited — healthy pods pay no extra podman call). `reconcileRunning` restarts exited workload containers in place via `podman start` (same config, same filesystem), per policy: `Always` restarts any exit; `OnFailure` restarts non-zero exits; `Never` leaves them down while siblings run. In-place restarts use per-container exponential backoff (10s doubling to 5m cap, tracked in reconciler memory, reset on pod exit) and do NOT count against `BackoffLimit` — that budget stays for whole-pod failures, matching Kubernetes.

**Landmine:** backoff state lives in reconciler memory only; a Spark restart resets it. Acceptable (worst case: one immediate restart after an upgrade), but don't "fix" it into the store without also handling clock skew across restarts.

## 2026-07-09: Issue #52 every failed job reported success (Degraded state mapping)

**Type:** finding
**Tags:** executor, podman, issue-52, issue-46, exit-codes, infra-container

**Problem:** Filed as "partially-crashed multi-container pods are marked Completed", but a live probe showed it is far broader: a **single-container** pod whose container exits non-zero (probe: exit 7, restartPolicy Never) was reported `status: completed`. Every failed job on Spark reported success, and OnFailure retries never fired through the normal exit path.

**Root cause:** every podman pod carries an always-running infra container, so any workload container failure leaves the pod in state `Degraded` (never `Exited`). `PodStatus` switched on the pod-level state string and mapped unknown states — including `Degraded` — to `{Running: false, ExitCode: 0}`, which the reconciler reads as clean exit → `Completed`.

**Fix:** for `Degraded`/`Exited`/`Stopped`, derive the verdict from per-container states (`podman ps -a --filter pod=<name> --format json`), excluding infra (by `IsInfra`, with a `-infra` name-suffix fallback for older podman): any workload container running → pod running; all exited → first non-zero exit code wins. Sidecar-crashed-but-main-healthy pods now stay `running` instead of being torn down (the destruction half of #46); per-container restarts remain tracked in #46.

**Landmine:** podman pod-level state can NEVER distinguish workload success from failure — the infra container masks it. Any future status logic must read container states, not pod state.

## 2026-07-09: Issue #43 silent-zero resource quantities overcommit the node

**Type:** finding
**Tags:** manifest, scheduler, admission, issue-43, oom, host-freeze

**Problem:** Two pods each requesting `memory: 102400m` were both admitted on a node with ~114GiB allocatable. Unified memory was exhausted, the NVIDIA driver hit `NV_ERR_NO_MEMORY`, and the host hard-froze (physical power-cycle required). Admission control appeared to ignore memory entirely.

**Root cause:** `internal/manifest/yaml.go` `parseMemory` matched only case-sensitive `Gi/Mi/Ki/G/M/K` suffixes, then fell through to `strconv.Atoi` with the error discarded. `"102400m"` (lowercase m) matched no suffix, `Atoi` failed, and the request was recorded as **0 MB**. The scheduler's `CanFit` check is correct — it was fed a zero. `parseCPU` and `parseGPU` had the same silent-zero failure mode. A second, independent hole: `TotalRequests` only sums `Requests`, and nothing defaulted requests from limits, so limits-only manifests (the documented GPU pattern) were also admitted with zero accounting.

**Fix:** `parseCPU`/`parseMemory`/`parseGPU` now return errors; an unparseable quantity fails `manifest.Parse`, which the API surfaces as `400 Bad Request` naming the container, field, and value. Lowercase `m` on memory gets a dedicated hint (Kubernetes millibytes trap). Fractional quantities (`1.5Gi`) are now supported. Requests left unspecified default to the corresponding limit, per Kubernetes semantics — checked per-key on the raw map so an explicit `"0"` request is preserved.

**Landmine for operators:** in Kubernetes quantity syntax `102400m` memory means 102.4 *bytes* (milli-units), while podman's `--memory 102400m` means 100 GiB. Always use `Mi`/`Gi` in Spark manifests.

**Impact:** the incident manifest is now rejected at submit time; limits-only pods are fully accounted. Remaining hardening (host memory headroom reservation, usage-based alerting) tracked separately in #47.

**Root cause #2 (found by live verification of v1.13.2):** with the parser fixed, two valid 60Gi-request pods STILL co-scheduled on the live host. `ResourceReconciler.ReconcileOnce` called `tracker.UpdateAllocation(pod, actual)` with `actual.MemoryMB = stats.MemoryMB` — the pod's instantaneous usage. A pod that requested 60Gi but was still ramping (using a few MB) had its reservation rewritten down to that usage within one 60s tick, and the next pod was admitted into the freed space. This, not the parser, is what co-scheduled the two render pods in the original incident timeline (pod B arrived minutes after pod A — plenty of ticks). Fix: `UpdateAllocation` is now monotonic per field — an allocation starts at the admitted request and may only grow to the observed high-water mark, never shrink. Live acceptance test: submit two 60Gi-request alpine pods back-to-back; the second must stay `pending` with an `awaiting-resources` shortfall reason for longer than one resource-reconcile interval (60s).

**Lesson:** a "sync actual vs requested" loop that writes into the same ledger admission reads from silently converts request-based admission into usage-based admission. Reservations and observations need one-way flow: observations may raise the ledger, never lower it.

## 2026-04-29: Issue #37 phantom-running pods leak resources

**Type:** finding
**Tags:** reconciler, executor, podman, issue-37, resource-leak

**Problem:** A pod stayed in `phase: running` in the Spark API after its podman pod had been removed (silent exit, OOM, or manual `podman pod rm`). The pod's CPU/GPU/memory quota was held forever; subsequent pods needing the same resources queued indefinitely. Workaround was a manual `DELETE /api/v1/pods/<name>`.

**Root cause:** `internal/executor/podman.go` `PodStatus` runs `podman pod inspect <name>`. When the pod has been removed entirely, that command exits non-zero and `PodStatus` returns an error. `internal/reconciler/reconciler.go` `reconcileRunning` treated every `PodStatus` error as transient (`slog.Error` + return), so the state machine never transitioned. A primitive `isNoSuchPod(err)` already existed and was used by `reconcileScheduled`; `reconcileRunning` simply did not consult it.

**Fix:** In `reconcileRunning`, switch on the error class. If `isNoSuchPod(err)`, synthesize `Status{Running: false, ExitCode: -1}`, append a `lost` event, and fall through to the existing exited-pod transition path (release scheduler resources and apply restart policy). All other errors keep the existing log-and-return behaviour. Test: `TestIssue37_NoSuchPodRecoversResources` injects a `no such pod` error from a stub executor and asserts the pod ends up Failed with the resources released and a `lost` event recorded.

**Impact:** A pod whose podman backing has gone away is detected within one reconcile tick (~5s) and its quota is released. No new dependencies, no API change, parallel to the existing `reconcileScheduled` recovery path.

## 2026-04-28: Issue #32 silent-pending investigation (T1.3)

**Type:** investigation
**Tags:** reconciler, scheduler, issue-32, watchdog, silent-pending

**Problem:** Issue #32: a high-priority GPU pod (`ztensor102-v17-r2`, GPU=1, priorityClassName=high, restartPolicy=Never) sat in `status: pending` for 20+ minutes with `events: []`, `startAttempts: 0`, empty `reason`. The user asked whether `reconcileOnce` could fail to reach `reconcilePending` for a "logically pending" pod -- e.g. because a prior failed attempt had left the record in `Scheduled` state.

**Reproduction:** New file `internal/reconciler/reconciler_issue32_test.go`. Three tests:

1. `TestIssue32_PendingPodReachesReconcilePending` -- submit the exact issue manifest as a `PodSpec` literal into a fresh `PodStore`, run `reconcileOnce` once, assert `Status == Running` and `CreatePod` was called. Passes -- the natural Pending state DOES reach `reconcilePending`.
2. `TestIssue32_StatusGatesReconcilePath` -- table-driven over `Pending|Scheduled|Running|Completed|Failed|Preempted`. Only `Pending` triggers a `CreatePod` (proxy for "reached `reconcilePending`"). Confirmed: every other status takes a different switch arm; none silently re-routes a pending-equivalent pod into `reconcilePending`.
3. `TestIssue32_SchedulerPendingIsSilent` -- give the scheduler too little CPU to fit the request and no preemption candidates; `Schedule()` returns `Action=Pending`. Reconciler reaches `reconcilePending` and hits the `case scheduler.Pending` arm.

**Root cause (pre-fix):** This is NOT a "wrong status routes around `reconcilePending`" bug. `reconcileOnce`'s switch is exhaustive over the states it cares about, and `Apply` always inserts new pods as `StatusPending`. The defect was INSIDE `reconcilePending`: when `Schedule()` returned `Action=Pending`, the only side effect was `slog.Debug(...)`. From an API consumer's perspective this was indistinguishable from "the reconciler never ran" -- exactly the user-reported symptom.

**Fix:** T1.2 (PR #34, this same wave) addresses the visibility half: the `case scheduler.Pending` arm now calls `updateStatus(name, StatusPending, "awaiting-resources: "+reason)` and `store.AddEvent(name, "PendingWatchdog", msg)`. The `ScheduleResult.Reason` field plumbs the scheduler's shortfall description (e.g. "no preemption candidates; shortfall: gpu 1 > 0 free") to the reconciler. After this Wave 1 lands, the issue's symptom (silent forever-pending) is impossible by construction.

**Outstanding:** the second-order question -- WHY did `Schedule()` return `Pending` for a pod that fit free capacity in the original repro -- remains. Likely candidate: `scheduler.go:89` skips candidates with `pod.Priority <= spec.Priority`, so if the running CPU-only pod's `Priority` was equal to (or lower numeric than) the new pod's, it was skipped during preemption-candidate gathering. Tracked as FU1.3b in the plan.

**Impact:** `reconcileOnce` cannot fail to reach `reconcilePending` for a `Pending` pod -- confirmed by test. The watchdog (T1.2) is sufficient to mitigate the user-visible symptom; the scheduler-accounting audit (FU1.3b) is a separate follow-up.

## 2026-04-16: Issue #22 cpuset pinning shipped (v1.9.0-v1.10.1), DGX validated

**Type:** finding
**Tags:** v1.9.0, v1.10.0, v1.10.1, cpuset, issue-22, deployment, auto-upgrade

**Problem:** Issue #22: pods with integer CPU limits still saturated all 20 DGX cores because Spark only passed `--cpus` (CFS quota) to podman, not `--cpuset-cpus` (core pinning). This caused 40+ minutes of 100% packet loss and SSH banner timeouts during Wolf CrossAsset GPU training.

**Root cause:** `internal/executor/podman.go` emitted only `--cpus=N.0` which is a cumulative time quota, not a CPU set restriction. Container threads could land on any core including those handling network IRQs and sshd.

**Fix:** Five waves across PRs #23, #24, #25: scheduler tracks per-pod core assignments mirroring the GPU device-slot pattern; executor emits `--cpuset-cpus`; SQLite persists assignments for restart recovery; `--system-reserve-cores` flag excludes host cores; `/api/v1/node` exposes the partition; admission rejects oversize pods; three new Prometheus metrics added. ADR-012 documents the decision. v1.9.0 released with the code; v1.10.0 added the deploy/spark.env wiring; v1.10.1 added version in /healthz + auto-upgrade conffile fix.

**Impact:** DGX validated: pod with limits.cpu=4 pinned to cpuset-cpus=2-5, oversize pod rejected 400, /healthz returns version=1.10.1. Auto-upgrade pipeline verified end-to-end (push -> release-please -> GoReleaser .deb -> DGX timer -> dpkg install -> restart). The `--force-confold` fix prevents future conffile prompt failures in non-interactive upgrades.

## 2026-04-15: Issue #13 fix shipped, v1.8.1 deployed, auto-upgrade live

**Type:** finding
**Tags:** v1.8.1, reconciler, issue-13, deployment, auto-upgrade

**Problem:** Issue #13 -- when `podman pod create` failed during `reconcilePending`, the pod was stored with `StatusScheduled` but the underlying podman pod was missing. Subsequent reconcile ticks called `podman pod inspect`, got `no such pod`, logged the error, and never re-queued. The pod stuck indefinitely until the client issued `DELETE` + fresh `POST`.

**Root cause:** `reconcileScheduled` returned on any inspect error without distinguishing `no such pod` (authoritative: missing) from transient daemon flakes.

**Fix:** Added `isNoSuchPod(err)` helper. `reconcileScheduled` now switches on inspect error: `nil` -> proceed; `no such pod` -> treat as not-running and continue to staleness check; other errors -> log and return. `scheduledStaleness` reduced from 30s to 10s so the next 5s tick can act. Pre-reset `BackoffLimit` check transitions to `StatusFailed` instead of resetting to `Pending` when attempts exceed the limit. Four new `TestReconcileScheduled_*` cases cover missing-after-staleness, missing-within-staleness, transient-error, and backoff-limit-exceeded.

**Impact:** PR #18 merged (commit `a35ac4e`), issue #13 auto-closed. v1.8.1 released and deployed to DGX `192.168.86.250`. Auto-upgrade infrastructure (script + systemd timer at 15-minute interval) installed and active on production DGX, so future Spark releases self-deploy without manual intervention.

## 2026-04-15: Resolve open GitHub issues (#8, #10, #12)

**Type:** finding
**Tags:** reconciler, api, manifest, yaml, state, issue-8, issue-10, issue-12

**Problem:**
- #12: `DELETE /api/v1/pods/{name}` removed the Spark store record even when podman failed to stop/remove the pod, leaving orphaned podman pods that saturated the DGX (GPU/CPU). The reconciler logged `orphaned pod discovered` every ~5s forever without acting.
- #10: Args/command values containing `://` (e.g. `nats://host:port`) were silently dropped by the hand-rolled YAML list parser because any `:` in an item was treated as a map separator.
- #8: When `podman pod create` succeeded but container start failed (e.g., missing volume), the reconciler looped infinitely retrying. No `backoffLimit` enforcement, no terminal failure state, no visibility of the container-start error.

**Root cause:**
- #12: `handleDeletePod` called `executor.StopPod`/`RemovePod` as best-effort (errors ignored) then unconditionally removed the store record. Reconciler's orphan branch was log-only.
- #10: `parseYAMLList` used `strings.Index(item, ":")`. YAML requires `:` to be followed by whitespace or EOL to act as a map separator; the parser did not enforce this and did not preserve quoted strings atomically.
- #8: No fields existed on `PodRecord` to track failure state. Reconciler kept rescheduling failed pods without a ceiling.

**Fix:**
- #12: `handleDeletePod` returns 500 + `deleted:false` and preserves the store record when podman stop/remove fails. "no such pod" treated as success. Reconciler now actively removes orphans after a 30s grace window via `orphanFirstSeen` tracking.
- #10: New `findMapSeparator` helper tracks quote state and only treats `:` as a map separator when followed by space/tab/EOL. Applied at both list-item and root parse sites.
- #8: Added `Reason`, `StartAttempts`, `LastAttemptAt` to `state.PodRecord` with idempotent SQLite column migration. Parsed `spec.backoffLimit` (default 3, `0` disables retry). Reconciler enforces exponential backoff (5s, 10s, 20s, 40s, cap 60s) and transitions to `StatusFailed` terminally when `StartAttempts > BackoffLimit`. Both `startAttempts` and `reason` surface on `GET /api/v1/pods/{name}`.

**Impact:**
- 7 new commits on main (PRs #14 and #16) plus T56.3 API surface in wave-3-apis.
- 304 tests pass with -race; no new linter findings.
- Wire check: DELETE handler, reconciler.RecoverPods, reconciler.reconcilePending, YAML parser, and GET /api/v1/pods/{name} all exercise the new paths.

## 2026-03-20: v1.6.0 GPU Count Model, Liveness Probes, CronJob Management, Node Info

**Type:** finding
**Tags:** v1.6.0, gpu, probes, cronjob, node-info

**Problem:** GPU resource model conflated device count and memory (parseGPU set GPUMemoryMB=1 for nvidia.com/gpu:1). No liveness probes for stuck containers. No HTTP management of cron jobs. No hardware detail endpoint.
**Root cause:** GPUMemoryMB was the only GPU field; parseGPU wrote device count into it. Liveness probes, cron management, and node info were not yet implemented.
**Fix:** Delivered 12 tasks across 3 parallel waves (8+1+3 agents):
- GPU count model: added GPUCount field to ResourceList, refactored parseGPU, updated scheduler to track device slots separately from memory, added heartbeat gpuCount field, reconciler GPU count drift detection.
- Liveness probes: ProbeSpec (exec, HTTP) parsed from manifests, ExecProbe/HTTPProbe executor methods, reconciler polls probes on each tick respecting InitialDelaySeconds/PeriodSeconds/FailureThreshold, restarts on threshold breach.
- CronJob HTTP management: CronScheduler.List()/Get() methods, GET /api/v1/cronjobs, GET /api/v1/cronjobs/{name}, DELETE /api/v1/cronjobs/{name}.
- Node info: GET /api/v1/node returns hostname, OS, arch, CPU cores, memory, GPU model/count/device IDs/memory.
**Impact:** 50 use cases (UC-001 through UC-050, excluding deferred UC-044 and UC-049). 48 WIRED, 2 PLANNED. 13 packages, 304 tests pass with -race. HTTP API now has 16 endpoints + auth + metrics.

## 2026-03-20: v1.5.0 Wiring Integrity and Reconciler Hardening

**Type:** finding
**Tags:** v1.5.0, wiring, reconciler, cron, securityContext, manifest-removal

**Problem:** Post-v1.4.0 audit found 7 broken wiring paths (CronJob registration on NATS/HTTP, manifest removal no-op, delete not releasing scheduler resources, restart counter never incremented, stuck Scheduled/Preempted pods, StreamPodLogs zombie processes) plus missing Ki suffix parsing and securityContext support.
**Root cause:** N/A -- audit-driven fix delivery.
**Fix:** Delivered 15 tasks across 3 parallel waves (10+2+3 agents):
- CronJob registration on all ingestion paths (NATS, HTTP, filesystem).
- Manifest file removal stops pods, releases resources, unregisters cron jobs.
- HTTP and NATS delete releases scheduler resources immediately.
- Restart counter increments on reconciler re-queue.
- Stuck StatusScheduled (30s timeout) and StatusPreempted pods recovered.
- StreamPodLogs reaps child processes via cmdReadCloser wrapping cmd.Wait().
- parseMemory Ki/K suffix support added.
- SecurityContext (runAsUser, privileged, capabilities add/drop) parsed and forwarded to podman.
- Source path tracking in state store for manifest-to-pod association.
**Impact:** 46 use cases (UC-001 through UC-046) all WIRED. Zero broken use cases. 13 packages, all tests pass.

## 2026-03-20: v1.4.0 Container Operations and GPU Device Management

**Type:** finding
**Tags:** v1.4.0, exec, ports, init-containers, gpu-devices, images

**Problem:** v1.3.0 lacked pod exec, container port mapping, init containers, GPU device isolation, and image management API.
**Root cause:** N/A -- planned feature delivery.
**Fix:** Delivered 5 features across 15 tasks in 3 parallel waves (8+4+3 agents):
- Pod exec: POST /api/v1/pods/{name}/exec runs commands inside containers, returns stdout/stderr/exitCode JSON.
- Container port mapping: manifest ports parsed, mapped via `podman pod create --publish`.
- Init containers: parsed from initContainers field, run sequentially before main containers, fail-fast on non-zero exit.
- GPU device assignment: nvidia-smi device enumeration, scheduler tracks device slots, NVIDIA_VISIBLE_DEVICES env var replaces --device nvidia.com/gpu=all, --gpu-max enforced.
- Image management: GET /api/v1/images lists images, POST /api/v1/images/pull pulls by name:tag.
**Impact:** 36 use cases (UC-001 through UC-036) all WIRED. 13 packages, all tests pass. HTTP API now has 12 endpoints + auth + metrics.

## 2026-03-19: v1.3.0 Observability, Security, and Operational Maturity

**Type:** finding
**Tags:** v1.3.0, metrics, auth, logs, events, emptydir

**Problem:** v1.2.0 had no Prometheus metrics, no HTTP auth, no HTTP access to pod logs/events, no structured logging, and broken emptyDir volumes.
**Root cause:** N/A -- planned feature delivery.
**Fix:** Delivered 6 features across 14 tasks in 2 parallel waves (8+4 agents):
- Prometheus /metrics endpoint (stdlib text exposition format, no client_golang). ADR-010.
- Bearer token HTTP auth middleware (--api-token-file, /healthz and /metrics exempt). ADR-011.
- Pod logs via HTTP: GET /api/v1/pods/{name}/logs with ?tail=N and ?follow=true (SSE).
- Pod events via HTTP: GET /api/v1/pods/{name}/events with ?since=RFC3339 filter.
- Structured JSON logging: --log-format json switches slog to JSONHandler.
- EmptyDir volumes: mapped to podman --mount type=tmpfs,destination=PATH.
**Impact:** 31 use cases (UC-001 through UC-031) all WIRED. 13 packages, all tests pass. HTTP API now has 9 endpoints + auth + metrics.

## 2026-03-19: v1.1.0 Full System Verification

**Type:** finding
**Tags:** verification, v1.1.0, use-cases, wiring

**Problem:** Needed to verify all 17 use cases (UC-001 through UC-017) were fully wired end-to-end after v1.1.0 (SQLite persistence, pod recovery, retention pruning).
**Root cause:** N/A -- verification audit, not a bug.
**Fix:** N/A -- all 17 use cases confirmed WIRED. Zero gaps found.
**Impact:** Validated that v1.1.0 is production-ready. 10 packages, all tests pass with -race. Startup sequence verified (21 steps). Shutdown sequence verified (graceful with 10s wait). Wiring verified across all layers: CLI -> state -> scheduler -> executor -> NATS -> SQLite.

Key findings:
- All 10 packages compile and pass tests (watcher takes ~38s due to polling intervals).
- Pod recovery reconciles podman state with SQLite on restart.
- Retention pruning (10-min tick) cleans both in-memory store and SQLite.
- No orphaned code paths or unconnected handlers found.
