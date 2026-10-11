package main

import (
	"errors"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func TestReconcilePersistedPriorities(t *testing.T) {
	classes := manifest.DefaultPriorityClasses()
	pods := map[string]*state.PodRecord{
		"old-high":  {Spec: manifest.PodSpec{Name: "old-high", PriorityClassName: "high", Priority: 100}},
		"old-low":   {Spec: manifest.PodSpec{Name: "old-low", PriorityClassName: "low", Priority: 10000}},
		"protected": {Spec: manifest.PodSpec{Name: "protected", PriorityClassName: "system-critical", Priority: 0}},
		"batch":     {Spec: manifest.PodSpec{Name: "batch", PriorityClassName: "batch", Priority: 20000}},
		"plain":     {Spec: manifest.PodSpec{Name: "plain", Priority: 1000}},
	}
	saved := map[string]int{}
	changed, err := reconcilePersistedPriorities(pods, classes, func(rec *state.PodRecord) error {
		saved[rec.Spec.Name] = rec.Spec.Priority
		return nil
	})
	if err != nil || changed != 2 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	if len(saved) != 2 || saved["old-high"] != 500 || saved["old-low"] != 2000 {
		t.Fatalf("unexpected saved priorities: %v", saved)
	}
	changed, err = reconcilePersistedPriorities(pods, classes, func(*state.PodRecord) error {
		t.Fatal("unchanged priorities should not be persisted")
		return nil
	})
	if err != nil || changed != 0 {
		t.Fatalf("second pass changed=%d err=%v", changed, err)
	}
}

func TestReconcilePersistedPrioritiesSaveFailure(t *testing.T) {
	rec := &state.PodRecord{Spec: manifest.PodSpec{Name: "work", PriorityClassName: "high", Priority: 100}}
	want := errors.New("disk full")
	changed, err := reconcilePersistedPriorities(map[string]*state.PodRecord{"work": rec}, manifest.DefaultPriorityClasses(), func(*state.PodRecord) error { return want })
	if !errors.Is(err, want) || changed != 0 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	if rec.Spec.Priority != 100 {
		t.Fatalf("failed save changed in-memory priority: %d", rec.Spec.Priority)
	}
}

func TestReconcilePersistedPrioritiesCustomNormal(t *testing.T) {
	rec := &state.PodRecord{Spec: manifest.PodSpec{Name: "classless", Priority: 1000}}
	changed, err := reconcilePersistedPriorities(map[string]*state.PodRecord{"classless": rec}, map[string]int{"normal": 250}, func(saved *state.PodRecord) error {
		if saved.Spec.Priority != 250 {
			t.Fatalf("saved priority = %d, want 250", saved.Spec.Priority)
		}
		return nil
	})
	if err != nil || changed != 1 || rec.Spec.Priority != 250 {
		t.Fatalf("changed=%d err=%v priority=%d", changed, err, rec.Spec.Priority)
	}
}
