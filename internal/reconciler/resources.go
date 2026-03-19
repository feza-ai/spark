package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// ResourceReconciler periodically checks actual resource usage against tracked allocations.
type ResourceReconciler struct {
	store    *state.PodStore
	executor executor.Executor
	tracker  *scheduler.ResourceTracker
	interval time.Duration
}

// NewResourceReconciler creates a ResourceReconciler that ticks at the given interval.
func NewResourceReconciler(store *state.PodStore, exec executor.Executor, tracker *scheduler.ResourceTracker, interval time.Duration) *ResourceReconciler {
	return &ResourceReconciler{
		store:    store,
		executor: exec,
		tracker:  tracker,
		interval: interval,
	}
}

// Run starts the resource reconciliation loop. Blocks until ctx is cancelled.
func (rr *ResourceReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(rr.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("resource reconciler stopped")
			return
		case <-ticker.C:
			rr.ReconcileOnce(ctx)
		}
	}
}

// ReconcileOnce performs a single resource reconciliation pass.
func (rr *ResourceReconciler) ReconcileOnce(ctx context.Context) {
	// 1. Check GPU usage
	gpuInfo, err := gpu.Detect()
	if err == nil {
		allocated := rr.tracker.Allocated()
		if allocated.GPUMemoryMB > 0 {
			drift := float64(gpuInfo.MemoryUsedMB-allocated.GPUMemoryMB) / float64(allocated.GPUMemoryMB)
			if drift > 0.2 || drift < -0.2 {
				slog.Warn("GPU memory drift detected",
					"allocated_mb", allocated.GPUMemoryMB,
					"actual_mb", gpuInfo.MemoryUsedMB,
					"drift_pct", int(drift*100),
				)
			}
		}
		if gpuInfo.UtilizationPercent > 90 {
			slog.Warn("GPU utilization high", "percent", gpuInfo.UtilizationPercent)
		}
	}

	// 2. Check per-pod resource usage
	pods := rr.store.List(state.StatusRunning)
	for _, pod := range pods {
		stats, err := rr.executor.PodStats(ctx, pod.Spec.Name)
		if err != nil {
			slog.Error("failed to get pod stats", "pod", pod.Spec.Name, "err", err)
			continue
		}

		requested := pod.Spec.TotalRequests()

		// Check memory drift
		if requested.MemoryMB > 0 && stats.MemoryMB > int(float64(requested.MemoryMB)*1.5) {
			slog.Warn("pod exceeds requested memory",
				"pod", pod.Spec.Name,
				"requested_mb", requested.MemoryMB,
				"actual_mb", stats.MemoryMB,
			)
		}

		// Update tracker with actual values.
		// Preserve original CPU and GPU requests (PodStats doesn't provide these in comparable units).
		actual := manifest.ResourceList{
			CPUMillis:   requested.CPUMillis,
			MemoryMB:    stats.MemoryMB,
			GPUMemoryMB: requested.GPUMemoryMB,
		}
		rr.tracker.UpdateAllocation(pod.Spec.Name, actual)
	}
}
