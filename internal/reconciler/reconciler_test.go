package reconciler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// stubExecutor implements executor.Executor for testing.
type stubExecutor struct {
	mu        sync.Mutex
	creates   []string
	stops     []string
	removes   []string
	statuses  map[string]executor.Status
	createErr error
	statusErr error
}

func newStubExecutor() *stubExecutor {
	return &stubExecutor{
		statuses: make(map[string]executor.Status),
	}
}

func (s *stubExecutor) CreatePod(_ context.Context, spec manifest.PodSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates = append(s.creates, spec.Name)
	if s.createErr != nil {
		return s.createErr
	}
	// Mark as running by default.
	s.statuses[spec.Name] = executor.Status{Running: true}
	return nil
}

func (s *stubExecutor) StopPod(_ context.Context, name string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops = append(s.stops, name)
	delete(s.statuses, name)
	return nil
}

func (s *stubExecutor) PodStatus(_ context.Context, name string) (executor.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusErr != nil {
		return executor.Status{}, s.statusErr
	}
	st, ok := s.statuses[name]
	if !ok {
		return executor.Status{Running: false, ExitCode: 1}, nil
	}
	return st, nil
}

func (s *stubExecutor) RemovePod(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes = append(s.removes, name)
	delete(s.statuses, name)
	return nil
}

func (s *stubExecutor) setStatus(name string, st executor.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[name] = st
}

func (s *stubExecutor) getCreates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.creates))
	copy(cp, s.creates)
	return cp
}

// newTestScheduler creates a scheduler with enough resources for testing.
func newTestScheduler() *scheduler.Scheduler {
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 49152},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	)
	return scheduler.NewScheduler(tracker)
}

func testPodSpec(name, restartPolicy string, backoffLimit int) manifest.PodSpec {
	return manifest.PodSpec{
		Name:          name,
		RestartPolicy: restartPolicy,
		BackoffLimit:  backoffLimit,
		Containers: []manifest.ContainerSpec{
			{
				Name:  "main",
				Image: "test:latest",
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 128},
				},
			},
		},
	}
}

func TestPendingPodGetsScheduledAndCreated(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("web", "Always", 0)
	store.Apply(spec)

	r.reconcileOnce(context.Background())

	rec, ok := store.Get("web")
	if !ok {
		t.Fatal("pod not found in store")
	}
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected status Running, got %s", rec.Status)
	}

	creates := exec.getCreates()
	if len(creates) != 1 || creates[0] != "web" {
		t.Fatalf("expected one create for 'web', got %v", creates)
	}
}

func TestCrashedServicePodGetsRescheduled(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("svc", "Always", 0)
	store.Apply(spec)

	// First tick: schedule and create.
	r.reconcileOnce(context.Background())

	rec, _ := store.Get("svc")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after first tick, got %s", rec.Status)
	}

	// Simulate crash: pod exits with non-zero code.
	exec.setStatus("svc", executor.Status{Running: false, ExitCode: 1})

	// Second tick: detect crash, set to Pending.
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("svc")
	if rec.Status != state.StatusPending {
		t.Fatalf("expected Pending after crash, got %s", rec.Status)
	}

	// Third tick: re-schedule.
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("svc")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after re-schedule, got %s", rec.Status)
	}

	creates := exec.getCreates()
	if len(creates) != 2 {
		t.Fatalf("expected 2 creates, got %d", len(creates))
	}
}

func TestFailedJobWithRetriesGetsRetried(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("job1", "OnFailure", 3)
	store.Apply(spec)

	// Tick 1: schedule.
	r.reconcileOnce(context.Background())
	rec, _ := store.Get("job1")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}

	// Simulate failure.
	exec.setStatus("job1", executor.Status{Running: false, ExitCode: 1})

	// Tick 2: detect failure, retry.
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("job1")
	if rec.Status != state.StatusPending {
		t.Fatalf("expected Pending for retry, got %s", rec.Status)
	}
	if rec.RetryCount != 1 {
		t.Fatalf("expected RetryCount=1, got %d", rec.RetryCount)
	}

	// Tick 3: re-schedule after retry.
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("job1")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after retry, got %s", rec.Status)
	}
}

func TestCompletedJobStaysCompleted(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("batch", "Never", 0)
	store.Apply(spec)

	// Tick 1: schedule.
	r.reconcileOnce(context.Background())

	// Simulate clean exit.
	exec.setStatus("batch", executor.Status{Running: false, ExitCode: 0})

	// Tick 2: detect exit, mark completed.
	r.reconcileOnce(context.Background())
	rec, _ := store.Get("batch")
	if rec.Status != state.StatusCompleted {
		t.Fatalf("expected Completed, got %s", rec.Status)
	}

	// Tick 3: should remain completed (not re-processed).
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("batch")
	if rec.Status != state.StatusCompleted {
		t.Fatalf("expected still Completed, got %s", rec.Status)
	}
}

func TestFailedJobWithExhaustedRetriesStaysFailed(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("job2", "OnFailure", 1)
	store.Apply(spec)

	// Tick 1: schedule.
	r.reconcileOnce(context.Background())

	// Fail once.
	exec.setStatus("job2", executor.Status{Running: false, ExitCode: 1})
	r.reconcileOnce(context.Background())

	rec, _ := store.Get("job2")
	if rec.Status != state.StatusPending {
		t.Fatalf("expected Pending for retry, got %s", rec.Status)
	}
	if rec.RetryCount != 1 {
		t.Fatalf("expected RetryCount=1, got %d", rec.RetryCount)
	}

	// Tick 3: re-schedule.
	r.reconcileOnce(context.Background())

	// Fail again — retries exhausted (retryCount=1 == backoffLimit=1).
	exec.setStatus("job2", executor.Status{Running: false, ExitCode: 1})
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("job2")
	if rec.Status != state.StatusFailed {
		t.Fatalf("expected Failed with exhausted retries, got %s", rec.Status)
	}

	// Should stay failed.
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("job2")
	if rec.Status != state.StatusFailed {
		t.Fatalf("expected still Failed, got %s", rec.Status)
	}
}
