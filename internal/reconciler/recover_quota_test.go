package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

// Issue #53: pods now survive control-plane restarts, so RecoverPods must
// re-register their quota in the fresh scheduler ledger. Before this fix a
// store-Running pod hit a bare `continue` and held no allocation, letting
// admission overcommit into resources the survivors were using.

func TestRecoverPods_StoreRunningPodReclaimsQuota(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("survivor", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("survivor", state.StatusRunning, "running")

	exec.listPods = []executor.PodListEntry{{Name: "survivor", Running: true}}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, held := sched.Tracker().AllocatedBy("survivor")
	if !held {
		t.Fatal("expected survivor to hold scheduler quota after recovery")
	}
	want := spec.TotalRequests()
	if got.CPUMillis != want.CPUMillis || got.MemoryMB != want.MemoryMB {
		t.Errorf("recovered allocation = %+v, want %+v", got, want)
	}
}

func TestRecoverPods_RecoveredPodReclaimsQuota(t *testing.T) {
	// Store says not-running (e.g. stale Scheduled), podman says running:
	// the existing recovery path must also register quota, not just
	// preemption candidacy.
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("lazarus", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("lazarus", state.StatusScheduled, "was mid-create")

	exec.listPods = []executor.PodListEntry{{Name: "lazarus", Running: true}}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	rec, _ := store.Get("lazarus")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}
	if _, held := sched.Tracker().AllocatedBy("lazarus"); !held {
		t.Fatal("expected lazarus to hold scheduler quota after recovery")
	}
}

func TestRecoverPods_DegradedSurvivorStillReclaimsQuota(t *testing.T) {
	// A pod that survived a control-plane restart can be Degraded at
	// startup (its infra container took a hit) while the workload runs on.
	// Quota must be re-registered on presence, not on pod-level Running;
	// a genuinely dead pod gets its quota released one reconcile tick
	// later via reconcileRunning.
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("degraded-survivor", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("degraded-survivor", state.StatusRunning, "running")

	exec.listPods = []executor.PodListEntry{
		{Name: "degraded-survivor", Running: false, Status: "Degraded"},
	}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, held := sched.Tracker().AllocatedBy("degraded-survivor"); !held {
		t.Fatal("expected degraded survivor to hold scheduler quota after recovery")
	}
	rec, _ := store.Get("degraded-survivor")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected still Running, got %s", rec.Status)
	}
}
