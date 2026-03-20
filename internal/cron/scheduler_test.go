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

func TestTriggerFiresAtCorrectTime(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time
		tickAt   time.Time
		wantFire bool
	}{
		{
			name:     "every minute fires after 1 min",
			schedule: "* * * * *",
			lastRun:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			tickAt:   time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC),
			wantFire: true,
		},
		{
			name:     "every minute does not fire at same minute",
			schedule: "* * * * *",
			lastRun:  time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC),
			tickAt:   time.Date(2026, 1, 1, 0, 5, 30, 0, time.UTC),
			wantFire: false,
		},
		{
			name:     "hourly fires at top of next hour",
			schedule: "0 * * * *",
			lastRun:  time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
			tickAt:   time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC),
			wantFire: true,
		},
		{
			name:     "hourly does not fire mid-hour",
			schedule: "0 * * * *",
			lastRun:  time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
			tickAt:   time.Date(2026, 1, 1, 1, 30, 0, 0, time.UTC),
			wantFire: false,
		},
		{
			name:     "specific day of week fires on matching day",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC), // Sunday
			tickAt:   time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC), // Monday
			wantFire: true,
		},
		{
			name:     "specific day of week skips non-matching day",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC), // Sunday
			tickAt:   time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC), // Tuesday
			wantFire: true, // Next after Sunday is Monday 9:00, which is before Tuesday 9:00
		},
		{
			name:     "zero lastRun fires immediately",
			schedule: "30 2 * * *",
			lastRun:  time.Time{},
			tickAt:   time.Date(2026, 6, 15, 3, 0, 0, 0, time.UTC),
			wantFire: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			cs := NewCronScheduler(store)

			spec := newTestSpec("trigger-test", tt.schedule, "Allow")
			if err := cs.Register(spec); err != nil {
				t.Fatal(err)
			}

			// Set lastRun directly.
			cs.mu.Lock()
			cs.jobs["trigger-test"].lastRun = tt.lastRun
			cs.mu.Unlock()

			cs.tick(tt.tickAt)

			pods := store.List("")
			fired := len(pods) > 0
			if fired != tt.wantFire {
				t.Fatalf("wantFire=%v but fired=%v (pods=%d)", tt.wantFire, fired, len(pods))
			}
		})
	}
}

func TestForbidPolicyResumesAfterCompletion(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	if err := cs.Register(newTestSpec("job-forbid", "* * * * *", "Forbid")); err != nil {
		t.Fatal(err)
	}

	// First tick creates a job.
	t1 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	cs.tick(t1)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after first tick, got %d", len(pods))
	}

	// Second tick skips because job is still active.
	t2 := t1.Add(2 * time.Minute)
	cs.tick(t2)

	pods = store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod (Forbid skipped), got %d", len(pods))
	}

	// Mark the active job as completed.
	store.UpdateStatus("job-forbid-1", state.StatusCompleted, "done")

	// Third tick should now create a new job since no active job exists.
	t3 := t2.Add(2 * time.Minute)
	cs.tick(t3)

	pods = store.List("")
	var found bool
	for _, p := range pods {
		if p.Spec.Name == "job-forbid-2" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected job-forbid-2 to be created after active job completed")
	}
}

func TestReplacePolicyDeletesMultipleActiveJobs(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	// Use Allow first to create multiple active pods, then switch to Replace.
	if err := cs.Register(newTestSpec("job-replace", "* * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	// Create two jobs with Allow policy.
	t1 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	cs.tick(t1)
	t2 := t1.Add(2 * time.Minute)
	cs.tick(t2)

	pods := store.List("")
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}

	// Mark one as running, leave other as pending -- both are active.
	store.UpdateStatus("job-replace-1", state.StatusRunning, "running")

	// Switch to Replace policy and tick again.
	cs.mu.Lock()
	cs.jobs["job-replace"].spec.ConcurrencyPolicy = "Replace"
	cs.mu.Unlock()

	t3 := t2.Add(2 * time.Minute)
	cs.tick(t3)

	// Both active jobs should be deleted, only the new one should remain.
	pods = store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after replace, got %d", len(pods))
	}
	if pods[0].Spec.Name != "job-replace-3" {
		t.Fatalf("expected job-replace-3, got %s", pods[0].Spec.Name)
	}
}

func TestFailedJobsHistoryPruning(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	spec := newTestSpec("job-fail-prune", "* * * * *", "Allow")
	spec.SuccessfulJobsHistoryLimit = 3
	spec.FailedJobsHistoryLimit = 1

	if err := cs.Register(spec); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create 3 jobs, marking each as failed before the next tick.
	for i := 0; i < 3; i++ {
		tickTime := base.Add(time.Duration(i+1) * 2 * time.Minute)

		for _, p := range store.List("") {
			if p.Status == state.StatusPending {
				store.UpdateStatus(p.Spec.Name, state.StatusFailed, "error")
			}
		}

		cs.tick(tickTime)
	}

	// Mark the last pending pod as failed.
	for _, p := range store.List("") {
		if p.Status == state.StatusPending {
			store.UpdateStatus(p.Spec.Name, state.StatusFailed, "error")
		}
	}

	// One more tick to trigger final pruning.
	cs.tick(base.Add(8 * time.Minute))

	var failed int
	for _, p := range store.List("") {
		if p.Status == state.StatusFailed {
			failed++
		}
	}
	if failed > spec.FailedJobsHistoryLimit {
		t.Fatalf("expected at most %d failed pods, got %d", spec.FailedJobsHistoryLimit, failed)
	}
}

func TestBackoffLimitPropagation(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	spec := newTestSpec("job-backoff", "* * * * *", "Allow")
	spec.BackoffLimit = 7

	if err := cs.Register(spec); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	cs.tick(now)

	pods := store.List("")
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}
	if pods[0].Spec.BackoffLimit != 7 {
		t.Fatalf("expected BackoffLimit 7, got %d", pods[0].Spec.BackoffLimit)
	}
}

func TestList(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	// Empty list.
	if got := cs.List(); len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}

	// Register two jobs.
	if err := cs.Register(newTestSpec("beta", "0 * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}
	if err := cs.Register(newTestSpec("alpha", "*/5 * * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	list := cs.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
	// Should be sorted by name.
	if list[0].Name != "alpha" {
		t.Fatalf("expected first entry alpha, got %s", list[0].Name)
	}
	if list[1].Name != "beta" {
		t.Fatalf("expected second entry beta, got %s", list[1].Name)
	}
	if list[0].Schedule != "*/5 * * * *" {
		t.Fatalf("expected schedule */5 * * * *, got %s", list[0].Schedule)
	}
	if list[0].RunCount != 0 {
		t.Fatalf("expected RunCount 0, got %d", list[0].RunCount)
	}

	// Tick to update lastRun and runCount.
	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	cs.tick(now)

	list = cs.List()
	for _, s := range list {
		if s.Name == "alpha" {
			if s.RunCount != 1 {
				t.Fatalf("expected RunCount 1 for alpha, got %d", s.RunCount)
			}
			if s.LastRun != now {
				t.Fatalf("expected LastRun %v, got %v", now, s.LastRun)
			}
			if s.NextRun.IsZero() {
				t.Fatal("expected non-zero NextRun")
			}
		}
	}
}

func TestGet(t *testing.T) {
	store := state.NewPodStore()
	cs := NewCronScheduler(store)

	// Not found.
	if _, ok := cs.Get("missing"); ok {
		t.Fatal("expected not found")
	}

	if err := cs.Register(newTestSpec("myjob", "30 2 * * *", "Allow")); err != nil {
		t.Fatal(err)
	}

	status, ok := cs.Get("myjob")
	if !ok {
		t.Fatal("expected to find myjob")
	}
	if status.Name != "myjob" {
		t.Fatalf("expected name myjob, got %s", status.Name)
	}
	if status.Schedule != "30 2 * * *" {
		t.Fatalf("expected schedule 30 2 * * *, got %s", status.Schedule)
	}
	if status.RunCount != 0 {
		t.Fatalf("expected RunCount 0, got %d", status.RunCount)
	}
	if status.NextRun.IsZero() {
		t.Fatal("expected non-zero NextRun")
	}

	// After tick, RunCount should update.
	now := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)
	cs.tick(now)

	status, ok = cs.Get("myjob")
	if !ok {
		t.Fatal("expected to find myjob after tick")
	}
	if status.RunCount != 1 {
		t.Fatalf("expected RunCount 1, got %d", status.RunCount)
	}
	if status.LastRun != now {
		t.Fatalf("expected LastRun %v, got %v", now, status.LastRun)
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
