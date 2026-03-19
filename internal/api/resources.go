package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) registerResourceRoutes() {
	s.mux.HandleFunc("GET /api/v1/resources", s.handleResources)
}

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	allocatable := s.tracker.Allocatable()
	allocated := s.tracker.Allocated()
	available := s.tracker.Available()

	resp := map[string]any{
		"allocatable": map[string]any{"cpuMillis": allocatable.CPUMillis, "memoryMB": allocatable.MemoryMB, "gpuMemoryMB": allocatable.GPUMemoryMB},
		"allocated":   map[string]any{"cpuMillis": allocated.CPUMillis, "memoryMB": allocated.MemoryMB, "gpuMemoryMB": allocated.GPUMemoryMB},
		"available":   map[string]any{"cpuMillis": available.CPUMillis, "memoryMB": available.MemoryMB, "gpuMemoryMB": available.GPUMemoryMB},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
