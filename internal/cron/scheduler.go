package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// CronScheduler manages registered CronJobs and triggers them on schedule.
type CronScheduler struct {
	mu    sync.Mutex
	jobs  map[string]*cronEntry
	store *state.PodStore
}

type cronEntry struct {
	spec     manifest.CronJobSpec
	schedule Schedule
	lastRun  time.Time
	jobCount int
}

// NewCronScheduler creates a new CronScheduler backed by the given PodStore.
func NewCronScheduler(store *state.PodStore) *CronScheduler {
	return &CronScheduler{
		jobs:  make(map[string]*cronEntry),
		store: store,
	}
}

// Register adds a CronJob to be scheduled.
func (cs *CronScheduler) Register(spec manifest.CronJobSpec) error {
	sched, err := Parse(spec.Schedule)
	if err != nil {
		return fmt.Errorf("cron: invalid schedule for %q: %w", spec.Name, err)
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.jobs[spec.Name] = &cronEntry{
		spec:     spec,
		schedule: sched,
	}
	return nil
}

// Unregister removes a CronJob.
func (cs *CronScheduler) Unregister(name string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.jobs, name)
}

// Run checks all registered CronJobs every 30 seconds and creates Jobs when due.
func (cs *CronScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			cs.tick(t)
		}
	}
}

// tick performs a single check of all CronJobs.
func (cs *CronScheduler) tick(now time.Time) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for name, entry := range cs.jobs {
		next := entry.schedule.Next(entry.lastRun)
		if next.After(now) {
			continue
		}

		// It's time to fire. Check concurrency policy.
		switch entry.spec.ConcurrencyPolicy {
		case "Forbid":
			if cs.hasActiveJob(name) {
				slog.Info("cron: skipping job due to Forbid policy", "cronjob", name)
				continue
			}
		case "Replace":
			cs.deleteActiveJobs(name)
		}
		// "Allow" (default): always create.

		entry.jobCount++
		podSpec := entry.spec.JobTemplate
		podSpec.Name = fmt.Sprintf("%s-%d", name, entry.jobCount)
		podSpec.SourceKind = "CronJob"
		podSpec.SourceName = name
		podSpec.BackoffLimit = entry.spec.BackoffLimit

		cs.store.Apply(podSpec)
		entry.lastRun = now
		slog.Info("cron: created job", "cronjob", name, "job", podSpec.Name)

		cs.pruneHistory(entry)
	}
}

// hasActiveJob returns true if any pod from this CronJob is pending, scheduled, or running.
func (cs *CronScheduler) hasActiveJob(cronJobName string) bool {
	for _, rec := range cs.store.List("") {
		if rec.Spec.SourceKind == "CronJob" && rec.Spec.SourceName == cronJobName {
			if rec.Status == state.StatusPending || rec.Status == state.StatusScheduled || rec.Status == state.StatusRunning {
				return true
			}
		}
	}
	return false
}

// deleteActiveJobs deletes all active pods belonging to the named CronJob.
func (cs *CronScheduler) deleteActiveJobs(cronJobName string) {
	for _, rec := range cs.store.List("") {
		if rec.Spec.SourceKind == "CronJob" && rec.Spec.SourceName == cronJobName {
			if rec.Status == state.StatusPending || rec.Status == state.StatusScheduled || rec.Status == state.StatusRunning {
				cs.store.Delete(rec.Spec.Name)
				slog.Info("cron: deleted active job for Replace", "cronjob", cronJobName, "job", rec.Spec.Name)
			}
		}
	}
}

// pruneHistory removes excess completed and failed jobs beyond the configured limits.
func (cs *CronScheduler) pruneHistory(entry *cronEntry) {
	var completed, failed []state.PodRecord

	for _, rec := range cs.store.List("") {
		if rec.Spec.SourceKind != "CronJob" || rec.Spec.SourceName != entry.spec.Name {
			continue
		}
		switch rec.Status {
		case state.StatusCompleted:
			completed = append(completed, rec)
		case state.StatusFailed:
			failed = append(failed, rec)
		}
	}

	pruneOldest(cs.store, completed, entry.spec.SuccessfulJobsHistoryLimit)
	pruneOldest(cs.store, failed, entry.spec.FailedJobsHistoryLimit)
}

// pruneOldest deletes the oldest records if len(records) > limit.
func pruneOldest(store *state.PodStore, records []state.PodRecord, limit int) {
	if len(records) <= limit {
		return
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].FinishedAt.Before(records[j].FinishedAt)
	})
	excess := len(records) - limit
	for i := 0; i < excess; i++ {
		store.Delete(records[i].Spec.Name)
	}
}
