package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type stubLogsExecutor struct {
	executor.Executor
	logs     []byte
	stream   io.ReadCloser
	lastTail int
}

func (s *stubLogsExecutor) PodLogs(_ context.Context, _ string, tail int) ([]byte, error) {
	s.lastTail = tail
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
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "", nil)
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
