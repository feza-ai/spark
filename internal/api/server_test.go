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

func newTestServer(t *testing.T) *Server {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 0},
	nil, 0,
	)
	return NewServer(store, tracker, nil, nil, nil, nil, "")
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %q", body["status"])
	}
}

func TestResources(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 0},
	nil, 0,
	)
	// Allocate some resources so allocated != zero.
	tracker.Allocate("test-pod", manifest.ResourceList{
		CPUMillis:   2000,
		MemoryMB:    4096,
		GPUMemoryMB: 8192,
	})

	srv := NewServer(store, tracker, nil, nil, nil, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]map[string]float64
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Allocatable = total - reserve = {7000, 14336, 32768}
	if v := body["allocatable"]["cpuMillis"]; v != 7000 {
		t.Errorf("allocatable cpuMillis: expected 7000, got %v", v)
	}
	if v := body["allocatable"]["memoryMB"]; v != 14336 {
		t.Errorf("allocatable memoryMB: expected 14336, got %v", v)
	}
	if v := body["allocatable"]["gpuMemoryMB"]; v != 32768 {
		t.Errorf("allocatable gpuMemoryMB: expected 32768, got %v", v)
	}

	// Allocated = {2000, 4096, 8192}
	if v := body["allocated"]["cpuMillis"]; v != 2000 {
		t.Errorf("allocated cpuMillis: expected 2000, got %v", v)
	}
	if v := body["allocated"]["memoryMB"]; v != 4096 {
		t.Errorf("allocated memoryMB: expected 4096, got %v", v)
	}
	if v := body["allocated"]["gpuMemoryMB"]; v != 8192 {
		t.Errorf("allocated gpuMemoryMB: expected 8192, got %v", v)
	}

	// Available = allocatable - allocated = {5000, 10240, 24576}
	if v := body["available"]["cpuMillis"]; v != 5000 {
		t.Errorf("available cpuMillis: expected 5000, got %v", v)
	}
	if v := body["available"]["memoryMB"]; v != 10240 {
		t.Errorf("available memoryMB: expected 10240, got %v", v)
	}
	if v := body["available"]["gpuMemoryMB"]; v != 24576 {
		t.Errorf("available gpuMemoryMB: expected 24576, got %v", v)
	}
}
