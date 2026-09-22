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

	// gpuCount is reported for all three blocks, and always, including when it
	// is zero. It is the number the preemption planner reads, and until now it
	// appeared in no endpoint at all: /api/v1/resources carried only the three
	// fields above, and /api/v1/node reports the *detected* GPU count, which
	// stays correct while the allocatable one is wrong.
	//
	// That gap is what made issue #114 cost hours. allocatable.GPUCount was 0
	// on every node because cmd/spark/main.go never set it, so any GPU pod that
	// needed preemption waited forever on "gpu 1 > 0 free" while every endpoint
	// an operator could reach showed the GPU idle and healthy. There was no way
	// from outside the process to tell an affected node from a working one.
	//
	// Emit it unconditionally rather than omitting a zero. An absent key is not
	// a zero value, and treating it as one is its own bug: tooling downstream
	// read the missing field as "no GPUs allocatable" and refused work on nodes
	// that were fine.
	resp := map[string]any{
		"allocatable": map[string]any{"cpuMillis": allocatable.CPUMillis, "memoryMB": allocatable.MemoryMB, "gpuMemoryMB": allocatable.GPUMemoryMB, "gpuCount": allocatable.GPUCount},
		"allocated":   map[string]any{"cpuMillis": allocated.CPUMillis, "memoryMB": allocated.MemoryMB, "gpuMemoryMB": allocated.GPUMemoryMB, "gpuCount": allocated.GPUCount},
		"available":   map[string]any{"cpuMillis": available.CPUMillis, "memoryMB": available.MemoryMB, "gpuMemoryMB": available.GPUMemoryMB, "gpuCount": available.GPUCount},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
