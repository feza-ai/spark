package main

import (
	"testing"

	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/scheduler"
)

// detected mirrors the shape of a single-GPU node: the GB10 box this was
// found on reports 20 cores, ~122GB of unified memory, and one device.
func detected() (gpu.SystemInfo, gpu.GPUInfo) {
	return gpu.SystemInfo{
			CPUMillis:     20000,
			MemoryTotalMB: 122564,
			CoreIDs:       []int{0, 1, 2, 3},
		}, gpu.GPUInfo{
			Model:         "NVIDIA GB10",
			MemoryTotalMB: 122564,
			GPUCount:      1,
			DeviceIDs:     []int{0},
		}
}

func TestTotalResources_CarriesEveryDetectedDimension(t *testing.T) {
	sysInfo, gpuInfo := detected()

	total := totalResources(sysInfo, gpuInfo)

	if total.CPUMillis != 20000 {
		t.Errorf("CPUMillis = %d, want 20000", total.CPUMillis)
	}
	if total.MemoryMB != 122564 {
		t.Errorf("MemoryMB = %d, want 122564", total.MemoryMB)
	}
	// The dimension issue #114 dropped. Detection reported count=1 the
	// whole time; the literal building total simply never copied it.
	if total.GPUCount != 1 {
		t.Errorf("GPUCount = %d, want 1 (issue #114)", total.GPUCount)
	}
	if total.GPUMemoryMB != 122564 {
		t.Errorf("GPUMemoryMB = %d, want 122564", total.GPUMemoryMB)
	}
	if len(total.Cores) != 4 {
		t.Errorf("Cores = %v, want 4 entries", total.Cores)
	}
}

// TestTotalResources_LeavesGPUAvailableToThePreemptionPlanner asserts the
// consequence rather than the field, because the field alone is not what
// broke: allocatable.GPUCount is the only GPU number the preemption planner
// can see, and a 0 there makes every GPU pod that needs preemption
// permanently unschedulable while the device-slot path still reports the
// GPU as idle. This test fails against the pre-fix literal.
func TestTotalResources_LeavesGPUAvailableToThePreemptionPlanner(t *testing.T) {
	sysInfo, gpuInfo := detected()

	tracker := scheduler.NewResourceTracker(
		totalResources(sysInfo, gpuInfo),
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 12288},
		gpuInfo.DeviceIDs,
		1,
	)

	if got := tracker.Available().GPUCount; got != 1 {
		t.Errorf("Available().GPUCount = %d, want 1 on an idle single-GPU node; "+
			"0 here is what produces the contradictory %q shortfall (issue #114)",
			got, "gpu 1 > 0 free")
	}
}
