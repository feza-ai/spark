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
