package lifecycle

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
	mu         sync.Mutex
	stopCalls  []string
	removeCalls []string
	stopErr    error
	stopDelay  time.Duration
}

func (s *stubExecutor) CreatePod(_ context.Context, _ manifest.PodSpec) error { return nil }

func (s *stubExecutor) StopPod(ctx context.Context, name string, _ int) error {
	if s.stopDelay > 0 {
		select {
		case <-time.After(s.stopDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopCalls = append(s.stopCalls, name)
	return s.stopErr
}

func (s *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return executor.Status{}, nil
}

func (s *stubExecutor) RemovePod(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeCalls = append(s.removeCalls, name)
	return nil
}

func (s *stubExecutor) ListPods(_ context.Context) ([]executor.PodListEntry, error) {
	return nil, nil
}

func (s *stubExecutor) PodStats(_ context.Context, _ string) (executor.PodResourceUsage, error) {
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

func (s *stubExecutor) getStopCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.stopCalls))
	copy(cp, s.stopCalls)
	return cp
}

func (s *stubExecutor) getRemoveCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.removeCalls))
	copy(cp, s.removeCalls)
	return cp
}

func newTestScheduler() *scheduler.Scheduler {
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 0},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	)
	return scheduler.NewScheduler(tracker)
}

func applyRunningPod(store *state.PodStore, name string) {
	store.Apply(manifest.PodSpec{
		Name: name,
		Containers: []manifest.ContainerSpec{
			{Name: "main", Image: "alpine:latest"},
		},
	})
	store.UpdateStatus(name, state.StatusRunning, "started")
}

func TestDrain_AllPodsStop(t *testing.T) {
	store := state.NewPodStore()
	exec := &stubExecutor{}
	sched := newTestScheduler()

	applyRunningPod(store, "pod-a")
	applyRunningPod(store, "pod-b")
	applyRunningPod(store, "pod-c")

	sc := NewShutdownCoordinator(store, exec, sched, 5*time.Second)
	err := sc.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stops := exec.getStopCalls()
	if len(stops) != 3 {
		t.Fatalf("expected 3 stop calls, got %d", len(stops))
	}

	// All pods should be completed.
	for _, name := range []string{"pod-a", "pod-b", "pod-c"} {
		rec, ok := store.Get(name)
		if !ok {
			t.Fatalf("expected %s to exist", name)
		}
		if rec.Status != state.StatusCompleted {
			t.Fatalf("expected %s to be Completed, got %s", name, rec.Status)
		}
	}
}

func TestDrain_TimeoutForceKill(t *testing.T) {
	store := state.NewPodStore()
	exec := &stubExecutor{
		stopDelay: 5 * time.Second, // StopPod will block
		stopErr:   errors.New("stop failed"),
	}
	sched := newTestScheduler()

	applyRunningPod(store, "slow-pod")

	sc := NewShutdownCoordinator(store, exec, sched, 100*time.Millisecond)
	err := sc.Drain(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}

	// Force remove should have been called on the still-running pod.
	removes := exec.getRemoveCalls()
	if len(removes) == 0 {
		t.Fatal("expected RemovePod to be called for force kill")
	}

	// Pod should be marked failed.
	rec, ok := store.Get("slow-pod")
	if !ok {
		t.Fatal("expected slow-pod to exist")
	}
	if rec.Status != state.StatusFailed {
		t.Fatalf("expected Failed, got %s", rec.Status)
	}
}

func TestDrain_NoPods(t *testing.T) {
	store := state.NewPodStore()
	exec := &stubExecutor{}
	sched := newTestScheduler()

	sc := NewShutdownCoordinator(store, exec, sched, 5*time.Second)
	err := sc.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stops := exec.getStopCalls()
	if len(stops) != 0 {
		t.Fatalf("expected no stop calls, got %d", len(stops))
	}
}

func TestDrain_StopErrorFallsBackToRemove(t *testing.T) {
	store := state.NewPodStore()
	exec := &stubExecutor{
		stopErr: errors.New("stop failed"),
	}
	sched := newTestScheduler()

	applyRunningPod(store, "err-pod")

	sc := NewShutdownCoordinator(store, exec, sched, 5*time.Second)
	err := sc.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// RemovePod should have been called as fallback.
	removes := exec.getRemoveCalls()
	if len(removes) != 1 || removes[0] != "err-pod" {
		t.Fatalf("expected RemovePod for 'err-pod', got %v", removes)
	}

	// Pod should still be marked completed (drain succeeded within timeout).
	rec, _ := store.Get("err-pod")
	if rec.Status != state.StatusCompleted {
		t.Fatalf("expected Completed, got %s", rec.Status)
	}
}
