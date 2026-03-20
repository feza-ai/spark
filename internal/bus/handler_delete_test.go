package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// stubExecutor implements executor.Executor for testing.
type stubExecutor struct {
	stopErr   error
	removeErr error
	stopped   []string
	removed   []string
}

func (e *stubExecutor) CreatePod(_ context.Context, _ manifest.PodSpec) error {
	return nil
}

func (e *stubExecutor) StopPod(_ context.Context, name string, _ int) error {
	e.stopped = append(e.stopped, name)
	return e.stopErr
}

func (e *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return executor.Status{}, nil
}

func (e *stubExecutor) RemovePod(_ context.Context, name string) error {
	e.removed = append(e.removed, name)
	return e.removeErr
}

func (e *stubExecutor) ListPods(_ context.Context) ([]executor.PodListEntry, error) {
	return nil, nil
}

func (e *stubExecutor) PodStats(_ context.Context, _ string) (executor.PodResourceUsage, error) {
	return executor.PodResourceUsage{}, nil
}

func (e *stubExecutor) PodLogs(_ context.Context, _ string, _ int) ([]byte, error) {
	return nil, nil
}

func (e *stubExecutor) StreamPodLogs(_ context.Context, _ string, _ int) (io.ReadCloser, error) {
	return nil, nil
}

func (e *stubExecutor) ListImages(_ context.Context) ([]executor.ImageInfo, error) {
	return nil, nil
}

func (e *stubExecutor) PullImage(_ context.Context, _ string) error {
	return nil
}

func TestDeleteHandler_ExistingPod(t *testing.T) {
	bus := NewStubBus()
	store := state.NewPodStore()
	exec := &stubExecutor{}

	store.Apply(manifest.PodSpec{
		Name:                          "test-pod",
		TerminationGracePeriodSeconds: 10,
	})

	RegisterDeleteHandler(bus, store, exec)

	reqData, _ := json.Marshal(DeleteRequest{Name: "test-pod"})
	resp, err := bus.Request(context.Background(), "req.spark.delete", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var dr DeleteResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if !dr.Deleted {
		t.Errorf("expected Deleted=true, got false; error=%q", dr.Error)
	}
	if dr.Name != "test-pod" {
		t.Errorf("Name = %q, want %q", dr.Name, "test-pod")
	}
	if dr.Error != "" {
		t.Errorf("unexpected error: %q", dr.Error)
	}

	if len(exec.stopped) != 1 || exec.stopped[0] != "test-pod" {
		t.Errorf("stopped = %v, want [test-pod]", exec.stopped)
	}
	if len(exec.removed) != 1 || exec.removed[0] != "test-pod" {
		t.Errorf("removed = %v, want [test-pod]", exec.removed)
	}

	if _, ok := store.Get("test-pod"); ok {
		t.Error("pod should have been removed from store")
	}
}

func TestDeleteHandler_NonExistentPod(t *testing.T) {
	bus := NewStubBus()
	store := state.NewPodStore()
	exec := &stubExecutor{}

	RegisterDeleteHandler(bus, store, exec)

	reqData, _ := json.Marshal(DeleteRequest{Name: "missing-pod"})
	resp, err := bus.Request(context.Background(), "req.spark.delete", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var dr DeleteResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if dr.Deleted {
		t.Error("expected Deleted=false for non-existent pod")
	}
	if dr.Error == "" {
		t.Error("expected error message for non-existent pod")
	}
	if dr.Name != "missing-pod" {
		t.Errorf("Name = %q, want %q", dr.Name, "missing-pod")
	}

	if len(exec.stopped) != 0 {
		t.Errorf("executor should not have been called, stopped = %v", exec.stopped)
	}
}

func TestDeleteHandler_StopError(t *testing.T) {
	bus := NewStubBus()
	store := state.NewPodStore()
	exec := &stubExecutor{stopErr: fmt.Errorf("stop failed")}

	store.Apply(manifest.PodSpec{Name: "fail-pod"})
	RegisterDeleteHandler(bus, store, exec)

	reqData, _ := json.Marshal(DeleteRequest{Name: "fail-pod"})
	resp, err := bus.Request(context.Background(), "req.spark.delete", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var dr DeleteResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if dr.Deleted {
		t.Error("expected Deleted=false when stop fails")
	}
	if dr.Error == "" {
		t.Error("expected error message when stop fails")
	}

	// Pod should still be in store since delete failed.
	if _, ok := store.Get("fail-pod"); !ok {
		t.Error("pod should still be in store after stop failure")
	}
}
