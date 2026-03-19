package scheduler

import (
	"fmt"
	"sync"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

func newTestTracker() *ResourceTracker {
	return NewResourceTracker(
		Resources{CPUMillis: 4000, MemoryMB: 8192, GPUMemoryMB: 16384},
		Resources{CPUMillis: 500, MemoryMB: 512, GPUMemoryMB: 0},
	)
}

func TestSystemReserve(t *testing.T) {
	rt := newTestTracker()
	avail := rt.Available()

	if avail.CPUMillis != 3500 {
		t.Errorf("expected 3500 CPU millis available, got %d", avail.CPUMillis)
	}
	if avail.MemoryMB != 7680 {
		t.Errorf("expected 7680 MB memory available, got %d", avail.MemoryMB)
	}
	if avail.GPUMemoryMB != 16384 {
		t.Errorf("expected 16384 MB GPU memory available, got %d", avail.GPUMemoryMB)
	}
}

func TestAllocateReducesAvailable(t *testing.T) {
	rt := newTestTracker()
	req := manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4096}

	if err := rt.Allocate("pod-a", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	avail := rt.Available()
	if avail.CPUMillis != 2500 {
		t.Errorf("expected 2500 CPU millis, got %d", avail.CPUMillis)
	}
	if avail.MemoryMB != 5632 {
		t.Errorf("expected 5632 MB memory, got %d", avail.MemoryMB)
	}
	if avail.GPUMemoryMB != 12288 {
		t.Errorf("expected 12288 MB GPU memory, got %d", avail.GPUMemoryMB)
	}
}

func TestReleaseIncreasesAvailable(t *testing.T) {
	rt := newTestTracker()
	req := manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4096}

	if err := rt.Allocate("pod-a", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt.Release("pod-a")

	avail := rt.Available()
	if avail.CPUMillis != 3500 {
		t.Errorf("expected 3500 CPU millis after release, got %d", avail.CPUMillis)
	}
}

func TestCanFitReturnsFalseWhenFull(t *testing.T) {
	rt := newTestTracker()

	// Fill up all CPU.
	if err := rt.Allocate("pod-a", manifest.ResourceList{CPUMillis: 3500, MemoryMB: 1024, GPUMemoryMB: 0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rt.CanFit(manifest.ResourceList{CPUMillis: 1, MemoryMB: 0, GPUMemoryMB: 0}) {
		t.Error("expected CanFit to return false when CPU is exhausted")
	}
}

func TestAllocateWhenFullReturnsError(t *testing.T) {
	rt := newTestTracker()

	if err := rt.Allocate("pod-a", manifest.ResourceList{CPUMillis: 3500, MemoryMB: 7680, GPUMemoryMB: 16384}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := rt.Allocate("pod-b", manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 100})
	if err == nil {
		t.Fatal("expected error when allocating on full tracker")
	}
}

func TestGPUExhaustion(t *testing.T) {
	rt := newTestTracker()

	// Allocate all GPU memory.
	if err := rt.Allocate("gpu-pod", manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 16384}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rt.CanFit(manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 1}) {
		t.Error("expected CanFit to return false when GPU memory is exhausted")
	}

	err := rt.Allocate("gpu-pod-2", manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 1})
	if err == nil {
		t.Fatal("expected error when GPU memory is exhausted")
	}
}

func TestReleaseIdempotent(t *testing.T) {
	rt := newTestTracker()

	// Release a pod that was never allocated — should not panic.
	rt.Release("nonexistent")

	// Allocate and release twice.
	if err := rt.Allocate("pod-a", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 1024, GPUMemoryMB: 0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rt.Release("pod-a")
	rt.Release("pod-a")

	avail := rt.Available()
	if avail.CPUMillis != 3500 {
		t.Errorf("expected 3500 CPU millis after double release, got %d", avail.CPUMillis)
	}
}

func TestAllocatedBy(t *testing.T) {
	rt := newTestTracker()
	req := manifest.ResourceList{CPUMillis: 500, MemoryMB: 256, GPUMemoryMB: 1024}

	if err := rt.Allocate("pod-a", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := rt.AllocatedBy("pod-a")
	if !ok {
		t.Fatal("expected pod-a to be tracked")
	}
	if got != req {
		t.Errorf("expected %+v, got %+v", req, got)
	}

	_, ok = rt.AllocatedBy("pod-b")
	if ok {
		t.Error("expected pod-b to not be tracked")
	}
}

func TestAllocated(t *testing.T) {
	rt := newTestTracker()

	rt.Allocate("pod-a", manifest.ResourceList{CPUMillis: 500, MemoryMB: 256, GPUMemoryMB: 1024})
	rt.Allocate("pod-b", manifest.ResourceList{CPUMillis: 300, MemoryMB: 128, GPUMemoryMB: 512})

	alloc := rt.Allocated()
	if alloc.CPUMillis != 800 {
		t.Errorf("expected 800 CPU millis allocated, got %d", alloc.CPUMillis)
	}
	if alloc.MemoryMB != 384 {
		t.Errorf("expected 384 MB memory allocated, got %d", alloc.MemoryMB)
	}
	if alloc.GPUMemoryMB != 1536 {
		t.Errorf("expected 1536 MB GPU memory allocated, got %d", alloc.GPUMemoryMB)
	}
}

func TestConcurrentAllocateRelease(t *testing.T) {
	rt := NewResourceTracker(
		Resources{CPUMillis: 100000, MemoryMB: 100000, GPUMemoryMB: 100000},
		Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := fmt.Sprintf("pod-%d", id)
			req := manifest.ResourceList{CPUMillis: 1, MemoryMB: 1, GPUMemoryMB: 1}
			_ = rt.Allocate(name, req)
			rt.Release(name)
		}(i)
	}
	wg.Wait()

	avail := rt.Available()
	if avail.CPUMillis != 100000 {
		t.Errorf("expected 100000 CPU millis after concurrent ops, got %d", avail.CPUMillis)
	}
}
