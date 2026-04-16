package scheduler

import (
	"fmt"
	"sync"

	"github.com/feza-ai/spark/internal/manifest"
)

// Resources represents a set of compute resources.
type Resources struct {
	CPUMillis   int
	MemoryMB    int
	GPUCount    int
	GPUMemoryMB int
	Cores       []int
}

// ResourceTracker tracks total, allocatable, and allocated resources on a node.
type ResourceTracker struct {
	mu              sync.Mutex
	allocatable     Resources
	allocations     map[string]manifest.ResourceList
	gpuDevices      []int
	gpuMax          int
	gpuAssignments  map[string][]int
	cores           []int
	coreAssignments map[string][]int
}

// NewResourceTracker creates a tracker with total resources and system reserve.
// Allocatable resources are total minus systemReserve.
// gpuDevices lists available GPU device IDs; gpuMax limits concurrent GPU pods.
// If gpuDevices is nil/empty, GPU slot tracking is disabled (memory-only).
func NewResourceTracker(total Resources, systemReserve Resources, gpuDevices []int, gpuMax int) *ResourceTracker {
	rt := &ResourceTracker{
		allocatable: Resources{
			CPUMillis:   total.CPUMillis - systemReserve.CPUMillis,
			MemoryMB:    total.MemoryMB - systemReserve.MemoryMB,
			GPUCount:    total.GPUCount - systemReserve.GPUCount,
			GPUMemoryMB: total.GPUMemoryMB - systemReserve.GPUMemoryMB,
		},
		allocations: make(map[string]manifest.ResourceList),
	}
	if len(gpuDevices) > 0 {
		rt.gpuDevices = make([]int, len(gpuDevices))
		copy(rt.gpuDevices, gpuDevices)
		rt.gpuMax = gpuMax
		rt.gpuAssignments = make(map[string][]int)
	}
	if len(total.Cores) > 0 {
		rt.cores = make([]int, len(total.Cores))
		copy(rt.cores, total.Cores)
		rt.coreAssignments = make(map[string][]int)
	}
	return rt
}

// Allocate reserves resources for a pod. Returns error if insufficient.
func (rt *ResourceTracker) Allocate(name string, req manifest.ResourceList) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	avail := rt.availableLocked()

	if req.CPUMillis > avail.CPUMillis {
		return fmt.Errorf("insufficient CPU: requested %d m, available %d m", req.CPUMillis, avail.CPUMillis)
	}
	if req.MemoryMB > avail.MemoryMB {
		return fmt.Errorf("insufficient memory: requested %d MB, available %d MB", req.MemoryMB, avail.MemoryMB)
	}
	if req.GPUMemoryMB > avail.GPUMemoryMB {
		return fmt.Errorf("insufficient GPU memory: requested %d MB, available %d MB", req.GPUMemoryMB, avail.GPUMemoryMB)
	}

	// GPUCount is the primary GPU allocation path: allocate N device slots.
	if req.GPUCount > 0 && len(rt.gpuDevices) > 0 {
		inUse := rt.assignedDeviceCountLocked()
		if inUse+req.GPUCount > rt.gpuMax {
			return fmt.Errorf("GPU slot limit reached: requested %d, max %d, in-use %d", req.GPUCount, rt.gpuMax, inUse)
		}
		devs := rt.unassignedDevicesLocked(req.GPUCount)
		if len(devs) < req.GPUCount {
			return fmt.Errorf("insufficient GPU devices: requested %d, available %d", req.GPUCount, len(devs))
		}
		rt.gpuAssignments[name] = devs
	} else if req.GPUMemoryMB > 0 && len(rt.gpuDevices) > 0 {
		// Backwards-compatible path: GPUMemoryMB without GPUCount gets 1 slot.
		inUse := rt.assignedDeviceCountLocked()
		if inUse+1 > rt.gpuMax {
			return fmt.Errorf("GPU slot limit reached")
		}
		devs := rt.unassignedDevicesLocked(1)
		if len(devs) < 1 {
			return fmt.Errorf("no GPU device available")
		}
		rt.gpuAssignments[name] = devs
	}

	rt.allocations[name] = req
	return nil
}

// Release frees resources for a pod. It is idempotent.
func (rt *ResourceTracker) Release(name string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	delete(rt.allocations, name)
	if rt.gpuAssignments != nil {
		delete(rt.gpuAssignments, name)
	}
}

// AssignedGPUs returns the GPU device IDs assigned to a pod, or nil if none.
func (rt *ResourceTracker) AssignedGPUs(name string) []int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.gpuAssignments == nil {
		return nil
	}
	return rt.gpuAssignments[name]
}

// AssignedCores returns the CPU core IDs assigned to a pod, or nil if none.
func (rt *ResourceTracker) AssignedCores(name string) []int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.coreAssignments == nil {
		return nil
	}
	return rt.coreAssignments[name]
}

// unassignedDevicesLocked returns up to n unassigned GPU device IDs.
func (rt *ResourceTracker) unassignedDevicesLocked(n int) []int {
	assigned := make(map[int]bool)
	for _, devs := range rt.gpuAssignments {
		for _, d := range devs {
			assigned[d] = true
		}
	}
	var result []int
	for _, dev := range rt.gpuDevices {
		if !assigned[dev] {
			result = append(result, dev)
			if len(result) == n {
				break
			}
		}
	}
	return result
}

// assignedDeviceCountLocked returns the total number of assigned device slots.
func (rt *ResourceTracker) assignedDeviceCountLocked() int {
	count := 0
	for _, devs := range rt.gpuAssignments {
		count += len(devs)
	}
	return count
}

// unassignedCoresLocked returns n unassigned CPU core IDs. It prefers the
// lowest-index contiguous block of n cores when one exists; otherwise it
// returns any n unassigned cores in ascending order. Returns nil if fewer
// than n unassigned cores are available.
func (rt *ResourceTracker) unassignedCoresLocked(n int) []int {
	if n <= 0 {
		return nil
	}
	assigned := make(map[int]bool)
	for _, cs := range rt.coreAssignments {
		for _, c := range cs {
			assigned[c] = true
		}
	}
	var free []int
	for _, c := range rt.cores {
		if !assigned[c] {
			free = append(free, c)
		}
	}
	if len(free) < n {
		return nil
	}
	// Prefer the lowest-index contiguous block of size n.
	for i := 0; i+n <= len(free); i++ {
		contiguous := true
		for j := 1; j < n; j++ {
			if free[i+j] != free[i+j-1]+1 {
				contiguous = false
				break
			}
		}
		if contiguous {
			result := make([]int, n)
			copy(result, free[i:i+n])
			return result
		}
	}
	// Fall back to the first n unassigned cores.
	result := make([]int, n)
	copy(result, free[:n])
	return result
}

// assignedCoreCountLocked returns the total number of assigned cores.
func (rt *ResourceTracker) assignedCoreCountLocked() int {
	count := 0
	for _, cs := range rt.coreAssignments {
		count += len(cs)
	}
	return count
}

// Available returns currently available resources.
func (rt *ResourceTracker) Available() Resources {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return rt.availableLocked()
}

// CanFit checks if a pod's resource requests can be satisfied.
func (rt *ResourceTracker) CanFit(req manifest.ResourceList) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	avail := rt.availableLocked()
	if req.CPUMillis > avail.CPUMillis || req.MemoryMB > avail.MemoryMB || req.GPUMemoryMB > avail.GPUMemoryMB {
		return false
	}
	if req.GPUCount > 0 && len(rt.gpuDevices) > 0 {
		if rt.assignedDeviceCountLocked()+req.GPUCount > rt.gpuMax {
			return false
		}
		if len(rt.unassignedDevicesLocked(req.GPUCount)) < req.GPUCount {
			return false
		}
	}
	return true
}

// Allocated returns total currently allocated resources.
func (rt *ResourceTracker) Allocated() Resources {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return rt.allocatedLocked()
}

// AllocatedBy returns resources allocated to a specific pod, and whether it exists.
func (rt *ResourceTracker) AllocatedBy(name string) (manifest.ResourceList, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	r, ok := rt.allocations[name]
	return r, ok
}

// Allocatable returns the total allocatable resources (total minus system reserve).
func (rt *ResourceTracker) Allocatable() Resources {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return Resources{
		CPUMillis:   rt.allocatable.CPUMillis,
		MemoryMB:    rt.allocatable.MemoryMB,
		GPUCount:    rt.allocatable.GPUCount,
		GPUMemoryMB: rt.allocatable.GPUMemoryMB,
	}
}

// UpdateAllocation updates the tracked allocation for a pod with actual resource values.
// Only updates if the pod exists in the allocation map.
func (rt *ResourceTracker) UpdateAllocation(name string, actual manifest.ResourceList) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if _, ok := rt.allocations[name]; !ok {
		return
	}
	rt.allocations[name] = actual
}

func (rt *ResourceTracker) availableLocked() Resources {
	alloc := rt.allocatedLocked()
	return Resources{
		CPUMillis:   rt.allocatable.CPUMillis - alloc.CPUMillis,
		MemoryMB:    rt.allocatable.MemoryMB - alloc.MemoryMB,
		GPUCount:    rt.allocatable.GPUCount - alloc.GPUCount,
		GPUMemoryMB: rt.allocatable.GPUMemoryMB - alloc.GPUMemoryMB,
	}
}

func (rt *ResourceTracker) allocatedLocked() Resources {
	var r Resources
	for _, a := range rt.allocations {
		r.CPUMillis += a.CPUMillis
		r.MemoryMB += a.MemoryMB
		r.GPUCount += a.GPUCount
		r.GPUMemoryMB += a.GPUMemoryMB
	}
	return r
}
