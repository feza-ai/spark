package reconciler

import (
	"context"
	"errors"
	"io"
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
	podStats    map[string]executor.PodResourceUsage
	podStatsErr error
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

	// Verify victim status is Preempted.
	rec, ok := store.Get("victim")
	if !ok {
		t.Fatal("victim pod not found in store")
	}
	if rec.Status != state.StatusPreempted {
		t.Fatalf("expected victim status Preempted, got %s", rec.Status)
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

	// Verify both victims are preempted.
	rec1, _ = store.Get("low1")
	rec2, _ = store.Get("low2")
	if rec1.Status != state.StatusPreempted || rec2.Status != state.StatusPreempted {
		t.Fatalf("expected both victims Preempted, got %s and %s", rec1.Status, rec2.Status)
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
