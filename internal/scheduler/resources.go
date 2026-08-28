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
	reservedCores   []int
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
	// Compute allocatable cores: total.Cores minus systemReserve.Cores.
	if len(total.Cores) > 0 {
		reserved := make(map[int]bool, len(systemReserve.Cores))
		for _, c := range systemReserve.Cores {
			reserved[c] = true
		}
		var allocCores []int
		for _, c := range total.Cores {
			if !reserved[c] {
				allocCores = append(allocCores, c)
			}
		}
		rt.cores = allocCores
		rt.allocatable.Cores = allocCores
		rt.coreAssignments = make(map[string][]int)
	}
	if len(systemReserve.Cores) > 0 {
		rt.reservedCores = make([]int, len(systemReserve.Cores))
		copy(rt.reservedCores, systemReserve.Cores)
	}
	return rt
}

// ReservedCores returns the host CPU core IDs reserved for the system
// (excluded from allocation). Returns nil if no cores are reserved.
func (rt *ResourceTracker) ReservedCores() []int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.reservedCores) == 0 {
		return nil
	}
	out := make([]int, len(rt.reservedCores))
	copy(out, rt.reservedCores)
	return out
}

// Allocate reserves resources for a pod. Returns error if insufficient.
func (rt *ResourceTracker) Allocate(name string, req manifest.ResourceList) error {
	return rt.allocate(name, req, false)
}

// AllocateOverCommittingCPU behaves like Allocate but skips the accounted
// CPU-capacity check (memory, GPU, GPU memory, and cpuset core-block checks
// are unchanged and remain strict). It exists for utilization-aware
// admission (issue #76): the caller (Scheduler) has already confirmed real
// host CPU headroom via a HostLoadSource before calling this, so accounted
// CPU reservations are allowed to exceed allocatable — CPU contention is a
// soft, recoverable failure mode on this hardware. Memory exhaustion is
// not, which is why memory accounting has no equivalent bypass.
func (rt *ResourceTracker) AllocateOverCommittingCPU(name string, req manifest.ResourceList) error {
	return rt.allocate(name, req, true)
}

// allocate is the shared implementation behind Allocate and
// AllocateOverCommittingCPU. skipCPUCheck disables only the accounted
// CPUMillis capacity check; every other dimension is checked identically.
func (rt *ResourceTracker) allocate(name string, req manifest.ResourceList, skipCPUCheck bool) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	avail := rt.availableLocked()

	if !skipCPUCheck && req.CPUMillis > avail.CPUMillis {
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
	} else if len(rt.gpuDevices) > 0 {
		// This request needs no GPU. Release any device-slot assignment
		// this pod name already holds from a prior incarnation -- without
		// this, gpuAssignments[name] survives forever once a pod is
		// re-applied under the same name with the GPU request dropped,
		// even though the allocations[name] entry set below correctly
		// reflects the new (GPU-less) request. That asymmetry is what let
		// a GPU device slot leak permanently while CPU/memory accounting
		// looked correct (issue #81).
		delete(rt.gpuAssignments, name)
	}

	// Core-set assignment: only integer-CPU pods get a contiguous core range.
	// If the pod already has an assignment (e.g., RestoreAssignment after
	// recovery, or a duplicate Allocate from a retry), reuse it so the
	// running container's cpuset stays consistent.
	if len(rt.cores) > 0 && req.CPUMillis >= 1000 && req.CPUMillis%1000 == 0 {
		n := req.CPUMillis / 1000
		if existing, ok := rt.coreAssignments[name]; ok && len(existing) == n {
			// Already assigned the right number of cores -- no change.
		} else {
			cores := rt.unassignedCoresLocked(n)
			if cores == nil {
				// Roll back any GPU assignment made above to avoid orphaning it.
				if rt.gpuAssignments != nil {
					delete(rt.gpuAssignments, name)
				}
				return fmt.Errorf("insufficient CPU cores: requested %d, available %d", n, len(rt.cores)-rt.assignedCoreCountLocked())
			}
			rt.coreAssignments[name] = cores
		}
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
	if rt.coreAssignments != nil {
		delete(rt.coreAssignments, name)
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

// GPUHolders returns a snapshot of every currently-held GPU device-slot
// assignment, keyed by pod name. Returns nil when GPU slot tracking is
// disabled or nothing currently holds a slot. Used to expose slot
// ownership over the API (issue #81) and by the housekeeper to reconcile
// the device-slot ledger against live pod state.
func (rt *ResourceTracker) GPUHolders() map[string][]int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if len(rt.gpuAssignments) == 0 {
		return nil
	}
	out := make(map[string][]int, len(rt.gpuAssignments))
	for name, devs := range rt.gpuAssignments {
		cp := make([]int, len(devs))
		copy(cp, devs)
		out[name] = cp
	}
	return out
}

// ReleaseGPU releases only the GPU device-slot assignment held by a pod
// name, leaving any CPU/memory/core allocation for that name untouched.
// Unlike Release, this is safe to call for a name that may have been
// re-applied to a different (still-live) pod in the meantime: it can
// only ever free a device slot, never another pod's CPU or memory.
// Used by the housekeeper to reclaim a slot held by a name with no
// corresponding live pod record (issue #81).
func (rt *ResourceTracker) ReleaseGPU(name string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.gpuAssignments != nil {
		delete(rt.gpuAssignments, name)
	}
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

// ForceAllocate records an allocation without capacity checks. Used when
// adopting pods that are already running after a restart: reality outranks
// the ledger, and an unrecorded running pod would let admission hand the
// same resources out twice. Core and GPU assignments are not touched.
func (rt *ResourceTracker) ForceAllocate(name string, req manifest.ResourceList) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.allocations[name] = req
}

// RestoreAssignment installs a pre-existing core assignment for a pod, used
// during recovery so the tracker matches the running container's cpuset.
// It is the caller's responsibility to ensure cores are not double-assigned.
func (rt *ResourceTracker) RestoreAssignment(name string, cores []int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.coreAssignments == nil {
		rt.coreAssignments = make(map[string][]int)
	}
	if len(cores) > 0 {
		cs := make([]int, len(cores))
		copy(cs, cores)
		rt.coreAssignments[name] = cs
	}
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

// assignedCoreCountLocked returns the total number of assigned CPU cores.
func (rt *ResourceTracker) assignedCoreCountLocked() int {
	count := 0
	for _, cs := range rt.coreAssignments {
		count += len(cs)
	}
	return count
}

// CoresEnabled reports whether cpuset pinning is active (i.e. the tracker
// was constructed with a non-empty pinnable-core list). When false, the
// whole-core block check in CanFit is skipped.
func (rt *ResourceTracker) CoresEnabled() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.cores) > 0
}

// UnassignedCoreCount returns the number of cpuset-pinnable cores not
// currently assigned to any pod. Returns 0 when cpuset pinning is disabled.
// Used by Schedule() to surface a precise shortfall reason when CanFit
// fails the whole-core block check while millis-level resources fit.
func (rt *ResourceTracker) UnassignedCoreCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.cores) == 0 {
		return 0
	}
	return len(rt.cores) - rt.assignedCoreCountLocked()
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
	return rt.canFitLocked(req, false)
}

// CanFitIgnoringCPU reports whether req fits every dimension except the
// accounted CPUMillis check — memory, GPU count, GPU memory, and the
// cpuset whole-core-block check are all still enforced exactly as CanFit
// would. Used by utilization-aware admission (issue #76) to confirm that
// accounted CPU is the *only* blocking dimension before consulting live
// host load; the cpuset check stays strict even here because a pinned
// core is a physical exclusivity guarantee, not an accounting fiction.
func (rt *ResourceTracker) CanFitIgnoringCPU(req manifest.ResourceList) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.canFitLocked(req, true)
}

func (rt *ResourceTracker) canFitLocked(req manifest.ResourceList, skipCPU bool) bool {
	avail := rt.availableLocked()
	if !skipCPU && req.CPUMillis > avail.CPUMillis {
		return false
	}
	if req.MemoryMB > avail.MemoryMB || req.GPUMemoryMB > avail.GPUMemoryMB {
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
	if len(rt.cores) > 0 && req.CPUMillis >= 1000 && req.CPUMillis%1000 == 0 {
		n := req.CPUMillis / 1000
		if rt.unassignedCoresLocked(n) == nil {
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

	out := Resources{
		CPUMillis:   rt.allocatable.CPUMillis,
		MemoryMB:    rt.allocatable.MemoryMB,
		GPUCount:    rt.allocatable.GPUCount,
		GPUMemoryMB: rt.allocatable.GPUMemoryMB,
	}
	if len(rt.allocatable.Cores) > 0 {
		out.Cores = make([]int, len(rt.allocatable.Cores))
		copy(out.Cores, rt.allocatable.Cores)
	}
	return out
}

// UpdateAllocation raises the tracked allocation for a pod to reflect actual
// resource usage. Only updates if the pod exists in the allocation map.
//
// Values are monotonic: each field may only grow, never shrink. The initial
// allocation is the admitted request; shrinking it to a pod's instantaneous
// usage would let the scheduler admit new pods into memory the running pod
// is entitled to claim back, overcommitting the node (issue #43). A pod
// observed using more than it requested is accounted at its high-water mark.
func (rt *ResourceTracker) UpdateAllocation(name string, actual manifest.ResourceList) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	cur, ok := rt.allocations[name]
	if !ok {
		return
	}
	if actual.CPUMillis > cur.CPUMillis {
		cur.CPUMillis = actual.CPUMillis
	}
	if actual.MemoryMB > cur.MemoryMB {
		cur.MemoryMB = actual.MemoryMB
	}
	if actual.GPUCount > cur.GPUCount {
		cur.GPUCount = actual.GPUCount
	}
	if actual.GPUMemoryMB > cur.GPUMemoryMB {
		cur.GPUMemoryMB = actual.GPUMemoryMB
	}
	rt.allocations[name] = cur
}

func (rt *ResourceTracker) availableLocked() Resources {
	alloc := rt.allocatedLocked()
	out := Resources{
		CPUMillis:   rt.allocatable.CPUMillis - alloc.CPUMillis,
		MemoryMB:    rt.allocatable.MemoryMB - alloc.MemoryMB,
		GPUCount:    rt.allocatable.GPUCount - alloc.GPUCount,
		GPUMemoryMB: rt.allocatable.GPUMemoryMB - alloc.GPUMemoryMB,
	}
	if len(rt.cores) > 0 {
		assigned := make(map[int]bool)
		for _, cs := range rt.coreAssignments {
			for _, c := range cs {
				assigned[c] = true
			}
		}
		free := make([]int, 0, len(rt.cores))
		for _, c := range rt.cores {
			if !assigned[c] {
				free = append(free, c)
			}
		}
		out.Cores = free
	}
	return out
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
