package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type stubExecutor struct {
	mu      sync.Mutex
	creates []string
	stops   []string
	removes []string
}

func (e *stubExecutor) CreatePod(_ context.Context, spec manifest.PodSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.creates = append(e.creates, spec.Name)
	return nil
}

func (e *stubExecutor) StopPod(_ context.Context, name string, _ int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stops = append(e.stops, name)
	return nil
}

func (e *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return executor.Status{}, nil
}

func (e *stubExecutor) RemovePod(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removes = append(e.removes, name)
	return nil
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

func (e *stubExecutor) ExecPod(_ context.Context, _ string, _ string, _ []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}

func newMutateTestServer(t *testing.T) (*Server, *state.PodStore, *stubExecutor) {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	nil, 0,
	)
	exec := &stubExecutor{}
	srv := NewServer(store, tracker, exec, nil, nil, nil, "")
	return srv, store, exec
}

const testPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: main
    image: alpine:latest
`

func TestApplyPod(t *testing.T) {
	srv, store, _ := newMutateTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(testPodYAML))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Pods []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"pods"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(body.Pods))
	}
	if body.Pods[0].Name != "test-pod" {
		t.Errorf("expected pod name test-pod, got %q", body.Pods[0].Name)
	}
	if body.Pods[0].Status != "pending" {
		t.Errorf("expected status pending, got %q", body.Pods[0].Status)
	}

	if _, ok := store.Get("test-pod"); !ok {
		t.Error("pod not found in store after apply")
	}
}

func TestApplyInvalidYAML(t *testing.T) {
	srv, _, _ := newMutateTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader("not: valid: yaml: [[["))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestDeletePod(t *testing.T) {
	srv, store, exec := newMutateTestServer(t)

	// First apply a pod.
	store.Apply(manifest.PodSpec{Name: "test-pod"})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/test-pod", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !body.Deleted {
		t.Error("expected deleted=true")
	}
	if body.Name != "test-pod" {
		t.Errorf("expected name test-pod, got %q", body.Name)
	}

	if _, ok := store.Get("test-pod"); ok {
		t.Error("pod should be removed from store after delete")
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.stops) != 1 || exec.stops[0] != "test-pod" {
		t.Errorf("expected StopPod called with test-pod, got %v", exec.stops)
	}
	if len(exec.removes) != 1 || exec.removes[0] != "test-pod" {
		t.Errorf("expected RemovePod called with test-pod, got %v", exec.removes)
	}
}

func TestDeletePodNotFound(t *testing.T) {
	srv, _, _ := newMutateTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Deleted {
		t.Error("expected deleted=false")
	}
	if body.Error == "" {
		t.Error("expected non-empty error")
	}
}
