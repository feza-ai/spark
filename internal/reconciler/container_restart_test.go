package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

// Issue #46: restartPolicy applies per container. Only the crashed container
// restarts (in place, via podman start); pod siblings keep running.

// degradedStatus builds the executor.Status a degraded pod reports: pod
// running, one healthy workload container, one exited with the given code.
func degradedStatus(pod string, exitCode int) executor.Status {
	return executor.Status{
		Running: true,
		Containers: []executor.ContainerStatus{
			{Name: pod + "-infra", Running: true, IsInfra: true},
			{Name: pod + "-healthy", Running: true},
			{Name: pod + "-crashy", Running: false, ExitCode: exitCode},
		},
	}
}

// startRunningPod drives a pod through schedule+create so the store has it
// Running and the stub executor controls its reported status.
func startRunningPod(t *testing.T, name, restartPolicy string) (*state.PodStore, *stubExecutor, *Reconciler) {
	t.Helper()
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	spec := testPodSpec(name, restartPolicy, 0)
	store.Apply(spec)
	r.reconcileOnce(context.Background())
	r.reconcileOnce(context.Background())

	rec, _ := store.Get(name)
	if rec.Status != state.StatusRunning {
		t.Fatalf("setup: expected Running, got %s", rec.Status)
	}
	return store, exec, r
}

func TestContainerRestart_AlwaysRestartsOnlyCrashedContainer(t *testing.T) {
	store, exec, r := startRunningPod(t, "svc", "Always")

	exec.mu.Lock()
	exec.statuses["svc"] = degradedStatus("svc", 1)
	exec.mu.Unlock()

	r.reconcileOnce(context.Background())

	exec.mu.Lock()
	starts := slices.Clone(exec.containerStarts)
	exec.mu.Unlock()

	if !slices.Contains(starts, "svc-crashy") {
		t.Fatalf("expected svc-crashy to be restarted, got %v", starts)
	}
	if slices.Contains(starts, "svc-healthy") || slices.Contains(starts, "svc-infra") {
		t.Errorf("healthy/infra containers must not be restarted, got %v", starts)
	}
	if len(exec.creates) != 1 {
		t.Errorf("pod must not be recreated (CreatePod calls = %d)", len(exec.creates))
	}
	if len(exec.stops) != 0 || len(exec.removes) != 0 {
		t.Errorf("pod must not be stopped/removed: stops=%v removes=%v", exec.stops, exec.removes)
	}

	rec, _ := store.Get("svc")
	if rec.Status != state.StatusRunning {
		t.Errorf("pod should stay Running, got %s", rec.Status)
	}
	if rec.Restarts != 1 {
		t.Errorf("expected restarts=1, got %d", rec.Restarts)
	}
	var sawEvent bool
	for _, e := range rec.Events {
		if e.Type == "container-restarted" {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Errorf("expected container-restarted event, got %+v", rec.Events)
	}
}

func TestContainerRestart_NeverPolicyLeavesContainerDown(t *testing.T) {
	store, exec, r := startRunningPod(t, "job", "Never")

	exec.mu.Lock()
	exec.statuses["job"] = degradedStatus("job", 1)
	exec.mu.Unlock()

	r.reconcileOnce(context.Background())

	exec.mu.Lock()
	starts := len(exec.containerStarts)
	exec.mu.Unlock()
	if starts != 0 {
		t.Errorf("Never policy must not restart containers, got %v", exec.containerStarts)
	}
	rec, _ := store.Get("job")
	if rec.Status != state.StatusRunning {
		t.Errorf("pod with a still-running workload container stays Running, got %s", rec.Status)
	}
}

func TestContainerRestart_OnFailureSkipsCleanExit(t *testing.T) {
	_, exec, r := startRunningPod(t, "wrk", "OnFailure")

	// Helper container exited 0 — a completed step, not a failure.
	exec.mu.Lock()
	exec.statuses["wrk"] = degradedStatus("wrk", 0)
	exec.mu.Unlock()

	r.reconcileOnce(context.Background())

	exec.mu.Lock()
	starts := len(exec.containerStarts)
	exec.mu.Unlock()
	if starts != 0 {
		t.Errorf("OnFailure must not restart a container that exited 0, got %v", exec.containerStarts)
	}
}

func TestContainerRestart_OnFailureRestartsNonZeroExit(t *testing.T) {
	_, exec, r := startRunningPod(t, "wrk", "OnFailure")

	exec.mu.Lock()
	exec.statuses["wrk"] = degradedStatus("wrk", 3)
	exec.mu.Unlock()

	r.reconcileOnce(context.Background())

	exec.mu.Lock()
	starts := slices.Clone(exec.containerStarts)
	exec.mu.Unlock()
	if !slices.Contains(starts, "wrk-crashy") {
		t.Errorf("expected wrk-crashy restarted under OnFailure, got %v", starts)
	}
}

func TestContainerRestart_BackoffDoublesBetweenAttempts(t *testing.T) {
	_, exec, r := startRunningPod(t, "svc", "Always")

	clock := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clock })

	exec.mu.Lock()
	exec.statuses["svc"] = degradedStatus("svc", 1)
	exec.mu.Unlock()

	countStarts := func() int {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return len(exec.containerStarts)
	}

	// First restart is immediate.
	r.reconcileOnce(context.Background())
	if got := countStarts(); got != 1 {
		t.Fatalf("expected 1 start after first tick, got %d", got)
	}

	// Still crashed on the next tick — inside the 10s base delay, no restart.
	clock = clock.Add(5 * time.Second)
	r.reconcileOnce(context.Background())
	if got := countStarts(); got != 1 {
		t.Fatalf("expected no restart inside base delay, got %d starts", got)
	}

	// Past the base delay — second restart.
	clock = clock.Add(6 * time.Second)
	r.reconcileOnce(context.Background())
	if got := countStarts(); got != 2 {
		t.Fatalf("expected second restart after base delay, got %d starts", got)
	}

	// The delay doubled to 20s: 11s later is too soon.
	clock = clock.Add(11 * time.Second)
	r.reconcileOnce(context.Background())
	if got := countStarts(); got != 2 {
		t.Fatalf("expected no restart inside doubled delay, got %d starts", got)
	}

	// 10 more seconds crosses the 20s threshold.
	clock = clock.Add(10 * time.Second)
	r.reconcileOnce(context.Background())
	if got := countStarts(); got != 3 {
		t.Fatalf("expected third restart after doubled delay, got %d starts", got)
	}
}

func TestContainerRestart_StateClearedWhenPodExits(t *testing.T) {
	store, exec, r := startRunningPod(t, "svc", "Always")

	exec.mu.Lock()
	exec.statuses["svc"] = degradedStatus("svc", 1)
	exec.mu.Unlock()
	r.reconcileOnce(context.Background())

	if _, ok := r.containerRestarts["svc"]; !ok {
		t.Fatal("expected backoff state for svc after a restart")
	}

	// Whole pod exits now.
	exec.mu.Lock()
	exec.statuses["svc"] = executor.Status{Running: false, ExitCode: 1}
	exec.mu.Unlock()
	r.reconcileOnce(context.Background())

	if _, ok := r.containerRestarts["svc"]; ok {
		t.Error("expected backoff state cleared after pod exit")
	}
	if rec, _ := store.Get("svc"); rec.Status != state.StatusPending {
		t.Errorf("Always pod should be rescheduled after exit, got %s", rec.Status)
	}
}
