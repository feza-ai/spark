package manifest

import (
	"testing"
)

func TestParseDeploymentReplicas(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: wolf-data-pipeline
spec:
  replicas: 3
  selector:
    matchLabels:
      app: wolf-pipeline
  template:
    metadata:
      labels:
        app: wolf-pipeline
    spec:
      priorityClassName: normal
      restartPolicy: Always
      containers:
        - name: pipeline
          image: localhost:5000/wolf-pipeline:latest
          resources:
            requests:
              cpu: "1"
              memory: 4Gi
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	pods, err := parseDeployment(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseDeployment: %v", err)
	}

	if got := len(pods); got != 3 {
		t.Fatalf("expected 3 pods, got %d", got)
	}

	wantNames := []string{"wolf-data-pipeline-0", "wolf-data-pipeline-1", "wolf-data-pipeline-2"}
	for i, want := range wantNames {
		if pods[i].Name != want {
			t.Errorf("pod[%d].Name = %q, want %q", i, pods[i].Name, want)
		}
	}
}

func TestParseDeploymentDefaultReplicas(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: single-app
spec:
  template:
    spec:
      containers:
        - name: app
          image: myimage:latest
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	pods, err := parseDeployment(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseDeployment: %v", err)
	}

	if got := len(pods); got != 1 {
		t.Fatalf("expected 1 pod, got %d", got)
	}

	if pods[0].Name != "single-app-0" {
		t.Errorf("Name = %q, want %q", pods[0].Name, "single-app-0")
	}
}

func TestParseDeploymentSourceKind(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: web
          image: nginx:latest
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	pods, err := parseDeployment(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseDeployment: %v", err)
	}

	for i, pod := range pods {
		if pod.SourceKind != "Deployment" {
			t.Errorf("pod[%d].SourceKind = %q, want %q", i, pod.SourceKind, "Deployment")
		}
		if pod.SourceName != "my-deploy" {
			t.Errorf("pod[%d].SourceName = %q, want %q", i, pod.SourceName, "my-deploy")
		}
		if pod.RestartPolicy != "Always" {
			t.Errorf("pod[%d].RestartPolicy = %q, want %q", i, pod.RestartPolicy, "Always")
		}
	}
}

func TestParseDeploymentPodTemplateFields(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: wolf-data-pipeline
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: wolf-pipeline
    spec:
      priorityClassName: normal
      containers:
        - name: pipeline
          image: localhost:5000/wolf-pipeline:latest
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
            limits:
              cpu: "4"
              memory: 16Gi
              nvidia.com/gpu: "1"
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	pods, err := parseDeployment(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseDeployment: %v", err)
	}

	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}

	for i, pod := range pods {
		if len(pod.Containers) != 1 {
			t.Fatalf("pod[%d] expected 1 container, got %d", i, len(pod.Containers))
		}

		c := pod.Containers[0]
		if c.Name != "pipeline" {
			t.Errorf("pod[%d] container name = %q, want %q", i, c.Name, "pipeline")
		}
		if c.Image != "localhost:5000/wolf-pipeline:latest" {
			t.Errorf("pod[%d] container image = %q, want %q", i, c.Image, "localhost:5000/wolf-pipeline:latest")
		}
		if c.Resources.Requests.CPUMillis != 2000 {
			t.Errorf("pod[%d] requests.cpu = %d, want 2000", i, c.Resources.Requests.CPUMillis)
		}
		if c.Resources.Requests.MemoryMB != 8192 {
			t.Errorf("pod[%d] requests.memory = %d, want 8192", i, c.Resources.Requests.MemoryMB)
		}
		if c.Resources.Limits.CPUMillis != 4000 {
			t.Errorf("pod[%d] limits.cpu = %d, want 4000", i, c.Resources.Limits.CPUMillis)
		}
		if c.Resources.Limits.MemoryMB != 16384 {
			t.Errorf("pod[%d] limits.memory = %d, want 16384", i, c.Resources.Limits.MemoryMB)
		}
		if c.Resources.Limits.GPUCount != 1 {
			t.Errorf("pod[%d] limits.gpuCount = %d, want 1", i, c.Resources.Limits.GPUCount)
		}

		if pod.PriorityClassName != "normal" {
			t.Errorf("pod[%d] PriorityClassName = %q, want %q", i, pod.PriorityClassName, "normal")
		}
		if pod.Priority != 1000 {
			t.Errorf("pod[%d] Priority = %d, want 1000", i, pod.Priority)
		}
		if pod.Labels["app"] != "wolf-pipeline" {
			t.Errorf("pod[%d] Labels[app] = %q, want %q", i, pod.Labels["app"], "wolf-pipeline")
		}
	}
}

func TestParseDeploymentMissingTemplate(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: bad-deploy
spec:
  replicas: 1
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	_, err = parseDeployment(root, DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected error for missing template, got nil")
	}
}

func TestParseDeploymentResourceParsing(t *testing.T) {
	tests := []struct {
		name    string
		cpu     string
		wantCPU int
		mem     string
		wantMem int
	}{
		{"whole cpu", "2", 2000, "8Gi", 8192},
		{"milli cpu", "500m", 500, "512Mi", 512},
		{"fractional cpu", "0.5", 500, "1G", 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCPU(tt.cpu); got != tt.wantCPU {
				t.Errorf("parseCPU(%q) = %d, want %d", tt.cpu, got, tt.wantCPU)
			}
			if got := parseMemory(tt.mem); got != tt.wantMem {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.mem, got, tt.wantMem)
			}
		})
	}
}
