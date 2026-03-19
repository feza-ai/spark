package cron

import (
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func newTestSpec(name, schedule, policy string) manifest.CronJobSpec {
	return manifest.CronJobSpec{
		Name:                       name,
		Schedule:                   schedule,
		ConcurrencyPolicy:          policy,
		SuccessfulJobsHistoryLimit: 3,
		FailedJobsHistoryLimit:     1,
		JobTemplate: manifest.PodSpec{
			Containers: []manifest.ContainerSpec{
				{Name: "worker", Image: "busybox"},
			},
		},
		BackoffLimit: 3,
	}
}

func TestRegisterAndTick(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	// "* * * * *" fires every minute.
	if err := cs.Register(newTestSpec("job-a", "* * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	// Tick at a time that is after Next(zero), so it should fire.
	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	cs.tick(now)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}
	if pods[0].Spec.Name != "job-a-1" {
		t.Fatalf("expected pod name job-a-1, got %s", pods[0].Spec.Name)
	}
	if pods[0].Spec.SourceKind != "CronJob" {
		t.Fatalf("expected SourceKind CronJob, got %s", pods[0].Spec.SourceKind)
	}
	if pods[0].Spec.SourceName != "job-a" {
		t.Fatalf("expected SourceName job-a, got %s", pods[0].Spec.SourceName)
	}
}

func TestTickNoFireBeforeSchedule(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-b", "* * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	// Set lastRun to now so Next is in the future.
	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	cs.tick(now)

	// Tick again at same time -- lastRun is now, Next is 1 minute later, should not fire.
	cs.tick(now)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod (no second fire), got %d", len(pods))
	}
}

func TestForbidPolicy(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-c", "* * * * *", "Forbid")); err != nil {
		t.Fatal(err)
	}

	// First tick creates a job.
	t1 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	cs.tick(t1)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after first tick, got %d", len(pods))
	}

	// Pod is still pending (active). Second tick should skip.
	t2 := t1.Add(2 * time.Minute)
	cs.tick(t2)

	pods = store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected still 1 pod (Forbid skipped), got %d", len(pods))
	}
}

func TestReplacePolicy(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-d", "* * * * *", "Replace")); err != nil {
		t.Fatal(err)
	}

	// First tick creates a job.
	t1 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	cs.tick(t1)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after first tick, got %d", len(pods))
	}
	if pods[0].Spec.Name != "job-d-1" {
		t.Fatalf("expected job-d-1, got %s", pods[0].Spec.Name)
	}

	// Second tick should delete active and create new.
	t2 := t1.Add(2 * time.Minute)
	cs.tick(t2)

	pods = store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after replace, got %d", len(pods))
	}
	if pods[0].Spec.Name != "job-d-2" {
		t.Fatalf("expected job-d-2, got %s", pods[0].Spec.Name)
	}
}

func TestAllowPolicy(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-e", "* * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	cs.tick(t1)

	t2 := t1.Add(2 * time.Minute)
	cs.tick(t2)

	pods := store.List("")
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods (Allow creates regardless), got %d", len(pods))
	}
}

func TestHistoryPruning(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	spec := newTestSpec("job-f", "* * * * *", "Allow")
	spec.SuccessfulJobsHistoryLimit = 1
	spec.FailedJobsHistoryLimit = 1

	if err := cs.Register(spec); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create 3 jobs, marking each completed before the next tick so pruning sees them.
	for i := 0; i < 3; i++ {
		tickTime := base.Add(time.Duration(i+1) * 2 * time.Minute)

		// Before ticking, mark all pending pods as completed so pruning can act on them.
		for _, p := range store.List("") {
			if p.Status == state.StatusPending {
				store.UpdateStatus(p.Spec.Name, state.StatusCompleted, "done")
			}
		}

		cs.tick(tickTime)
	}

	// Mark the last pending pod as completed too.
	for _, p := range store.List("") {
		if p.Status == state.StatusPending {
			store.UpdateStatus(p.Spec.Name, state.StatusCompleted, "done")
		}
	}

	// One more tick to trigger final pruning.
	cs.tick(base.Add(8 * time.Minute))

	var completed int
	for _, p := range store.List("") {
		if p.Status == state.StatusCompleted {
			completed++
		}
	}
	if completed > spec.SuccessfulJobsHistoryLimit {
		t.Fatalf("expected at most %d completed pods, got %d", spec.SuccessfulJobsHistoryLimit, completed)
	}
}

func TestUnregister(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-g", "* * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	cs.Unregister("job-g")

	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	cs.tick(now)

	pods := store.List("")
	if len(pods) != 0 {
		t.Fatalf("expected 0 pods after unregister, got %d", len(pods))
	}
}
