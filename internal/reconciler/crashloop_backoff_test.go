package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

// Issue #54: whole-pod restarts (policy Always/OnFailure) recreated the pod
// at a flat per-tick interval — 128 restarts in ~54 minutes observed, each
// re-running full container startup. Restarts must back off exponentially.

func TestCrashLoop_BackoffDelaysRecreate(t *testing.T) {
	store := state.NewPodStore()
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(store, sched, exec, time.Second)

	clock := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clock })

	spec := testPodSpec("loop", "Always", 0)
	store.Apply(spec)

	// Tick 1: initial schedule and create (no backoff for first start).
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 1 {
		t.Fatalf("expected 1 create, got %d", got)
	}

	// Crash. Tick detects it and queues with 10s backoff.
	exec.setStatus("loop", executor.Status{Running: false, ExitCode: 1})
	r.reconcileOnce(context.Background())

	// 5s later (one reconcile tick): still inside backoff — no recreate.
	clock = clock.Add(5 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 1 {
		t.Fatalf("recreated inside 10s backoff: %d creates", got)
	}

	// 6 more seconds: past 10s — recreate happens.
	clock = clock.Add(6 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 2 {
		t.Fatalf("expected recreate after backoff, got %d creates", got)
	}

	// Crash again: the delay doubles to 20s.
	exec.setStatus("loop", executor.Status{Running: false, ExitCode: 1})
	r.reconcileOnce(context.Background())

	clock = clock.Add(15 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 2 {
		t.Fatalf("recreated inside doubled 20s backoff: %d creates", got)
	}

	clock = clock.Add(6 * time.Second)
	r.reconcileOnce(context.Background())
	if got := len(exec.getCreates()); got != 3 {
		t.Fatalf("expected recreate after doubled backoff, got %d creates", got)
	}

	// The pending event should surface the backoff to operators.
	exec.setStatus("loop", executor.Status{Running: false, ExitCode: 1})
	r.reconcileOnce(context.Background())
	rec, _ := store.Get("loop")
	if rec.Status != state.StatusPending {
		t.Fatalf("expected pending after third crash, got %s", rec.Status)
	}
	last := rec.Events[len(rec.Events)-1]
	if !strings.Contains(last.Message, "crash-loop backoff") {
		t.Fatalf("expected backoff in pending event, got %q", last.Message)
	}
}

func TestNextPodBackoff_CapsAtMax(t *testing.T) {
	r := NewReconciler(state.NewPodStore(), newTestScheduler(), newStubExecutor(), time.Second)
	clock := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clock })

	rec := state.PodRecord{Spec: testPodSpec("cap", "Always", 0)}
	var last time.Duration
	for i := 0; i < 12; i++ {
		last = r.nextPodBackoff(rec)
	}
	if last != podBackoffMax {
		t.Errorf("expected cap %s after many crashes, got %s", podBackoffMax, last)
	}
}

func TestNextPodBackoff_ResetsAfterCleanRun(t *testing.T) {
	r := NewReconciler(state.NewPodStore(), newTestScheduler(), newStubExecutor(), time.Second)
	clock := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clock })

	rec := state.PodRecord{Spec: testPodSpec("healed", "Always", 0)}

	// Ramp the schedule up.
	for i := 0; i < 5; i++ {
		r.nextPodBackoff(rec)
	}

	// This exit comes after a clean 10+ minute run: schedule starts over.
	rec.StartedAt = clock.Add(-11 * time.Minute)
	if got := r.nextPodBackoff(rec); got != podBackoffBase {
		t.Errorf("expected reset to base %s after clean run, got %s", podBackoffBase, got)
	}
}
