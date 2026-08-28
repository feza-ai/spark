package api

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"sort"
)

// NodeResponse represents the JSON body returned by GET /api/v1/node.
type NodeResponse struct {
	Hostname            string          `json:"hostname"`
	OS                  string          `json:"os"`
	Arch                string          `json:"arch"`
	CPUCores            int             `json:"cpu_cores"`
	CPUReservedCores    []int           `json:"cpu_reserved_cores"`
	CPUAllocatableCores []int           `json:"cpu_allocatable_cores"`
	MemoryTotalMB       int             `json:"memory_total_mb"`
	GPUModel            string          `json:"gpu_model,omitempty"`
	GPUCount            int             `json:"gpu_count"`
	GPUDeviceIDs        []int           `json:"gpu_device_ids"`
	GPUMemoryMB         int             `json:"gpu_memory_mb"`
	GPUAllocations      []GPUAllocation `json:"gpu_allocations"`
}

// GPUAllocation reports one currently-held GPU device-slot assignment.
// Exposed so an operator can see which pod holds a device slot without
// cross-referencing podman directly (issue #81).
type GPUAllocation struct {
	Device int    `json:"device"`
	Pod    string `json:"pod"`
}

func (s *Server) registerNodeRoutes() {
	s.mux.HandleFunc("GET /api/v1/node", s.handleNode)
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()

	resp := NodeResponse{
		Hostname:            hostname,
		OS:                  runtime.GOOS,
		Arch:                runtime.GOARCH,
		CPUCores:            runtime.NumCPU(),
		GPUDeviceIDs:        []int{},
		CPUReservedCores:    []int{},
		CPUAllocatableCores: []int{},
		GPUAllocations:      []GPUAllocation{},
	}

	if s.sysInfo != nil {
		resp.MemoryTotalMB = s.sysInfo.MemoryTotalMB
	}

	if s.tracker != nil {
		if r := s.tracker.ReservedCores(); r != nil {
			resp.CPUReservedCores = r
		}
		if alloc := s.tracker.Allocatable().Cores; alloc != nil {
			resp.CPUAllocatableCores = alloc
		}
		for pod, devices := range s.tracker.GPUHolders() {
			for _, d := range devices {
				resp.GPUAllocations = append(resp.GPUAllocations, GPUAllocation{Device: d, Pod: pod})
			}
		}
		sort.Slice(resp.GPUAllocations, func(i, j int) bool {
			return resp.GPUAllocations[i].Device < resp.GPUAllocations[j].Device
		})
	}

	if s.gpuInfo != nil {
		resp.GPUModel = s.gpuInfo.Model
		resp.GPUCount = s.gpuInfo.GPUCount
		resp.GPUMemoryMB = s.gpuInfo.MemoryTotalMB
		if s.gpuInfo.DeviceIDs != nil {
			resp.GPUDeviceIDs = s.gpuInfo.DeviceIDs
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
