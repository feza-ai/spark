package bus

import (
	"encoding/json"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func TestPublishOnceProducesCorrectJSON(t *testing.T) {
	stub := NewStubBus()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192, GPUMemoryMB: 16000},
		scheduler.Resources{CPUMillis: 200, MemoryMB: 512, GPUMemoryMB: 0},
		nil, 0,
	)
	store := state.NewPodStore()

	hp := NewHeartbeatPublisher(stub, "node-1", tracker, store, "GH200", 1, 16000, 4000, 8192)

	if err := hp.publishOnce(); err != nil {
		t.Fatalf("publishOnce() error = %v", err)
	}

	msgs := stub.Published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Subject != "heartbeat.spark.node-1" {
		t.Errorf("subject = %q, want %q", msgs[0].Subject, "heartbeat.spark.node-1")
	}

	var hb Heartbeat
	if err := json.Unmarshal(msgs[0].Data, &hb); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}

	if hb.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", hb.NodeID, "node-1")
	}
	if hb.GPUModel != "GH200" {
		t.Errorf("GPUModel = %q, want %q", hb.GPUModel, "GH200")
	}
	if hb.GPUCount != 1 {
		t.Errorf("GPUCount = %d, want %d", hb.GPUCount, 1)
	}
	if hb.GPUMemoryMB != 16000 {
		t.Errorf("GPUMemoryMB = %d, want %d", hb.GPUMemoryMB, 16000)
	}
	if hb.CPUTotal != 4000 {
		t.Errorf("CPUTotal = %d, want %d", hb.CPUTotal, 4000)
	}
	// Available = allocatable (4000-200=3800) minus allocated (0)
	if hb.CPUAvailable != 3800 {
		t.Errorf("CPUAvailable = %d, want %d", hb.CPUAvailable, 3800)
	}
	if hb.RAMTotal != 8192 {
		t.Errorf("RAMTotal = %d, want %d", hb.RAMTotal, 8192)
	}
	// Available = allocatable (8192-512=7680) minus allocated (0)
	if hb.RAMAvailable != 7680 {
		t.Errorf("RAMAvailable = %d, want %d", hb.RAMAvailable, 7680)
	}
	if hb.Timestamp == "" {
		t.Error("Timestamp is empty")
	}
	if hb.Uptime == "" {
		t.Error("Uptime is empty")
	}
}

func TestPublishOncePodCounts(t *testing.T) {
	stub := NewStubBus()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
		nil, 0,
	)
	store := state.NewPodStore()

	// Add running pods.
	store.Apply(manifest.PodSpec{Name: "run-1"})
	store.UpdateStatus("run-1", state.StatusRunning, "started")
	store.Apply(manifest.PodSpec{Name: "run-2"})
	store.UpdateStatus("run-2", state.StatusRunning, "started")

	// Add pending pods.
	store.Apply(manifest.PodSpec{Name: "pend-1"})
	store.Apply(manifest.PodSpec{Name: "pend-2"})
	store.Apply(manifest.PodSpec{Name: "pend-3"})

	hp := NewHeartbeatPublisher(stub, "node-2", tracker, store, "", 0, 0, 4000, 8192)

	if err := hp.publishOnce(); err != nil {
		t.Fatalf("publishOnce() error = %v", err)
	}

	msgs := stub.Published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	var hb Heartbeat
	if err := json.Unmarshal(msgs[0].Data, &hb); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}

	if hb.RunningPods != 2 {
		t.Errorf("RunningPods = %d, want 2", hb.RunningPods)
	}
	if hb.PendingPods != 3 {
		t.Errorf("PendingPods = %d, want 3", hb.PendingPods)
	}

	// No GPU fields when not configured.
	if hb.GPUModel != "" {
		t.Errorf("GPUModel = %q, want empty", hb.GPUModel)
	}
	if hb.GPUCount != 0 {
		t.Errorf("GPUCount = %d, want 0", hb.GPUCount)
	}
	if hb.GPUMemoryMB != 0 {
		t.Errorf("GPUMemoryMB = %d, want 0", hb.GPUMemoryMB)
	}
}

func TestPublishOnceGPUCount(t *testing.T) {
	tests := []struct {
		name     string
		gpuCount int
		gpuMem   int
		wantJSON bool
	}{
		{name: "multi-gpu", gpuCount: 4, gpuMem: 64000, wantJSON: true},
		{name: "single-gpu", gpuCount: 1, gpuMem: 16000, wantJSON: true},
		{name: "no-gpu", gpuCount: 0, gpuMem: 0, wantJSON: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := NewStubBus()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192, GPUCount: tt.gpuCount, GPUMemoryMB: tt.gpuMem},
				scheduler.Resources{},
				nil, 0,
			)
			store := state.NewPodStore()

			hp := NewHeartbeatPublisher(stub, "node-gpu", tracker, store, "GH200", tt.gpuCount, tt.gpuMem, 4000, 8192)
			if err := hp.publishOnce(); err != nil {
				t.Fatalf("publishOnce() error = %v", err)
			}

			msgs := stub.Published()
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message, got %d", len(msgs))
			}

			var hb Heartbeat
			if err := json.Unmarshal(msgs[0].Data, &hb); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if hb.GPUCount != tt.gpuCount {
				t.Errorf("GPUCount = %d, want %d", hb.GPUCount, tt.gpuCount)
			}

			// Verify omitempty: gpuCount should not appear in JSON when zero.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(msgs[0].Data, &raw); err != nil {
				t.Fatalf("unmarshal raw: %v", err)
			}
			_, present := raw["gpuCount"]
			if present != tt.wantJSON {
				t.Errorf("gpuCount in JSON = %v, want %v", present, tt.wantJSON)
			}
		})
	}
}
