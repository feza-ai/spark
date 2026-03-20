package bus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// mockCronRegisterer records Register calls for testing.
type mockCronRegisterer struct {
	registered []manifest.CronJobSpec
	err        error
}

func (m *mockCronRegisterer) Register(cj manifest.CronJobSpec) error {
	m.registered = append(m.registered, cj)
	return m.err
}

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

func TestApplyHandler_MissingKind(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: v1
metadata:
  name: no-kind-pod
spec:
  containers:
  - name: main
    image: nginx
`)
	if resp.Error == "" {
		t.Fatal("expected error for manifest without kind, got none")
	}
}

func TestApplyHandler_UnsupportedKind(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
data:
  key: value
`)
	if resp.Error != "" {
		t.Errorf("unexpected error for unsupported kind: %s", resp.Error)
	}
	if len(resp.Pods) != 0 {
		t.Errorf("expected 0 pods for unsupported kind, got %d", len(resp.Pods))
	}
}

func TestApplyHandler_CronJob(t *testing.T) {
	resp := registerAndApply(t, `
apiVersion: batch/v1
kind: CronJob
metadata:
  name: my-cron
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: worker
            image: busybox
          restartPolicy: Never
`)
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
	if len(resp.CronJobs) != 1 {
		t.Fatalf("expected 1 cronJob, got %d", len(resp.CronJobs))
	}
	if resp.CronJobs[0] != "my-cron" {
		t.Errorf("cronJob name = %q, want %q", resp.CronJobs[0], "my-cron")
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

func TestApplyHandler_CronJobRegistered(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		regErr     error
		wantCount  int
		wantName   string
		wantErrSub string
	}{
		{
			name: "register called on CronJob apply",
			yaml: `
apiVersion: batch/v1
kind: CronJob
metadata:
  name: scheduled-task
spec:
  schedule: "0 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: runner
            image: alpine
          restartPolicy: Never
`,
			wantCount: 1,
			wantName:  "scheduled-task",
		},
		{
			name:   "register error propagates",
			regErr: errors.New("scheduler full"),
			yaml: `
apiVersion: batch/v1
kind: CronJob
metadata:
  name: fail-cron
spec:
  schedule: "0 0 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: runner
            image: alpine
          restartPolicy: Never
`,
			wantCount:  1,
			wantErrSub: "cron register: scheduler full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewStubBus()
			store := state.NewPodStore()
			mock := &mockCronRegisterer{err: tt.regErr}
			RegisterApplyHandler(b, store, map[string]int{}, mock)

			raw, err := b.Request(context.Background(), "req.spark.apply", []byte(tt.yaml))
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}

			var resp ApplyResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			if len(mock.registered) != tt.wantCount {
				t.Fatalf("Register called %d times, want %d", len(mock.registered), tt.wantCount)
			}

			if tt.wantErrSub != "" {
				if resp.Error != tt.wantErrSub {
					t.Errorf("error = %q, want %q", resp.Error, tt.wantErrSub)
				}
				return
			}

			if resp.Error != "" {
				t.Errorf("unexpected error: %s", resp.Error)
			}
			if tt.wantName != "" && mock.registered[0].Name != tt.wantName {
				t.Errorf("registered cron name = %q, want %q", mock.registered[0].Name, tt.wantName)
			}
		})
	}
}

func TestApplyHandler_NilCronRegisterer(t *testing.T) {
	b := NewStubBus()
	store := state.NewPodStore()
	RegisterApplyHandler(b, store, map[string]int{})

	yaml := `
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nil-reg-cron
spec:
  schedule: "0 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: runner
            image: alpine
          restartPolicy: Never
`
	raw, err := b.Request(context.Background(), "req.spark.apply", []byte(yaml))
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var resp ApplyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
	if len(resp.CronJobs) != 1 {
		t.Fatalf("expected 1 cronJob, got %d", len(resp.CronJobs))
	}
	if resp.CronJobs[0] != "nil-reg-cron" {
		t.Errorf("cronJob name = %q, want %q", resp.CronJobs[0], "nil-reg-cron")
	}
}
