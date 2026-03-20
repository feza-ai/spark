package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type stubExecExecutor struct {
	executor.Executor
	stdout   []byte
	stderr   []byte
	exitCode int
}

func (e *stubExecExecutor) ExecPod(_ context.Context, _ string, _ string, _ []string) ([]byte, []byte, int, error) {
	return e.stdout, e.stderr, e.exitCode, nil
}

func newExecTestServer(t *testing.T, exec executor.Executor) (*Server, *state.PodStore) {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
	srv := NewServer(store, tracker, exec, nil, nil, nil, nil, "")
	return srv, store
}

func TestExecPod_Success(t *testing.T) {
	stub := &stubExecExecutor{
		stdout:   []byte("file1\nfile2\n"),
		stderr:   []byte(""),
		exitCode: 0,
	}
	srv, store := newExecTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	store.UpdateStatus("my-pod", state.StatusRunning, "started")

	body := `{"command":["ls","-la"],"container":"worker"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods/my-pod/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var resp execResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Stdout != "file1\nfile2\n" {
		t.Errorf("unexpected stdout: %q", resp.Stdout)
	}
	if resp.ExitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", resp.ExitCode)
	}
}

func TestExecPod_NotFound(t *testing.T) {
	stub := &stubExecExecutor{}
	srv, _ := newExecTestServer(t, stub)

	body := `{"command":["ls"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods/nonexistent/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["error"] != "pod not found" {
		t.Errorf("expected 'pod not found', got %q", resp["error"])
	}
}

func TestExecPod_NotRunning(t *testing.T) {
	stub := &stubExecExecutor{}
	srv, store := newExecTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	// Pod stays in pending status (not running).

	body := `{"command":["ls"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods/my-pod/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["error"] != "pod not running" {
		t.Errorf("expected 'pod not running', got %q", resp["error"])
	}
}

func TestExecPod_EmptyCommand(t *testing.T) {
	stub := &stubExecExecutor{}
	srv, store := newExecTestServer(t, stub)
	store.Apply(manifest.PodSpec{Name: "my-pod"})
	store.UpdateStatus("my-pod", state.StatusRunning, "started")

	body := `{"command":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pods/my-pod/exec", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["error"] != "command is required" {
		t.Errorf("expected 'command is required', got %q", resp["error"])
	}
}
