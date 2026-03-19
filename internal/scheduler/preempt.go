package scheduler

import (
	"context"
	"log/slog"
)

// PreemptFunc is called to stop a victim pod. Returns error if stop fails.
type PreemptFunc func(ctx context.Context, podName string, gracePeriod int) error

// defaultGracePeriod is the fallback termination grace period in seconds.
const defaultGracePeriod = 30

// Preempt executes preemption of victim pods.
// For each victim: calls stopFn, releases resources from tracker, calls onEvicted callback.
// Returns list of successfully evicted pod names.
func (s *Scheduler) Preempt(ctx context.Context, victims []string, podStates map[string]PodInfo, stopFn PreemptFunc, onEvicted func(podName string)) ([]string, error) {
	var evicted []string

	for _, victim := range victims {
		gracePeriod := defaultGracePeriod

		if err := stopFn(ctx, victim, gracePeriod); err != nil {
			slog.Warn("failed to stop victim pod", "pod", victim, "error", err)
			continue
		}

		s.tracker.Release(victim)
		s.RemovePod(victim)

		if onEvicted != nil {
			onEvicted(victim)
		}

		evicted = append(evicted, victim)
	}

	return evicted, nil
}
