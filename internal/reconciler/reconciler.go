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
