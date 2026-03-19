package bus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/feza-ai/spark/internal/state"
)

func registerAndApply(t *testing.T, yaml string) ApplyResponse {
	t.Helper()
	b := NewStubBus()
	store := state.NewPodStore()
	pc := map[string]int{}
	RegisterApplyHandler(b, store, pc)

	raw, err := b.Request(context.Background(), "req.spark.apply", []byte(yaml))
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp ApplyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return resp
}

func TestApplyHandler_Pod(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
spec:
  containers:
  - name: main
    image: nginx
`)
	if len(resp.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(resp.Pods))
	}
	if resp.Pods[0].Name != "my-pod" {
		t.Errorf("pod name = %q, want %q", resp.Pods[0].Name, "my-pod")
	}
	if resp.Pods[0].Status != "pending" {
		t.Errorf("pod status = %q, want %q", resp.Pods[0].Status, "pending")
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestApplyHandler_Deployment(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
spec:
  replicas: 2
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
      - name: main
        image: nginx
`)
	if len(resp.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(resp.Pods))
	}
	for _, p := range resp.Pods {
		if p.Status != "pending" {
			t.Errorf("pod %q status = %q, want %q", p.Name, p.Status, "pending")
		}
	}
}

func TestApplyHandler_InvalidManifest(t *testing.T) {
	resp := registerAndApply(t, `
kind: Pod
metadata:
  name: ""
spec:
  containers:
  - name: main
    image: nginx
`)
	if resp.Error == "" {
		t.Fatal("expected error for invalid manifest, got none")
	}
}

func TestApplyHandler_Job(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: batch/v1
kind: Job
metadata:
  name: my-job
spec:
  template:
    spec:
      containers:
      - name: worker
        image: busybox
      restartPolicy: Never
`)
	if len(resp.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(resp.Pods))
	}
	if resp.Pods[0].Status != "pending" {
		t.Errorf("pod status = %q, want %q", resp.Pods[0].Status, "pending")
	}
}

func TestApplyHandler_StoreContainsPods(t *testing.T) {
	b := NewStubBus()
	store := state.NewPodStore()
	RegisterApplyHandler(b, store, map[string]int{})

	yaml := `
apiVersion: v1
kind: Pod
metadata:
  name: stored-pod
spec:
  containers:
  - name: main
    image: nginx
`
	_, err := b.Request(context.Background(), "req.spark.apply", []byte(yaml))
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	rec, ok := store.Get("stored-pod")
	if !ok {
		t.Fatal("expected pod in store, not found")
	}
	if rec.Spec.Name != "stored-pod" {
		t.Errorf("stored pod name = %q, want %q", rec.Spec.Name, "stored-pod")
	}
}
