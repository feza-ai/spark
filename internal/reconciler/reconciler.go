package reconciler

import (
	"context"
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
		switch pod.Status {
		case state.StatusPending:
			r.reconcilePending(ctx, pod)
		case state.StatusRunning:
			r.reconcileRunning(ctx, pod)
		}
	}
}

// reconcilePending attempts to schedule and create a pending pod.
func (r *Reconciler) reconcilePending(ctx context.Context, pod state.PodRecord) {
	result := r.scheduler.Schedule(pod.Spec)

	switch result.Action {
	case scheduler.Scheduled:
		slog.Info("pod scheduled", "pod", pod.Spec.Name)
		r.store.UpdateStatus(pod.Spec.Name, state.StatusScheduled, "scheduled by reconciler")

		if err := r.executor.CreatePod(ctx, pod.Spec); err != nil {
			slog.Error("failed to create pod", "pod", pod.Spec.Name, "err", err)
			r.store.UpdateStatus(pod.Spec.Name, state.StatusPending, "create failed: "+err.Error())
			r.scheduler.RemovePod(pod.Spec.Name)
			return
		}

		r.scheduler.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: time.Now(),
		})
		r.store.UpdateStatus(pod.Spec.Name, state.StatusRunning, "pod created and running")
		slog.Info("pod running", "pod", pod.Spec.Name)

	case scheduler.Preempting:
		slog.Info("pod requires preemption", "pod", pod.Spec.Name, "victims", result.Victims)

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

	// Release scheduler resources.
	r.scheduler.RemovePod(pod.Spec.Name)

	policy := pod.Spec.RestartPolicy

	switch {
	case policy == "Always":
		// Service-style: always restart.
		r.store.UpdateStatus(pod.Spec.Name, state.StatusPending, "restarting (policy=Always)")
		slog.Info("pod rescheduled", "pod", pod.Spec.Name, "reason", "restart-always")

	case policy == "OnFailure" && st.ExitCode != 0 && pod.RetryCount < pod.Spec.BackoffLimit:
		// Job with retries remaining.
		r.store.IncrementRetry(pod.Spec.Name)
		r.store.UpdateStatus(pod.Spec.Name, state.StatusPending, "retrying after failure")
		slog.Info("pod retry scheduled", "pod", pod.Spec.Name, "retry", pod.RetryCount+1, "limit", pod.Spec.BackoffLimit)

	default:
		// Never policy, success, or retries exhausted.
		if st.ExitCode == 0 {
			r.store.UpdateStatus(pod.Spec.Name, state.StatusCompleted, "exited successfully")
			slog.Info("pod completed", "pod", pod.Spec.Name)
		} else {
			r.store.UpdateStatus(pod.Spec.Name, state.StatusFailed, "exited with error")
			slog.Info("pod failed", "pod", pod.Spec.Name, "exitCode", st.ExitCode)
		}
	}
}
