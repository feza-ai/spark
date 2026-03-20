package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func newPodEventsTestServer(t *testing.T) *Server {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 0},
	)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	sqlStore, err := state.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlStore.Close() })

	srv := NewServer(store, tracker, nil, nil, sqlStore, nil, "")
	return srv
}

func TestPodEvents_All(t *testing.T) {
	srv := newPodEventsTestServer(t)

	srv.store.Apply(manifest.PodSpec{Name: "web"})
	srv.sqlStore.SavePod(&state.PodRecord{Spec: manifest.PodSpec{Name: "web"}, Status: state.StatusPending})

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	srv.sqlStore.SaveEvent("web", state.PodEvent{Time: t1, Type: "scheduled", Message: "pod scheduled"})
	srv.sqlStore.SaveEvent("web", state.PodEvent{Time: t2, Type: "started", Message: "pod started"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/web/events", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var body podEventsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(body.Events))
	}
	if body.Events[0].PodName != "web" {
		t.Errorf("expected pod_name web, got %s", body.Events[0].PodName)
	}
	if body.Events[0].Type != "scheduled" {
		t.Errorf("expected type scheduled, got %s", body.Events[0].Type)
	}
	if body.Events[1].Type != "started" {
		t.Errorf("expected type started, got %s", body.Events[1].Type)
	}
}

func TestPodEvents_Since(t *testing.T) {
	srv := newPodEventsTestServer(t)

	srv.store.Apply(manifest.PodSpec{Name: "worker"})
	srv.sqlStore.SavePod(&state.PodRecord{Spec: manifest.PodSpec{Name: "worker"}, Status: state.StatusPending})

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	srv.sqlStore.SaveEvent("worker", state.PodEvent{Time: t1, Type: "scheduled", Message: "scheduled"})
	srv.sqlStore.SaveEvent("worker", state.PodEvent{Time: t2, Type: "started", Message: "started"})
	srv.sqlStore.SaveEvent("worker", state.PodEvent{Time: t3, Type: "completed", Message: "done"})

	since := t2.Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/worker/events?since="+since, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body podEventsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("expected 2 events (since filter), got %d", len(body.Events))
	}
	if body.Events[0].Type != "started" {
		t.Errorf("expected first event type started, got %s", body.Events[0].Type)
	}
	if body.Events[1].Type != "completed" {
		t.Errorf("expected second event type completed, got %s", body.Events[1].Type)
	}
}

func TestPodEvents_NotFound(t *testing.T) {
	srv := newPodEventsTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/nonexistent/events", nil)
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

func TestPodEvents_InvalidSince(t *testing.T) {
	srv := newPodEventsTestServer(t)

	srv.store.Apply(manifest.PodSpec{Name: "app"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods/app/events?since=not-a-date", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected non-empty error message")
	}
}
