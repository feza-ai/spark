package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// probeState tracks liveness probe state for a running pod.
type probeState struct {
	consecutiveFailures int
	lastCheck           time.Time
	started             time.Time
}

// Reconciler drives the control loop that moves pods from desired to actual state.
type Reconciler struct {
	store     *state.PodStore
	scheduler *scheduler.Scheduler
	executor  executor.Executor
	interval  time.Duration

	probeStates map[string]*probeState

	// containerRestarts tracks per-container backoff state for in-place
	// restarts of crashed containers inside still-running pods (issue #46).
	// Keyed by pod name, then podman container name. Like probeStates,
	// only touched from the single reconcile goroutine.
	containerRestarts map[string]map[string]*containerRestartState

	// podBackoff tracks crash-loop backoff for whole-pod restarts
	// (issue #54). Only touched from the single reconcile goroutine.
	podBackoff map[string]*podRestartBackoff

	// orphanFirstSeen tracks the first time an orphaned podman pod was
	// observed. Orphans only become eligible for removal once they have
	// been seen for at least orphanGrace, avoiding a race with
	// handleApplyPod which writes the store record after podman pod
	// creation returns.
	orphanFirstSeen map[string]time.Time
	orphanGrace     time.Duration

	onStatusChange func(podName, status, message string)
	onPodRunning   func(podName string)
	onPodStopped   func(podName string)

	// now returns the current time. Tests inject a fake clock via SetClock.
	now func() time.Time
}

// defaultOrphanGrace is the minimum time an orphaned podman pod must be
// observed before the reconciler removes it. The window must cover the
// gap between podman pod creation and the subsequent store write in
// handleApplyPod.
const defaultOrphanGrace = 30 * time.Second

// NewReconciler creates a reconciler that ticks at the given interval.
func NewReconciler(store *state.PodStore, sched *scheduler.Scheduler, exec executor.Executor, interval time.Duration) *Reconciler {
	return &Reconciler{
		store:             store,
		scheduler:         sched,
		executor:          exec,
		interval:          interval,
		probeStates:       make(map[string]*probeState),
		containerRestarts: make(map[string]map[string]*containerRestartState),
		podBackoff:        make(map[string]*podRestartBackoff),
		orphanFirstSeen:   make(map[string]time.Time),
		orphanGrace:       defaultOrphanGrace,
		now:               time.Now,
	}
}

// SetClock overrides the time source. Intended for tests that need
// deterministic backoff behavior.
func (r *Reconciler) SetClock(now func() time.Time) {
	r.now = now
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

// recordStartFailure records a container-start failure on the pod record and
// fires the status-change callback so persistence layers pick up the new
// Reason/StartAttempts/LastAttemptAt. Returns the new attempt count. Status
// itself is not transitioned here — the caller decides whether to retry or
// terminate based on backoffLimit.
func (r *Reconciler) recordStartFailure(podName string, err error) int {
	reason := err.Error()
	attempts := r.store.RecordStartFailure(podName, reason, r.now())
	slog.Warn("pod container start failed",
		"pod", podName,
		"reason", reason,
		"attempts", attempts,
	)
	if r.onStatusChange != nil {
		rec, ok := r.store.Get(podName)
		if ok {
			r.onStatusChange(podName, string(rec.Status), "start failed: "+reason)
		}
	}
	return attempts
}

// maxBackoff caps the exponential backoff delay between retry attempts.
const maxBackoff = 60 * time.Second

// backoffDelay returns the delay required before the next retry after
// `attempts` failed attempts. Delay doubles from 5s with each attempt, capped
// at maxBackoff.
func backoffDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	d := 5 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// retryEligible reports whether enough time has elapsed since the last failed
// attempt to justify another CreatePod call.
func (r *Reconciler) retryEligible(rec state.PodRecord) bool {
	if rec.StartAttempts <= 0 || rec.LastAttemptAt.IsZero() {
		return true
	}
	return !r.now().Before(rec.LastAttemptAt.Add(backoffDelay(rec.StartAttempts)))
}

// terminateAfterStartFailure transitions a pod to Failed after exhausting its
// backoffLimit. It cleans up any podman pod best-effort and releases scheduler
// resources.
func (r *Reconciler) terminateAfterStartFailure(ctx context.Context, podName, reason string) {
	if err := r.executor.RemovePod(ctx, podName); err != nil {
		slog.Warn("best-effort RemovePod after backoff exhaustion failed", "pod", podName, "err", err)
	}
	r.scheduler.RemovePod(podName)
	r.updateStatus(podName, state.StatusFailed, "start failed after backoffLimit: "+reason)
	r.store.AddEvent(podName, "PodFailed", "start failed after backoffLimit: "+reason)
	slog.Error("pod failed terminally after backoffLimit", "pod", podName, "reason", reason)
}

// clearStartFailure resets Reason/StartAttempts after a successful start.
func (r *Reconciler) clearStartFailure(podName string) {
	prior, ok := r.store.Get(podName)
	if !ok || (prior.Reason == "" && prior.StartAttempts == 0) {
		return
	}
	r.store.ClearStartFailure(podName)
	if r.onStatusChange != nil {
		rec, ok := r.store.Get(podName)
		if ok {
			r.onStatusChange(podName, string(rec.Status), "start failure cleared")
		}
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
	now := time.Now()
	currentOrphans := make(map[string]struct{})
	for _, p := range podmanPods {
		rec, ok := r.store.Get(p.Name)
		if !ok {
			// Not in store: orphaned pod. Remove only after the pod has
			// been observed as an orphan for at least orphanGrace, so
			// we don't race handleApplyPod (which writes the store
			// record after podman pod creation completes).
			currentOrphans[p.Name] = struct{}{}
			firstSeen, seen := r.orphanFirstSeen[p.Name]
			if !seen {
				r.orphanFirstSeen[p.Name] = now
				slog.Warn("orphaned pod discovered in podman", "pod", p.Name)
				continue
			}
			if now.Sub(firstSeen) < r.orphanGrace {
				slog.Warn("orphaned pod discovered in podman", "pod", p.Name)
				continue
			}
			if err := r.executor.RemovePod(ctx, p.Name); err != nil {
				slog.Warn("failed to remove orphaned podman pod", "pod", p.Name, "error", err)
				slog.Warn("orphaned pod discovered in podman", "pod", p.Name)
				continue
			}
			slog.Info("removed orphaned podman pod", "pod", p.Name)
			delete(r.orphanFirstSeen, p.Name)
			continue
		}
		if rec.Status == state.StatusRunning {
			// Tracked as running, but this is a fresh process with an empty
			// scheduler ledger. Re-adopt so the pod's quota is held again —
			// otherwise admission overcommits into resources the survivors
			// are using (issue #53). Adopt on mere presence, not on the
			// pod-level Running state: a surviving pod can be Degraded at
			// startup (e.g. its infra container took a hit during the
			// restart) while its workload containers run on. If the pod is
			// genuinely dead, the next reconcile tick releases the quota.
			r.scheduler.AdoptPod(scheduler.PodInfo{
				Name:      rec.Spec.Name,
				Priority:  rec.Spec.Priority,
				Resources: rec.Spec.TotalRequests(),
				StartTime: rec.StartedAt,
			})
			slog.Info("re-adopted running pod after restart", "pod", p.Name, "podmanRunning", p.Running)
			continue
		}
		if p.Running {
			// Pod is running in podman but store says otherwise. Update store.
			r.store.UpdateStatus(p.Name, state.StatusRunning, "recovered: pod found running in podman after restart")
			// Re-register in scheduler, ledger included.
			r.scheduler.AdoptPod(scheduler.PodInfo{
				Name:      rec.Spec.Name,
				Priority:  rec.Spec.Priority,
				Resources: rec.Spec.TotalRequests(),
				StartTime: rec.StartedAt,
			})
			slog.Info("recovered pod", "pod", p.Name, "previousStatus", rec.Status)
		}
	}

	// Forget first-seen times for pods that are no longer orphans
	// (removed from podman, or since adopted into the store).
	for name := range r.orphanFirstSeen {
		if _, stillOrphan := currentOrphans[name]; !stillOrphan {
			delete(r.orphanFirstSeen, name)
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

	// Drop crash-loop state for pods that no longer exist (deleted via API).
	for name := range r.podBackoff {
		if _, ok := r.store.Get(name); !ok {
			delete(r.podBackoff, name)
		}
	}

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
	// Respect exponential backoff between failed attempts.
	if !r.retryEligible(pod) {
		slog.Debug("pod retry suppressed by backoff", "pod", pod.Spec.Name, "attempts", pod.StartAttempts)
		return
	}
	// Respect crash-loop backoff after exit-driven restarts (issue #54).
	// The pending reason already carries the delay, so stay quiet here.
	if bo := r.podBackoff[pod.Spec.Name]; bo != nil && r.now().Before(bo.notBefore) {
		return
	}

	result := r.scheduler.Schedule(pod.Spec)

	switch result.Action {
	case scheduler.Scheduled:
		slog.Info("pod scheduled", "pod", pod.Spec.Name)
		// Capture the core assignment so it survives a Spark restart; the
		// subsequent updateStatus triggers SavePod, which flushes it to
		// SQLite. Must happen after Allocate (inside Schedule) and before
		// updateStatus so the write lands in the same persistence pass.
		if cores := r.scheduler.AssignedCores(pod.Spec.Name); len(cores) > 0 {
			r.store.SetAssignedCores(pod.Spec.Name, cores)
		}
		// A non-empty Reason on a Scheduled result means utilization-aware
		// CPU overcommit admitted this pod despite an accounted CPU
		// shortfall (issue #76). Record it as an event so operators can
		// see why the pod's presence pushed accounted CPU negative.
		if result.Reason != "" {
			r.store.AddEvent(pod.Spec.Name, "CPUOvercommitAdmitted", result.Reason)
		}
		r.updateStatus(pod.Spec.Name, state.StatusScheduled, "scheduled by reconciler")

		pod.Spec.CpusetCores = r.scheduler.Tracker().AssignedCores(pod.Spec.Name)
		// Carry the scheduler's actual device assignment into the spec the
		// executor creates from. Without this, spec.GPUDevices always stays
		// empty and NVIDIA_VISIBLE_DEVICES is never set, decoupling what the
		// scheduler thinks it handed out from what the container can see
		// (issue #81).
		pod.Spec.GPUDevices = r.scheduler.Tracker().AssignedGPUs(pod.Spec.Name)

		if err := r.executor.CreatePod(ctx, pod.Spec); err != nil {
			slog.Error("failed to create pod", "pod", pod.Spec.Name, "err", err)
			attempts := r.recordStartFailure(pod.Spec.Name, err)
			r.scheduler.RemovePod(pod.Spec.Name)
			if pod.Spec.BackoffLimit > 0 && attempts > pod.Spec.BackoffLimit {
				r.terminateAfterStartFailure(ctx, pod.Spec.Name, err.Error())
				return
			}
			r.updateStatus(pod.Spec.Name, state.StatusPending, "create failed: "+err.Error())
			return
		}

		r.scheduler.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: time.Now(),
		})
		r.clearStartFailure(pod.Spec.Name)
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
			if cores := r.scheduler.AssignedCores(pod.Spec.Name); len(cores) > 0 {
				pod.Spec.CpusetCores = cores
				r.store.SetAssignedCores(pod.Spec.Name, cores)
			}
			pod.Spec.GPUDevices = r.scheduler.Tracker().AssignedGPUs(pod.Spec.Name)
			if retry.Reason != "" {
				r.store.AddEvent(pod.Spec.Name, "CPUOvercommitAdmitted", retry.Reason)
			}
			if err := r.executor.CreatePod(ctx, pod.Spec); err != nil {
				slog.Error("failed to create pod after preemption", "pod", pod.Spec.Name, "err", err)
				attempts := r.recordStartFailure(pod.Spec.Name, err)
				r.scheduler.RemovePod(pod.Spec.Name)
				if pod.Spec.BackoffLimit > 0 && attempts > pod.Spec.BackoffLimit {
					r.terminateAfterStartFailure(ctx, pod.Spec.Name, err.Error())
					return
				}
				r.updateStatus(pod.Spec.Name, state.StatusPending, "create failed after preemption: "+err.Error())
				return
			}
			r.scheduler.AddPod(scheduler.PodInfo{
				Name:      pod.Spec.Name,
				Priority:  pod.Spec.Priority,
				Resources: pod.Spec.TotalRequests(),
				StartTime: time.Now(),
			})
			r.clearStartFailure(pod.Spec.Name)
			r.updateStatus(pod.Spec.Name, state.StatusRunning, "pod created after preemption")
			slog.Info("pod running after preemption", "pod", pod.Spec.Name)
			if r.onPodRunning != nil {
				r.onPodRunning(pod.Spec.Name)
			}
		} else {
			reason := retry.Reason
			if reason == "" {
				reason = "still not schedulable after preemption"
			}
			msg := "awaiting-resources: " + reason
			slog.Warn("pod still not schedulable after preemption", "pod", pod.Spec.Name, "reason", reason)
			r.updateStatus(pod.Spec.Name, state.StatusPending, msg)
			r.store.AddEvent(pod.Spec.Name, "PendingWatchdog", msg)
		}

	case scheduler.Pending:
		// Watchdog: every Pending reconcile MUST emit at least one
		// observable signal so operators can see why a pod is stuck.
		reason := result.Reason
		if reason == "" {
			reason = "scheduler reported Pending without a reason"
		}
		msg := "awaiting-resources: " + reason
		slog.Info("pod awaiting resources", "pod", pod.Spec.Name, "reason", reason)
		r.updateStatus(pod.Spec.Name, state.StatusPending, msg)
		r.store.AddEvent(pod.Spec.Name, "PendingWatchdog", msg)
	}
}

// reconcileRunning checks the actual status of running pods and handles failures.
func (r *Reconciler) reconcileRunning(ctx context.Context, pod state.PodRecord) {
	st, err := r.executor.PodStatus(ctx, pod.Spec.Name)
	switch {
	case err == nil:
		// fall through to normal handling below.
	case isNoSuchPod(err):
		// Issue #37: the podman pod has been removed behind Spark's back
		// (silent exit, OOM, manual `podman pod rm`). Treat as a non-running
		// container with an unknown exit code so the policy switch below
		// either restarts it or marks it failed; either way the scheduler
		// resources are released so the quota does not leak.
		slog.Warn("running pod missing in podman; recovering", "pod", pod.Spec.Name, "err", err)
		st = executor.Status{Running: false, ExitCode: -1}
		r.store.AddEvent(pod.Spec.Name, "lost", "podman pod missing; releasing resources")
	default:
		slog.Error("failed to get pod status", "pod", pod.Spec.Name, "err", err)
		return
	}

	if st.Running {
		// The pod is up, but individual containers may have exited
		// (podman "Degraded"). Kubernetes semantics: restartPolicy applies
		// per container — restart the crashed ones in place, never their
		// healthy siblings (issue #46).
		r.restartCrashedContainers(ctx, pod, st.Containers)
		// Check liveness probes for the first container.
		r.checkLivenessProbe(ctx, pod)
		return
	}

	// Pod is no longer running in the executor.
	slog.Info("pod exited", "pod", pod.Spec.Name, "exitCode", st.ExitCode)
	if r.onPodStopped != nil {
		r.onPodStopped(pod.Spec.Name)
	}

	// Clean up probe and container-restart state for exited pods.
	delete(r.probeStates, pod.Spec.Name)
	delete(r.containerRestarts, pod.Spec.Name)

	// Release scheduler resources.
	r.scheduler.RemovePod(pod.Spec.Name)

	policy := pod.Spec.RestartPolicy

	switch {
	case policy == "Always":
		// Service-style: always restart, with crash-loop backoff (issue #54).
		delay := r.nextPodBackoff(pod)
		r.store.IncrementRestarts(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending,
			fmt.Sprintf("restarting (policy=Always), crash-loop backoff %s", delay))
		slog.Info("pod rescheduled", "pod", pod.Spec.Name, "reason", "restart-always", "backoff", delay)

	case policy == "OnFailure" && st.ExitCode != 0 && pod.RetryCount < pod.Spec.BackoffLimit:
		// Job with retries remaining, same crash-loop backoff.
		delay := r.nextPodBackoff(pod)
		r.store.IncrementRestarts(pod.Spec.Name)
		r.store.IncrementRetry(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending,
			fmt.Sprintf("retrying after failure, crash-loop backoff %s", delay))
		slog.Info("pod retry scheduled", "pod", pod.Spec.Name, "retry", pod.RetryCount+1, "limit", pod.Spec.BackoffLimit, "backoff", delay)

	default:
		// Never policy, success, or retries exhausted.
		delete(r.podBackoff, pod.Spec.Name)
		if st.ExitCode == 0 {
			r.updateStatus(pod.Spec.Name, state.StatusCompleted, "exited successfully")
			slog.Info("pod completed", "pod", pod.Spec.Name)
		} else {
			r.updateStatus(pod.Spec.Name, state.StatusFailed, "exited with error")
			slog.Info("pod failed", "pod", pod.Spec.Name, "exitCode", st.ExitCode)
		}
	}
}

// podRestartBackoff tracks the crash-loop schedule for one pod's whole-pod
// restarts (policy Always / OnFailure recreates).
type podRestartBackoff struct {
	count     int
	notBefore time.Time
}

const (
	podBackoffBase       = 10 * time.Second
	podBackoffMax        = 5 * time.Minute
	podBackoffResetAfter = 10 * time.Minute
)

// nextPodBackoff computes the delay before this pod may be recreated, and
// records it. Delays double from podBackoffBase to podBackoffMax, mirroring
// Kubernetes CrashLoopBackOff. A pod that ran cleanly for at least
// podBackoffResetAfter before exiting starts the schedule over.
func (r *Reconciler) nextPodBackoff(pod state.PodRecord) time.Duration {
	now := r.now()
	bo := r.podBackoff[pod.Spec.Name]
	if bo == nil {
		bo = &podRestartBackoff{}
		r.podBackoff[pod.Spec.Name] = bo
	}
	if !pod.StartedAt.IsZero() && now.Sub(pod.StartedAt) >= podBackoffResetAfter {
		bo.count = 0
	}
	delay := podBackoffBase
	for i := 0; i < bo.count && delay < podBackoffMax; i++ {
		delay *= 2
	}
	if delay > podBackoffMax {
		delay = podBackoffMax
	}
	bo.count++
	bo.notBefore = now.Add(delay)
	return delay
}

// containerRestartState tracks the exponential-backoff schedule for
// in-place restarts of one crashed container.
type containerRestartState struct {
	count       int
	lastAttempt time.Time
}

const (
	containerRestartBaseDelay = 10 * time.Second
	containerRestartMaxDelay  = 5 * time.Minute
)

// restartCrashedContainers restarts exited workload containers of a pod
// that is still running, per the pod's restartPolicy. The containers list
// is only populated by the executor when the pod is degraded, so the
// common all-healthy case is a no-op. Never restarts pod siblings: podman
// start brings an exited container back with its config and filesystem
// intact.
func (r *Reconciler) restartCrashedContainers(ctx context.Context, pod state.PodRecord, containers []executor.ContainerStatus) {
	for _, c := range containers {
		if c.IsInfra || c.Running {
			continue
		}
		switch policy := pod.Spec.RestartPolicy; {
		case policy == "Always":
			// Restart regardless of exit code.
		case policy == "OnFailure" && c.ExitCode != 0:
			// Restart on failure. Per Kubernetes semantics, in-place
			// container restarts do not count against the pod BackoffLimit;
			// that budget applies to whole-pod failures.
		default:
			// Never, or OnFailure after a clean exit: leave the container
			// exited. The pod-level verdict engages once all workload
			// containers have exited.
			continue
		}
		if !r.containerBackoffElapsed(pod.Spec.Name, c.Name) {
			continue
		}
		if err := r.executor.StartContainer(ctx, c.Name); err != nil {
			slog.Warn("container restart failed", "pod", pod.Spec.Name, "container", c.Name, "err", err)
			continue
		}
		slog.Info("container restarted in place", "pod", pod.Spec.Name, "container", c.Name, "exitCode", c.ExitCode)
		r.store.IncrementRestarts(pod.Spec.Name)
		r.store.AddEvent(pod.Spec.Name, "container-restarted",
			fmt.Sprintf("restarted exited container %s (exit code %d); siblings untouched", c.Name, c.ExitCode))
	}
}

// containerBackoffElapsed reports whether the container is due for another
// restart attempt, and records the attempt when it is. Delays double from
// containerRestartBaseDelay up to containerRestartMaxDelay; the first
// restart is immediate. State is per pod and cleared when the pod exits.
func (r *Reconciler) containerBackoffElapsed(podName, containerName string) bool {
	pods := r.containerRestarts[podName]
	if pods == nil {
		pods = make(map[string]*containerRestartState)
		r.containerRestarts[podName] = pods
	}
	s := pods[containerName]
	if s == nil {
		pods[containerName] = &containerRestartState{count: 1, lastAttempt: r.now()}
		return true
	}
	delay := containerRestartBaseDelay
	for i := 1; i < s.count && delay < containerRestartMaxDelay; i++ {
		delay *= 2
	}
	if delay > containerRestartMaxDelay {
		delay = containerRestartMaxDelay
	}
	if r.now().Sub(s.lastAttempt) < delay {
		return false
	}
	s.count++
	s.lastAttempt = r.now()
	return true
}

// scheduledStaleness is how long a pod can stay in StatusScheduled with the
// podman pod missing before being reset to StatusPending for retry. Chosen
// shorter than the reconciler tick (5s) so that one subsequent tick is enough
// to retry, while still longer than the brief window between updateStatus
// (Scheduled) and CreatePod returning in reconcilePending.
const scheduledStaleness = 10 * time.Second

// isNoSuchPod reports whether err is the podman "no such pod" error, which
// indicates the pod does not exist in podman state — distinct from transient
// executor failures (network glitches, podman daemon hiccups).
func isNoSuchPod(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such pod")
}

// reconcileScheduled handles pods stuck in the Scheduled state. If the pod is
// actually running in podman, it transitions to Running. If the podman pod is
// missing ("no such pod") and has been Scheduled longer than
// scheduledStaleness — meaning a prior CreatePod either failed partway or was
// killed — it resets to Pending so reconcilePending can retry (gated by
// retryEligible/BackoffLimit, which already wire exponential backoff and
// terminal failure after too many attempts).
func (r *Reconciler) reconcileScheduled(ctx context.Context, pod state.PodRecord) {
	st, err := r.executor.PodStatus(ctx, pod.Spec.Name)
	switch {
	case err == nil:
		// status fetch succeeded; fall through to normal handling below.
	case isNoSuchPod(err):
		// Pod is missing in podman. Treat as "not running" and proceed to
		// staleness check so we retry via reconcilePending.
		slog.Warn("scheduled pod missing in podman", "pod", pod.Spec.Name, "err", err)
		st = executor.Status{Running: false}
	default:
		// Transient error (daemon unavailable, etc.) — don't flip state.
		slog.Error("failed to get pod status for scheduled pod", "pod", pod.Spec.Name, "err", err)
		return
	}

	if st.Running {
		r.scheduler.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: r.now(),
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

	// Corner case: pod is persisted as StatusScheduled but has no Scheduled
	// event (e.g. SQL persistence was interrupted mid-transition). Without
	// the event we cannot apply staleness, but the pod is demonstrably
	// wedged — it's in Scheduled with no podman pod backing it. Reset
	// immediately so reconcilePending can retry on the next tick. This
	// only fires on the missing-pod path; transient inspect errors
	// returned earlier.
	if scheduledAt.IsZero() && isNoSuchPod(err) {
		r.scheduler.RemovePod(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending, "reset from scheduled: pod missing with no scheduled-event timestamp")
		r.store.AddEvent(pod.Spec.Name, "ScheduledRetry", "podman pod missing and no scheduled-event timestamp; re-queued")
		slog.Warn("scheduled pod with missing event reset to pending", "pod", pod.Spec.Name)
		return
	}

	if scheduledAt.IsZero() || r.now().Sub(scheduledAt) <= scheduledStaleness {
		return
	}

	// Stale: check BackoffLimit before re-queueing. If we've exceeded
	// backoffLimit attempts to start, transition to Failed terminally
	// rather than spinning in Pending → Scheduled → missing forever.
	if pod.Spec.BackoffLimit > 0 && pod.StartAttempts > pod.Spec.BackoffLimit {
		reason := pod.Reason
		if reason == "" {
			reason = "pod missing in podman after " + scheduledStaleness.String()
		}
		r.terminateAfterStartFailure(ctx, pod.Spec.Name, reason)
		return
	}

	// Re-queue for retry. reconcilePending gates retries on retryEligible,
	// which enforces exponential backoff off LastAttemptAt.
	r.scheduler.RemovePod(pod.Spec.Name)
	r.updateStatus(pod.Spec.Name, state.StatusPending, "reset from scheduled: pod not found after timeout")
	r.store.AddEvent(pod.Spec.Name, "ScheduledRetry", "podman pod missing; re-queued for create retry")
	slog.Warn("scheduled pod reset to pending", "pod", pod.Spec.Name, "attempts", pod.StartAttempts)
}

// checkLivenessProbe checks the liveness probe of the first container in a running pod.
// If the probe fails FailureThreshold consecutive times, the pod is stopped and set to
// StatusPending to trigger a restart.
func (r *Reconciler) checkLivenessProbe(ctx context.Context, pod state.PodRecord) {
	if len(pod.Spec.Containers) == 0 {
		return
	}
	probe := pod.Spec.Containers[0].LivenessProbe
	if probe == nil {
		return
	}

	now := time.Now()
	ps, ok := r.probeStates[pod.Spec.Name]
	if !ok {
		// First time seeing this pod: initialize probe state.
		r.probeStates[pod.Spec.Name] = &probeState{
			started: now,
		}
		return
	}

	// Respect InitialDelaySeconds.
	if now.Sub(ps.started) < time.Duration(probe.InitialDelaySeconds)*time.Second {
		return
	}

	// Respect PeriodSeconds.
	period := probe.PeriodSeconds
	if period <= 0 {
		period = 10
	}
	if !ps.lastCheck.IsZero() && now.Sub(ps.lastCheck) < time.Duration(period)*time.Second {
		return
	}

	ps.lastCheck = now

	timeout := time.Duration(probe.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Second
	}

	// Execute the probe.
	var probeErr error
	if probe.Exec != nil {
		containerName := pod.Spec.Containers[0].Name
		exitCode, err := r.executor.ExecProbe(ctx, pod.Spec.Name, containerName, probe.Exec.Command, timeout)
		if err != nil {
			probeErr = err
		} else if exitCode != 0 {
			probeErr = fmt.Errorf("exec probe exited %d", exitCode)
		}
	} else if probe.HTTPGet != nil {
		probeErr = r.executor.HTTPProbe(ctx, probe.HTTPGet.Port, probe.HTTPGet.Path, timeout)
	}

	if probeErr == nil {
		// Success: reset failure counter.
		ps.consecutiveFailures = 0
		return
	}

	// Failure.
	ps.consecutiveFailures++
	threshold := probe.FailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	slog.Warn("liveness probe failed", "pod", pod.Spec.Name, "failures", ps.consecutiveFailures, "threshold", threshold, "err", probeErr)

	if ps.consecutiveFailures >= threshold {
		slog.Info("liveness probe exceeded failure threshold, restarting pod", "pod", pod.Spec.Name)

		// Stop the pod.
		gracePeriod := pod.Spec.TerminationGracePeriodSeconds
		if gracePeriod == 0 {
			gracePeriod = 10
		}
		if err := r.executor.StopPod(ctx, pod.Spec.Name, gracePeriod); err != nil {
			slog.Error("failed to stop pod after liveness failure", "pod", pod.Spec.Name, "err", err)
			return
		}

		if r.onPodStopped != nil {
			r.onPodStopped(pod.Spec.Name)
		}

		// Clean up probe state and scheduler resources.
		delete(r.probeStates, pod.Spec.Name)
		r.scheduler.RemovePod(pod.Spec.Name)

		// Increment restarts and set to Pending to trigger reschedule.
		r.store.IncrementRestarts(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending, "liveness probe failed")
	}
}

// CleanupProbeState removes probe state for a pod. Call when a pod is deleted.
func (r *Reconciler) CleanupProbeState(podName string) {
	delete(r.probeStates, podName)
}

// reconcilePreempted resets a preempted pod to Pending so the scheduler can
// re-evaluate it when resources become available.
func (r *Reconciler) reconcilePreempted(pod state.PodRecord) {
	r.updateStatus(pod.Spec.Name, state.StatusPending, "re-queued after preemption")
	slog.Info("preempted pod re-queued", "pod", pod.Spec.Name)
}
