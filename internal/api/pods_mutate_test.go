package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type stubExecutor struct {
	mu        sync.Mutex
	creates   []string
	stops     []string
	removes   []string
	stopErr   error
	removeErr error
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
	return e.stopErr
}

func (e *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return executor.Status{}, nil
}

func (e *stubExecutor) RemovePod(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removes = append(e.removes, name)
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

func (e *stubExecutor) ExecPod(_ context.Context, _ string, _ string, _ []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}

func (e *stubExecutor) ListImages(_ context.Context) ([]executor.ImageInfo, error) {
	return nil, nil
}

func (e *stubExecutor) PullImage(_ context.Context, _ string) error {
	return nil
}

func (e *stubExecutor) ExecProbe(_ context.Context, _ string, _ string, _ []string, _ time.Duration) (int, error) {
	return 0, nil
}

func (e *stubExecutor) HTTPProbe(_ context.Context, _ int, _ string, _ time.Duration) error {
	return nil
}

type stubScheduler struct {
	mu      sync.Mutex
	removed []string
}

func (s *stubScheduler) RemovePod(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, name)
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
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", nil, nil, nil, "test")
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

// newMutateTestServerWithCores builds a server whose tracker has the given
// allocatable cores, used by admission tests for issue #22.
func newMutateTestServerWithCores(t *testing.T, cores []int) *Server {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, Cores: cores},
		scheduler.Resources{},
		nil, 0,
	)
	return NewServer(store, tracker, &stubExecutor{}, nil, nil, nil, nil, "", nil, nil, nil, "test")
}

const oversizeCPUPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: oversize-pod
spec:
  containers:
    - name: main
      image: alpine:latest
      resources:
        requests:
          cpu: "999"
          memory: 100Mi
        limits:
          cpu: "999"
          memory: 100Mi
`

const fitsCPUPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: fits-pod
spec:
  containers:
    - name: main
      image: alpine:latest
      resources:
        requests:
          cpu: "2"
          memory: 100Mi
        limits:
          cpu: "2"
          memory: 100Mi
`

const fractionalCPUPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: fractional-pod
spec:
  containers:
    - name: main
      image: alpine:latest
      resources:
        requests:
          cpu: 500m
          memory: 100Mi
        limits:
          cpu: 500m
          memory: 100Mi
`

func TestApplyPod_RejectsOversizedCPU(t *testing.T) {
	srv := newMutateTestServerWithCores(t, []int{0, 1, 2, 3})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(oversizeCPUPodYAML))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeds allocatable cores") {
		t.Errorf("body should mention 'exceeds allocatable cores', got %s", rec.Body.String())
	}
}

func TestApplyPod_AcceptsFitsCPU(t *testing.T) {
	srv := newMutateTestServerWithCores(t, []int{0, 1, 2, 3})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(fitsCPUPodYAML))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestApplyPod_AcceptsFractionalCPU(t *testing.T) {
	srv := newMutateTestServerWithCores(t, []int{0, 1, 2, 3})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(fractionalCPUPodYAML))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for fractional-CPU pod, got %d: %s", rec.Code, rec.Body.String())
	}
}

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

func TestDeletePodSchedulerRemove(t *testing.T) {
	tests := []struct {
		name      string
		sched     PodRemover
		wantCalls int
	}{
		{
			name:      "scheduler receives RemovePod call",
			sched:     &stubScheduler{},
			wantCalls: 1,
		},
		{
			name:      "nil scheduler does not panic",
			sched:     nil,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
				scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
				nil, 0,
			)
			exec := &stubExecutor{}
			srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", tt.sched, nil, nil, "test")

			store.Apply(manifest.PodSpec{Name: "sched-pod"})

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/sched-pod", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			if tt.sched == nil {
				return
			}
			ss := tt.sched.(*stubScheduler)
			ss.mu.Lock()
			defer ss.mu.Unlock()
			if len(ss.removed) != tt.wantCalls {
				t.Errorf("expected %d RemovePod calls, got %d", tt.wantCalls, len(ss.removed))
			}
			if tt.wantCalls > 0 && ss.removed[0] != "sched-pod" {
				t.Errorf("expected RemovePod called with sched-pod, got %q", ss.removed[0])
			}
		})
	}
}

func TestDeletePodStopFails(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
	exec := &stubExecutor{stopErr: errors.New("podman stop: boom")}
	sched := &stubScheduler{}
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", sched, nil, nil, "test")

	store.Apply(manifest.PodSpec{Name: "stuck-pod"})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/stuck-pod", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
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
	if !strings.Contains(body.Error, "stop pod") {
		t.Errorf("expected error to mention 'stop pod', got %q", body.Error)
	}

	if _, ok := store.Get("stuck-pod"); !ok {
		t.Error("store record must remain intact when stop fails")
	}

	exec.mu.Lock()
	if len(exec.removes) != 0 {
		t.Errorf("RemovePod should not be called after StopPod fails, got %v", exec.removes)
	}
	exec.mu.Unlock()

	sched.mu.Lock()
	if len(sched.removed) != 0 {
		t.Errorf("scheduler resources must not be released when stop fails, got %v", sched.removed)
	}
	sched.mu.Unlock()
}

func TestDeletePodRemoveFails(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
	exec := &stubExecutor{removeErr: errors.New("podman rm: locked")}
	sched := &stubScheduler{}
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", sched, nil, nil, "test")

	store.Apply(manifest.PodSpec{Name: "stuck-pod"})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/stuck-pod", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
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
	if !strings.Contains(body.Error, "remove pod") {
		t.Errorf("expected error to mention 'remove pod', got %q", body.Error)
	}

	if _, ok := store.Get("stuck-pod"); !ok {
		t.Error("store record must remain intact when remove fails")
	}

	sched.mu.Lock()
	if len(sched.removed) != 0 {
		t.Errorf("scheduler resources must not be released when remove fails, got %v", sched.removed)
	}
	sched.mu.Unlock()
}

func TestDeletePodNoSuchPodTreatedAsSuccess(t *testing.T) {
	tests := []struct {
		name string
		exec *stubExecutor
	}{
		{
			name: "stop returns no such pod",
			exec: &stubExecutor{stopErr: errors.New("Error: no such pod foo")},
		},
		{
			name: "remove returns no such pod",
			exec: &stubExecutor{removeErr: errors.New("Error: no such pod foo")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
				scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
				nil, 0,
			)
			sched := &stubScheduler{}
			srv := NewServer(store, tracker, tt.exec, nil, nil, nil, nil, "", sched, nil, nil, "test")

			store.Apply(manifest.PodSpec{Name: "ghost-pod"})

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/pods/ghost-pod", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if _, ok := store.Get("ghost-pod"); ok {
				t.Error("store record should be removed when pod was already gone")
			}
			sched.mu.Lock()
			if len(sched.removed) != 1 {
				t.Errorf("expected scheduler.RemovePod to be called once, got %d", len(sched.removed))
			}
			sched.mu.Unlock()
		})
	}
}
