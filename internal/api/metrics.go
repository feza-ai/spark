package api

import (
	"net/http"

	"github.com/feza-ai/spark/internal/metrics"
)

// handleMetrics serves Prometheus text exposition format metrics.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.collector == nil {
		http.Error(w, "metrics collector not configured", http.StatusServiceUnavailable)
		return
	}
	families := s.collector.Collect()
	data := metrics.Render(families)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write(data)
}
