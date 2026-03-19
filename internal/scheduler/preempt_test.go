package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

func TestPreempt_AllVictimsSuccessful(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	// Allocate resources for two victims.
	tracker.Allocate("pod-a", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000})
	tracker.Allocate("pod-b", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000})
	s.AddPod(PodInfo{Name: "pod-a", Priority: 10})
	s.AddPod(PodInfo{Name: "pod-b", Priority: 10})

	stopFn := func(ctx context.Context, podName string, gracePeriod int) error {
		return nil
	}

	var evictedNames []string
	onEvicted := func(podName string) {
		evictedNames = append(evictedNames, podName)
	}

	victims := []string{"pod-a", "pod-b"}
	evicted, err := s.Preempt(context.Background(), victims, nil, stopFn, onEvicted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evicted) != 2 {
		t.Fatalf("expected 2 evicted, got %d", len(evicted))
	}
	if evicted[0] != "pod-a" || evicted[1] != "pod-b" {
		t.Fatalf("expected [pod-a pod-b], got %v", evicted)
	}

	// Verify onEvicted was called for each.
	if len(evictedNames) != 2 {
		t.Fatalf("expected onEvicted called 2 times, got %d", len(evictedNames))
	}

	// Verify resources were released.
	if _, ok := tracker.AllocatedBy("pod-a"); ok {
		t.Fatal("expected pod-a resources to be released")
	}
	if _, ok := tracker.AllocatedBy("pod-b"); ok {
		t.Fatal("expected pod-b resources to be released")
	}
}

func TestPreempt_OneVictimFails(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	tracker.Allocate("pod-a", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000})
	tracker.Allocate("pod-b", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000})
	tracker.Allocate("pod-c", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000})
	s.AddPod(PodInfo{Name: "pod-a", Priority: 10})
	s.AddPod(PodInfo{Name: "pod-b", Priority: 10})
	s.AddPod(PodInfo{Name: "pod-c", Priority: 10})

	stopFn := func(ctx context.Context, podName string, gracePeriod int) error {
		if podName == "pod-b" {
			return fmt.Errorf("stop failed")
		}
		return nil
	}

	var evictedNames []string
	onEvicted := func(podName string) {
		evictedNames = append(evictedNames, podName)
	}

	victims := []string{"pod-a", "pod-b", "pod-c"}
	evicted, err := s.Preempt(context.Background(), victims, nil, stopFn, onEvicted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// pod-b should have failed, pod-a and pod-c should succeed.
	if len(evicted) != 2 {
		t.Fatalf("expected 2 evicted, got %d: %v", len(evicted), evicted)
	}
	if evicted[0] != "pod-a" || evicted[1] != "pod-c" {
		t.Fatalf("expected [pod-a pod-c], got %v", evicted)
	}

	// pod-b resources should still be allocated.
	if _, ok := tracker.AllocatedBy("pod-b"); !ok {
		t.Fatal("expected pod-b resources to still be allocated")
	}
}

func TestPreempt_OnEvictedCalledForEachSuccess(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	tracker.Allocate("pod-x", manifest.ResourceList{CPUMillis: 500, MemoryMB: 1024, GPUMemoryMB: 2000})
	s.AddPod(PodInfo{Name: "pod-x", Priority: 10})

	stopFn := func(ctx context.Context, podName string, gracePeriod int) error {
		return nil
	}

	callCount := 0
	onEvicted := func(podName string) {
		callCount++
		if podName != "pod-x" {
			t.Errorf("expected pod-x, got %s", podName)
		}
	}

	evicted, err := s.Preempt(context.Background(), []string{"pod-x"}, nil, stopFn, onEvicted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evicted) != 1 {
		t.Fatalf("expected 1 evicted, got %d", len(evicted))
	}
	if callCount != 1 {
		t.Fatalf("expected onEvicted called once, got %d", callCount)
	}
}

func TestPreempt_ResourcesReleasedForEvictedVictims(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	tracker.Allocate("pod-1", manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000})
	s.AddPod(PodInfo{Name: "pod-1", Priority: 10})

	availBefore := tracker.Available()

	stopFn := func(ctx context.Context, podName string, gracePeriod int) error {
		return nil
	}

	evicted, err := s.Preempt(context.Background(), []string{"pod-1"}, nil, stopFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evicted) != 1 {
		t.Fatalf("expected 1 evicted, got %d", len(evicted))
	}

	availAfter := tracker.Available()

	if availAfter.CPUMillis != availBefore.CPUMillis+2000 {
		t.Errorf("expected CPU freed: got %d, want %d", availAfter.CPUMillis, availBefore.CPUMillis+2000)
	}
	if availAfter.MemoryMB != availBefore.MemoryMB+4096 {
		t.Errorf("expected memory freed: got %d, want %d", availAfter.MemoryMB, availBefore.MemoryMB+4096)
	}
	if availAfter.GPUMemoryMB != availBefore.GPUMemoryMB+8000 {
		t.Errorf("expected GPU memory freed: got %d, want %d", availAfter.GPUMemoryMB, availBefore.GPUMemoryMB+8000)
	}
}
