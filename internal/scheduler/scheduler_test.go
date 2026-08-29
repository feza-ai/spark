package scheduler

import (
	"context"
	"strings"
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
		nil, 0,
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

// TestSchedule_CpusetShortfallReason covers FU1.3b from Issue #32. When
// cpuset pinning is on and the only constraint that blocks scheduling is
// the contiguous-core block (millis-level resources fit), the Pending
// reason must name the cpuset shortfall instead of the useless
// "resources unavailable".
func TestSchedule_CpusetShortfallReason(t *testing.T) {
	// 8 pinnable cores, plenty of memory, no GPU. Construct the tracker
	// the supported way: pass total.Cores so coreAssignments is initialised.
	tracker := NewResourceTracker(
		Resources{CPUMillis: 8000, MemoryMB: 32768, GPUMemoryMB: 0, Cores: []int{0, 1, 2, 3, 4, 5, 6, 7}},
		Resources{},
		nil, 0,
	)
	s := NewScheduler(tracker)

	// Allocate a 5-core hog so 5 cores are pinned and 3 remain free.
	if err := tracker.Allocate("hog", manifest.ResourceList{CPUMillis: 5000, MemoryMB: 4096}); err != nil {
		t.Fatalf("allocate hog: %v", err)
	}
	s.AddPod(PodInfo{
		Name:      "hog",
		Priority:  0, // highest priority — never a preemption candidate
		Resources: manifest.ResourceList{CPUMillis: 5000, MemoryMB: 4096},
		StartTime: time.Now(),
	})

	// Manually shrink the hog's millis claim to 3000m without releasing
	// cores. Millis-available is now 8000-3000=5000m — a 4-core (4000m)
	// request fits the millis check but cannot find 4 unassigned cores
	// (only 3 are free), so CanFit fails on the cpuset block. This is the
	// FU1.3b shape: millis fit, cpuset doesn't.
	tracker.allocations["hog"] = manifest.ResourceList{CPUMillis: 3000, MemoryMB: 4096}

	result := s.Schedule(podSpec("need-4-cores", 100, 4000, 1024, 0))

	if result.Action != Pending {
		t.Fatalf("expected Pending, got action=%d reason=%q", result.Action, result.Reason)
	}
	if result.Reason == "" {
		t.Fatal("expected non-empty Reason")
	}
	wantSub := "cpuset cores: need 4 unassigned, 3 free"
	if !strings.Contains(result.Reason, wantSub) {
		t.Fatalf("Reason missing cpuset shortfall: got %q, want substring %q", result.Reason, wantSub)
	}
}

// TestDescribeShortfall_NeverShowsNegativeFree covers issue #80's exact
// reported symptom: "gpu 0 > -1 free" in a Pending reason, from a pod that
// doesn't even request a GPU (req.GPUCount == 0), because Available() had
// gone negative. Available() now floors non-CPU dimensions at 0 (see
// TestAvailable_FloorsGPUAndMemoryAtZero), so this constructs the
// pre-fix-shaped input directly against describeShortfall to also pin the
// display side: even a hand-built negative avail must never surface as a
// negative "free" figure, and a zero request against zero (floored) free
// must not be reported as a shortfall at all.
func TestDescribeShortfall_NeverShowsNegativeFree(t *testing.T) {
	req := manifest.ResourceList{CPUMillis: 6000, GPUCount: 0}
	avail := Resources{CPUMillis: 5700, GPUCount: 0} // already floored, as Available() now guarantees

	got := describeShortfall(req, avail, false, 0)

	if strings.Contains(got, "gpu 0 > -1 free") {
		t.Fatalf("describeShortfall reproduced issue #80's exact bug: %q", got)
	}
	if strings.Contains(got, "> -") {
		t.Fatalf("describeShortfall displayed a negative free value: %q", got)
	}
	if strings.Contains(got, "gpu") {
		t.Fatalf("describeShortfall flagged a GPU shortfall for a pod that requested 0 GPUs: %q", got)
	}
	wantSub := "cpu 6000m > 5700m free"
	if !strings.Contains(got, wantSub) {
		t.Fatalf("describeShortfall = %q, want substring %q", got, wantSub)
	}
}

// TestSchedule_Issue80ExactScenario reproduces the full event sequence from
// issue #80: a restarted pod's stale GPU allocation survives on the ledger
// (ForceAllocate, the restart-adoption path) alongside its live
// replacement, over-counting GPUCount past allocatable. A second, unrelated
// pod that requests CPU but no GPU must be reported Pending with an honest
// CPU shortfall and no negative or spurious GPU figure.
func TestSchedule_Issue80ExactScenario(t *testing.T) {
	tracker := NewResourceTracker(
		Resources{CPUMillis: 6000, MemoryMB: 16384, GPUCount: 1, GPUMemoryMB: 16384},
		Resources{},
		[]int{0}, 1,
	)
	s := NewScheduler(tracker)

	// Two GPU allocations force-recorded for the same logical pod across a
	// crash-restart (the old one never released before the new one landed).
	tracker.ForceAllocate("runner-old", manifest.ResourceList{CPUMillis: 300, MemoryMB: 1024, GPUCount: 1, GPUMemoryMB: 8192})
	tracker.ForceAllocate("runner-new", manifest.ResourceList{CPUMillis: 300, MemoryMB: 1024, GPUCount: 1, GPUMemoryMB: 8192})

	result := s.Schedule(podSpec("cpu-only-pod", 100, 6000, 1024, 0))

	if result.Action != Pending {
		t.Fatalf("expected Pending, got action=%d reason=%q", result.Action, result.Reason)
	}
	if strings.Contains(result.Reason, "gpu") {
		t.Fatalf("Reason wrongly names a GPU shortfall for a pod requesting no GPU: %q", result.Reason)
	}
	if strings.Contains(result.Reason, "> -") {
		t.Fatalf("Reason displays a negative free value: %q", result.Reason)
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

	// Simulate 4 preemptions within 5 minutes by recording them directly,
	// all attributed to the SAME requester ("pod-urgent") repeatedly
	// re-preempting the same victim — the flip-flop scenario ADR 005's
	// anti-thrash mitigation targets.
	for i := 0; i < 4; i++ {
		s.recordPreemption("pod-low", "pod-urgent", now.Add(-time.Duration(4-i)*time.Minute))
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

	// Advance time beyond 5 minutes — anti-thrash should expire. Reuse the
	// SAME requester name ("pod-urgent") so this asserts window expiry,
	// not the (victim, requester) pair-scoping added for issue #79.
	s.now = func() time.Time { return now.Add(6 * time.Minute) }

	result = s.Schedule(podSpec("pod-urgent", 0, 2000, 4096, 8000))

	if result.Action != Preempting {
		t.Fatalf("expected Preempting after anti-thrash expiry, got %d", result.Action)
	}
}

// TestSchedule_AntiThrashStarvesUnrelatedRequester reproduces issue #79: a
// small pool of lower-priority victims that some EARLIER, unrelated
// high-priority pod already cycled through the anti-thrash cap (>3
// preemptions in 5 minutes each) becomes permanently ineligible for ANY
// preemption, even by a brand-new high-priority pod that has never preempted
// anyone. On a resource-constrained single-GPU node with few low-priority
// pods, a burst of legitimate preemption activity from a stream of
// DIFFERENT pending pods exhausts each victim's shared budget, after which
// every subsequent high-priority pod is silently starved for up to the
// anti-thrash window — even though evicting the very same victims again
// would satisfy it. ADR 005's anti-thrash mitigation is about a SINGLE pod
// repeatedly flip-flopping with the SAME victim (TestSchedule_AntiThrash
// covers that); it was never meant to block unrelated pods that have not
// caused any thrash themselves.
func TestSchedule_AntiThrashStarvesUnrelatedRequester(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	now := time.Now()
	s.now = func() time.Time { return now }

	// 4 lower-priority victims — more than the "3" in the anti-thrash
	// threshold — each already preempted 4 times within the last 5
	// minutes by an earlier, unrelated high-priority pod's retry loop.
	victims := []string{"victim-a", "victim-b", "victim-c", "victim-d"}
	for i, name := range victims {
		s.AddPod(PodInfo{
			Name:      name,
			Priority:  10,
			Resources: manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000},
			StartTime: now.Add(-time.Duration(10-i) * time.Minute),
		})
		for j := 0; j < 4; j++ {
			s.recordPreemption(name, "pod-old-requester", now.Add(-time.Duration(4-j)*time.Minute))
		}
	}
	// Victims fully occupy the node.
	for _, name := range victims {
		if err := tracker.Allocate(name, manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000}); err != nil {
			t.Fatalf("allocate %s: %v", name, err)
		}
	}

	// A brand-new high-priority pod, unrelated to whatever caused the
	// victims' thrash history, needs exactly the resources all 4 victims
	// hold.
	result := s.Schedule(podSpec("pod-urgent-new", 0, 4000, 8192, 16000))

	if result.Action != Preempting {
		t.Fatalf("expected a fresh requester to preempt thrashed victims it never preempted itself, got action=%d reason=%q", result.Action, result.Reason)
	}
	if len(result.Victims) != 4 {
		t.Fatalf("expected all 4 victims, got %v", result.Victims)
	}
}

// TestSchedule_PreemptionFairness_Issue79ExactScenario walks the exact
// issue #79 shape end to end: a "high" priority pod (priorityClassName:
// high) repeatedly preempts a pool of 4 "normal" priority pods (more than
// the anti-thrash cap of 3) that restart immediately each time, exactly as
// ADR 005 describes it and as the reconciler's Schedule/Preempt/AddPod loop
// drives it in production. It asserts two things T5.2 must both hold:
//  1. The SAME pod retrying against the SAME victims a 5th time is still
//     correctly capped (ADR 005's flip-flop protection is not weakened),
//     and the Pending reason names the anti-thrash cap explicitly instead
//     of a generic "no preemption candidates" or "evicting N candidates".
//  2. A DIFFERENT, brand-new high-priority pod queued moments later is not
//     starved by the first pod's exhausted budget against the same victims
//     -- issue #79's reported defect.
func TestSchedule_PreemptionFairness_Issue79ExactScenario(t *testing.T) {
	tracker := newTracker(4000, 8192, 16000)
	s := NewScheduler(tracker)

	now := time.Now()
	s.now = func() time.Time { return now }

	const highPriority = 100    // priorityClassName: high
	const normalPriority = 1000 // priorityClassName: normal (default)

	victims := []string{"ci-runner-a", "ci-runner-b", "ci-runner-c", "ci-runner-d"}
	addAndAllocateVictims := func(startOffset time.Duration) {
		for i, name := range victims {
			s.AddPod(PodInfo{
				Name:      name,
				Priority:  normalPriority,
				Resources: manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000},
				StartTime: now.Add(startOffset + time.Duration(i)*time.Second),
			})
			if err := tracker.Allocate(name, manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4000}); err != nil {
				t.Fatalf("allocate %s: %v", name, err)
			}
		}
	}
	addAndAllocateVictims(-10 * time.Minute)

	highSpec := podSpec("ltx-render", highPriority, 4000, 8192, 16000)

	// This same render job legitimately preempts the same 4-pod pool 4
	// times in a row -- the "max 3 preemptions per pod" ceiling from the
	// issue's ltx-render.yaml comment. The 4th preemption tips each
	// victim's count to "more than 3".
	for round := 0; round < 4; round++ {
		result := s.Schedule(highSpec)
		if result.Action != Preempting {
			t.Fatalf("round %d: expected Preempting, got %d reason=%q", round, result.Action, result.Reason)
		}
		if len(result.Victims) != 4 {
			t.Fatalf("round %d: expected all 4 victims, got %v", round, result.Victims)
		}
		for _, v := range result.Victims {
			s.RemovePod(v)
		}
		// The CI runners restart immediately, as issue #79 describes
		// (all resident pods were genuinely busy and kept getting
		// rescheduled).
		addAndAllocateVictims(time.Duration(round) * time.Second)
	}

	// 5th attempt by the SAME render job: correctly still capped -- this
	// is the flip-flop scenario ADR 005's anti-thrash cap exists for, not
	// the bug. The event message must name the cap, not just say
	// "no preemption candidates" as if nothing were available.
	result := s.Schedule(highSpec)
	if result.Action != Pending {
		t.Fatalf("expected the 5th same-pod retry to stay capped, got %d", result.Action)
	}
	if !strings.Contains(result.Reason, "anti-thrash cap") {
		t.Fatalf("expected Reason to name the anti-thrash cap, got %q", result.Reason)
	}

	// A DIFFERENT, brand-new high-priority pod queued moments later (a
	// second, unrelated render job) must schedule despite ltx-render's
	// exhausted budget against the same victims -- issue #79's actual
	// starvation defect.
	otherHighSpec := podSpec("ltx-render-2", highPriority, 4000, 8192, 16000)
	result = s.Schedule(otherHighSpec)
	if result.Action != Preempting {
		t.Fatalf("expected an unrelated high-priority pod to preempt despite ltx-render's exhausted budget, got %d reason=%q", result.Action, result.Reason)
	}
}

func TestRemovePod_ReleasesResources(t *testing.T) {
	tracker := newTracker(2000, 4096, 8000)
	s := NewScheduler(tracker)

	spec := podSpec("pod-a", 5, 2000, 4096, 8000)
	result := s.Schedule(spec)
	if result.Action != Scheduled {
		t.Fatalf("expected Scheduled, got %d", result.Action)
	}
	s.AddPod(PodInfo{
		Name:      "pod-a",
		Priority:  5,
		Resources: manifest.ResourceList{CPUMillis: 2000, MemoryMB: 4096, GPUMemoryMB: 8000},
		StartTime: time.Now(),
	})

	// Resources are fully consumed — same request should not fit.
	if tracker.CanFit(spec.TotalRequests()) {
		t.Fatal("expected resources to be fully consumed")
	}

	// Remove the pod — resources should be released.
	s.RemovePod("pod-a")

	// Same resource request should now fit.
	if !tracker.CanFit(spec.TotalRequests()) {
		t.Fatal("expected resources to be available after RemovePod")
	}

	// Verify pod is no longer tracked.
	if _, ok := tracker.AllocatedBy("pod-a"); ok {
		t.Fatal("expected pod-a allocation to be released")
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

func TestScheduleAttempts(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
	}{
		{"single attempt", 1},
		{"five attempts", 5},
		{"ten attempts", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTracker(100000, 100000, 100000)
			s := NewScheduler(tracker)

			for i := 0; i < tt.attempts; i++ {
				s.Schedule(podSpec("pod", 5, 1, 1, 1))
			}

			if got := s.ScheduleAttempts(); got != int64(tt.attempts) {
				t.Fatalf("expected %d schedule attempts, got %d", tt.attempts, got)
			}
		})
	}
}

func TestPreemptionCount(t *testing.T) {
	noopStop := func(ctx context.Context, podName string, gracePeriod int) error {
		return nil
	}

	tests := []struct {
		name           string
		victimCount    int
		wantPreemption int64
	}{
		{"single preemption", 1, 1},
		{"two preemptions", 2, 2},
		{"three preemptions", 3, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTracker(100000, 100000, 100000)
			s := NewScheduler(tracker)

			// Register victim pods.
			victims := make([]string, tt.victimCount)
			podStates := make(map[string]PodInfo)
			for i := 0; i < tt.victimCount; i++ {
				name := "victim-" + string(rune('a'+i))
				victims[i] = name
				tracker.Allocate(name, manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 100})
				info := PodInfo{
					Name:      name,
					Priority:  10,
					Resources: manifest.ResourceList{CPUMillis: 100, MemoryMB: 100, GPUMemoryMB: 100},
					StartTime: time.Now(),
				}
				s.AddPod(info)
				podStates[name] = info
			}

			_, err := s.Preempt(context.Background(), victims, podStates, noopStop, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := s.PreemptionCount(); got != tt.wantPreemption {
				t.Fatalf("expected %d preemptions, got %d", tt.wantPreemption, got)
			}
		})
	}
}

// fakeHostLoad is a test double for HostLoadSource with a fixed reading.
type fakeHostLoad struct {
	millis int
	ok     bool
}

func (f fakeHostLoad) AvailableCPUMillis() (int, bool) { return f.millis, f.ok }

// TestSchedule_UtilizationAwareAdmission_AdmitsOnRealHeadroom reproduces
// the issue #76 incident: a node whose accounting shows almost no CPU
// free (idle reservations, e.g. mostly-idle CI runner pods) but whose real
// load average shows ample headroom. Without a HostLoadSource, this
// request is rejected (Pending) purely on phantom accounting; with one
// reporting real headroom, it must be admitted despite the accounted
// shortfall, and no priority-based preemption should be attempted.
func TestSchedule_UtilizationAwareAdmission_AdmitsOnRealHeadroom(t *testing.T) {
	// 20-core node accounted at 17150m/18000m allocated (matches the real
	// incident numbers from issue #76), same priority so nothing is a
	// preemption candidate.
	tracker := newTracker(18000, 65536, 0)
	if err := tracker.Allocate("ci-runner-holder", manifest.ResourceList{CPUMillis: 17150, MemoryMB: 4096}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := NewScheduler(tracker)
	s.AddPod(PodInfo{Name: "ci-runner-holder", Priority: 1000, Resources: manifest.ResourceList{CPUMillis: 17150, MemoryMB: 4096}, StartTime: time.Now()})

	req := podSpec("ci-job", 1000, 6000, 2048, 0) // cpu: 6 like the real incident

	// Without utilization awareness (today's behavior): rejected, even
	// though the host is nearly idle in reality.
	before := s.Schedule(req)
	if before.Action != Pending {
		t.Fatalf("expected Pending without a HostLoadSource (phantom accounting ceiling), got %d: %s", before.Action, before.Reason)
	}

	// With a HostLoadSource reporting ample real CPU headroom (load
	// average near 0 on a 20-core box): must admit.
	s.SetHostLoad(fakeHostLoad{millis: 19000, ok: true})
	result := s.Schedule(req)
	if result.Action != Scheduled {
		t.Fatalf("expected Scheduled once real headroom is available, got %d: %s", result.Action, result.Reason)
	}
	if len(result.Victims) != 0 {
		t.Fatalf("expected no preemption when real headroom covers the request, got victims %v", result.Victims)
	}
	if result.Reason == "" {
		t.Fatal("expected a non-empty Reason explaining the utilization-aware admission")
	}
	if got := s.CPUOvercommitAdmissions(); got != 1 {
		t.Fatalf("expected CPUOvercommitAdmissions to be 1, got %d", got)
	}

	avail := tracker.Available()
	if avail.CPUMillis >= 0 {
		t.Errorf("expected accounted CPU to go negative reflecting the overcommit, got %d", avail.CPUMillis)
	}
}

// TestSchedule_UtilizationAwareAdmission_NeverBypassesMemory covers the
// hard requirement from issue #76: even with abundant reported CPU
// headroom, a request that doesn't fit memory must never be admitted.
func TestSchedule_UtilizationAwareAdmission_NeverBypassesMemory(t *testing.T) {
	tracker := newTracker(4000, 1024, 0)
	if err := tracker.Allocate("holder", manifest.ResourceList{CPUMillis: 3500, MemoryMB: 900}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := NewScheduler(tracker)
	s.AddPod(PodInfo{Name: "holder", Priority: 1000, Resources: manifest.ResourceList{CPUMillis: 3500, MemoryMB: 900}, StartTime: time.Now()})
	s.SetHostLoad(fakeHostLoad{millis: 10000, ok: true})

	// Fits CPU via overcommit, but memory does not fit (only 124MB free).
	result := s.Schedule(podSpec("memory-heavy", 1000, 1000, 500, 0))
	if result.Action == Scheduled {
		t.Fatalf("expected memory shortfall to block admission despite CPU headroom, got Scheduled: %s", result.Reason)
	}
}

// TestSchedule_UtilizationAwareAdmission_NoSampleFallsBackToPending covers
// the "ok=false" case: when the HostLoadSource has no sample yet, the
// scheduler must not admit and must fall back to today's behavior.
func TestSchedule_UtilizationAwareAdmission_NoSampleFallsBackToPending(t *testing.T) {
	tracker := newTracker(4000, 8192, 0)
	if err := tracker.Allocate("holder", manifest.ResourceList{CPUMillis: 3500, MemoryMB: 512}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := NewScheduler(tracker)
	s.AddPod(PodInfo{Name: "holder", Priority: 1000, Resources: manifest.ResourceList{CPUMillis: 3500, MemoryMB: 512}, StartTime: time.Now()})
	s.SetHostLoad(fakeHostLoad{millis: 0, ok: false})

	result := s.Schedule(podSpec("job", 1000, 1000, 512, 0))
	if result.Action != Pending {
		t.Fatalf("expected Pending when HostLoadSource has no sample yet, got %d", result.Action)
	}
}
