package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// Heartbeat is the payload published on each heartbeat tick.
type Heartbeat struct {
	NodeID       string `json:"nodeId"`
	GPUModel     string `json:"gpuModel,omitempty"`
	GPUMemoryMB  int    `json:"gpuMemoryMB,omitempty"`
	CPUTotal     int    `json:"cpuTotal"`
	CPUAvailable int    `json:"cpuAvailable"`
	RAMTotal     int    `json:"ramTotal"`
	RAMAvailable int    `json:"ramAvailable"`
	RunningPods  int    `json:"runningPods"`
	PendingPods  int    `json:"pendingPods"`
	Uptime       string `json:"uptime"`
	Timestamp    string `json:"timestamp"`
}

// HeartbeatPublisher periodically publishes node heartbeats to the bus.
type HeartbeatPublisher struct {
	bus      Bus
	nodeID   string
	tracker  *scheduler.ResourceTracker
	store    *state.PodStore
	gpuModel string
	gpuMem   int
	cpuTotal int
	ramTotal int
	started  time.Time
}

// NewHeartbeatPublisher creates a HeartbeatPublisher.
func NewHeartbeatPublisher(b Bus, nodeID string, tracker *scheduler.ResourceTracker, store *state.PodStore, gpuModel string, gpuMem, cpuTotal, ramTotal int) *HeartbeatPublisher {
	return &HeartbeatPublisher{
		bus:      b,
		nodeID:   nodeID,
		tracker:  tracker,
		store:    store,
		gpuModel: gpuModel,
		gpuMem:   gpuMem,
		cpuTotal: cpuTotal,
		ramTotal: ramTotal,
		started:  time.Now(),
	}
}

// Run publishes heartbeats at the given interval until ctx is cancelled.
func (hp *HeartbeatPublisher) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := hp.publishOnce(); err != nil {
				slog.Error("heartbeat publish failed", "error", err)
			}
		}
	}
}

// publishOnce sends a single heartbeat.
func (hp *HeartbeatPublisher) publishOnce() error {
	avail := hp.tracker.Available()

	running := len(hp.store.List(state.StatusRunning))
	pending := len(hp.store.List(state.StatusPending))

	hb := Heartbeat{
		NodeID:       hp.nodeID,
		GPUModel:     hp.gpuModel,
		GPUMemoryMB:  hp.gpuMem,
		CPUTotal:     hp.cpuTotal,
		CPUAvailable: avail.CPUMillis,
		RAMTotal:     hp.ramTotal,
		RAMAvailable: avail.MemoryMB,
		RunningPods:  running,
		PendingPods:  pending,
		Uptime:       time.Since(hp.started).Round(time.Second).String(),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(hb)
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}

	subject := fmt.Sprintf("heartbeat.spark.%s", hp.nodeID)
	return hp.bus.Publish(subject, data)
}
