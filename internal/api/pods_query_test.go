package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func newPodQueryTestServer(t *testing.T) (*Server, *state.PodStore) {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 0},
	nil, 0,
	)
	srv := NewServer(store, tracker, nil, nil, nil, nil, "")
	return srv, store
}

func TestListPods(t *testing.T) {
	srv, store := newPodQueryTestServer(t)

	store.Apply(manifest.PodSpec{Name: "pod-a", Priority: 100})
	store.Apply(manifest.PodSpec{Name: "pod-b", Priority: 200})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body listPodsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(body.Pods))
	}

	names := map[string]bool{}
	for _, p := range body.Pods {
		names[p.Name] = true
	}
	if !names["pod-a"] || !names["pod-b"] {
		t.Errorf("expected pod-a and pod-b in response, got %v", body.Pods)
	}
}

func TestListPodsFilterStatus(t *testing.T) {
	srv, store := newPodQueryTestServer(t)

	store.Apply(manifest.PodSpec{Name: "pod-a", Priority: 100})
	store.Apply(manifest.PodSpec{Name: "pod-b", Priority: 200})
	store.UpdateStatus("pod-a", state.StatusRunning, "started")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods?status=running", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body listPodsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(body.Pods))
	}
	if body.Pods[0].Name != "pod-a" {
		t.Errorf("expected pod-a, got %s", body.Pods[0].Name)
	}
	if body.Pods[0].Status != "running" {
		t.Errorf("expected status running, got %s", body.Pods[0].Status)
	}
}

func TestGetPod(t *testing.T) {
	srv, store := newPodQueryTestServer(t)

	store.Apply(manifest.PodSpec{Name: "my-pod", Priority: 500})
	store.UpdateStatus("my-pod", state.StatusRunning, "started")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/my-pod", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body getPodResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Name != "my-pod" {
		t.Errorf("expected name my-pod, got %s", body.Name)
	}
	if body.Status != "running" {
		t.Errorf("expected status running, got %s", body.Status)
	}
	if body.Priority != 500 {
		t.Errorf("expected priority 500, got %d", body.Priority)
	}
	if len(body.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(body.Events))
	}
	if body.Events[0].Type != "running" {
		t.Errorf("expected event type running, got %s", body.Events[0].Type)
	}
}

func TestGetPodNotFound(t *testing.T) {
	srv, _ := newPodQueryTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/nonexistent", nil)
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
