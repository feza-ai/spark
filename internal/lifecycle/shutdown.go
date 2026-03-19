package lifecycle

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// ShutdownCoordinator orchestrates graceful shutdown by draining running pods.
type ShutdownCoordinator struct {
	store     *state.PodStore
	executor  executor.Executor
	scheduler *scheduler.Scheduler
	timeout   time.Duration
}

// NewShutdownCoordinator creates a new shutdown coordinator.
func NewShutdownCoordinator(store *state.PodStore, exec executor.Executor, sched *scheduler.Scheduler, timeout time.Duration) *ShutdownCoordinator {
	return &ShutdownCoordinator{
		store:     store,
		executor:  exec,
		scheduler: sched,
		timeout:   timeout,
	}
}

// Drain stops all running pods concurrently, waiting up to the configured timeout.
// Pods that do not stop in time are force-killed.
func (sc *ShutdownCoordinator) Drain(ctx context.Context) error {
	pods := sc.store.List(state.StatusRunning)
	if len(pods) == 0 {
		slog.Info("no running pods to drain")
		return nil
	}

	slog.Info("draining pods", "count", len(pods))

	drainCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, pod := range pods {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			gracePeriod := int(sc.timeout.Seconds())
			if err := sc.executor.StopPod(drainCtx, name, gracePeriod); err != nil {
				slog.Error("failed to stop pod during drain", "pod", name, "err", err)
				// If the drain context expired, mark as failed and force remove.
				if drainCtx.Err() != nil {
					sc.executor.RemovePod(context.Background(), name)
					sc.store.UpdateStatus(name, state.StatusFailed, "force-killed during shutdown timeout")
					sc.scheduler.RemovePod(name)
					slog.Info("pod drained", "pod", name)
					return
				}
				if err := sc.executor.RemovePod(drainCtx, name); err != nil {
					slog.Error("failed to force remove pod", "pod", name, "err", err)
				}
			}
			sc.store.UpdateStatus(name, state.StatusCompleted, "drained during shutdown")
			sc.scheduler.RemovePod(name)
			slog.Info("pod drained", "pod", name)
		}(pod.Spec.Name)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Check if any pod ended up failed (due to timeout in goroutine).
		for _, pod := range pods {
			rec, ok := sc.store.Get(pod.Spec.Name)
			if ok && rec.Status == state.StatusFailed {
				return context.DeadlineExceeded
			}
		}
		slog.Info("all pods drained successfully")
		return nil
	case <-drainCtx.Done():
		slog.Warn("drain timeout reached, force-killing remaining pods")
		// Wait for goroutines to finish their cleanup.
		<-done
		return drainCtx.Err()
	}
}
