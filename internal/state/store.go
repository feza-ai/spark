package state

import (
	"sync"
	"sync/atomic"
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
	Spec          manifest.PodSpec
	Status        PodStatus
	Events        []PodEvent
	StartedAt     time.Time
	FinishedAt    time.Time
	Restarts      int
	RetryCount    int // for jobs with backoffLimit
	SourcePath    string
	Reason        string    // human-readable failure reason from the last start attempt
	StartAttempts int       // number of failed pod-create/start attempts since last success
	LastAttemptAt time.Time // time of the most recent failed start attempt; zero when none pending
}

// maxReasonBytes caps the size of PodRecord.Reason to keep the store bounded.
const maxReasonBytes = 512

// PodStore is a thread-safe in-memory store for pod state.
type PodStore struct {
	mu       sync.RWMutex
	pods     map[string]*PodRecord
	OnDelete func(name string)
	readOnly atomic.Bool
}

// SetReadOnly enables or disables read-only mode. When read-only, Apply is a no-op.
func (s *PodStore) SetReadOnly(ro bool) {
	s.readOnly.Store(ro)
}

// NewPodStore creates a new empty store.
func NewPodStore() *PodStore {
	return &PodStore{
		pods: make(map[string]*PodRecord),
	}
}

// LoadFrom populates the store with persisted records. Must be called before any concurrent access.
func (s *PodStore) LoadFrom(pods map[string]*PodRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, rec := range pods {
		s.pods[name] = rec
	}
}

// Apply adds or updates a pod's desired state. If new, status is Pending.
// In read-only mode, Apply is a no-op.
func (s *PodStore) Apply(spec manifest.PodSpec) {
	if s.readOnly.Load() {
		return
	}
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
	_, ok := s.pods[name]
	if ok {
		delete(s.pods, name)
	}
	onDelete := s.OnDelete
	s.mu.Unlock()
	if ok && onDelete != nil {
		onDelete(name)
	}
	return ok
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

// IncrementRestarts increments a pod's restart counter. Returns false if not found.
func (s *PodStore) IncrementRestarts(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}
	rec.Restarts++
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

// RecordStartFailure records a container-start failure on a pod: sets Reason
// (truncated), increments StartAttempts, and stamps LastAttemptAt with the
// provided time. Returns the new StartAttempts value, or 0 if the pod is not
// found.
func (s *PodStore) RecordStartFailure(name string, reason string, at time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return 0
	}
	if len(reason) > maxReasonBytes {
		reason = reason[:maxReasonBytes]
	}
	rec.Reason = reason
	rec.StartAttempts++
	rec.LastAttemptAt = at
	return rec.StartAttempts
}

// ClearStartFailure clears Reason and resets StartAttempts for a pod.
// Returns false if not found.
func (s *PodStore) ClearStartFailure(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}
	rec.Reason = ""
	rec.StartAttempts = 0
	rec.LastAttemptAt = time.Time{}
	return true
}

// Prune removes completed or failed pods whose FinishedAt is older than the given duration.
// Returns the number of pods pruned.
func (s *PodStore) Prune(olderThan time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	pruned := 0
	for name, rec := range s.pods {
		if (rec.Status == StatusCompleted || rec.Status == StatusFailed) && !rec.FinishedAt.IsZero() && rec.FinishedAt.Before(cutoff) {
			delete(s.pods, name)
			pruned++
		}
	}
	return pruned
}

// BackdateLastEvent shifts the last event's timestamp back by the given duration.
// Intended for testing staleness checks.
func (s *PodStore) BackdateLastEvent(name string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok || len(rec.Events) == 0 {
		return
	}
	rec.Events[len(rec.Events)-1].Time = rec.Events[len(rec.Events)-1].Time.Add(-d)
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

// SetSourcePath sets the SourcePath for a pod. Returns false if not found.
func (s *PodStore) SetSourcePath(name string, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.pods[name]
	if !ok {
		return false
	}
	rec.SourcePath = path
	return true
}

// ListBySourcePath returns all pod records with the given SourcePath.
func (s *PodStore) ListBySourcePath(path string) []PodRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []PodRecord
	for _, rec := range s.pods {
		if rec.SourcePath == path {
			cp := *rec
			cp.Events = make([]PodEvent, len(rec.Events))
			copy(cp.Events, rec.Events)
			result = append(result, cp)
		}
	}
	return result
}
