package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func newTestTracker() *scheduler.ResourceTracker {
	return scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192, GPUMemoryMB: 16384},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
}

func TestResourceReconciler_NoDiscrepancy(t *testing.T) {
	store := state.NewPodStore()
	exec := newStubExecutor()
	tracker := newTestTracker()

	spec := testPodSpec("web", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("web", state.StatusRunning, "running")

	// Allocate in tracker to match
	tracker.Allocate("web", spec.TotalRequests())

	// PodStats returns values matching requests
	exec.podStats = map[string]executor.PodResourceUsage{
		"web": {CPUPercent: 10.0, MemoryMB: 128},
	}

	rr := NewResourceReconciler(store, exec, tracker, time.Second)
	rr.ReconcileOnce(context.Background())

	// No panic, no error — reconciliation completes cleanly.
	alloc := tracker.Allocated()
	if alloc.MemoryMB != 128 {
		t.Errorf("expected allocated memory updated to 128, got %d", alloc.MemoryMB)
	}
}

func TestResourceReconciler_MemoryExceeded(t *testing.T) {
	store := state.NewPodStore()
	exec := newStubExecutor()
	tracker := newTestTracker()

	spec := testPodSpec("heavy", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("heavy", state.StatusRunning, "running")

	tracker.Allocate("heavy", spec.TotalRequests())

	// Return memory usage > 1.5x requested (128 * 1.5 = 192, return 200)
	exec.podStats = map[string]executor.PodResourceUsage{
		"heavy": {CPUPercent: 50.0, MemoryMB: 200},
	}

	rr := NewResourceReconciler(store, exec, tracker, time.Second)
	rr.ReconcileOnce(context.Background())

	// Verify tracker was updated with actual values
	alloc := tracker.Allocated()
	if alloc.MemoryMB != 200 {
		t.Errorf("expected allocated memory updated to 200, got %d", alloc.MemoryMB)
	}
}

func TestResourceReconciler_UpdatesTracker(t *testing.T) {
	store := state.NewPodStore()
	exec := newStubExecutor()
	tracker := newTestTracker()

	spec := testPodSpec("track", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("track", state.StatusRunning, "running")

	req := spec.TotalRequests()
	tracker.Allocate("track", req)

	// Verify initial allocation
	before := tracker.Allocated()
	if before.MemoryMB != req.MemoryMB {
		t.Fatalf("expected initial memory %d, got %d", req.MemoryMB, before.MemoryMB)
	}

	// Return different actual memory
	exec.podStats = map[string]executor.PodResourceUsage{
		"track": {CPUPercent: 25.0, MemoryMB: 96},
	}

	rr := NewResourceReconciler(store, exec, tracker, time.Second)
	rr.ReconcileOnce(context.Background())

	after := tracker.Allocated()
	if after.MemoryMB != 96 {
		t.Errorf("expected allocated memory updated to 96, got %d", after.MemoryMB)
	}
	// CPU should be preserved from original request
	if after.CPUMillis != req.CPUMillis {
		t.Errorf("expected CPU preserved at %d, got %d", req.CPUMillis, after.CPUMillis)
	}
}

func TestResourceReconciler_RunStopsOnCancel(t *testing.T) {
	store := state.NewPodStore()
	exec := newStubExecutor()
	tracker := newTestTracker()

	rr := NewResourceReconciler(store, exec, tracker, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rr.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestResourceReconciler_GPUCountPreserved(t *testing.T) {
	tests := []struct {
		name     string
		gpuCount int
		gpuMem   int
	}{
		{name: "single-gpu", gpuCount: 1, gpuMem: 16384},
		{name: "multi-gpu", gpuCount: 4, gpuMem: 65536},
		{name: "no-gpu", gpuCount: 0, gpuMem: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			exec := newStubExecutor()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192, GPUCount: tt.gpuCount, GPUMemoryMB: tt.gpuMem},
				scheduler.Resources{},
				nil, 0,
			)

			spec := testPodSpec("gpu-pod", "Always", 0)
			spec.Containers[0].Resources.Requests.GPUCount = tt.gpuCount
			spec.Containers[0].Resources.Requests.GPUMemoryMB = tt.gpuMem
			store.Apply(spec)
			store.UpdateStatus("gpu-pod", state.StatusRunning, "running")
			tracker.Allocate("gpu-pod", spec.TotalRequests())

			exec.podStats = map[string]executor.PodResourceUsage{
				"gpu-pod": {CPUPercent: 10.0, MemoryMB: 256},
			}

			rr := NewResourceReconciler(store, exec, tracker, time.Second)
			rr.ReconcileOnce(context.Background())

			alloc := tracker.Allocated()
			if alloc.GPUCount != tt.gpuCount {
				t.Errorf("GPUCount = %d, want %d", alloc.GPUCount, tt.gpuCount)
			}
			if alloc.GPUMemoryMB != tt.gpuMem {
				t.Errorf("GPUMemoryMB = %d, want %d", alloc.GPUMemoryMB, tt.gpuMem)
			}
			if alloc.MemoryMB != 256 {
				t.Errorf("MemoryMB = %d, want 256", alloc.MemoryMB)
			}
		})
	}
}
