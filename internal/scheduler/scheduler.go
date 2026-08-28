package scheduler

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

// describeShortfall returns a human-readable summary of which resource
// dimensions in req exceed avail. When cpusetEnabled is true and the
// request is for whole cores (CPUMillis a positive multiple of 1000),
// the cpuset core-block constraint is also reported when fewer
// unassignedCores are free than requested. Returned string is never empty
// when the request does not fit.
func describeShortfall(req manifest.ResourceList, avail Resources, cpusetEnabled bool, unassignedCores int) string {
	var parts []string
	if req.CPUMillis > avail.CPUMillis {
		parts = append(parts, fmt.Sprintf("cpu %dm > %dm free", req.CPUMillis, avail.CPUMillis))
	}
	if req.MemoryMB > avail.MemoryMB {
		parts = append(parts, fmt.Sprintf("memory %dMB > %dMB free", req.MemoryMB, avail.MemoryMB))
	}
	if req.GPUCount > avail.GPUCount {
		parts = append(parts, fmt.Sprintf("gpu %d > %d free", req.GPUCount, avail.GPUCount))
	}
	if req.GPUMemoryMB > avail.GPUMemoryMB {
		parts = append(parts, fmt.Sprintf("gpu-memory %dMB > %dMB free", req.GPUMemoryMB, avail.GPUMemoryMB))
	}
	if cpusetEnabled && req.CPUMillis >= 1000 && req.CPUMillis%1000 == 0 {
		needed := req.CPUMillis / 1000
		if unassignedCores < needed {
			parts = append(parts, fmt.Sprintf("cpuset cores: need %d unassigned, %d free", needed, unassignedCores))
		}
	}
	if len(parts) == 0 {
		return "resources unavailable"
	}
	return strings.Join(parts, ", ")
}

// HostLoadSource reports live host CPU headroom, independent of resource
// accounting, for utilization-aware admission (issue #76). Implementations
// sample real load (e.g. /proc/loadavg) on their own schedule; Schedule
// only reads the latest sample and never blocks on it. ok is false when no
// sample is available yet (e.g. during startup), in which case
// utilization-aware admission is skipped for that call and behavior falls
// back to pure accounting.
type HostLoadSource interface {
	AvailableCPUMillis() (millis int, ok bool)
}

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
	// Reason is a human-readable explanation of why the scheduler picked
	// this Action. Always populated for Pending (e.g. "no node has 1 free
	// GPU", "preemption candidate set empty"); optional for Scheduled and
	// Preempting. Watchdogs in the reconciler quote this verbatim.
	Reason string
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

// antiThrashMaxPreemptions and antiThrashWindow bound how many times a
// single pending pod may preempt the same victim within a rolling window
// before that (victim, requester) pair is excluded from further preemption
// (issue #79, ADR 005's thrash mitigation). The cap is scoped per requester
// pod, not per victim globally: it exists to stop ONE pod that keeps
// failing and re-triggering from flip-flopping with the SAME victim
// (ADR 005's "high-priority pod preempts, fails, low-priority restarts,
// gets preempted again"). It must not also block a different, unrelated
// pending pod from preempting that victim just because some other pod's
// retries already used up the count — that starves the node's entire
// small victim pool for every future high-priority pod, not just the one
// that caused the thrashing.
const (
	antiThrashMaxPreemptions = 3
	antiThrashWindow         = 5 * time.Minute
)

// preemptionKey identifies a single (victim, requester) pair for anti-thrash
// tracking. Composed rather than nested-mapped for a simpler zero value and
// lookup.
func preemptionKey(victim, requester string) string {
	return victim + "\x00" + requester
}

// Scheduler makes scheduling and preemption decisions.
type Scheduler struct {
	mu          sync.Mutex
	tracker     *ResourceTracker
	pods        map[string]PodInfo
	preemptions map[string]*preemptionRecord
	now         func() time.Time // injectable clock for testing
	hostLoad    HostLoadSource   // optional; nil disables utilization-aware admission (issue #76)

	scheduleAttempts        int64 // atomic counter for Schedule() calls
	preemptionCount         int64 // atomic counter for executed preemptions
	cpuOvercommitAdmissions int64 // atomic counter for utilization-aware CPU admissions (issue #76)
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

// AssignedCores proxies to the underlying tracker so callers that only hold
// a *Scheduler (e.g. the reconciler) can read per-pod core assignments.
func (s *Scheduler) AssignedCores(name string) []int {
	return s.tracker.AssignedCores(name)
}

// SetHostLoad wires a live CPU-headroom source for utilization-aware
// admission (issue #76). Pass nil (the default) to disable the feature:
// Schedule then behaves exactly as it did before this existed, admitting
// purely on requested-resource accounting.
func (s *Scheduler) SetHostLoad(src HostLoadSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostLoad = src
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

	// Step 1b: utilization-aware CPU admission (issue #76). Accounted CPU
	// reservations can starve scheduling even when the host is mostly
	// idle (idle-but-reserved CI runners, etc.). When every other
	// dimension fits by strict accounting and live host load shows real
	// CPU headroom, admit despite the phantom CPU ceiling. This never
	// applies to memory, GPU count/memory, or cpuset core blocks — those
	// stay exactly as strict as the Step 1 check above.
	if s.hostLoad != nil && s.tracker.CanFitIgnoringCPU(req) {
		if freeMillis, ok := s.hostLoad.AvailableCPUMillis(); ok && freeMillis >= req.CPUMillis {
			if err := s.tracker.AllocateOverCommittingCPU(spec.Name, req); err == nil {
				atomic.AddInt64(&s.cpuOvercommitAdmissions, 1)
				return ScheduleResult{
					Action: Scheduled,
					Reason: fmt.Sprintf("admitted via utilization-aware CPU overcommit: accounted CPU short but %dm real headroom available", freeMillis),
				}
			}
		}
	}

	// Step 2: find preemption candidates — pods with strictly lower priority
	// (higher numeric value means lower priority).
	now := s.now()
	var candidates []PodInfo
	thrashExcluded := 0
	for _, pod := range s.pods {
		if pod.Priority <= spec.Priority {
			continue // equal or higher priority — skip
		}
		if s.isAntiThrashed(pod.Name, spec.Name, now) {
			thrashExcluded++
			continue
		}
		candidates = append(candidates, pod)
	}

	cpusetOn := s.tracker.CoresEnabled()
	freeCores := s.tracker.UnassignedCoreCount()
	shortfall := describeShortfall(req, s.tracker.Available(), cpusetOn, freeCores)

	if len(candidates) == 0 {
		reason := "no preemption candidates (lower-priority pods); shortfall: " + shortfall
		if thrashExcluded > 0 {
			reason = fmt.Sprintf(
				"no preemption candidates: %d lower-priority pod(s) excluded by the anti-thrash cap (already preempted by this pod %d+ times in the last %s); shortfall: %s",
				thrashExcluded, antiThrashMaxPreemptions, antiThrashWindow, shortfall)
		}
		return ScheduleResult{Action: Pending, Reason: reason}
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
			s.recordPreemption(v, spec.Name, now)
		}
		return ScheduleResult{Action: Preempting, Victims: victims}
	}

	reason := fmt.Sprintf("preemption insufficient: even after evicting %d candidate(s), shortfall remains: %s",
		len(candidates), describeShortfall(req, Resources{
			CPUMillis:   freed.CPUMillis,
			MemoryMB:    freed.MemoryMB,
			GPUCount:    freed.GPUCount,
			GPUMemoryMB: freed.GPUMemoryMB,
		}, cpusetOn, freeCores))
	if thrashExcluded > 0 {
		reason = fmt.Sprintf("%s (%d additional lower-priority pod(s) excluded by the anti-thrash cap for this pod, not by availability)",
			reason, thrashExcluded)
	}
	return ScheduleResult{Action: Pending, Reason: reason}
}

// AddPod registers a running pod for preemption candidacy.
func (s *Scheduler) AddPod(info PodInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pods[info.Name] = info
}

// AdoptPod registers a pod that is already running — after a control-plane
// restart — for both preemption candidacy and the resource ledger. Unlike
// Schedule, adoption never rejects: the pod is running whether or not the
// ledger has room, and leaving it unrecorded would let admission hand out
// the same resources twice (issues #53, #43). Idempotent: a pod that
// already holds an allocation is left untouched.
func (s *Scheduler) AdoptPod(info PodInfo) {
	s.mu.Lock()
	s.pods[info.Name] = info
	s.mu.Unlock()

	if _, held := s.tracker.AllocatedBy(info.Name); held {
		return
	}
	if err := s.tracker.Allocate(info.Name, info.Resources); err != nil {
		slog.Warn("adopting running pod despite ledger capacity error",
			"pod", info.Name, "err", err)
		s.tracker.ForceAllocate(info.Name, info.Resources)
	}
}

// RemovePod unregisters a pod (after it exits or is preempted).
func (s *Scheduler) RemovePod(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracker.Release(name)
	delete(s.pods, name)
}

// isAntiThrashed returns true if requester has preempted victim more than
// antiThrashMaxPreemptions times in the last antiThrashWindow. Scoped to the
// (victim, requester) pair rather than the victim alone: a victim already
// thrashed by one requester stays eligible for a different requester that
// has not itself contributed to that thrashing (issue #79).
func (s *Scheduler) isAntiThrashed(victim, requester string, now time.Time) bool {
	rec, ok := s.preemptions[preemptionKey(victim, requester)]
	if !ok {
		return false
	}
	cutoff := now.Add(-antiThrashWindow)
	count := 0
	for _, t := range rec.times {
		if !t.Before(cutoff) {
			count++
		}
	}
	return count > antiThrashMaxPreemptions
}

// Tracker returns the underlying ResourceTracker. Callers outside the
// scheduler package (e.g. the reconciler) use it to read assignment state
// such as AssignedCores after a successful Schedule().
func (s *Scheduler) Tracker() *ResourceTracker {
	return s.tracker
}

// ScheduleAttempts returns the total number of Schedule() calls.
func (s *Scheduler) ScheduleAttempts() int64 {
	return atomic.LoadInt64(&s.scheduleAttempts)
}

// PreemptionCount returns the total number of executed preemptions.
func (s *Scheduler) PreemptionCount() int64 {
	return atomic.LoadInt64(&s.preemptionCount)
}

// CPUOvercommitAdmissions returns the total number of pods admitted via
// utilization-aware CPU overcommit (issue #76) — i.e. admitted despite
// exceeding accounted CPU headroom because live host load showed real
// headroom. Always 0 when no HostLoadSource is set.
func (s *Scheduler) CPUOvercommitAdmissions() int64 {
	return atomic.LoadInt64(&s.cpuOvercommitAdmissions)
}

// recordPreemption records a preemption event for anti-thrash tracking,
// scoped to the (victim, requester) pair — see isAntiThrashed.
func (s *Scheduler) recordPreemption(victim, requester string, now time.Time) {
	key := preemptionKey(victim, requester)
	rec, ok := s.preemptions[key]
	if !ok {
		rec = &preemptionRecord{}
		s.preemptions[key] = rec
	}
	rec.times = append(rec.times, now)

	// Prune old entries beyond the anti-thrash window.
	cutoff := now.Add(-antiThrashWindow)
	kept := rec.times[:0]
	for _, t := range rec.times {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	rec.times = kept
}
