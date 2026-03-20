package manifest

import "testing"

func TestParseStatefulSet_WithReplicas(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: myapp
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: myapp
    spec:
      containers:
        - name: web
          image: nginx:latest
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
              nvidia.com/gpu: "1"
            limits:
              cpu: 500m
              memory: 512Mi
      volumes:
        - name: data
          emptyDir: {}
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	pods, err := parseStatefulSet(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseStatefulSet() error: %v", err)
	}

	if got := len(pods); got != 2 {
		t.Fatalf("expected 2 pods, got %d", got)
	}

	// Verify stable ordered naming.
	if got := pods[0].Name; got != "myapp-0" {
		t.Errorf("pods[0].Name = %q, want %q", got, "myapp-0")
	}
	if got := pods[1].Name; got != "myapp-1" {
		t.Errorf("pods[1].Name = %q, want %q", got, "myapp-1")
	}

	// Verify SourceKind and SourceName.
	for i, pod := range pods {
		if pod.SourceKind != "StatefulSet" {
			t.Errorf("pods[%d].SourceKind = %q, want %q", i, pod.SourceKind, "StatefulSet")
		}
		if pod.SourceName != "myapp" {
			t.Errorf("pods[%d].SourceName = %q, want %q", i, pod.SourceName, "myapp")
		}
		if pod.RestartPolicy != "Always" {
			t.Errorf("pods[%d].RestartPolicy = %q, want %q", i, pod.RestartPolicy, "Always")
		}
	}

	// Verify resource parsing on first pod.
	pod := pods[0]
	if len(pod.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	if c.Name != "web" {
		t.Errorf("container.Name = %q, want %q", c.Name, "web")
	}
	if c.Image != "nginx:latest" {
		t.Errorf("container.Image = %q, want %q", c.Image, "nginx:latest")
	}

	// cpu: "2" -> 2000 millis
	if got := c.Resources.Requests.CPUMillis; got != 2000 {
		t.Errorf("Requests.CPUMillis = %d, want 2000", got)
	}
	// memory: 8Gi -> 8192 MB
	if got := c.Resources.Requests.MemoryMB; got != 8192 {
		t.Errorf("Requests.MemoryMB = %d, want 8192", got)
	}
	// nvidia.com/gpu: "1" -> 1
	if got := c.Resources.Requests.GPUCount; got != 1 {
		t.Errorf("Requests.GPUCount = %d, want 1", got)
	}
	// limits: cpu: 500m -> 500
	if got := c.Resources.Limits.CPUMillis; got != 500 {
		t.Errorf("Limits.CPUMillis = %d, want 500", got)
	}
	// limits: memory: 512Mi -> 512
	if got := c.Resources.Limits.MemoryMB; got != 512 {
		t.Errorf("Limits.MemoryMB = %d, want 512", got)
	}

	// Verify labels propagated.
	if pod.Labels["app"] != "myapp" {
		t.Errorf("Labels[app] = %q, want %q", pod.Labels["app"], "myapp")
	}

	// Verify volumes.
	if len(pod.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(pod.Volumes))
	}
	if pod.Volumes[0].Name != "data" {
		t.Errorf("volume.Name = %q, want %q", pod.Volumes[0].Name, "data")
	}
}

func TestParseStatefulSet_DefaultReplicas(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: single
spec:
  template:
    spec:
      containers:
        - name: app
          image: busybox
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	pods, err := parseStatefulSet(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseStatefulSet() error: %v", err)
	}

	if got := len(pods); got != 1 {
		t.Fatalf("expected 1 pod, got %d", got)
	}

	pod := pods[0]
	if pod.Name != "single-0" {
		t.Errorf("pod.Name = %q, want %q", pod.Name, "single-0")
	}
	if pod.SourceKind != "StatefulSet" {
		t.Errorf("pod.SourceKind = %q, want %q", pod.SourceKind, "StatefulSet")
	}
}

func TestParseStatefulSet_StableOrderedNames(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
spec:
  replicas: 5
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:16
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	pods, err := parseStatefulSet(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseStatefulSet() error: %v", err)
	}

	if got := len(pods); got != 5 {
		t.Fatalf("expected 5 pods, got %d", got)
	}

	want := []string{"db-0", "db-1", "db-2", "db-3", "db-4"}
	for i, w := range want {
		if pods[i].Name != w {
			t.Errorf("pods[%d].Name = %q, want %q", i, pods[i].Name, w)
		}
	}
}

func TestParseStatefulSet_PriorityClass(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: prioritized
spec:
  replicas: 1
  template:
    spec:
      priorityClassName: critical
      containers:
        - name: app
          image: myapp
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	classes := DefaultPriorityClasses()
	pods, err := parseStatefulSet(root, classes)
	if err != nil {
		t.Fatalf("parseStatefulSet() error: %v", err)
	}

	pod := pods[0]
	if pod.PriorityClassName != "critical" {
		t.Errorf("PriorityClassName = %q, want %q", pod.PriorityClassName, "critical")
	}
	if pod.Priority != 100 {
		t.Errorf("Priority = %d, want 100", pod.Priority)
	}
}

func TestParseStatefulSet_MissingName(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    app: test
spec:
  template:
    spec:
      containers:
        - name: app
          image: myapp
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	_, err = parseStatefulSet(root, DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected error for missing metadata.name")
	}
}

func TestParseStatefulSet_MissingTemplateSpec(t *testing.T) {
	input := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: broken
spec:
  replicas: 1
`

	root, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML() error: %v", err)
	}

	_, err = parseStatefulSet(root, DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected error for missing spec.template.spec")
	}
}
