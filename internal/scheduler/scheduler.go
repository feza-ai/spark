package scheduler

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

// ScheduleAction represents the outcome type of a scheduling attempt.
type ScheduleAction int

const (
	Scheduled  ScheduleAction = iota // pod fits, resources allocated
	Preempting                       // pod doesn't fit, but can preempt victims
	Pending                          // pod doesn't fit, no valid victims
)

// ScheduleResult represents the outcome of a scheduling attempt.
type ScheduleResult struct {
	Action  ScheduleAction
	Victims []string // pod names to preempt (only when Action == Preempting)
}

// PodInfo tracks a running pod's metadata for scheduling decisions.
type PodInfo struct {
	Name      string
	Priority  int
	Resources manifest.ResourceList
	StartTime time.Time
}

// preemptionRecord tracks when a pod was preempted for anti-thrash.
type preemptionRecord struct {
	times []time.Time
}

// Scheduler makes scheduling and preemption decisions.
type Scheduler struct {
	mu          sync.Mutex
	tracker     *ResourceTracker
	pods        map[string]PodInfo
	preemptions map[string]*preemptionRecord
	now         func() time.Time // injectable clock for testing

	scheduleAttempts int64 // atomic counter for Schedule() calls
	preemptionCount  int64 // atomic counter for executed preemptions
}

// NewScheduler creates a scheduler backed by a resource tracker.
func NewScheduler(tracker *ResourceTracker) *Scheduler {
	return &Scheduler{
		tracker:     tracker,
		pods:        make(map[string]PodInfo),
		preemptions: make(map[string]*preemptionRecord),
		now:         time.Now,
	}
}

// Schedule attempts to schedule a pod. Returns Scheduled if resources fit,
// Preempting with victim list if preemption is possible, or Pending otherwise.
func (s *Scheduler) Schedule(spec manifest.PodSpec) ScheduleResult {
	atomic.AddInt64(&s.scheduleAttempts, 1)

	s.mu.Lock()
	defer s.mu.Unlock()

	req := spec.TotalRequests()

	// Step 1: try to fit directly.
	if s.tracker.CanFit(req) {
		s.tracker.Allocate(spec.Name, req)
		return ScheduleResult{Action: Scheduled}
	}

	// Step 2: find preemption candidates — pods with strictly lower priority
	// (higher numeric value means lower priority).
	now := s.now()
	var candidates []PodInfo
	for _, pod := range s.pods {
		if pod.Priority <= spec.Priority {
			continue // equal or higher priority — skip
		}
		if s.isAntiThrashed(pod.Name, now) {
			continue
		}
		candidates = append(candidates, pod)
	}

	if len(candidates) == 0 {
		return ScheduleResult{Action: Pending}
	}

	// Step 3: sort candidates by StartTime descending (most recent first)
	// so we prefer evicting pods that have done the least work.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].StartTime.After(candidates[j].StartTime)
	})

	// Step 4: find minimum set of victims whose combined resources,
	// added to currently available resources, satisfy the request.
	avail := s.tracker.Available()
	freed := Resources{
		CPUMillis:   avail.CPUMillis,
		MemoryMB:    avail.MemoryMB,
		GPUCount:    avail.GPUCount,
		GPUMemoryMB: avail.GPUMemoryMB,
	}

	var victims []string
	for _, c := range candidates {
		if freed.CPUMillis >= req.CPUMillis &&
			freed.MemoryMB >= req.MemoryMB &&
			freed.GPUCount >= req.GPUCount &&
			freed.GPUMemoryMB >= req.GPUMemoryMB {
			break
		}
		victims = append(victims, c.Name)
		freed.CPUMillis += c.Resources.CPUMillis
		freed.MemoryMB += c.Resources.MemoryMB
		freed.GPUCount += c.Resources.GPUCount
		freed.GPUMemoryMB += c.Resources.GPUMemoryMB
	}

	// Check if we freed enough.
	if freed.CPUMillis >= req.CPUMillis &&
		freed.MemoryMB >= req.MemoryMB &&
		freed.GPUCount >= req.GPUCount &&
		freed.GPUMemoryMB >= req.GPUMemoryMB {
		// Record preemption events for anti-thrash tracking.
		for _, v := range victims {
			s.recordPreemption(v, now)
		}
		return ScheduleResult{Action: Preempting, Victims: victims}
	}

	return ScheduleResult{Action: Pending}
}

// AddPod registers a running pod for preemption candidacy.
func (s *Scheduler) AddPod(info PodInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pods[info.Name] = info
}

// RemovePod unregisters a pod (after it exits or is preempted).
func (s *Scheduler) RemovePod(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracker.Release(name)
	delete(s.pods, name)
}

// isAntiThrashed returns true if a pod has been preempted more than 3 times
// in the last 5 minutes.
func (s *Scheduler) isAntiThrashed(name string, now time.Time) bool {
	rec, ok := s.preemptions[name]
	if !ok {
		return false
	}
	cutoff := now.Add(-5 * time.Minute)
	count := 0
	for _, t := range rec.times {
		if !t.Before(cutoff) {
			count++
		}
	}
	return count > 3
}

// ScheduleAttempts returns the total number of Schedule() calls.
func (s *Scheduler) ScheduleAttempts() int64 {
	return atomic.LoadInt64(&s.scheduleAttempts)
}

// PreemptionCount returns the total number of executed preemptions.
func (s *Scheduler) PreemptionCount() int64 {
	return atomic.LoadInt64(&s.preemptionCount)
}

// recordPreemption records a preemption event for anti-thrash tracking.
func (s *Scheduler) recordPreemption(name string, now time.Time) {
	rec, ok := s.preemptions[name]
	if !ok {
		rec = &preemptionRecord{}
		s.preemptions[name] = rec
	}
	rec.times = append(rec.times, now)

	// Prune old entries beyond 5 minutes.
	cutoff := now.Add(-5 * time.Minute)
	kept := rec.times[:0]
	for _, t := range rec.times {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	rec.times = kept
}
