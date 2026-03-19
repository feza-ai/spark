package state

import (
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

func podSpec(name string) manifest.PodSpec {
	return manifest.PodSpec{
		Name: name,
		Containers: []manifest.ContainerSpec{
			{Name: "main", Image: "alpine:latest"},
		},
	}
}

func TestApplyNewPod(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))

	rec, ok := s.Get("pod-a")
	if !ok {
		t.Fatal("expected pod-a to exist")
	}
	if rec.Status != StatusPending {
		t.Fatalf("expected status %q, got %q", StatusPending, rec.Status)
	}
}

func TestApplyExistingPreservesStatus(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))
	s.UpdateStatus("pod-a", StatusRunning, "started")

	// Re-apply with updated spec
	spec := podSpec("pod-a")
	spec.Containers[0].Image = "ubuntu:latest"
	s.Apply(spec)

	rec, ok := s.Get("pod-a")
	if !ok {
		t.Fatal("expected pod-a to exist")
	}
	if rec.Status != StatusRunning {
		t.Fatalf("expected status %q preserved, got %q", StatusRunning, rec.Status)
	}
	if rec.Spec.Containers[0].Image != "ubuntu:latest" {
		t.Fatalf("expected spec updated to ubuntu:latest, got %s", rec.Spec.Containers[0].Image)
	}
}

func TestDeleteExisting(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))

	if !s.Delete("pod-a") {
		t.Fatal("expected Delete to return true for existing pod")
	}
	if _, ok := s.Get("pod-a"); ok {
		t.Fatal("expected pod-a to be deleted")
	}
}

func TestDeleteMissing(t *testing.T) {
	s := NewPodStore()
	if s.Delete("nonexistent") {
		t.Fatal("expected Delete to return false for missing pod")
	}
}

func TestGetExisting(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))

	rec, ok := s.Get("pod-a")
	if !ok {
		t.Fatal("expected pod-a to exist")
	}
	if rec.Spec.Name != "pod-a" {
		t.Fatalf("expected name pod-a, got %s", rec.Spec.Name)
	}
}

func TestGetMissing(t *testing.T) {
	s := NewPodStore()
	if _, ok := s.Get("nonexistent"); ok {
		t.Fatal("expected Get to return false for missing pod")
	}
}

func TestListAll(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))
	s.Apply(podSpec("pod-b"))
	s.Apply(podSpec("pod-c"))

	all := s.List("")
	if len(all) != 3 {
		t.Fatalf("expected 3 pods, got %d", len(all))
	}
}

func TestListByStatus(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))
	s.Apply(podSpec("pod-b"))
	s.Apply(podSpec("pod-c"))
	s.UpdateStatus("pod-a", StatusRunning, "started")
	s.UpdateStatus("pod-b", StatusRunning, "started")

	running := s.List(StatusRunning)
	if len(running) != 2 {
		t.Fatalf("expected 2 running pods, got %d", len(running))
	}

	pending := s.List(StatusPending)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending pod, got %d", len(pending))
	}
}

func TestUpdateStatus(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))

	if !s.UpdateStatus("pod-a", StatusRunning, "container started") {
		t.Fatal("expected UpdateStatus to return true")
	}

	rec, _ := s.Get("pod-a")
	if rec.Status != StatusRunning {
		t.Fatalf("expected status %q, got %q", StatusRunning, rec.Status)
	}
	if rec.StartedAt.IsZero() {
		t.Fatal("expected StartedAt to be set")
	}
	if len(rec.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rec.Events))
	}
	if rec.Events[0].Message != "container started" {
		t.Fatalf("expected event message %q, got %q", "container started", rec.Events[0].Message)
	}
}

func TestUpdateStatusSetsFinishedAt(t *testing.T) {
	tests := []struct {
		name   string
		status PodStatus
	}{
		{"completed", StatusCompleted},
		{"failed", StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPodStore()
			s.Apply(podSpec("pod-a"))
			s.UpdateStatus("pod-a", tt.status, "done")

			rec, _ := s.Get("pod-a")
			if rec.FinishedAt.IsZero() {
				t.Fatal("expected FinishedAt to be set")
			}
		})
	}
}

func TestUpdateStatusMissing(t *testing.T) {
	s := NewPodStore()
	if s.UpdateStatus("nonexistent", StatusRunning, "msg") {
		t.Fatal("expected UpdateStatus to return false for missing pod")
	}
}

func TestAddEvent(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))

	if !s.AddEvent("pod-a", "restarted", "OOM kill") {
		t.Fatal("expected AddEvent to return true")
	}

	rec, _ := s.Get("pod-a")
	if len(rec.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rec.Events))
	}
	if rec.Events[0].Type != "restarted" {
		t.Fatalf("expected event type %q, got %q", "restarted", rec.Events[0].Type)
	}
}

func TestAddEventMissing(t *testing.T) {
	s := NewPodStore()
	if s.AddEvent("nonexistent", "restarted", "msg") {
		t.Fatal("expected AddEvent to return false for missing pod")
	}
}

func TestNames(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))
	s.Apply(podSpec("pod-b"))

	names := s.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}

	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["pod-a"] || !nameSet["pod-b"] {
		t.Fatalf("expected pod-a and pod-b, got %v", names)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("pod-a"))
	s.AddEvent("pod-a", "info", "test")

	rec, _ := s.Get("pod-a")
	rec.Events = append(rec.Events, PodEvent{Type: "extra"})

	original, _ := s.Get("pod-a")
	if len(original.Events) != 1 {
		t.Fatalf("expected internal events unmodified (1), got %d", len(original.Events))
	}
}

func TestPrune_RemovesOldCompleted(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("old-pod"))
	s.UpdateStatus("old-pod", StatusCompleted, "done")

	// Manually set FinishedAt to 2 hours ago
	s.mu.Lock()
	s.pods["old-pod"].FinishedAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	pruned := s.Prune(1 * time.Hour)
	if pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", pruned)
	}
	if _, ok := s.Get("old-pod"); ok {
		t.Fatal("expected old-pod to be pruned")
	}
}

func TestPrune_KeepsRecent(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("recent-pod"))
	s.UpdateStatus("recent-pod", StatusCompleted, "done")
	// FinishedAt is set to now by UpdateStatus

	pruned := s.Prune(1 * time.Hour)
	if pruned != 0 {
		t.Fatalf("expected 0 pruned, got %d", pruned)
	}
	if _, ok := s.Get("recent-pod"); !ok {
		t.Fatal("expected recent-pod to be kept")
	}
}

func TestPrune_KeepsRunning(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("running-pod"))
	s.UpdateStatus("running-pod", StatusRunning, "running")

	// Even if we set a fake old StartedAt, Running pods should not be pruned
	pruned := s.Prune(0) // zero duration = prune everything terminal
	if pruned != 0 {
		t.Fatalf("expected 0 pruned for running pod, got %d", pruned)
	}
	if _, ok := s.Get("running-pod"); !ok {
		t.Fatal("expected running-pod to be kept")
	}
}

func TestPrune_RemovesOldFailed(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("failed-pod"))
	s.UpdateStatus("failed-pod", StatusFailed, "error")

	s.mu.Lock()
	s.pods["failed-pod"].FinishedAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	pruned := s.Prune(1 * time.Hour)
	if pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", pruned)
	}
}

func TestSetReadOnly(t *testing.T) {
	s := NewPodStore()
	s.Apply(podSpec("before"))

	s.SetReadOnly(true)
	s.Apply(podSpec("rejected"))

	if _, ok := s.Get("before"); !ok {
		t.Fatal("expected 'before' to exist")
	}
	if _, ok := s.Get("rejected"); ok {
		t.Fatal("expected 'rejected' to be rejected in read-only mode")
	}

	// UpdateStatus still works in read-only mode.
	s.UpdateStatus("before", StatusRunning, "started")
	rec, _ := s.Get("before")
	if rec.Status != StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}

	// Delete still works in read-only mode.
	s.Delete("before")
	if _, ok := s.Get("before"); ok {
		t.Fatal("expected delete to work in read-only mode")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewPodStore()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(3)
		name := "pod-concurrent"
		go func() {
			defer wg.Done()
			s.Apply(podSpec(name))
		}()
		go func() {
			defer wg.Done()
			s.Get(name)
		}()
		go func() {
			defer wg.Done()
			s.Delete(name)
		}()
	}
	wg.Wait()
}
