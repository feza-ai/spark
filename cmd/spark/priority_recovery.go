package main

import (
	"fmt"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// reconcilePersistedPriorities applies the current class table before recovered
// pods enter the scheduler. Persisting each change keeps the database and
// in-memory scheduler consistent across subsequent restarts.
func reconcilePersistedPriorities(pods map[string]*state.PodRecord, classes map[string]int, save func(*state.PodRecord) error) (int, error) {
	changed := 0
	for name, rec := range pods {
		className := rec.Spec.PriorityClassName
		if className == "" {
			className = "normal"
		}
		priority := manifest.ResolvePriority(classes, className)
		if rec.Spec.Priority == priority {
			continue
		}
		previous := rec.Spec.Priority
		rec.Spec.Priority = priority
		if err := save(rec); err != nil {
			rec.Spec.Priority = previous
			return changed, fmt.Errorf("persist priority for %s: %w", name, err)
		}
		changed++
	}
	return changed, nil
}
