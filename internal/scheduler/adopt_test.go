package scheduler

import (
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

// Issue #53: AdoptPod registers an already-running pod after a restart.
// Unlike Schedule it never rejects — a running pod must be in the ledger.

func TestAdoptPod_AllocatesQuota(t *testing.T) {
	rt := newTestTracker()
	s := NewScheduler(rt)

	s.AdoptPod(PodInfo{
		Name:      "web",
		Resources: manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048},
	})

	got, held := rt.AllocatedBy("web")
	if !held {
		t.Fatal("expected web to hold an allocation after adoption")
	}
	if got.CPUMillis != 1000 || got.MemoryMB != 2048 {
		t.Errorf("allocation = %+v, want 1000m/2048MB", got)
	}
}

func TestAdoptPod_Idempotent(t *testing.T) {
	rt := newTestTracker()
	s := NewScheduler(rt)

	info := PodInfo{Name: "web", Resources: manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048}}
	s.AdoptPod(info)
	s.AdoptPod(info)

	alloc := rt.Allocated()
	if alloc.CPUMillis != 1000 || alloc.MemoryMB != 2048 {
		t.Errorf("double adoption changed totals: %+v", alloc)
	}
}

func TestAdoptPod_OverCapacityStillRecorded(t *testing.T) {
	// The pod is running whether or not the ledger has room; adoption must
	// record it anyway so admission sees the true commitment.
	rt := newTestTracker() // 4000m / 8192MB allocatable
	s := NewScheduler(rt)

	s.AdoptPod(PodInfo{
		Name:      "giant",
		Resources: manifest.ResourceList{CPUMillis: 999000, MemoryMB: 999999},
	})

	if _, held := rt.AllocatedBy("giant"); !held {
		t.Fatal("expected over-capacity pod to be force-recorded")
	}
	avail := rt.Available()
	if avail.MemoryMB > 0 {
		t.Errorf("expected no available memory after over-capacity adoption, got %d", avail.MemoryMB)
	}
}
