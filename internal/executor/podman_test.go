package executor

import (
	"slices"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

func TestBuildRunArgs_EnvMapping(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		Env: []manifest.EnvVar{
			{Name: "FOO", Value: "bar"},
			{Name: "BAZ", Value: "qux"},
		},
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")

	wantPairs := []string{"--env", "FOO=bar", "--env", "BAZ=qux"}
	for i := 0; i < len(wantPairs); i += 2 {
		idx := slices.Index(args, wantPairs[i+1])
		if idx < 1 || args[idx-1] != wantPairs[i] {
			t.Errorf("expected %s %s in args, got %v", wantPairs[i], wantPairs[i+1], args)
		}
	}
}

func TestBuildRunArgs_VolumeMapping(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		VolumeMounts: []manifest.VolumeMount{
			{Name: "data", MountPath: "/data", ReadOnly: false},
			{Name: "config", MountPath: "/etc/config", ReadOnly: true},
		},
	}
	volumes := []manifest.VolumeSpec{
		{Name: "data", HostPath: "/host/data"},
		{Name: "config", HostPath: "/host/config"},
	}
	args := buildRunArgs("mypod", container, volumes, "spark-net")

	tests := []struct {
		name string
		want string
	}{
		{"rw volume", "/host/data:/data"},
		{"ro volume", "/host/config:/etc/config:ro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := slices.Index(args, tt.want)
			if idx < 1 || args[idx-1] != "--volume" {
				t.Errorf("expected --volume %s in args, got %v", tt.want, args)
			}
		})
	}
}

func TestBuildRunArgs_GPUFlag(t *testing.T) {
	tests := []struct {
		name    string
		gpuMB   int
		wantGPU bool
	}{
		{"no GPU", 0, false},
		{"with GPU", 4096, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := manifest.ContainerSpec{
				Name:  "app",
				Image: "myimage:latest",
				Resources: manifest.ResourceRequirements{
					Limits: manifest.ResourceList{GPUMemoryMB: tt.gpuMB},
				},
			}
			args := buildRunArgs("mypod", container, nil, "spark-net")
			hasGPU := slices.Contains(args, "nvidia.com/gpu=all")
			if hasGPU != tt.wantGPU {
				t.Errorf("GPU flag present=%v, want %v; args=%v", hasGPU, tt.wantGPU, args)
			}
		})
	}
}

func TestBuildRunArgs_ResourceLimits(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		Resources: manifest.ResourceRequirements{
			Limits: manifest.ResourceList{CPUMillis: 2500, MemoryMB: 512},
		},
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")

	memIdx := slices.Index(args, "512m")
	if memIdx < 1 || args[memIdx-1] != "--memory" {
		t.Errorf("expected --memory 512m in args, got %v", args)
	}

	cpuIdx := slices.Index(args, "2.5")
	if cpuIdx < 1 || args[cpuIdx-1] != "--cpus" {
		t.Errorf("expected --cpus 2.5 in args, got %v", args)
	}
}

func TestBuildRunArgs_CommandAndArgs(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:    "app",
		Image:   "myimage:latest",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{"echo hello"},
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")

	// Image should appear, followed by command and args.
	imgIdx := slices.Index(args, "myimage:latest")
	if imgIdx < 0 {
		t.Fatalf("image not found in args: %v", args)
	}
	tail := args[imgIdx+1:]
	want := []string{"/bin/sh", "-c", "echo hello"}
	if !slices.Equal(tail, want) {
		t.Errorf("command+args = %v, want %v", tail, want)
	}
}

func TestBuildStopArgs(t *testing.T) {
	tests := []struct {
		name        string
		podName     string
		gracePeriod int
		want        []string
	}{
		{"default grace", "mypod", 10, []string{"pod", "stop", "--time", "10", "mypod"}},
		{"zero grace", "web", 0, []string{"pod", "stop", "--time", "0", "web"}},
		{"long grace", "worker-pod", 300, []string{"pod", "stop", "--time", "300", "worker-pod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStopArgs(tt.podName, tt.gracePeriod)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildStopArgs(%q, %d) = %v, want %v", tt.podName, tt.gracePeriod, got, tt.want)
			}
		})
	}
}

func TestBuildRemoveArgs(t *testing.T) {
	tests := []struct {
		name    string
		podName string
		want    []string
	}{
		{"simple name", "mypod", []string{"pod", "rm", "mypod"}},
		{"hyphenated name", "worker-pod", []string{"pod", "rm", "worker-pod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRemoveArgs(tt.podName)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildRemoveArgs(%q) = %v, want %v", tt.podName, got, tt.want)
			}
		})
	}
}

func TestListPodsParseOutput(t *testing.T) {
	data := []byte(`[{"Name":"web","Status":"Running"},{"Name":"batch","Status":"Exited"}]`)
	pods, err := parsePodsJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}
	if pods[0].Name != "web" || !pods[0].Running {
		t.Errorf("expected web running, got %+v", pods[0])
	}
	if pods[1].Name != "batch" || pods[1].Running {
		t.Errorf("expected batch not running, got %+v", pods[1])
	}
}

func TestListPodsEmptyOutput(t *testing.T) {
	data := []byte(`[]`)
	pods, err := parsePodsJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 0 {
		t.Fatalf("expected 0 pods, got %d", len(pods))
	}
}

func TestBuildRunArgs_PodAndContainerName(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "worker",
		Image: "myimage:latest",
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")

	podIdx := slices.Index(args, "--pod")
	if podIdx < 0 || args[podIdx+1] != "mypod" {
		t.Errorf("expected --pod mypod in args, got %v", args)
	}

	nameIdx := slices.Index(args, "--name")
	if nameIdx < 0 || args[nameIdx+1] != "mypod-worker" {
		t.Errorf("expected --name mypod-worker in args, got %v", args)
	}
}
