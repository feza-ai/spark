package manifest

import (
	"testing"
)

const validJobYAML = `apiVersion: batch/v1
kind: Job
metadata:
  name: training-job
spec:
  backoffLimit: 4
  template:
    metadata:
      labels:
        app: trainer
    spec:
      restartPolicy: OnFailure
      priorityClassName: high
      terminationGracePeriodSeconds: 30
      containers:
        - name: trainer
          image: registry.example.com/trainer:v1
          command:
            - python
            - train.py
          args:
            - --epochs
            - "100"
          env:
            - name: BATCH_SIZE
              value: "32"
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
              nvidia.com/gpu: "1"
            limits:
              cpu: "4"
              memory: 16Gi
              nvidia.com/gpu: "1"
          volumeMounts:
            - name: data-vol
              mountPath: /data
              readOnly: true
            - name: scratch
              mountPath: /tmp/scratch
      volumes:
        - name: data-vol
          hostPath:
            path: /mnt/datasets
        - name: scratch
          emptyDir: {}
`

func TestParseJob(t *testing.T) {
	classes := DefaultPriorityClasses()

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, pods []PodSpec)
	}{
		{
			name: "valid job manifest",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				if len(pods) != 1 {
					t.Fatalf("expected 1 pod, got %d", len(pods))
				}
				pod := pods[0]

				if pod.SourceKind != "Job" {
					t.Errorf("SourceKind = %q, want %q", pod.SourceKind, "Job")
				}
				if pod.SourceName != "training-job" {
					t.Errorf("SourceName = %q, want %q", pod.SourceName, "training-job")
				}
				if pod.Name != "training-job" {
					t.Errorf("Name = %q, want %q", pod.Name, "training-job")
				}
			},
		},
		{
			name: "backoff limit extraction",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.BackoffLimit != 4 {
					t.Errorf("BackoffLimit = %d, want 4", pod.BackoffLimit)
				}
			},
		},
		{
			name: "restart policy from spec",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.RestartPolicy != "OnFailure" {
					t.Errorf("RestartPolicy = %q, want %q", pod.RestartPolicy, "OnFailure")
				}
			},
		},
		{
			name: "priority resolution",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.PriorityClassName != "high" {
					t.Errorf("PriorityClassName = %q, want %q", pod.PriorityClassName, "high")
				}
				if pod.Priority != 500 {
					t.Errorf("Priority = %d, want 500", pod.Priority)
				}
			},
		},
		{
			name: "container fields",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if len(pod.Containers) != 1 {
					t.Fatalf("expected 1 container, got %d", len(pod.Containers))
				}
				c := pod.Containers[0]

				if c.Name != "trainer" {
					t.Errorf("container name = %q, want %q", c.Name, "trainer")
				}
				if c.Image != "registry.example.com/trainer:v1" {
					t.Errorf("container image = %q, want %q", c.Image, "registry.example.com/trainer:v1")
				}
				if len(c.Command) != 2 || c.Command[0] != "python" || c.Command[1] != "train.py" {
					t.Errorf("command = %v, want [python train.py]", c.Command)
				}
				if len(c.Args) != 2 || c.Args[0] != "--epochs" || c.Args[1] != "100" {
					t.Errorf("args = %v, want [--epochs 100]", c.Args)
				}
				if len(c.Env) != 1 || c.Env[0].Name != "BATCH_SIZE" || c.Env[0].Value != "32" {
					t.Errorf("env = %v, want [{BATCH_SIZE 32}]", c.Env)
				}
			},
		},
		{
			name: "resource parsing",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				c := pods[0].Containers[0]

				if c.Resources.Requests.CPUMillis != 2000 {
					t.Errorf("requests.cpu = %d millis, want 2000", c.Resources.Requests.CPUMillis)
				}
				if c.Resources.Requests.MemoryMB != 8192 {
					t.Errorf("requests.memory = %d MB, want 8192", c.Resources.Requests.MemoryMB)
				}
				if c.Resources.Requests.GPUCount != 1 {
					t.Errorf("requests.gpuCount = %d, want 1", c.Resources.Requests.GPUCount)
				}
				if c.Resources.Requests.GPUMemoryMB != 0 {
					t.Errorf("requests.gpuMemoryMB = %d, want 0", c.Resources.Requests.GPUMemoryMB)
				}
				if c.Resources.Limits.CPUMillis != 4000 {
					t.Errorf("limits.cpu = %d millis, want 4000", c.Resources.Limits.CPUMillis)
				}
				if c.Resources.Limits.MemoryMB != 16384 {
					t.Errorf("limits.memory = %d MB, want 16384", c.Resources.Limits.MemoryMB)
				}
				if c.Resources.Limits.GPUCount != 1 {
					t.Errorf("limits.gpuCount = %d, want 1", c.Resources.Limits.GPUCount)
				}
			},
		},
		{
			name: "volume parsing",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if len(pod.Volumes) != 2 {
					t.Fatalf("expected 2 volumes, got %d", len(pod.Volumes))
				}

				if pod.Volumes[0].Name != "data-vol" || pod.Volumes[0].HostPath != "/mnt/datasets" {
					t.Errorf("volume[0] = %+v, want data-vol hostPath=/mnt/datasets", pod.Volumes[0])
				}
				if pod.Volumes[1].Name != "scratch" || !pod.Volumes[1].EmptyDir {
					t.Errorf("volume[1] = %+v, want scratch emptyDir=true", pod.Volumes[1])
				}
			},
		},
		{
			name: "volume mount parsing",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				c := pods[0].Containers[0]
				if len(c.VolumeMounts) != 2 {
					t.Fatalf("expected 2 volume mounts, got %d", len(c.VolumeMounts))
				}
				if c.VolumeMounts[0].Name != "data-vol" || c.VolumeMounts[0].MountPath != "/data" || !c.VolumeMounts[0].ReadOnly {
					t.Errorf("volumeMount[0] = %+v", c.VolumeMounts[0])
				}
				if c.VolumeMounts[1].Name != "scratch" || c.VolumeMounts[1].MountPath != "/tmp/scratch" {
					t.Errorf("volumeMount[1] = %+v", c.VolumeMounts[1])
				}
			},
		},
		{
			name: "labels from template",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.Labels == nil || pod.Labels["app"] != "trainer" {
					t.Errorf("labels = %v, want app=trainer", pod.Labels)
				}
			},
		},
		{
			name: "termination grace period",
			yaml: validJobYAML,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.TerminationGracePeriodSeconds != 30 {
					t.Errorf("TerminationGracePeriodSeconds = %d, want 30", pod.TerminationGracePeriodSeconds)
				}
			},
		},
		{
			name:    "missing spec.template",
			yaml:    "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: bad-job\nspec:\n  backoffLimit: 1\n",
			wantErr: true,
		},
		{
			name: "default backoff limit",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: simple-job
spec:
  template:
    spec:
      containers:
        - name: worker
          image: busybox
`,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.BackoffLimit != 2 {
					t.Errorf("BackoffLimit = %d, want 2 (default)", pod.BackoffLimit)
				}
			},
		},
		{
			name: "default restart policy for job",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: simple-job
spec:
  template:
    spec:
      containers:
        - name: worker
          image: busybox
`,
			check: func(t *testing.T, pods []PodSpec) {
				pod := pods[0]
				if pod.RestartPolicy != "Never" {
					t.Errorf("RestartPolicy = %q, want %q (default for jobs)", pod.RestartPolicy, "Never")
				}
			},
		},
		{
			name: "cpu millicore format",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: milli-job
spec:
  template:
    spec:
      containers:
        - name: worker
          image: busybox
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
`,
			check: func(t *testing.T, pods []PodSpec) {
				c := pods[0].Containers[0]
				if c.Resources.Requests.CPUMillis != 500 {
					t.Errorf("cpu = %d millis, want 500", c.Resources.Requests.CPUMillis)
				}
				if c.Resources.Requests.MemoryMB != 512 {
					t.Errorf("memory = %d MB, want 512", c.Resources.Requests.MemoryMB)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := ParseYAML([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("ParseYAML failed: %v", err)
			}

			pods, err := parseJob(root, classes)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, pods)
			}
		})
	}
}

func TestParseGPU(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"2", 2},
		{"1", 1},
		{"0", 0},
		{"", 0},
		{"4", 4},
		{"8", 8},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseGPU(tt.input)
			if got != tt.want {
				t.Errorf("parseGPU(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseGPUCount_ResourceList(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantGPUCount int
		wantGPUMemMB int
	}{
		{
			name: "nvidia.com/gpu: 2 produces GPUCount=2, GPUMemoryMB=0",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: gpu-job
spec:
  template:
    spec:
      containers:
        - name: worker
          image: nvidia/cuda
          resources:
            requests:
              nvidia.com/gpu: "2"
`,
			wantGPUCount: 2,
			wantGPUMemMB: 0,
		},
		{
			name: "no GPU specified",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: cpu-job
spec:
  template:
    spec:
      containers:
        - name: worker
          image: busybox
          resources:
            requests:
              cpu: "1"
              memory: 512Mi
`,
			wantGPUCount: 0,
			wantGPUMemMB: 0,
		},
		{
			name: "nvidia.com/gpu: 1",
			yaml: `apiVersion: batch/v1
kind: Job
metadata:
  name: single-gpu
spec:
  template:
    spec:
      containers:
        - name: worker
          image: nvidia/cuda
          resources:
            requests:
              nvidia.com/gpu: "1"
`,
			wantGPUCount: 1,
			wantGPUMemMB: 0,
		},
	}

	classes := DefaultPriorityClasses()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := ParseYAML([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("ParseYAML: %v", err)
			}
			pods, err := parseJob(root, classes)
			if err != nil {
				t.Fatalf("parseJob: %v", err)
			}
			c := pods[0].Containers[0]
			if c.Resources.Requests.GPUCount != tt.wantGPUCount {
				t.Errorf("GPUCount = %d, want %d", c.Resources.Requests.GPUCount, tt.wantGPUCount)
			}
			if c.Resources.Requests.GPUMemoryMB != tt.wantGPUMemMB {
				t.Errorf("GPUMemoryMB = %d, want %d", c.Resources.Requests.GPUMemoryMB, tt.wantGPUMemMB)
			}
		})
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"2", 2000},
		{"500m", 500},
		{"0.5", 500},
		{"1", 1000},
		{"", 0},
		{"100m", 100},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseCPU(tt.input)
			if got != tt.want {
				t.Errorf("parseCPU(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"8Gi", 8192},
		{"512Mi", 512},
		{"1Gi", 1024},
		{"", 0},
		{"16Gi", 16384},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseMemory(tt.input)
			if got != tt.want {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
