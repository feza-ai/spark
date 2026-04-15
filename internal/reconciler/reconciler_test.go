package reconciler

import (
	"context"
	"errors"
	"io"
	"strings"
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
	mu          sync.Mutex
	creates     []string
	stops       []string
	removes     []string
	statuses    map[string]executor.Status
	createErr   error
	statusErr   error
	listPods    []executor.PodListEntry
	listErr     error
	removeErr   error
	podStats    map[string]executor.PodResourceUsage
	podStatsErr error

	// Probe stubs.
	execProbeExit int
	execProbeErr  error
	httpProbeErr  error
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
	if s.removeErr != nil {
		return s.removeErr
	}
	delete(s.statuses, name)
	return nil
}

func (s *stubExecutor) ListPods(_ context.Context) ([]executor.PodListEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listPods != nil {
		return s.listPods, nil
	}
	var result []executor.PodListEntry
	for name, st := range s.statuses {
		result = append(result, executor.PodListEntry{Name: name, Running: st.Running})
	}
	return result, nil
}

func (s *stubExecutor) PodStats(_ context.Context, name string) (executor.PodResourceUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.podStatsErr != nil {
		return executor.PodResourceUsage{}, s.podStatsErr
	}
	if s.podStats != nil {
		if stats, ok := s.podStats[name]; ok {
			return stats, nil
		}
	}
	return executor.PodResourceUsage{}, nil
}

func (s *stubExecutor) PodLogs(_ context.Context, _ string, _ int) ([]byte, error) {
	return nil, nil
}

func (s *stubExecutor) StreamPodLogs(_ context.Context, _ string, _ int) (io.ReadCloser, error) {
	return nil, nil
}

func (s *stubExecutor) ExecPod(_ context.Context, _ string, _ string, _ []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}

func (s *stubExecutor) ListImages(_ context.Context) ([]executor.ImageInfo, error) {
	return nil, nil
}

func (s *stubExecutor) PullImage(_ context.Context, _ string) error {
	return nil
}

func (s *stubExecutor) ExecProbe(_ context.Context, _ string, _ string, _ []string, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execProbeExit, s.execProbeErr
}

func (s *stubExecutor) HTTPProbe(_ context.Context, _ int, _ string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpProbeErr
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

func (s *stubExecutor) getStops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.stops))
	copy(cp, s.stops)
	return cp
}

func (s *stubExecutor) getRemoves() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.removes))
	copy(cp, s.removes)
	return cp
}

// newTestScheduler creates a scheduler with enough resources for testing.
func newTestScheduler() *scheduler.Scheduler {
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 49152},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	nil, 0,
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

func TestDeletedPodIsRemovedFromStore(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("del", "Always", 0)
	store.Apply(spec)

	// Tick 1: schedule and create.
	r.reconcileOnce(context.Background())
	rec, ok := store.Get("del")
	if !ok || rec.Status != state.StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}

	// Delete from store while running.
	store.Delete("del")

	_, ok = store.Get("del")
	if ok {
		t.Fatal("expected pod to be removed from store after Delete")
	}

	// Tick 2: reconciler should not crash with missing pod.
	r.reconcileOnce(context.Background())

	_, ok = store.Get("del")
	if ok {
		t.Fatal("deleted pod should not reappear after reconcile")
	}
}

func TestCreatePodErrorReverts(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.createErr = errors.New("podman create failed")
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("fail-create", "Always", 0)
	store.Apply(spec)

	r.reconcileOnce(context.Background())

	rec, ok := store.Get("fail-create")
	if !ok {
		t.Fatal("pod not found in store")
	}
	if rec.Status != state.StatusPending {
		t.Fatalf("expected Pending after create error, got %s", rec.Status)
	}
}

func TestPodStatusErrorKeepsRunning(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("status-err", "Always", 0)
	store.Apply(spec)

	// Tick 1: schedule and create.
	r.reconcileOnce(context.Background())

	rec, _ := store.Get("status-err")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}

	// Inject status error.
	exec.mu.Lock()
	exec.statusErr = errors.New("inspect failed")
	exec.mu.Unlock()

	// Tick 2: status check fails, pod should remain Running.
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("status-err")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after status error, got %s", rec.Status)
	}
}

func TestRunLoopStopsOnCancel(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, 10*time.Millisecond)

	spec := testPodSpec("loop", "Always", 0)
	store.Apply(spec)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Wait for at least one tick to process the pod.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}

	rec, ok := store.Get("loop")
	if !ok {
		t.Fatal("pod not found")
	}
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after loop, got %s", rec.Status)
	}
}

func TestReconcileRunningExitCodes(t *testing.T) {
	tests := []struct {
		name          string
		restartPolicy string
		backoffLimit  int
		exitCode      int
		wantStatus    state.PodStatus
		wantRetry     int
	}{
		{
			name:          "Always/exit0 restarts",
			restartPolicy: "Always",
			exitCode:      0,
			wantStatus:    state.StatusPending,
		},
		{
			name:          "Always/exit1 restarts",
			restartPolicy: "Always",
			exitCode:      1,
			wantStatus:    state.StatusPending,
		},
		{
			name:          "Never/exit0 completes",
			restartPolicy: "Never",
			exitCode:      0,
			wantStatus:    state.StatusCompleted,
		},
		{
			name:          "Never/exit1 fails",
			restartPolicy: "Never",
			exitCode:      1,
			wantStatus:    state.StatusFailed,
		},
		{
			name:          "OnFailure/exit0 completes",
			restartPolicy: "OnFailure",
			backoffLimit:  3,
			exitCode:      0,
			wantStatus:    state.StatusCompleted,
		},
		{
			name:          "OnFailure/exit1 retries",
			restartPolicy: "OnFailure",
			backoffLimit:  3,
			exitCode:      1,
			wantStatus:    state.StatusPending,
			wantRetry:     1,
		},
		{
			name:          "OnFailure/exit1 no retries left",
			restartPolicy: "OnFailure",
			backoffLimit:  0,
			exitCode:      1,
			wantStatus:    state.StatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			sched := newTestScheduler()
			exec := newStubExecutor()
			r := NewReconciler(store, sched, exec, time.Second)

			spec := testPodSpec("pod-"+tt.name, tt.restartPolicy, tt.backoffLimit)
			store.Apply(spec)

			// Schedule and create.
			r.reconcileOnce(context.Background())

			// Simulate exit.
			exec.setStatus("pod-"+tt.name, executor.Status{Running: false, ExitCode: tt.exitCode})

			// Detect exit.
			r.reconcileOnce(context.Background())

			rec, ok := store.Get("pod-" + tt.name)
			if !ok {
				t.Fatal("pod not found")
			}
			if rec.Status != tt.wantStatus {
				t.Fatalf("expected %s, got %s", tt.wantStatus, rec.Status)
			}
			if rec.RetryCount != tt.wantRetry {
				t.Fatalf("expected RetryCount=%d, got %d", tt.wantRetry, rec.RetryCount)
			}
		})
	}
}

func TestReconcileRunningIncrementsRestarts(t *testing.T) {
	tests := []struct {
		name          string
		restartPolicy string
		backoffLimit  int
		exitCode      int
		wantRestarts  int
	}{
		{
			name:          "Always/exit0 increments restarts",
			restartPolicy: "Always",
			exitCode:      0,
			wantRestarts:  1,
		},
		{
			name:          "Always/exit1 increments restarts",
			restartPolicy: "Always",
			exitCode:      1,
			wantRestarts:  1,
		},
		{
			name:          "OnFailure/exit1 increments restarts",
			restartPolicy: "OnFailure",
			backoffLimit:  3,
			exitCode:      1,
			wantRestarts:  1,
		},
		{
			name:          "Never/exit0 no restart increment",
			restartPolicy: "Never",
			exitCode:      0,
			wantRestarts:  0,
		},
		{
			name:          "Never/exit1 no restart increment",
			restartPolicy: "Never",
			exitCode:      1,
			wantRestarts:  0,
		},
		{
			name:          "OnFailure/exit0 no restart increment",
			restartPolicy: "OnFailure",
			backoffLimit:  3,
			exitCode:      0,
			wantRestarts:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			sched := newTestScheduler()
			exec := newStubExecutor()
			r := NewReconciler(store, sched, exec, time.Second)

			spec := testPodSpec("rst-"+tt.name, tt.restartPolicy, tt.backoffLimit)
			store.Apply(spec)

			// Tick 1: schedule and create.
			r.reconcileOnce(context.Background())

			// Simulate exit.
			exec.setStatus("rst-"+tt.name, executor.Status{Running: false, ExitCode: tt.exitCode})

			// Tick 2: detect exit.
			r.reconcileOnce(context.Background())

			rec, ok := store.Get("rst-" + tt.name)
			if !ok {
				t.Fatal("pod not found")
			}
			if rec.Restarts != tt.wantRestarts {
				t.Fatalf("expected Restarts=%d, got %d", tt.wantRestarts, rec.Restarts)
			}
		})
	}
}

func TestReconcileRunningMultipleRestartsAccumulate(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("svc-multi", "Always", 0)
	store.Apply(spec)

	for i := 0; i < 3; i++ {
		// Schedule and create.
		r.reconcileOnce(context.Background())

		// Simulate crash.
		exec.setStatus("svc-multi", executor.Status{Running: false, ExitCode: 1})

		// Detect crash, re-queue.
		r.reconcileOnce(context.Background())
	}

	rec, ok := store.Get("svc-multi")
	if !ok {
		t.Fatal("pod not found")
	}
	if rec.Restarts != 3 {
		t.Fatalf("expected Restarts=3 after 3 crash cycles, got %d", rec.Restarts)
	}
}

func TestOnStatusChangeCallback(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	type statusEvent struct {
		podName string
		status  string
		message string
	}
	var events []statusEvent

	r.SetOnStatusChange(func(podName, status, message string) {
		events = append(events, statusEvent{podName, status, message})
	})

	spec := testPodSpec("cb-pod", "Never", 0)
	store.Apply(spec)

	// Tick 1: schedule and create -> scheduled + running.
	r.reconcileOnce(context.Background())

	if len(events) != 2 {
		t.Fatalf("expected 2 events after scheduling, got %d", len(events))
	}
	if events[0].status != "scheduled" {
		t.Fatalf("expected first event status 'scheduled', got %q", events[0].status)
	}
	if events[1].status != "running" {
		t.Fatalf("expected second event status 'running', got %q", events[1].status)
	}
	if events[0].podName != "cb-pod" || events[1].podName != "cb-pod" {
		t.Fatal("expected all events for 'cb-pod'")
	}

	// Simulate clean exit.
	exec.setStatus("cb-pod", executor.Status{Running: false, ExitCode: 0})

	// Tick 2: detect exit -> completed.
	r.reconcileOnce(context.Background())

	if len(events) != 3 {
		t.Fatalf("expected 3 events total, got %d", len(events))
	}
	if events[2].status != "completed" {
		t.Fatalf("expected third event status 'completed', got %q", events[2].status)
	}
}

// testPodSpecWithPriority creates a PodSpec with a specific priority and resource request.
func testPodSpecWithPriority(name string, priority, cpuMillis, memoryMB int) manifest.PodSpec {
	return manifest.PodSpec{
		Name:          name,
		Priority:      priority,
		RestartPolicy: "Never",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "main",
				Image: "test:latest",
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: cpuMillis, MemoryMB: memoryMB},
				},
			},
		},
	}
}

// newTightScheduler creates a scheduler with limited resources to force preemption.
func newTightScheduler(cpuMillis, memoryMB int) *scheduler.Scheduler {
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: cpuMillis, MemoryMB: memoryMB, GPUMemoryMB: 0},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	nil, 0,
	)
	return scheduler.NewScheduler(tracker)
}

func TestPreemptionStopsVictims(t *testing.T) {
	store := state.NewPodStore()
	sched := newTightScheduler(1000, 1024)
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	// Schedule a low-priority pod that uses all resources.
	lowSpec := testPodSpecWithPriority("low", 1000, 1000, 1024)
	store.Apply(lowSpec)
	r.reconcileOnce(context.Background())

	rec, _ := store.Get("low")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected low-priority pod Running, got %s", rec.Status)
	}

	// Add a high-priority pod that needs the same resources.
	highSpec := testPodSpecWithPriority("high", 100, 1000, 1024)
	store.Apply(highSpec)
	r.reconcileOnce(context.Background())

	// Verify StopPod was called on the victim.
	stops := exec.getStops()
	if len(stops) != 1 || stops[0] != "low" {
		t.Fatalf("expected StopPod called on 'low', got %v", stops)
	}

	// Verify the high-priority pod is now running.
	rec, _ = store.Get("high")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected high-priority pod Running, got %s", rec.Status)
	}
}

func TestPreemptionUpdatesVictimStatus(t *testing.T) {
	store := state.NewPodStore()
	sched := newTightScheduler(1000, 1024)
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	// Schedule a low-priority pod.
	lowSpec := testPodSpecWithPriority("victim", 1000, 1000, 1024)
	store.Apply(lowSpec)
	r.reconcileOnce(context.Background())

	// Add a high-priority pod.
	highSpec := testPodSpecWithPriority("preemptor", 100, 1000, 1024)
	store.Apply(highSpec)
	r.reconcileOnce(context.Background())

	// Verify victim status is Preempted or Pending (the reconciler may process
	// the preempted pod in the same pass and reset it to Pending).
	rec, ok := store.Get("victim")
	if !ok {
		t.Fatal("victim pod not found in store")
	}
	if rec.Status != state.StatusPreempted && rec.Status != state.StatusPending {
		t.Fatalf("expected victim status Preempted or Pending, got %s", rec.Status)
	}
}

func TestPreemptionReschedulesPendingPod(t *testing.T) {
	store := state.NewPodStore()
	sched := newTightScheduler(2000, 2048)
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	// Schedule two low-priority pods.
	low1 := testPodSpecWithPriority("low1", 1000, 1000, 1024)
	low2 := testPodSpecWithPriority("low2", 1000, 1000, 1024)
	store.Apply(low1)
	store.Apply(low2)
	r.reconcileOnce(context.Background())

	rec1, _ := store.Get("low1")
	rec2, _ := store.Get("low2")
	if rec1.Status != state.StatusRunning || rec2.Status != state.StatusRunning {
		t.Fatalf("expected both low-priority pods Running, got %s and %s", rec1.Status, rec2.Status)
	}

	// Add a high-priority pod that needs all the resources.
	highSpec := testPodSpecWithPriority("high", 100, 2000, 2048)
	store.Apply(highSpec)
	r.reconcileOnce(context.Background())

	// Verify both victims were stopped.
	stops := exec.getStops()
	if len(stops) != 2 {
		t.Fatalf("expected 2 stops, got %d: %v", len(stops), stops)
	}

	// Verify the high-priority pod is running.
	rec, _ := store.Get("high")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected high-priority pod Running after preemption, got %s", rec.Status)
	}

	// Verify both victims are preempted or pending (the reconciler may process
	// preempted pods in the same pass and reset them to Pending).
	rec1, _ = store.Get("low1")
	rec2, _ = store.Get("low2")
	validStatus := func(s state.PodStatus) bool {
		return s == state.StatusPreempted || s == state.StatusPending
	}
	if !validStatus(rec1.Status) || !validStatus(rec2.Status) {
		t.Fatalf("expected both victims Preempted or Pending, got %s and %s", rec1.Status, rec2.Status)
	}
}

func TestRecoverPods_PodInBothStoreAndPodman(t *testing.T) {
	// Pod is Running in both store and podman -> no-op
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("web", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("web", state.StatusRunning, "running")

	exec.listPods = []executor.PodListEntry{{Name: "web", Running: true}}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	rec, _ := store.Get("web")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running, got %s", rec.Status)
	}
}

func TestRecoverPods_PodInStoreNotPodman(t *testing.T) {
	// Pod Running in store but not in podman -> mark Failed
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("ghost", "Always", 0)
	store.Apply(spec)
	store.UpdateStatus("ghost", state.StatusRunning, "was running")

	exec.listPods = []executor.PodListEntry{} // empty, pod not in podman

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	rec, _ := store.Get("ghost")
	if rec.Status != state.StatusFailed {
		t.Fatalf("expected Failed, got %s", rec.Status)
	}
}

func TestRecoverPods_PodInPodmanNotStore(t *testing.T) {
	// Pod in podman but not in store -> log as orphan, don't adopt
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	exec.listPods = []executor.PodListEntry{{Name: "orphan", Running: true}}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, ok := store.Get("orphan")
	if ok {
		t.Fatal("orphaned pod should not be adopted into store")
	}
}

func TestRecoverPods_StoreStatusMismatch(t *testing.T) {
	// Pod is Pending in store but Running in podman -> update to Running
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec("mismatch", "Always", 0)
	store.Apply(spec) // status is Pending

	exec.listPods = []executor.PodListEntry{{Name: "mismatch", Running: true}}

	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	rec, _ := store.Get("mismatch")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after recovery, got %s", rec.Status)
	}
}

func TestReconcileScheduledAndPreempted(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(store *state.PodStore, exec *stubExecutor)
		wantStatus state.PodStatus
	}{
		{
			name: "scheduled pod found running in podman transitions to Running",
			setup: func(store *state.PodStore, exec *stubExecutor) {
				store.Apply(testPodSpec("pod", "Always", 0))
				store.UpdateStatus("pod", state.StatusScheduled, "scheduled by reconciler")
				exec.setStatus("pod", executor.Status{Running: true})
			},
			wantStatus: state.StatusRunning,
		},
		{
			name: "scheduled pod not found in podman and stale resets to Pending",
			setup: func(store *state.PodStore, exec *stubExecutor) {
				store.Apply(testPodSpec("pod", "Always", 0))
				store.UpdateStatus("pod", state.StatusScheduled, "scheduled by reconciler")
				// Backdate the scheduled event to make it stale.
				backdateLastEvent(store, "pod", 60*time.Second)
			},
			wantStatus: state.StatusPending,
		},
		{
			name: "scheduled pod not found in podman but not yet stale stays Scheduled",
			setup: func(store *state.PodStore, exec *stubExecutor) {
				store.Apply(testPodSpec("pod", "Always", 0))
				store.UpdateStatus("pod", state.StatusScheduled, "scheduled by reconciler")
				// Event was just created, so it's fresh — pod stays Scheduled.
			},
			wantStatus: state.StatusScheduled,
		},
		{
			name: "preempted pod resets to Pending",
			setup: func(store *state.PodStore, exec *stubExecutor) {
				store.Apply(testPodSpec("pod", "Always", 0))
				store.UpdateStatus("pod", state.StatusPreempted, "preempted for high-prio")
			},
			wantStatus: state.StatusPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			sched := newTestScheduler()
			exec := newStubExecutor()
			r := NewReconciler(store, sched, exec, time.Second)

			tt.setup(store, exec)

			r.reconcileOnce(context.Background())

			rec, ok := store.Get("pod")
			if !ok {
				t.Fatal("pod not found in store")
			}
			if rec.Status != tt.wantStatus {
				t.Fatalf("expected status %s, got %s", tt.wantStatus, rec.Status)
			}
		})
	}
}

// backdateLastEvent shifts the last event's timestamp back by the given duration.
// This is used to simulate staleness in tests.
func backdateLastEvent(store *state.PodStore, name string, d time.Duration) {
	store.BackdateLastEvent(name, d)
}

// testPodSpecWithProbe creates a PodSpec with a liveness probe on the first container.
func testPodSpecWithExecProbe(name string, initialDelay, period, failure, timeout int) manifest.PodSpec {
	return manifest.PodSpec{
		Name:          name,
		RestartPolicy: "Always",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "main",
				Image: "test:latest",
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 128},
				},
				LivenessProbe: &manifest.ProbeSpec{
					Exec:                &manifest.ExecProbe{Command: []string{"cat", "/tmp/healthy"}},
					InitialDelaySeconds: initialDelay,
					PeriodSeconds:       period,
					FailureThreshold:    failure,
					TimeoutSeconds:      timeout,
				},
			},
		},
	}
}

func testPodSpecWithHTTPProbe(name string, port int, path string, initialDelay, period, failure, timeout int) manifest.PodSpec {
	return manifest.PodSpec{
		Name:          name,
		RestartPolicy: "Always",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "main",
				Image: "test:latest",
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 128},
				},
				LivenessProbe: &manifest.ProbeSpec{
					HTTPGet:             &manifest.HTTPGetProbe{Path: path, Port: port},
					InitialDelaySeconds: initialDelay,
					PeriodSeconds:       period,
					FailureThreshold:    failure,
					TimeoutSeconds:      timeout,
				},
			},
		},
	}
}

func TestLivenessProbe(t *testing.T) {
	tests := []struct {
		name       string
		podSpec    manifest.PodSpec
		setup      func(exec *stubExecutor, r *Reconciler)
		ticks      int
		wantStatus state.PodStatus
		wantStops  int
	}{
		{
			name:    "exec probe passes: no restart",
			podSpec: testPodSpecWithExecProbe("probe-ok", 0, 1, 3, 1),
			setup: func(exec *stubExecutor, r *Reconciler) {
				// Exec probe returns 0 (healthy) by default.
				// Backdate the probe state so it fires immediately.
				r.probeStates["probe-ok"] = &probeState{
					started: time.Now().Add(-10 * time.Second),
				}
			},
			ticks:      3,
			wantStatus: state.StatusRunning,
			wantStops:  0,
		},
		{
			name:    "exec probe fails FailureThreshold times: restart triggered",
			podSpec: testPodSpecWithExecProbe("probe-fail", 0, 1, 3, 1),
			setup: func(exec *stubExecutor, r *Reconciler) {
				exec.mu.Lock()
				exec.execProbeExit = 1
				exec.mu.Unlock()
				// Pre-populate probe state with 2 failures and a lastCheck in the past
				// so the next check fires and hits the threshold.
				r.probeStates["probe-fail"] = &probeState{
					started:             time.Now().Add(-10 * time.Second),
					lastCheck:           time.Now().Add(-10 * time.Second),
					consecutiveFailures: 2,
				}
			},
			ticks:      1,
			wantStatus: state.StatusPending,
			wantStops:  1,
		},
		{
			name:    "HTTP probe fails: restart triggered",
			podSpec: testPodSpecWithHTTPProbe("http-fail", 8080, "/healthz", 0, 1, 2, 1),
			setup: func(exec *stubExecutor, r *Reconciler) {
				exec.mu.Lock()
				exec.httpProbeErr = errors.New("connection refused")
				exec.mu.Unlock()
				// Pre-populate with 1 failure so next check triggers restart.
				r.probeStates["http-fail"] = &probeState{
					started:             time.Now().Add(-10 * time.Second),
					lastCheck:           time.Now().Add(-10 * time.Second),
					consecutiveFailures: 1,
				}
			},
			ticks:      1,
			wantStatus: state.StatusPending,
			wantStops:  1,
		},
		{
			name:    "initial delay respected: no probing during delay",
			podSpec: testPodSpecWithExecProbe("probe-delay", 3600, 1, 1, 1),
			setup: func(exec *stubExecutor, r *Reconciler) {
				exec.mu.Lock()
				exec.execProbeExit = 1
				exec.mu.Unlock()
				// Probe was just started; 3600s delay means no probes will fire.
			},
			ticks:      5,
			wantStatus: state.StatusRunning,
			wantStops:  0,
		},
		{
			name:    "probe passes after failures: counter resets",
			podSpec: testPodSpecWithExecProbe("probe-reset", 0, 1, 3, 1),
			setup: func(exec *stubExecutor, r *Reconciler) {
				// Set up probe state with 2 consecutive failures (one away from threshold).
				r.probeStates["probe-reset"] = &probeState{
					started:             time.Now().Add(-10 * time.Second),
					lastCheck:           time.Now().Add(-10 * time.Second),
					consecutiveFailures: 2,
				}
				// Probe will pass (exit code 0) -- failures should reset.
			},
			ticks:      1,
			wantStatus: state.StatusRunning,
			wantStops:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			sched := newTestScheduler()
			exec := newStubExecutor()
			r := NewReconciler(store, sched, exec, time.Second)

			store.Apply(tt.podSpec)

			// First tick: schedule and create the pod.
			r.reconcileOnce(context.Background())

			rec, ok := store.Get(tt.podSpec.Name)
			if !ok || rec.Status != state.StatusRunning {
				t.Fatalf("expected Running after scheduling, got %v", rec.Status)
			}

			// Apply test-specific setup (probe behavior).
			if tt.setup != nil {
				tt.setup(exec, r)
			}

			// Run the specified number of ticks to check probes.
			for i := 0; i < tt.ticks; i++ {
				r.reconcileOnce(context.Background())
			}

			rec, ok = store.Get(tt.podSpec.Name)
			if !ok {
				t.Fatal("pod not found in store")
			}
			if rec.Status != tt.wantStatus {
				t.Fatalf("expected status %s, got %s", tt.wantStatus, rec.Status)
			}

			stops := exec.getStops()
			if len(stops) != tt.wantStops {
				t.Fatalf("expected %d stops, got %d: %v", tt.wantStops, len(stops), stops)
			}
		})
	}
}

// TestCreatePodFailureRecordsReasonAndAttempts verifies that when CreatePod
// fails (e.g., missing volume), the reconciler surfaces the error as Reason
// and increments StartAttempts on the pod record. Backoff is advanced with a
// fake clock so successive ticks actually retry.
func TestCreatePodFailureRecordsReasonAndAttempts(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.createErr = errors.New("volume \"data\" not found")
	r := NewReconciler(store, sched, exec, time.Second)

	base := time.Unix(0, 0)
	current := base
	r.SetClock(func() time.Time { return current })

	// BackoffLimit high enough that we stay in retry-pending across attempts.
	spec := testPodSpec("needs-vol", "Never", 0)
	spec.BackoffLimit = 10
	store.Apply(spec)

	// First tick: CreatePod fails.
	r.reconcileOnce(context.Background())

	rec, ok := store.Get("needs-vol")
	if !ok {
		t.Fatal("pod not found in store")
	}
	if rec.StartAttempts != 1 {
		t.Fatalf("expected StartAttempts=1 after first failure, got %d", rec.StartAttempts)
	}
	if rec.Reason == "" {
		t.Fatal("expected Reason to be set after CreatePod error, got empty")
	}
	if !strings.Contains(rec.Reason, "volume") {
		t.Fatalf("expected Reason to include executor error text, got %q", rec.Reason)
	}

	// Advance past the 5s backoff window.
	current = current.Add(6 * time.Second)

	// Second tick: same failure — attempts should climb.
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("needs-vol")
	if rec.StartAttempts != 2 {
		t.Fatalf("expected StartAttempts=2 after second failure, got %d", rec.StartAttempts)
	}
	if rec.Reason == "" {
		t.Fatal("expected Reason still set after second failure")
	}
}

// TestCreatePodSuccessClearsFailureState verifies that a successful start
// after a prior failure resets Reason, StartAttempts, and LastAttemptAt.
func TestCreatePodSuccessClearsFailureState(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.createErr = errors.New("transient failure")
	r := NewReconciler(store, sched, exec, time.Second)

	base := time.Unix(0, 0)
	current := base
	r.SetClock(func() time.Time { return current })

	spec := testPodSpec("flaky", "Never", 0)
	spec.BackoffLimit = 5
	store.Apply(spec)

	// Tick 1: fail.
	r.reconcileOnce(context.Background())
	rec, _ := store.Get("flaky")
	if rec.StartAttempts != 1 || rec.Reason == "" {
		t.Fatalf("setup: expected failure recorded, got attempts=%d reason=%q", rec.StartAttempts, rec.Reason)
	}
	if rec.LastAttemptAt.IsZero() {
		t.Fatal("expected LastAttemptAt set after failure")
	}

	// Advance past backoff, clear executor error, tick again.
	current = current.Add(6 * time.Second)
	exec.mu.Lock()
	exec.createErr = nil
	exec.mu.Unlock()
	r.reconcileOnce(context.Background())

	rec, _ = store.Get("flaky")
	if rec.Status != state.StatusRunning {
		t.Fatalf("expected Running after recovery, got %s", rec.Status)
	}
	if rec.StartAttempts != 0 {
		t.Fatalf("expected StartAttempts=0 after success, got %d", rec.StartAttempts)
	}
	if rec.Reason != "" {
		t.Fatalf("expected Reason cleared after success, got %q", rec.Reason)
	}
	if !rec.LastAttemptAt.IsZero() {
		t.Fatalf("expected LastAttemptAt cleared after success, got %v", rec.LastAttemptAt)
	}
}

// TestCreatePodFailureTerminalAfterBackoffLimit verifies that once
// StartAttempts exceeds the pod's BackoffLimit, the reconciler transitions
// the pod to Failed, removes the podman pod best-effort, and stops retrying.
func TestCreatePodFailureTerminalAfterBackoffLimit(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.createErr = errors.New("boom")
	r := NewReconciler(store, sched, exec, time.Second)

	current := time.Unix(0, 0)
	r.SetClock(func() time.Time { return current })

	spec := testPodSpec("doomed", "Never", 0)
	spec.BackoffLimit = 2
	store.Apply(spec)

	// Attempt 1: fails, retry pending.
	r.reconcileOnce(context.Background())
	rec, _ := store.Get("doomed")
	if rec.Status != state.StatusPending {
		t.Fatalf("after attempt 1: expected Pending, got %s", rec.Status)
	}
	if rec.StartAttempts != 1 {
		t.Fatalf("after attempt 1: expected StartAttempts=1, got %d", rec.StartAttempts)
	}

	// Attempt 2: advance past 5s backoff. Fails again, still retry pending
	// because attempts (2) is not strictly greater than BackoffLimit (2).
	current = current.Add(6 * time.Second)
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("doomed")
	if rec.Status != state.StatusPending {
		t.Fatalf("after attempt 2: expected Pending, got %s", rec.Status)
	}
	if rec.StartAttempts != 2 {
		t.Fatalf("after attempt 2: expected StartAttempts=2, got %d", rec.StartAttempts)
	}

	// Attempt 3: advance past 10s backoff. attempts becomes 3 > limit 2 →
	// pod should transition to Failed and RemovePod should be called.
	current = current.Add(20 * time.Second)
	r.reconcileOnce(context.Background())
	rec, _ = store.Get("doomed")
	if rec.Status != state.StatusFailed {
		t.Fatalf("after attempt 3: expected Failed, got %s", rec.Status)
	}
	if rec.Reason == "" {
		t.Fatal("expected Reason set on terminal failure")
	}
	exec.mu.Lock()
	gotRemoves := append([]string(nil), exec.removes...)
	exec.mu.Unlock()
	found := false
	for _, n := range gotRemoves {
		if n == "doomed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected RemovePod(\"doomed\") to be called; removes=%v", gotRemoves)
	}

	// Subsequent ticks must not attempt CreatePod again — pod is terminal.
	createsBefore := len(exec.getCreates())
	current = current.Add(5 * time.Minute)
	r.reconcileOnce(context.Background())
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != createsBefore {
		t.Fatalf("expected no further CreatePod calls after terminal Failed, got %d new", got-createsBefore)
	}
}

// TestCreatePodBackoffDelaysRetry verifies that immediately after a failure,
// the next reconcile tick does not attempt CreatePod again until the backoff
// delay has elapsed.
func TestCreatePodBackoffDelaysRetry(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.createErr = errors.New("nope")
	r := NewReconciler(store, sched, exec, time.Second)

	current := time.Unix(1000, 0)
	r.SetClock(func() time.Time { return current })

	spec := testPodSpec("slowbo", "Never", 0)
	spec.BackoffLimit = 10
	store.Apply(spec)

	// First failure.
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 1 {
		t.Fatalf("expected 1 CreatePod call after first tick, got %d", got)
	}

	// Second tick without advancing the clock: should be suppressed.
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 1 {
		t.Fatalf("expected CreatePod NOT retried within backoff window, got %d calls", got)
	}

	// Advance < 5s: still suppressed.
	current = current.Add(4 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 1 {
		t.Fatalf("expected CreatePod suppressed at 4s, got %d calls", got)
	}

	// Advance past 5s: retry allowed.
	current = current.Add(2 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 2 {
		t.Fatalf("expected CreatePod retried after backoff elapsed, got %d calls", got)
	}
}

func TestRecoverPodsRemovesOrphans(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)
	r.orphanGrace = 50 * time.Millisecond

	exec.listPods = []executor.PodListEntry{{Name: "orphan", Running: true}}

	// First call: grace window not yet elapsed, orphan is only recorded.
	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := exec.getRemoves(); len(got) != 0 {
		t.Fatalf("expected no RemovePod on first sighting, got %v", got)
	}

	// Wait past the grace window and reconcile again.
	time.Sleep(75 * time.Millisecond)
	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}

	removes := exec.getRemoves()
	if len(removes) != 1 || removes[0] != "orphan" {
		t.Fatalf("expected RemovePod(\"orphan\") after grace window, got %v", removes)
	}
}

func TestRecoverPodsSkipsRecentOrphans(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)
	r.orphanGrace = 10 * time.Second

	exec.listPods = []executor.PodListEntry{{Name: "fresh", Running: true}}

	// Multiple reconcile passes within the grace window: must not remove.
	for i := 0; i < 3; i++ {
		if err := r.RecoverPods(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if got := exec.getRemoves(); len(got) != 0 {
		t.Fatalf("expected RemovePod NOT to be called for fresh orphan, got %v", got)
	}
}

func TestRecoverPodsLogsOnRemoveFailure(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	exec.removeErr = errors.New("boom")
	r := NewReconciler(store, sched, exec, time.Second)
	r.orphanGrace = 20 * time.Millisecond

	exec.listPods = []executor.PodListEntry{{Name: "orphan", Running: true}}

	// First tick records first-seen.
	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	// Second tick attempts remove (which fails) — must not panic or error.
	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatalf("RecoverPods returned error on remove failure: %v", err)
	}
	removes := exec.getRemoves()
	if len(removes) != 1 || removes[0] != "orphan" {
		t.Fatalf("expected RemovePod attempt, got %v", removes)
	}
	// Orphan still present — must not be forgotten, so a retry remains possible.
	if _, ok := r.orphanFirstSeen["orphan"]; !ok {
		t.Fatal("expected orphan first-seen to persist after remove failure")
	}

	// Third tick retries (remove still fails) — confirm no state corruption / infinite loop.
	if err := r.RecoverPods(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(exec.getRemoves()); got != 2 {
		t.Fatalf("expected 2 RemovePod attempts total, got %d", got)
	}
}
