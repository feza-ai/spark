package state

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrPodNotFound  = errors.New("pod not found")
	ErrNotOnDemand  = errors.New("pod is not an on-demand service")
	ErrRequestLimit = errors.New("workload request limit reached")
	ErrUnavailable  = errors.New("workload unavailable")
)

// AcquireRequest pins the workload through queueing and the entire response,
// including streams/upgraded connections. It shares the eviction lock so a
// request either prevents eviction or waits for the next activation.
func (s *PodStore) AcquireRequest(name string) (PodRecord, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pods[name]
	if !ok {
		return PodRecord{}, nil, ErrPodNotFound
	}
	if rec.Spec.OnDemand == nil {
		return PodRecord{}, nil, ErrNotOnDemand
	}
	if s.readOnly.Load() || rec.Status == StatusFailed || rec.Status == StatusCompleted {
		return PodRecord{}, nil, ErrUnavailable
	}
	if rec.InFlight >= rec.Spec.OnDemand.MaxRequests {
		return PodRecord{}, nil, ErrRequestLimit
	}
	rec.InFlight++
	rec.LastActivity = time.Now()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// An old request must never decrement a replacement pod's counter.
			if s.pods[name] == rec {
				rec.InFlight--
				rec.LastActivity = time.Now()
			}
		})
	}
	cp := *rec
	cp.Events = nil
	return cp, release, nil
}

// TryIdleStop atomically commits eviction only when no request holds the pod.
// Stopping is durable and retried until the executor confirms removal.
func (s *PodStore) TryIdleStop(name string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pods[name]
	if !ok || rec.Spec.OnDemand == nil || rec.Spec.OnDemand.AlwaysWarm || rec.InFlight != 0 {
		return false
	}
	if rec.Status != StatusRunning && rec.Status != StatusPending && rec.Status != StatusPreempted {
		return false
	}
	if rec.Spec.OnDemand.ActivationMode() == "github" {
		if rec.QueueCheckedAt.IsZero() || now.Sub(rec.QueueCheckedAt) > 2*time.Minute || rec.QueueDemand {
			return false
		}
		// A runner listener may have accepted a job that REST has not reported yet.
		// Only its own one-job process exit is authoritative proof it can retire.
		if rec.Status == StatusRunning && !rec.WorkerRetired {
			return false
		}
	}
	if rec.LastActivity.IsZero() {
		rec.LastActivity = now
		return false
	}
	if now.Sub(rec.LastActivity) < rec.Spec.OnDemand.IdleTimeout {
		return false
	}
	rec.Status = StatusStopping
	rec.Ready = false
	return true
}

// SetReady updates runtime readiness; readiness is always rechecked on restart.
func (s *PodStore) SetReady(name string, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.pods[name]; ok && rec.Status == StatusRunning {
		rec.Ready = ready
	}
}

// SetQueueDemand publishes a successful queue observation. A zero timestamp
// marks the source unavailable and prevents eviction based on stale data.
func (s *PodStore) SetQueueDemand(name string, demand bool, checked time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.pods[name]; ok && rec.Spec.OnDemand != nil && rec.Spec.OnDemand.ActivationMode() == "github" {
		rec.QueueDemand = demand
		rec.QueueCheckedAt = checked
		if demand {
			rec.LastActivity = checked
		}
	}
}

func (s *PodStore) RetireWorker(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pods[name]
	if !ok || rec.WorkerRetired {
		return false
	}
	rec.WorkerRetired = true
	rec.Ready = false
	rec.LastActivity = time.Now()
	return true
}

// StopRetiredWorker allows a new queued job to replace a finished worker
// immediately. An unfinished worker never enters this path.
func (s *PodStore) StopRetiredWorker(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pods[name]
	if !ok || !rec.WorkerRetired || rec.Status != StatusRunning || rec.InFlight != 0 || (!rec.QueueDemand && (rec.Spec.OnDemand == nil || !rec.Spec.OnDemand.AlwaysWarm)) {
		return false
	}
	rec.Status = StatusStopping
	rec.Ready = false
	return true
}
