package manifest

import (
	"testing"
)

func TestParse_ValidPod(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: training-pod
  labels:
    app: training
    team: ml
  annotations:
    description: "A training pod"
spec:
  priorityClassName: high
  restartPolicy: Never
  terminationGracePeriodSeconds: 30
  containers:
    - name: trainer
      image: nvidia/cuda:12.0
      command:
        - python
        - train.py
      args:
        - --epochs
        - "10"
      env:
        - name: BATCH_SIZE
          value: "32"
      volumeMounts:
        - name: data-vol
          mountPath: /data
          readOnly: true
      resources:
        requests:
          cpu: "2"
          memory: 8Gi
          nvidia.com/gpu: "1"
        limits:
          cpu: "4"
          memory: 16Gi
          nvidia.com/gpu: "1"
  volumes:
    - name: data-vol
      hostPath:
        path: /mnt/data
    - name: tmp-vol
      emptyDir: {}
`
	pc := DefaultPriorityClasses()
	result, err := Parse([]byte(yaml), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(result.Pods))
	}

	pod := result.Pods[0]
	if pod.Name != "training-pod" {
		t.Errorf("name = %q, want %q", pod.Name, "training-pod")
	}
	if pod.SourceKind != "Pod" {
		t.Errorf("sourceKind = %q, want %q", pod.SourceKind, "Pod")
	}
	if pod.Labels["app"] != "training" {
		t.Errorf("labels[app] = %q, want %q", pod.Labels["app"], "training")
	}
	if pod.Labels["team"] != "ml" {
		t.Errorf("labels[team] = %q, want %q", pod.Labels["team"], "ml")
	}
	if pod.Annotations["description"] != "A training pod" {
		t.Errorf("annotations[description] = %q, want %q", pod.Annotations["description"], "A training pod")
	}
	if pod.RestartPolicy != "Never" {
		t.Errorf("restartPolicy = %q, want %q", pod.RestartPolicy, "Never")
	}
	if pod.PriorityClassName != "high" {
		t.Errorf("priorityClassName = %q, want %q", pod.PriorityClassName, "high")
	}
	if pod.Priority != 500 {
		t.Errorf("priority = %d, want %d", pod.Priority, 500)
	}
	if pod.TerminationGracePeriodSeconds != 30 {
		t.Errorf("terminationGracePeriodSeconds = %d, want %d", pod.TerminationGracePeriodSeconds, 30)
	}

	// Container checks
	if len(pod.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	if c.Name != "trainer" {
		t.Errorf("container name = %q, want %q", c.Name, "trainer")
	}
	if c.Image != "nvidia/cuda:12.0" {
		t.Errorf("container image = %q, want %q", c.Image, "nvidia/cuda:12.0")
	}
	if len(c.Command) != 2 || c.Command[0] != "python" || c.Command[1] != "train.py" {
		t.Errorf("command = %v, want [python train.py]", c.Command)
	}
	if len(c.Args) != 2 || c.Args[0] != "--epochs" || c.Args[1] != "10" {
		t.Errorf("args = %v, want [--epochs 10]", c.Args)
	}

	// Env
	if len(c.Env) != 1 || c.Env[0].Name != "BATCH_SIZE" || c.Env[0].Value != "32" {
		t.Errorf("env = %v, want [{BATCH_SIZE 32}]", c.Env)
	}

	// Volume mounts
	if len(c.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(c.VolumeMounts))
	}
	if c.VolumeMounts[0].Name != "data-vol" || c.VolumeMounts[0].MountPath != "/data" || !c.VolumeMounts[0].ReadOnly {
		t.Errorf("volumeMount = %+v", c.VolumeMounts[0])
	}

	// Resources
	if c.Resources.Requests.CPUMillis != 2000 {
		t.Errorf("requests.cpu = %d, want 2000", c.Resources.Requests.CPUMillis)
	}
	if c.Resources.Requests.MemoryMB != 8192 {
		t.Errorf("requests.memory = %d, want 8192", c.Resources.Requests.MemoryMB)
	}
	if c.Resources.Requests.GPUCount != 1 {
		t.Errorf("requests.gpuCount = %d, want 1", c.Resources.Requests.GPUCount)
	}
	if c.Resources.Limits.CPUMillis != 4000 {
		t.Errorf("limits.cpu = %d, want 4000", c.Resources.Limits.CPUMillis)
	}
	if c.Resources.Limits.MemoryMB != 16384 {
		t.Errorf("limits.memory = %d, want 16384", c.Resources.Limits.MemoryMB)
	}

	// Volumes
	if len(pod.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(pod.Volumes))
	}
	if pod.Volumes[0].Name != "data-vol" || pod.Volumes[0].HostPath != "/mnt/data" {
		t.Errorf("volume[0] = %+v", pod.Volumes[0])
	}
	if pod.Volumes[1].Name != "tmp-vol" || !pod.Volumes[1].EmptyDir {
		t.Errorf("volume[1] = %+v", pod.Volumes[1])
	}
}

func TestParse_MultiContainerPod(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: multi-container
spec:
  containers:
    - name: app
      image: myapp:latest
      resources:
        requests:
          cpu: 500m
          memory: 512Mi
    - name: sidecar
      image: sidecar:latest
      resources:
        requests:
          cpu: 100m
          memory: 128Mi
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(result.Pods))
	}
	pod := result.Pods[0]
	if len(pod.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(pod.Containers))
	}
	if pod.Containers[0].Name != "app" {
		t.Errorf("container[0].name = %q, want %q", pod.Containers[0].Name, "app")
	}
	if pod.Containers[1].Name != "sidecar" {
		t.Errorf("container[1].name = %q, want %q", pod.Containers[1].Name, "sidecar")
	}

	total := pod.TotalRequests()
	if total.CPUMillis != 600 {
		t.Errorf("total cpu = %d, want 600", total.CPUMillis)
	}
	if total.MemoryMB != 640 {
		t.Errorf("total memory = %d, want 640", total.MemoryMB)
	}
}

func TestParse_ResourceQuantities(t *testing.T) {
	cpuTests := []struct {
		name    string
		cpu     string
		wantCPU int
	}{
		{"whole cores", "2", 2000},
		{"millicores", "500m", 500},
		{"fractional", "0.5", 500},
		{"one core", "1", 1000},
	}
	for _, tt := range cpuTests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCPU(tt.cpu)
			if got != tt.wantCPU {
				t.Errorf("parseCPU(%q) = %d, want %d", tt.cpu, got, tt.wantCPU)
			}
		})
	}

	memTests := []struct {
		name   string
		mem    string
		wantMB int
	}{
		{"gibibytes", "8Gi", 8192},
		{"mebibytes", "512Mi", 512},
	}
	for _, tt := range memTests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMemory(tt.mem)
			if got != tt.wantMB {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.mem, got, tt.wantMB)
			}
		})
	}
}

func TestParse_EnvVarsAndVolumeMounts(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: env-test
spec:
  containers:
    - name: main
      image: test:latest
      env:
        - name: FOO
          value: bar
        - name: BAZ
          value: "qux"
      volumeMounts:
        - name: config
          mountPath: /etc/config
          readOnly: true
        - name: logs
          mountPath: /var/log
  volumes:
    - name: config
      hostPath:
        path: /host/config
    - name: logs
      emptyDir: {}
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := result.Pods[0].Containers[0]
	if len(c.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(c.Env))
	}
	if c.Env[0].Name != "FOO" || c.Env[0].Value != "bar" {
		t.Errorf("env[0] = %+v", c.Env[0])
	}
	if c.Env[1].Name != "BAZ" || c.Env[1].Value != "qux" {
		t.Errorf("env[1] = %+v", c.Env[1])
	}
	if len(c.VolumeMounts) != 2 {
		t.Fatalf("expected 2 volume mounts, got %d", len(c.VolumeMounts))
	}
	if !c.VolumeMounts[0].ReadOnly {
		t.Error("volumeMount[0] should be readOnly")
	}
	if c.VolumeMounts[1].ReadOnly {
		t.Error("volumeMount[1] should not be readOnly")
	}

	vols := result.Pods[0].Volumes
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}
	if vols[0].HostPath != "/host/config" {
		t.Errorf("volume[0].hostPath = %q", vols[0].HostPath)
	}
	if !vols[1].EmptyDir {
		t.Error("volume[1] should be emptyDir")
	}
}

func TestParse_LabelsAndAnnotations(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: label-test
  labels:
    app: web
    version: v2
  annotations:
    note: "test annotation"
spec:
  containers:
    - name: main
      image: test:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pod := result.Pods[0]
	if len(pod.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(pod.Labels))
	}
	if pod.Labels["version"] != "v2" {
		t.Errorf("labels[version] = %q", pod.Labels["version"])
	}
	if pod.Annotations["note"] != "test annotation" {
		t.Errorf("annotations[note] = %q", pod.Annotations["note"])
	}
}

func TestParse_UnsupportedKind(t *testing.T) {
	yaml := "apiVersion: v1\nkind: DaemonSet\nmetadata:\n  name: test\n"
	_, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unknown kinds should be silently ignored, got error: %v", err)
	}
}

func TestParse_DeploymentViaDispatch(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: app
          image: alpine:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(result.Pods))
	}
	if result.Pods[0].Name != "my-deploy-0" {
		t.Errorf("expected my-deploy-0, got %s", result.Pods[0].Name)
	}
}

func TestParse_CronJobViaDispatch(t *testing.T) {
	yaml := `apiVersion: batch/v1
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
              image: alpine:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.CronJobs) != 1 {
		t.Fatalf("expected 1 cronjob, got %d", len(result.CronJobs))
	}
	if result.CronJobs[0].Schedule != "*/5 * * * *" {
		t.Errorf("expected schedule */5 * * * *, got %s", result.CronJobs[0].Schedule)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	yaml := `not: valid: yaml: content`
	_, err := Parse([]byte(yaml), nil)
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
}

func TestParse_MissingMetadataName(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  labels:
    app: test
spec:
  containers:
    - name: main
      image: test:latest
`
	_, err := Parse([]byte(yaml), nil)
	if err == nil {
		t.Fatal("expected error for missing metadata.name")
	}
	if got := err.Error(); got != "parsing Pod: metadata.name is required" {
		t.Errorf("error = %q", got)
	}
}

func TestParse_MultiDocument(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: pod-one
spec:
  containers:
    - name: main
      image: test:latest
---
apiVersion: v1
kind: Pod
metadata:
  name: pod-two
spec:
  containers:
    - name: main
      image: test:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(result.Pods))
	}
	if result.Pods[0].Name != "pod-one" {
		t.Errorf("pod[0].name = %q", result.Pods[0].Name)
	}
	if result.Pods[1].Name != "pod-two" {
		t.Errorf("pod[1].name = %q", result.Pods[1].Name)
	}
}

func TestParsePorts(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: port-test
spec:
  containers:
    - name: web
      image: nginx:latest
      ports:
        - containerPort: 80
          hostPort: 8080
          protocol: tcp
        - containerPort: 443
          hostPort: 8443
          protocol: udp
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := result.Pods[0].Containers[0]
	if len(c.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(c.Ports))
	}
	if c.Ports[0].ContainerPort != 80 {
		t.Errorf("ports[0].containerPort = %d, want 80", c.Ports[0].ContainerPort)
	}
	if c.Ports[0].HostPort != 8080 {
		t.Errorf("ports[0].hostPort = %d, want 8080", c.Ports[0].HostPort)
	}
	if c.Ports[0].Protocol != "tcp" {
		t.Errorf("ports[0].protocol = %q, want %q", c.Ports[0].Protocol, "tcp")
	}
	if c.Ports[1].ContainerPort != 443 {
		t.Errorf("ports[1].containerPort = %d, want 443", c.Ports[1].ContainerPort)
	}
	if c.Ports[1].HostPort != 8443 {
		t.Errorf("ports[1].hostPort = %d, want 8443", c.Ports[1].HostPort)
	}
	if c.Ports[1].Protocol != "udp" {
		t.Errorf("ports[1].protocol = %q, want %q", c.Ports[1].Protocol, "udp")
	}
}

func TestParsePortsDefaultProtocol(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: port-default-proto
spec:
  containers:
    - name: web
      image: nginx:latest
      ports:
        - containerPort: 80
          hostPort: 8080
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := result.Pods[0].Containers[0]
	if len(c.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(c.Ports))
	}
	if c.Ports[0].Protocol != "tcp" {
		t.Errorf("protocol = %q, want %q", c.Ports[0].Protocol, "tcp")
	}
}

func TestParsePortsMissingHostPort(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: port-no-host
spec:
  containers:
    - name: web
      image: nginx:latest
      ports:
        - containerPort: 3000
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := result.Pods[0].Containers[0]
	if len(c.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(c.Ports))
	}
	if c.Ports[0].ContainerPort != 3000 {
		t.Errorf("containerPort = %d, want 3000", c.Ports[0].ContainerPort)
	}
	if c.Ports[0].HostPort != 3000 {
		t.Errorf("hostPort = %d, want 3000 (should default to containerPort)", c.Ports[0].HostPort)
	}
	if c.Ports[0].Protocol != "tcp" {
		t.Errorf("protocol = %q, want %q", c.Ports[0].Protocol, "tcp")
	}
}

func TestParse_MissingKind(t *testing.T) {
	yaml := `apiVersion: v1
metadata:
  name: test
`
	_, err := Parse([]byte(yaml), nil)
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
}
