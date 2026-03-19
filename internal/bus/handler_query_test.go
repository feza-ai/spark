package bus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func TestGetHandler_ExistingPod(t *testing.T) {
	store := state.NewPodStore()
	store.Apply(manifest.PodSpec{Name: "web-1", Priority: 10})
	store.UpdateStatus("web-1", state.StatusRunning, "container started")

	bus := NewStubBus()
	RegisterGetHandler(bus, store)

	reqData, _ := json.Marshal(GetRequest{Name: "web-1"})
	reply, err := bus.Request(context.Background(), "req.spark.get", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp GetResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if resp.Name != "web-1" {
		t.Errorf("Name = %q, want %q", resp.Name, "web-1")
	}
	if resp.Status != "running" {
		t.Errorf("Status = %q, want %q", resp.Status, "running")
	}
	if resp.Priority != 10 {
		t.Errorf("Priority = %d, want %d", resp.Priority, 10)
	}
	if resp.StartedAt == "" {
		t.Error("StartedAt should be set for running pod")
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.Events[0].Type != "running" {
		t.Errorf("event type = %q, want %q", resp.Events[0].Type, "running")
	}
	if resp.Events[0].Message != "container started" {
		t.Errorf("event message = %q, want %q", resp.Events[0].Message, "container started")
	}
}

func TestGetHandler_MissingPod(t *testing.T) {
	store := state.NewPodStore()
	bus := NewStubBus()
	RegisterGetHandler(bus, store)

	reqData, _ := json.Marshal(GetRequest{Name: "nonexistent"})
	reply, err := bus.Request(context.Background(), "req.spark.get", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp GetResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if resp.Error == "" {
		t.Fatal("expected error for missing pod, got empty")
	}
}

func TestListHandler_EmptyStore(t *testing.T) {
	store := state.NewPodStore()
	bus := NewStubBus()
	RegisterListHandler(bus, store)

	reqData, _ := json.Marshal(ListRequest{})
	reply, err := bus.Request(context.Background(), "req.spark.list", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp ListResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Pods) != 0 {
		t.Fatalf("expected 0 pods, got %d", len(resp.Pods))
	}
}

func TestListHandler_AllPods(t *testing.T) {
	store := state.NewPodStore()
	store.Apply(manifest.PodSpec{Name: "web-1", Priority: 10})
	store.Apply(manifest.PodSpec{Name: "worker-1", Priority: 5})
	store.UpdateStatus("web-1", state.StatusRunning, "started")

	bus := NewStubBus()
	RegisterListHandler(bus, store)

	reqData, _ := json.Marshal(ListRequest{})
	reply, err := bus.Request(context.Background(), "req.spark.list", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp ListResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(resp.Pods))
	}
}

func TestListHandler_FilterByStatus(t *testing.T) {
	store := state.NewPodStore()
	store.Apply(manifest.PodSpec{Name: "web-1", Priority: 10})
	store.Apply(manifest.PodSpec{Name: "worker-1", Priority: 5})
	store.UpdateStatus("web-1", state.StatusRunning, "started")
	// worker-1 remains pending

	bus := NewStubBus()
	RegisterListHandler(bus, store)

	reqData, _ := json.Marshal(ListRequest{Status: "running"})
	reply, err := bus.Request(context.Background(), "req.spark.list", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp ListResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(resp.Pods))
	}
	if resp.Pods[0].Name != "web-1" {
		t.Errorf("Name = %q, want %q", resp.Pods[0].Name, "web-1")
	}
	if resp.Pods[0].Status != "running" {
		t.Errorf("Status = %q, want %q", resp.Pods[0].Status, "running")
	}
}
