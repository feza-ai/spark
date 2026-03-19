package scheduler

import (
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

func podSpec(name string, priority, cpu, mem, gpu int) manifest.PodSpec {
	return manifest.PodSpec{
		Name:     name,
		Priority: priority,
		Containers: []manifest.ContainerSpec{
			{
				Name:  "main",
				Image: "test",
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{
						CPUMillis:   cpu,
						MemoryMB:    mem,
						GPUMemoryMB: gpu,
					},
				},
			},
		},
	}
}

func newTracker(cpu, mem, gpu int) *ResourceTracker {
	return NewResourceTracker(
		Resources{CPUMillis: cpu, MemoryMB: mem, GPUMemoryMB: gpu},
		Resources{},
	)
}

func TestSchedule_ResourcesAvailable(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	result := s.Schedule(podSpec("pod-a", 5, 1000, 2048, 4000))

	if result.Action != Scheduled {
		t.Fatalf("expected Scheduled, got %d", result.Action)
	}
	if len(result.Victims) != 0 {
		t.Fatalf("expected no victims, got %v", result.Victims)
	}
	// Verify resources were allocated.
	if _, ok := tracker.AllocatedBy("pod-a"); !ok {
		t.Fatal("expected pod-a to be allocated")
	}
}

func TestSchedule_FullNoPreemptionCandidates(t *testing.T) {
	tracker := newTracker(2000, 4096, 8000)
	s := NewScheduler(tracker)

	// Fill up with a high-priority pod (priority 0 = highest).
	s.Schedule(podSpec("pod-high", 0, 2000, 4096, 8000))
	s.AddPod(PodInfo{
		Name:      "pod-high",
		Priority:  0,
		Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
		StartTime: time.Now(),
	})

	// Try to schedule another high-priority pod — no victims available.
	result := s.Schedule(podSpec("pod-new", 0, 1000, 2048, 4000))

	if result.Action != Pending {
		t.Fatalf("expected Pending, got %d", result.Action)
	}
}

func TestSchedule_PreemptLowPriority(t *testing.T) {
	tracker := newTracker(2000, 4096, 8000)
	s := NewScheduler(tracker)

	// Fill with a low-priority pod (priority 10 = low).
	s.Schedule(podSpec("pod-low", 10, 2000, 4096, 8000))
	s.AddPod(PodInfo{
		Name:      "pod-low",
		Priority:  10,
		Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
		StartTime: time.Now(),
	})

	// Schedule a high-priority pod (priority 0).
	result := s.Schedule(podSpec("pod-critical", 0, 2000, 4096, 8000))

	if result.Action != Preempting {
		t.Fatalf("expected Preempting, got %d", result.Action)
	}
	if len(result.Victims) != 1 || result.Victims[0] != "pod-low" {
		t.Fatalf("expected victims [pod-low], got %v", result.Victims)
	}
	// Resources should NOT have been allocated (caller handles that).
	if _, ok := tracker.AllocatedBy("pod-critical"); ok {
		t.Fatal("expected pod-critical to NOT be allocated during preemption")
	}
}

func TestSchedule_EqualPriorityDoesNotPreempt(t *testing.T) {
	tracker := newTracker(2000, 4096, 8000)
	s := NewScheduler(tracker)

	s.Schedule(podSpec("pod-a", 5, 2000, 4096, 8000))
	s.AddPod(PodInfo{
		Name:      "pod-a",
		Priority:  5,
		Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
		StartTime: time.Now(),
	})

	result := s.Schedule(podSpec("pod-b", 5, 1000, 2048, 4000))

	if result.Action != Pending {
		t.Fatalf("expected Pending for equal priority, got %d", result.Action)
	}
}

func TestSchedule_VictimSelectionPrefersRecentlyStarted(t *testing.T) {
	tracker := newTracker(3000, 6144, 12000)
	s := NewScheduler(tracker)

	now := time.Now()

	// Fill with three low-priority pods started at different times.
	pods := []struct {
		name  string
		start time.Time
	}{
		{"pod-old", now.Add(-30 * time.Minute)},
		{"pod-mid", now.Add(-15 * time.Minute)},
		{"pod-new", now.Add(-1 * time.Minute)},
	}
	for _, p := range pods {
		s.Schedule(podSpec(p.name, 10, 1000, 2048, 4000))
		s.AddPod(PodInfo{
			Name:      p.name,
			Priority:  10,
			Resources: manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000},
			StartTime: p.start,
		})
	}

	// Need 2000 CPU — should pick the 2 most recently started pods.
	result := s.Schedule(podSpec("pod-critical", 0, 2000, 4096, 8000))

	if result.Action != Preempting {
		t.Fatalf("expected Preempting, got %d", result.Action)
	}
	if len(result.Victims) != 2 {
		t.Fatalf("expected 2 victims, got %d: %v", len(result.Victims), result.Victims)
	}
	// Most recently started should be first victim.
	if result.Victims[0] != "pod-new" {
		t.Fatalf("expected first victim pod-new, got %s", result.Victims[0])
	}
	if result.Victims[1] != "pod-mid" {
		t.Fatalf("expected second victim pod-mid, got %s", result.Victims[1])
	}
}

func TestSchedule_AntiThrash(t *testing.T) {
	tracker := newTracker(2000, 4096, 8000)
	s := NewScheduler(tracker)

	now := time.Now()
	s.now = func() time.Time { return now }

	// Add a low-priority pod.
	s.Schedule(podSpec("pod-low", 10, 2000, 4096, 8000))
	s.AddPod(PodInfo{
		Name:      "pod-low",
		Priority:  10,
		Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
		StartTime: now.Add(-10 * time.Minute),
	})

	// Simulate 4 preemptions within 5 minutes by recording them directly.
	for i := 0; i < 4; i++ {
		s.recordPreemption("pod-low", now.Add(-time.Duration(4-i)*time.Minute))
	}

	// Release and re-add to simulate it being rescheduled each time.
	// The pod is still running but has been preempted 4 times.

	// Try to preempt again — should be skipped due to anti-thrash.
	// First release resources so the new pod wouldn't just fit.
	// Actually the pod-low is still allocated, so new pod won't fit.
	result := s.Schedule(podSpec("pod-urgent", 0, 2000, 4096, 8000))

	if result.Action != Pending {
		t.Fatalf("expected Pending due to anti-thrash, got %d", result.Action)
	}

	// Advance time beyond 5 minutes — anti-thrash should expire.
	s.now = func() time.Time { return now.Add(6 * time.Minute) }

	result = s.Schedule(podSpec("pod-urgent2", 0, 2000, 4096, 8000))

	if result.Action != Preempting {
		t.Fatalf("expected Preempting after anti-thrash expiry, got %d", result.Action)
	}
}

func TestSchedule_MultipleVictimsNeeded(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	now := time.Now()

	// Add two small low-priority pods.
	for i, name := range []string{"pod-a", "pod-b"} {
		s.Schedule(podSpec(name, 10, 2000, 4096, 8000))
		s.AddPod(PodInfo{
			Name:      name,
			Priority:  10,
			Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
			StartTime: now.Add(-time.Duration(10-i) * time.Minute),
		})
	}

	// Need all resources — both victims required.
	result := s.Schedule(podSpec("pod-big", 0, 4000, 8192, 16000))

	if result.Action != Preempting {
		t.Fatalf("expected Preempting, got %d", result.Action)
	}
	if len(result.Victims) != 2 {
		t.Fatalf("expected 2 victims, got %d: %v", len(result.Victims), result.Victims)
	}
}
