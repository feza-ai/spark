package reconciler

// Tests in this file investigate Issue #32: a high-priority GPU pod
// that stayed in StatusPending for 20+ minutes with zero events.
//
// They are intentionally additive (separate file from reconciler_test.go)
// so the watchdog work tracked under T1.2 can edit reconciler.go without
// merge conflicts.

import (
	"context"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// issue32Spec returns a PodSpec equivalent to the manifest the user
// submitted in https://github.com/feza-ai/spark/issues/32:
//
//	priorityClassName: high
//	restartPolicy: Never
//	resources.limits.nvidia.com/gpu: "1"
//	resources.requests: cpu=6, memory=16Gi
func issue32Spec() manifest.PodSpec {
	return manifest.PodSpec{
		Name:              "ztensor102-v17-r2",
		PriorityClassName: "high",
		Priority:          100, // higher numeric == lower priority in this scheduler; "high" should be a low number, but Schedule only uses it for preemption candidate selection
		RestartPolicy:     "Never",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "train",
				Image: "localhost:5000/wolf-train-crossasset:t33-34f1b288",
				Args:  []string{"-gpu", "-stride=60", "-folds=2", "-epochs=2"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{
						CPUMillis: 6000,
						MemoryMB:  16384,
						GPUCount:  1,
					},
					Limits: manifest.ResourceList{
						CPUMillis: 6000,
						MemoryMB:  24576,
						GPUCount:  1,
					},
				},
			},
		},
	}
}

// gpuTestScheduler builds a scheduler with enough GPU capacity to fit
// the issue32 pod, mirroring the DGX node free resources reported in
// the issue (>1 GPU, ample CPU/RAM).
func gpuTestScheduler() *scheduler.Scheduler {
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 16000, MemoryMB: 131072, GPUCount: 1, GPUMemoryMB: 122564},
		scheduler.Resources{},
		[]int{0}, 1,
	)
	return scheduler.NewScheduler(tracker)
}

// TestIssue32_PendingPodReachesReconcilePending asserts that a fresh
// pod identical to the issue manifest, in its natural Pending state,
// is picked up by reconcileOnce and acted on by reconcilePending — the
// observable proof being a CreatePod call on the executor.
func TestIssue32_PendingPodReachesReconcilePending(t *testing.T) {
	store := state.NewPodStore()
	sched := gpuTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, 0)

	store.Apply(issue32Spec())

	r.reconcileOnce(context.Background())

	rec, ok := store.Get("ztensor102-v17-r2")
	if !ok {
		t.Fatal("pod missing from store after reconcile")
	}
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running (proves reconcilePending fired and CreatePod succeeded), got %s; events=%v",
			rec.Status, rec.Events)
	}
	creates := exec.getCreates()
	if len(creates) != 1 || creates[0] != "ztensor102-v17-r2" {
		t.Fatalf("expected single CreatePod for issue32 pod, got %v", creates)
	}
}

// TestIssue32_StatusGatesReconcilePath proves that only StatusPending
// reaches reconcilePending. For every other status the switch in
// reconcileOnce takes a different branch (or no branch). The point is
// to rule out the hypothesis that the issue was caused by a prior
// failed attempt leaving the record in StatusScheduled (or another
// state) where reconcilePending no longer fires.
func TestIssue32_StatusGatesReconcilePath(t *testing.T) {
	cases := []struct {
		name        string
		status      state.PodStatus
		wantCreates int // CreatePod is the externally observable side effect of reconcilePending
		notes       string
	}{
		{"pending", state.StatusPending, 1, "natural initial state — must reach reconcilePending"},
		{"scheduled", state.StatusScheduled, 0, "reconcileScheduled path; no CreatePod (already created or stale-reset)"},
		{"running", state.StatusRunning, 0, "reconcileRunning path"},
		{"completed", state.StatusCompleted, 0, "no switch case — silently skipped"},
		{"failed", state.StatusFailed, 0, "no switch case — silently skipped"},
		{"preempted", state.StatusPreempted, 0, "reconcilePreempted resets to Pending; no CreatePod this tick"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := state.NewPodStore()
			sched := gpuTestScheduler()
			exec := newStubExecutor()
			r := NewReconciler(store, sched, exec, 0)

			spec := issue32Spec()
			store.Apply(spec) // lands as Pending
			if tc.status != state.StatusPending {
				store.UpdateStatus(spec.Name, tc.status, "test fixture: forced status")
			}
			// Tell the executor "no such pod" so reconcileScheduled
			// follows the missing-pod branch deterministically.
			exec.statuses = map[string]executor.Status{}

			r.reconcileOnce(context.Background())

			if got := len(exec.getCreates()); got != tc.wantCreates {
				t.Errorf("status=%s: CreatePod calls = %d, want %d (%s)",
					tc.status, got, tc.wantCreates, tc.notes)
			}
		})
	}
}

// TestIssue32_SchedulerPendingIsSilent reproduces the user-visible
// symptom: reconcilePending IS reached, but when the scheduler returns
// Action=Pending (no fit, no preemption candidate) the reconciler emits
// only a slog.Debug — no PodEvent, no Reason update, no status change.
// From the API consumer's perspective this is indistinguishable from
// "the reconciler never ran". This is the real defect behind #32. The
// watchdog tracked in T1.2 mitigates the user-visible symptom; a
// targeted fix would be to AddEvent / set Reason here so the silent
// path is no longer silent. Documented in docs/devlog.md.
func TestIssue32_SchedulerPendingIsSilent(t *testing.T) {
	store := state.NewPodStore()
	// Tracker too small to fit the pod (1000m CPU vs 6000m request) and
	// no preemption candidates, so Schedule returns Action=Pending.
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 1024, GPUCount: 1, GPUMemoryMB: 122564},
		scheduler.Resources{}, []int{0}, 1,
	)
	sched := scheduler.NewScheduler(tracker)
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, 0)

	store.Apply(issue32Spec())

	r.reconcileOnce(context.Background())

	rec, _ := store.Get("ztensor102-v17-r2")
	if rec.Status != state.StatusPending {
		t.Fatalf("expected status to remain Pending, got %s", rec.Status)
	}
	// reconcilePending DID run (proven by the path being taken — there
	// is no other code that handles StatusPending), but it left no
	// trace. The Apply itself adds no event, so the only event we'd
	// expect is from reconcilePending — and there is none.
	for _, e := range rec.Events {
		if e.Type == "PodPending" || e.Type == "ScheduleDeferred" {
			t.Fatalf("found a diagnostic event %q — looks like the silent-pending defect was fixed; please update this test", e.Type)
		}
	}
	if rec.Reason != "" {
		t.Fatalf("expected empty Reason on silent-pending path, got %q (defect may be fixed)", rec.Reason)
	}
	if got := exec.getCreates(); len(got) != 0 {
		t.Fatalf("expected no CreatePod calls when Schedule returns Pending, got %v", got)
	}
}
