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
	GPUMemoryMB int
}

// ResourceTracker tracks total, allocatable, and allocated resources on a node.
type ResourceTracker struct {
	mu          sync.Mutex
	allocatable Resources
	allocations map[string]manifest.ResourceList
}

// NewResourceTracker creates a tracker with total resources and system reserve.
// Allocatable resources are total minus systemReserve.
func NewResourceTracker(total Resources, systemReserve Resources) *ResourceTracker {
	return &ResourceTracker{
		allocatable: Resources{
			CPUMillis:   total.CPUMillis - systemReserve.CPUMillis,
			MemoryMB:    total.MemoryMB - systemReserve.MemoryMB,
			GPUMemoryMB: total.GPUMemoryMB - systemReserve.GPUMemoryMB,
		},
		allocations: make(map[string]manifest.ResourceList),
	}
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

	rt.allocations[name] = req
	return nil
}

// Release frees resources for a pod. It is idempotent.
func (rt *ResourceTracker) Release(name string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	delete(rt.allocations, name)
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
	return req.CPUMillis <= avail.CPUMillis &&
		req.MemoryMB <= avail.MemoryMB &&
		req.GPUMemoryMB <= avail.GPUMemoryMB
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
		GPUMemoryMB: rt.allocatable.GPUMemoryMB - alloc.GPUMemoryMB,
	}
}

func (rt *ResourceTracker) allocatedLocked() Resources {
	var r Resources
	for _, a := range rt.allocations {
		r.CPUMillis += a.CPUMillis
		r.MemoryMB += a.MemoryMB
		r.GPUMemoryMB += a.GPUMemoryMB
	}
	return r
}
