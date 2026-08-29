package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type stubLogsExecutor struct {
	executor.Executor
	logs     []byte
	err      error
	stream   io.ReadCloser
	lastTail int
}

func (s *stubLogsExecutor) PodLogs(_ context.Context, _ string, tail int) ([]byte, error) {
	s.lastTail = tail
	if s.err != nil {
		return nil, s.err
	}
	return s.logs, nil
}

func (s *stubLogsExecutor) StreamPodLogs(_ context.Context, _ string, tail int) (io.ReadCloser, error) {
	s.lastTail = tail
	return s.stream, nil
}

func newPodLogsTestServer(t *testing.T, exec executor.Executor) (*Server, *state.PodStore) {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 0},
		nil, 0,
	)
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", nil, nil, nil, "test")
	return srv, store
}

func TestPodLogs_Tail(t *testing.T) {
	stub := &stubLogsExecutor{logs: []byte("line1\nline2\nline3\n")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs?tail=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %s", ct)
	}
	if stub.lastTail != 10 {
		t.Errorf("expected tail=10, got %d", stub.lastTail)
	}
	body := rec.Body.String()
	if body != "line1\nline2\nline3\n" {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestPodLogs_NotFound(t *testing.T) {
	stub := &stubLogsExecutor{}
	srv, _ := newPodLogsTestServer(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/nonexistent/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "pod not found: nonexistent" {
		t.Errorf("expected error message, got %q", body["error"])
	}
}

func TestPodLogs_DefaultTail(t *testing.T) {
	stub := &stubLogsExecutor{logs: []byte("log output")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if stub.lastTail != 100 {
		t.Errorf("expected default tail=100, got %d", stub.lastTail)
	}
}

func TestPodLogs_FollowNotRunning(t *testing.T) {
	stub := &stubLogsExecutor{}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs?follow=true", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "pod not running" {
		t.Errorf("expected 'pod not running', got %q", body["error"])
	}
}

func TestPodLogs_FollowSSE(t *testing.T) {
	logData := "line1\nline2\n"
	stub := &stubLogsExecutor{
		stream: io.NopCloser(strings.NewReader(logData)),
	}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	store.UpdateStatus("my-pod", state.StatusRunning, "started")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs?follow=true", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: line1\n\n") {
		t.Errorf("expected SSE event for line1, got %q", body)
	}
	if !strings.Contains(body, "data: line2\n\n") {
		t.Errorf("expected SSE event for line2, got %q", body)
	}
}

// TestPodLogs_PendingAwaitingResources_NotStartedYet covers issue #78: a
// pod that is Pending (queued, awaiting resources) was never created in
// podman, so /logs must report "not started yet" (200, empty body), not an
// error implying the pod vanished.
func TestPodLogs_PendingAwaitingResources_NotStartedYet(t *testing.T) {
	stub := &stubLogsExecutor{err: errors.New("podman pod logs: exit status 125: Error: no pod with name or ID my-pod found: no such pod\n")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"}) // Status defaults to Pending, zero Events.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %s", ct)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
}

// TestPodLogs_PendingAwaitingResources_TimeoutExceeded covers issue #78's
// pendingTimeout: once a pod has been continuously Pending longer than the
// timeout (the default here), /logs must stop staying silent and report
// the real shortfall instead -- never implying the pod vanished, never the
// raw podman error.
func TestPodLogs_PendingAwaitingResources_TimeoutExceeded(t *testing.T) {
	stub := &stubLogsExecutor{err: errors.New("podman pod logs: exit status 125: Error: no pod with name or ID my-pod found: no such pod\n")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	store.UpdateStatus("my-pod", state.StatusPending, "awaiting-resources: no preemption candidates (lower-priority, non-thrashed pods); shortfall: cpu 250m > 100m free")
	store.BackdateLastEvent("my-pod", 15*time.Minute) // Past the 10m default -- exercises SetPendingLogTimeout's default, not a custom value.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.Contains(body["error"], "cpu 250m > 100m free") {
		t.Errorf("expected error to name the real shortfall, got %q", body["error"])
	}
	lower := strings.ToLower(body["error"])
	if strings.Contains(lower, "lost") {
		t.Errorf("error must never imply the pod vanished/was lost, got %q", body["error"])
	}
	if strings.Contains(lower, "no pod with name") {
		t.Errorf("error must not leak the raw podman stderr, got %q", body["error"])
	}
}

// TestPodLogs_PendingUnrelatedError_StillErrors is a negative-space
// companion: only the specific isNoSuchPod+Pending combination gets the
// new empty/timeout handling. Any other PodLogs error on a Pending pod
// must keep today's behavior (500, raw error surfaced) unchanged.
func TestPodLogs_PendingUnrelatedError_StillErrors(t *testing.T) {
	stub := &stubLogsExecutor{err: errors.New("podman pod logs: exit status 1: some other failure")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.Contains(body["error"], "some other failure") {
		t.Errorf("expected the raw error surfaced, got %q", body["error"])
	}
}

// TestPodLogs_NotPending_NoSuchPod_StillErrors is a negative-space
// companion: the new handling applies only when the pod's own status is
// Pending. A Running pod whose podman backing has vanished is genuinely
// lost, and /logs must keep erroring rather than silently going quiet.
func TestPodLogs_NotPending_NoSuchPod_StillErrors(t *testing.T) {
	stub := &stubLogsExecutor{err: errors.New("podman pod logs: exit status 125: Error: no pod with name or ID my-pod found: no such pod\n")}
	srv, store := newPodLogsTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	store.UpdateStatus("my-pod", state.StatusRunning, "started")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
