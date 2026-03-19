package state

import (
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

// PodStatus represents the current phase of a pod.
type PodStatus string

const (
	StatusPending   PodStatus = "pending"
	StatusScheduled PodStatus = "scheduled"
	StatusRunning   PodStatus = "running"
	StatusCompleted PodStatus = "completed"
	StatusFailed    PodStatus = "failed"
	StatusPreempted PodStatus = "preempted"
)

// PodEvent records a timestamped lifecycle event.
type PodEvent struct {
	Time    time.Time
	Type    string // scheduled, started, completed, failed, preempted, restarted, deleted
	Message string
}

// PodRecord holds desired and actual state for a pod.
type PodRecord struct {
	Spec       manifest.PodSpec
	Status     PodStatus
	Events     []PodEvent
	StartedAt  time.Time
	FinishedAt time.Time
	Restarts   int
	RetryCount int // for jobs with backoffLimit
}

// PodStore is a thread-safe in-memory store for pod state.
type PodStore struct {
	mu   sync.RWMutex
	pods map[string]*PodRecord
}

// NewPodStore creates a new empty store.
func NewPodStore() *PodStore {
	return &PodStore{
		pods: make(map[string]*PodRecord),
	}
}

// Apply adds or updates a pod's desired state. If new, status is Pending.
func (s *PodStore) Apply(spec manifest.PodSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, ok := s.pods[spec.Name]; ok {
		rec.Spec = spec
		return
	}
	s.pods[spec.Name] = &PodRecord{
		Spec:   spec,
		Status: StatusPending,
	}
}

// Delete removes a pod from the store. Returns false if not found.
func (s *PodStore) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pods[name]; !ok {
		return false
	}
	delete(s.pods, name)
	return true
}

// Get returns a copy of a pod record by name.
func (s *PodStore) Get(name string) (PodRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.pods[name]
	if !ok {
		return PodRecord{}, false
	}
	cp := *rec
	cp.Events = make([]PodEvent, len(rec.Events))
	copy(cp.Events, rec.Events)
	return cp, true
}

// List returns all pod records, optionally filtered by status.
// Pass empty string to return all pods.
func (s *PodStore) List(status PodStatus) []PodRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []PodRecord
	for _, rec := range s.pods {
		if status != "" && rec.Status != status {
			continue
		}
		cp := *rec
		cp.Events = make([]PodEvent, len(rec.Events))
		copy(cp.Events, rec.Events)
		result = append(result, cp)
	}
	return result
}

// UpdateStatus sets a pod's status and appends an event. Returns false if not found.
func (s *PodStore) UpdateStatus(name string, status PodStatus, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}

	rec.Status = status
	now := time.Now()
	rec.Events = append(rec.Events, PodEvent{
		Time:    now,
		Type:    string(status),
		Message: message,
	})

	switch status {
	case StatusRunning:
		rec.StartedAt = now
	case StatusCompleted, StatusFailed:
		rec.FinishedAt = now
	}

	return true
}

// AddEvent appends a lifecycle event to a pod. Returns false if not found.
func (s *PodStore) AddEvent(name string, eventType string, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}

	rec.Events = append(rec.Events, PodEvent{
		Time:    time.Now(),
		Type:    eventType,
		Message: message,
	})
	return true
}

// IncrementRetry increments a pod's retry count. Returns false if not found.
func (s *PodStore) IncrementRetry(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}
	rec.RetryCount++
	return true
}

// Names returns all pod names.
func (s *PodStore) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.pods))
	for name := range s.pods {
		names = append(names, name)
	}
	return names
}
