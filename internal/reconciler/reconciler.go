package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// Reconciler drives the control loop that moves pods from desired to actual state.
type Reconciler struct {
	store     *state.PodStore
	scheduler *scheduler.Scheduler
	executor  executor.Executor
	interval  time.Duration

	onStatusChange func(podName, status, message string)
	onPodRunning   func(podName string)
	onPodStopped   func(podName string)
}

// NewReconciler creates a reconciler that ticks at the given interval.
func NewReconciler(store *state.PodStore, sched *scheduler.Scheduler, exec executor.Executor, interval time.Duration) *Reconciler {
	return &Reconciler{
		store:     store,
		scheduler: sched,
		executor:  exec,
		interval:  interval,
	}
}

// SetOnStatusChange registers a callback invoked after every pod status update.
func (r *Reconciler) SetOnStatusChange(fn func(podName, status, message string)) {
	r.onStatusChange = fn
}

// OnPodRunning registers a callback invoked when a pod enters Running state.
func (r *Reconciler) OnPodRunning(fn func(podName string)) {
	r.onPodRunning = fn
}

// OnPodStopped registers a callback invoked when a pod exits Running state.
func (r *Reconciler) OnPodStopped(fn func(podName string)) {
	r.onPodStopped = fn
}

// updateStatus updates the pod status in the store and fires the status change callback.
func (r *Reconciler) updateStatus(podName string, status state.PodStatus, message string) {
	r.store.UpdateStatus(podName, status, message)
	if r.onStatusChange != nil {
		r.onStatusChange(podName, string(status), message)
	}
}

// RecoverPods reconciles the store with the actual podman state after a restart.
func (r *Reconciler) RecoverPods(ctx context.Context) error {
	// 1. Call executor.ListPods(ctx) to get all podman pods.
	podmanPods, err := r.executor.ListPods(ctx)
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	// Build a set of podman pod names for fast lookup.
	podmanSet := make(map[string]bool, len(podmanPods))
	for _, p := range podmanPods {
		podmanSet[p.Name] = p.Running
	}

	// 2. For each podman pod, check if it exists in store.
	for _, p := range podmanPods {
		rec, ok := r.store.Get(p.Name)
		if !ok {
			// Not in store: orphaned pod. Log warning, don't adopt.
			slog.Warn("orphaned pod discovered in podman", "pod", p.Name)
			continue
		}
		if rec.Status == state.StatusRunning {
			// Already tracked as running, no action needed.
			continue
		}
		if p.Running {
			// Pod is running in podman but store says otherwise. Update store.
			r.store.UpdateStatus(p.Name, state.StatusRunning, "recovered: pod found running in podman after restart")
			// Re-register in scheduler.
			r.scheduler.AddPod(scheduler.PodInfo{
				Name:      rec.Spec.Name,
				Priority:  rec.Spec.Priority,
				Resources: rec.Spec.TotalRequests(),
				StartTime: rec.StartedAt,
			})
			slog.Info("recovered pod", "pod", p.Name, "previousStatus", rec.Status)
		}
	}

	// 3. For each pod in store with status Running, check if in podman.
	for _, rec := range r.store.List(state.StatusRunning) {
		if _, inPodman := podmanSet[rec.Spec.Name]; !inPodman {
			// Running in store but not in podman: pod was lost.
			r.store.UpdateStatus(rec.Spec.Name, state.StatusFailed, "pod not found in podman after restart")
			r.scheduler.RemovePod(rec.Spec.Name)
			slog.Warn("pod marked failed: not found in podman", "pod", rec.Spec.Name)
		}
	}

	return nil
}

// Run starts the reconciliation loop. Blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce performs a single reconciliation pass.
func (r *Reconciler) reconcileOnce(ctx context.Context) {
	pods := r.store.List("")

	for _, pod := range pods {
		// Re-read current status; a prior iteration may have changed it (e.g. preemption).
		current, ok := r.store.Get(pod.Spec.Name)
		if !ok {
			continue
		}
		switch current.Status {
		case state.StatusPending:
			r.reconcilePending(ctx, current)
		case state.StatusRunning:
			r.reconcileRunning(ctx, current)
		case state.StatusScheduled:
			r.reconcileScheduled(ctx, current)
		case state.StatusPreempted:
			r.reconcilePreempted(current)
		}
	}
}

// reconcilePending attempts to schedule and create a pending pod.
func (r *Reconciler) reconcilePending(ctx context.Context, pod state.PodRecord) {
	result := r.scheduler.Schedule(pod.Spec)

	switch result.Action {
	case scheduler.Scheduled:
		slog.Info("pod scheduled", "pod", pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusScheduled, "scheduled by reconciler")

		if err := r.executor.CreatePod(ctx, pod.Spec); err != nil {
			slog.Error("failed to create pod", "pod", pod.Spec.Name, "err", err)
			r.updateStatus(pod.Spec.Name, state.StatusPending, "create failed: "+err.Error())
			r.scheduler.RemovePod(pod.Spec.Name)
			return
		}

		r.scheduler.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: time.Now(),
		})
		r.updateStatus(pod.Spec.Name, state.StatusRunning, "pod created and running")
		slog.Info("pod running", "pod", pod.Spec.Name)
		if r.onPodRunning != nil {
			r.onPodRunning(pod.Spec.Name)
		}

	case scheduler.Preempting:
		slog.Info("pod requires preemption", "pod", pod.Spec.Name, "victims", result.Victims)

		// Stop each victim pod and release its resources.
		for _, victim := range result.Victims {
			if err := r.executor.StopPod(ctx, victim, 10); err != nil {
				slog.Error("failed to stop victim pod", "victim", victim, "err", err)
				return
			}
			r.scheduler.RemovePod(victim)
			r.updateStatus(victim, state.StatusPreempted, "preempted for "+pod.Spec.Name)
			slog.Info("victim preempted", "victim", victim, "preemptedBy", pod.Spec.Name)
		}

		// Re-schedule the pending pod now that resources are freed.
		retry := r.scheduler.Schedule(pod.Spec)
		if retry.Action == scheduler.Scheduled {
			if err := r.executor.CreatePod(ctx, pod.Spec); err != nil {
				slog.Error("failed to create pod after preemption", "pod", pod.Spec.Name, "err", err)
				r.updateStatus(pod.Spec.Name, state.StatusPending, "create failed after preemption: "+err.Error())
				r.scheduler.RemovePod(pod.Spec.Name)
				return
			}
			r.scheduler.AddPod(scheduler.PodInfo{
				Name:      pod.Spec.Name,
				Priority:  pod.Spec.Priority,
				Resources: pod.Spec.TotalRequests(),
				StartTime: time.Now(),
			})
			r.updateStatus(pod.Spec.Name, state.StatusRunning, "pod created after preemption")
			slog.Info("pod running after preemption", "pod", pod.Spec.Name)
			if r.onPodRunning != nil {
				r.onPodRunning(pod.Spec.Name)
			}
		} else {
			slog.Warn("pod still not schedulable after preemption", "pod", pod.Spec.Name)
		}

	case scheduler.Pending:
		slog.Debug("pod cannot be scheduled yet", "pod", pod.Spec.Name)
	}
}

// reconcileRunning checks the actual status of running pods and handles failures.
func (r *Reconciler) reconcileRunning(ctx context.Context, pod state.PodRecord) {
	st, err := r.executor.PodStatus(ctx, pod.Spec.Name)
	if err != nil {
		slog.Error("failed to get pod status", "pod", pod.Spec.Name, "err", err)
		return
	}

	if st.Running {
		return
	}

	// Pod is no longer running in the executor.
	slog.Info("pod exited", "pod", pod.Spec.Name, "exitCode", st.ExitCode)
	if r.onPodStopped != nil {
		r.onPodStopped(pod.Spec.Name)
	}

	// Release scheduler resources.
	r.scheduler.RemovePod(pod.Spec.Name)

	policy := pod.Spec.RestartPolicy

	switch {
	case policy == "Always":
		// Service-style: always restart.
		r.updateStatus(pod.Spec.Name, state.StatusPending, "restarting (policy=Always)")
		slog.Info("pod rescheduled", "pod", pod.Spec.Name, "reason", "restart-always")

	case policy == "OnFailure" && st.ExitCode != 0 && pod.RetryCount < pod.Spec.BackoffLimit:
		// Job with retries remaining.
		r.store.IncrementRetry(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending, "retrying after failure")
		slog.Info("pod retry scheduled", "pod", pod.Spec.Name, "retry", pod.RetryCount+1, "limit", pod.Spec.BackoffLimit)

	default:
		// Never policy, success, or retries exhausted.
		if st.ExitCode == 0 {
			r.updateStatus(pod.Spec.Name, state.StatusCompleted, "exited successfully")
			slog.Info("pod completed", "pod", pod.Spec.Name)
		} else {
			r.updateStatus(pod.Spec.Name, state.StatusFailed, "exited with error")
			slog.Info("pod failed", "pod", pod.Spec.Name, "exitCode", st.ExitCode)
		}
	}
}

// scheduledStaleness is how long a pod can stay in StatusScheduled before
// being reset to StatusPending for retry.
const scheduledStaleness = 30 * time.Second

// reconcileScheduled handles pods stuck in the Scheduled state. If the pod is
// actually running in podman, it transitions to Running. If the pod is not
// found and has been Scheduled for longer than scheduledStaleness, it resets
// to Pending so the scheduler can retry.
func (r *Reconciler) reconcileScheduled(ctx context.Context, pod state.PodRecord) {
	st, err := r.executor.PodStatus(ctx, pod.Spec.Name)
	if err != nil {
		slog.Error("failed to get pod status for scheduled pod", "pod", pod.Spec.Name, "err", err)
		return
	}

	if st.Running {
		r.scheduler.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: time.Now(),
		})
		r.updateStatus(pod.Spec.Name, state.StatusRunning, "pod found running in podman")
		slog.Info("scheduled pod now running", "pod", pod.Spec.Name)
		if r.onPodRunning != nil {
			r.onPodRunning(pod.Spec.Name)
		}
		return
	}

	// Pod is not running. Check if it has been stuck long enough to retry.
	var scheduledAt time.Time
	for i := len(pod.Events) - 1; i >= 0; i-- {
		if pod.Events[i].Type == string(state.StatusScheduled) {
			scheduledAt = pod.Events[i].Time
			break
		}
	}
	if scheduledAt.IsZero() || time.Since(scheduledAt) <= scheduledStaleness {
		return
	}

	// Stale: reset to Pending.
	r.scheduler.RemovePod(pod.Spec.Name)
	r.updateStatus(pod.Spec.Name, state.StatusPending, "reset from scheduled: pod not found after timeout")
	slog.Warn("scheduled pod reset to pending", "pod", pod.Spec.Name)
}

// reconcilePreempted resets a preempted pod to Pending so the scheduler can
// re-evaluate it when resources become available.
func (r *Reconciler) reconcilePreempted(pod state.PodRecord) {
	r.updateStatus(pod.Spec.Name, state.StatusPending, "re-queued after preemption")
	slog.Info("preempted pod re-queued", "pod", pod.Spec.Name)
}
