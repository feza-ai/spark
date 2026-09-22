package main

import (
	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/scheduler"
)

// totalResources builds the resource tracker's total node capacity from the
// detected host and GPU facts.
//
// This is a named function rather than a struct literal inline in main so
// that the wiring is testable. Every field here is a separate opportunity
// to silently drop a dimension: GPUCount was missing from the literal this
// replaces, from the daemon's first version until issue #114. Because
// ResourceTracker derives allocatable as total minus reserve, that omission
// pinned allocatable.GPUCount at 0 on every node, forever.
//
// The failure it caused was invisible for months because nothing on the
// direct-admission path reads that field: canFitLocked and allocate both
// gate GPUs through the device-slot map (gpuDevices/gpuMax), which was
// populated correctly. Only the preemption planner reads the scalar, via
// Available().GPUCount. So a GPU pod that fit directly ran fine, while a
// GPU pod that needed preemption could never be satisfied -- freed.GPUCount
// starts at 0 and no CPU-only victim ever contributes a GPU -- and reported
// the contradictory "gpu 1 > 0 free" against a completely idle device.
func totalResources(sysInfo gpu.SystemInfo, gpuInfo gpu.GPUInfo) scheduler.Resources {
	return scheduler.Resources{
		CPUMillis:   sysInfo.CPUMillis,
		MemoryMB:    sysInfo.MemoryTotalMB,
		GPUCount:    gpuInfo.GPUCount,
		GPUMemoryMB: gpuInfo.MemoryTotalMB,
		Cores:       sysInfo.CoreIDs,
	}
}
