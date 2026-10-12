package reconciler

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// Wake requests a reconciliation pass without starting another lifecycle loop.
func (r *Reconciler) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// reconcileOnDemand handles dormant/evicting phases before normal scheduling.
func (r *Reconciler) reconcileOnDemand(ctx context.Context, pod state.PodRecord) bool {
	if pod.Status == state.StatusStopping {
		r.stopIdlePod(ctx, pod)
		return true
	}
	if pod.Spec.OnDemand == nil {
		return false
	}
	if pod.Status == state.StatusDormant {
		if pod.InFlight == 0 && !pod.QueueDemand && !pod.Spec.OnDemand.AlwaysWarm {
			return true
		}
		if pod.Spec.OnDemand.ActivationMode() == "github" {
			if bo := r.podBackoff[pod.Spec.Name]; bo != nil && r.now().Before(bo.notBefore) {
				return true
			}
		} else {
			delete(r.podBackoff, pod.Spec.Name)
		}
		r.store.ClearStartFailure(pod.Spec.Name)
		r.updateStatus(pod.Spec.Name, state.StatusPending, "activating on-demand workload")
		r.reconcilePending(ctx, pod)
		return true
	}
	if r.store.StopRetiredWorker(pod.Spec.Name) || r.store.TryIdleStop(pod.Spec.Name, r.now()) {
		r.updateStatus(pod.Spec.Name, state.StatusStopping, "workload idle or worker completed; unloading workload")
		r.stopIdlePod(ctx, pod)
		return true
	}
	return false
}

func (r *Reconciler) stopIdlePod(ctx context.Context, pod state.PodRecord) {
	name := pod.Spec.Name
	// Keep the reservation through failed cleanup. Releasing it while a GPU
	// process survives would allow another workload to overcommit the device.
	grace := pod.Spec.TerminationGracePeriodSeconds
	if grace <= 0 {
		grace = 10
	}
	if err := r.executor.StopPod(ctx, name, grace); err != nil && !podAbsent(err) {
		r.store.AddEvent(name, "IdleStopFailed", err.Error())
		return
	}
	if err := r.executor.RemovePod(ctx, name); err != nil && !podAbsent(err) {
		r.store.AddEvent(name, "IdleStopFailed", err.Error())
		return
	}
	r.scheduler.RemovePod(name)
	r.store.SetAssignedCores(name, nil)
	delete(r.probeStates, name)
	delete(r.containerRestarts, name)
	if pod.Spec.OnDemand == nil || pod.Spec.OnDemand.ActivationMode() != "github" {
		delete(r.podBackoff, name)
	}
	if r.onPodStopped != nil {
		r.onPodStopped(name)
	}
	r.updateStatus(name, state.StatusDormant, "workload unloaded; awaiting requests")
	// A request that arrived during shutdown must not wait for the next tick.
	r.Wake()
}

func podAbsent(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such pod")
}

// checkReadiness uses the declared host port and never follows redirects. The
// application must return 2xx only after it can serve real work/model requests.
func (r *Reconciler) checkReadiness(ctx context.Context, pod state.PodRecord) {
	c := pod.Spec.OnDemand
	if c == nil {
		return
	}
	if c.ActivationMode() == "github" {
		r.store.SetReady(pod.Spec.Name, true)
		return
	}
	if len(c.ReadinessCommand) > 0 {
		code, err := r.executor.ExecProbe(ctx, pod.Spec.Name, c.ReadinessContainer, c.ReadinessCommand, time.Second)
		r.store.SetReady(pod.Spec.Name, err == nil && code == 0)
		return
	}
	if c.ActivationMode() == "tcp" {
		dialer := net.Dialer{Timeout: time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.HostPort)))
		if err == nil {
			conn.Close()
		}
		r.store.SetReady(pod.Spec.Name, err == nil)
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.BackendURL()+c.ReadinessPath, nil)
	if err != nil {
		r.store.SetReady(pod.Spec.Name, false)
		return
	}
	resp, err := r.readinessClient.Do(req)
	ready := false
	if err == nil {
		ready = resp.StatusCode >= 200 && resp.StatusCode < 300
		resp.Body.Close()
	}
	r.store.SetReady(pod.Spec.Name, ready)
	if ready != pod.Ready {
		message := "application readiness check failed"
		if ready {
			message = "application is ready for requests"
		}
		r.store.AddEvent(pod.Spec.Name, "ReadinessChanged", message)
		slog.Info(message, "pod", pod.Spec.Name)
	}
}

// retireCompletedWorker observes the runner process itself, not eventually
// consistent queue metadata or a completion hook that fires before job end.
func (r *Reconciler) retireCompletedWorker(pod state.PodRecord, st executor.Status) bool {
	c := pod.Spec.OnDemand
	if c == nil || c.ActivationMode() != "github" {
		return false
	}
	finished := !st.Running
	exitCode := st.ExitCode
	for _, container := range st.Containers {
		if container.Name == pod.Spec.Name+"-"+c.WorkerContainer && !container.Running {
			finished = true
			exitCode = container.ExitCode
		}
	}
	if !finished {
		return false
	}
	if r.store.RetireWorker(pod.Spec.Name) {
		if exitCode != 0 {
			r.nextPodBackoff(pod)
		} else {
			delete(r.podBackoff, pod.Spec.Name)
		}
		r.store.AddEvent(pod.Spec.Name, "WorkerRetired", "one-job worker exited; waiting for new queue demand or idle cleanup")
	}
	return true
}

// withGatewayHosts keeps service names reachable when the backend pod is absent.
// These aliases are runtime-only and leave the submitted manifest intact.
func (r *Reconciler) withGatewayHosts(spec manifest.PodSpec) manifest.PodSpec {
	seen := map[string]bool{}
	for _, service := range r.store.List("") {
		if service.Spec.OnDemand == nil || service.Spec.OnDemand.ActivationMode() != "tcp" {
			continue
		}
		for _, name := range []string{service.Spec.Name, service.Spec.SourceName} {
			if name != "" && name != spec.Name && !seen[name] {
				spec.GatewayHosts = append(spec.GatewayHosts, name)
				seen[name] = true
			}
		}
	}
	return spec
}
