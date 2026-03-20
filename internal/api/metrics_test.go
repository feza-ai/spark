package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/metrics"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func TestMetricsEndpoint(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	nil, 0,
	)
	collector := metrics.NewCollector(store, tracker, nil)
	srv := NewServer(store, tracker, nil, nil, nil, collector, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("expected Prometheus Content-Type, got %q", ct)
	}

	body := rec.Body.String()
	expectedMetrics := []string{
		"spark_node_cpu_total_millis",
		"spark_node_cpu_available_millis",
		"spark_node_memory_total_mb",
		"spark_node_memory_available_mb",
		"spark_node_gpu_memory_total_mb",
		"spark_node_gpu_memory_available_mb",
		"spark_pods_total",
		"spark_pod_restarts_total",
	}
	for _, name := range expectedMetrics {
		if !strings.Contains(body, name) {
			t.Errorf("response body missing metric %q", name)
		}
	}
}

func TestMetricsEndpoint_NoCollector(t *testing.T) {
	srv := newTestServer(t) // uses nil collector

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
