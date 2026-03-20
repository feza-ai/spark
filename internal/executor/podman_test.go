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

func TestFormatDeviceIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []int
		want string
	}{
		{"single", []int{0}, "0"},
		{"multiple", []int{0, 1, 2}, "0,1,2"},
		{"non-contiguous", []int{1, 3}, "1,3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDeviceIDs(tt.ids)
			if got != tt.want {
				t.Errorf("formatDeviceIDs(%v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}

func TestInjectGPUDevices(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		Resources: manifest.ResourceRequirements{
			Limits: manifest.ResourceList{GPUMemoryMB: 4096},
		},
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")
	injected := injectGPUDevices(args, []int{0, 2})

	// Should have NVIDIA_VISIBLE_DEVICES env var
	envVal := "NVIDIA_VISIBLE_DEVICES=0,2"
	idx := slices.Index(injected, envVal)
	if idx < 1 || injected[idx-1] != "--env" {
		t.Errorf("expected --env %s in args, got %v", envVal, injected)
	}

	// Should still have --device nvidia.com/gpu=all
	if !slices.Contains(injected, "nvidia.com/gpu=all") {
		t.Errorf("expected --device nvidia.com/gpu=all in args, got %v", injected)
	}

	// Image should still be present
	if !slices.Contains(injected, "myimage:latest") {
		t.Errorf("expected image myimage:latest in args, got %v", injected)
	}
}

func TestInjectGPUDevices_NoGPUDevices(t *testing.T) {
	// When no GPUDevices are set but GPUMemoryMB > 0, buildRunArgs should
	// still add --device nvidia.com/gpu=all (fallback behavior).
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		Resources: manifest.ResourceRequirements{
			Limits: manifest.ResourceList{GPUMemoryMB: 4096},
		},
	}
	args := buildRunArgs("mypod", container, nil, "spark-net")

	// Should have --device nvidia.com/gpu=all
	if !slices.Contains(args, "nvidia.com/gpu=all") {
		t.Errorf("expected --device nvidia.com/gpu=all in args, got %v", args)
	}

	// Should NOT have NVIDIA_VISIBLE_DEVICES
	for _, a := range args {
		if a == "NVIDIA_VISIBLE_DEVICES" || (len(a) > 24 && a[:24] == "NVIDIA_VISIBLE_DEVICES=") {
			t.Errorf("unexpected NVIDIA_VISIBLE_DEVICES in args: %v", args)
		}
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

func TestPodStatsParseOutput(t *testing.T) {
	data := []byte(`[{"cpu_percent":"25.5%","mem_usage":"256.0MiB / 512.0MiB"},{"cpu_percent":"10.2%","mem_usage":"128.0MiB / 256.0MiB"}]`)
	stats, err := parsePodStats(data)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CPUPercent != 35.7 {
		t.Fatalf("expected CPU 35.7%%, got %.1f%%", stats.CPUPercent)
	}
	if stats.MemoryMB != 384 {
		t.Fatalf("expected 384MB, got %d", stats.MemoryMB)
	}
}

func TestPodStatsEmptyOutput(t *testing.T) {
	data := []byte(`[]`)
	stats, err := parsePodStats(data)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CPUPercent != 0 || stats.MemoryMB != 0 {
		t.Fatalf("expected zero usage, got cpu=%.1f mem=%d", stats.CPUPercent, stats.MemoryMB)
	}
}

func TestParseMemUsage(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"256.0MiB / 512.0MiB", 256},
		{"1.5GiB / 4.0GiB", 1536},
		{"512.0KiB / 1024.0KiB", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseMemUsage(tt.raw)
		if got != tt.want {
			t.Errorf("parseMemUsage(%q) = %d, want %d", tt.raw, got, tt.want)
		}
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

func TestBuildRunArgs_EmptyDirVolumes(t *testing.T) {
	tests := []struct {
		name     string
		mounts   []manifest.VolumeMount
		volumes  []manifest.VolumeSpec
		wantFlag string
		wantVal  string
	}{
		{
			name:     "emptyDir tmpfs mount",
			mounts:   []manifest.VolumeMount{{Name: "scratch", MountPath: "/tmp/scratch"}},
			volumes:  []manifest.VolumeSpec{{Name: "scratch", EmptyDir: true}},
			wantFlag: "--mount",
			wantVal:  "type=tmpfs,destination=/tmp/scratch",
		},
		{
			name:     "emptyDir readonly",
			mounts:   []manifest.VolumeMount{{Name: "cache", MountPath: "/cache", ReadOnly: true}},
			volumes:  []manifest.VolumeSpec{{Name: "cache", EmptyDir: true}},
			wantFlag: "--mount",
			wantVal:  "type=tmpfs,destination=/cache,ro",
		},
		{
			name:     "hostPath unchanged",
			mounts:   []manifest.VolumeMount{{Name: "data", MountPath: "/data"}},
			volumes:  []manifest.VolumeSpec{{Name: "data", HostPath: "/host/data"}},
			wantFlag: "--volume",
			wantVal:  "/host/data:/data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := manifest.ContainerSpec{
				Name:         "app",
				Image:        "myimage:latest",
				VolumeMounts: tt.mounts,
			}
			args := buildRunArgs("mypod", container, tt.volumes, "spark-net")
			idx := slices.Index(args, tt.wantVal)
			if idx < 1 || args[idx-1] != tt.wantFlag {
				t.Errorf("expected %s %s in args, got %v", tt.wantFlag, tt.wantVal, args)
			}
		})
	}
}

func TestBuildExecArgs(t *testing.T) {
	tests := []struct {
		name          string
		podName       string
		containerName string
		command       []string
		want          []string
	}{
		{
			name:          "with container name",
			podName:       "mypod",
			containerName: "worker",
			command:       []string{"ls", "-la"},
			want:          []string{"exec", "mypod-worker", "ls", "-la"},
		},
		{
			name:          "single command",
			podName:       "web",
			containerName: "app",
			command:       []string{"whoami"},
			want:          []string{"exec", "web-app", "whoami"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildExecArgs(tt.podName, tt.containerName, tt.command)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildExecArgs(%q, %q, %v) = %v, want %v", tt.podName, tt.containerName, tt.command, got, tt.want)
			}
		})
	}
}

func TestBuildExecArgs_NoContainer(t *testing.T) {
	tests := []struct {
		name    string
		podName string
		command []string
		want    []string
	}{
		{
			name:    "no container name",
			podName: "mypod",
			command: []string{"echo", "hello"},
			want:    []string{"exec", "mypod", "echo", "hello"},
		},
		{
			name:    "empty container name",
			podName: "worker-pod",
			command: []string{"/bin/sh", "-c", "date"},
			want:    []string{"exec", "worker-pod", "/bin/sh", "-c", "date"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildExecArgs(tt.podName, "", tt.command)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildExecArgs(%q, \"\", %v) = %v, want %v", tt.podName, tt.command, got, tt.want)
			}
		})
	}
}

func TestBuildRunArgs_MixedVolumes(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "app",
		Image: "myimage:latest",
		VolumeMounts: []manifest.VolumeMount{
			{Name: "data", MountPath: "/data"},
			{Name: "scratch", MountPath: "/tmp/scratch"},
			{Name: "config", MountPath: "/etc/config", ReadOnly: true},
		},
	}
	volumes := []manifest.VolumeSpec{
		{Name: "data", HostPath: "/host/data"},
		{Name: "scratch", EmptyDir: true},
		{Name: "config", HostPath: "/host/config"},
	}
	args := buildRunArgs("mypod", container, volumes, "spark-net")

	// hostPath volume for data
	idx := slices.Index(args, "/host/data:/data")
	if idx < 1 || args[idx-1] != "--volume" {
		t.Errorf("expected --volume /host/data:/data in args, got %v", args)
	}

	// emptyDir tmpfs for scratch
	idx = slices.Index(args, "type=tmpfs,destination=/tmp/scratch")
	if idx < 1 || args[idx-1] != "--mount" {
		t.Errorf("expected --mount type=tmpfs,destination=/tmp/scratch in args, got %v", args)
	}

	// hostPath ro volume for config
	idx = slices.Index(args, "/host/config:/etc/config:ro")
	if idx < 1 || args[idx-1] != "--volume" {
		t.Errorf("expected --volume /host/config:/etc/config:ro in args, got %v", args)
	}

	// Ensure no --volume flag was used for scratch
	for i, a := range args {
		if a == "--volume" && i+1 < len(args) && args[i+1] == ":/tmp/scratch" {
			t.Errorf("emptyDir should not use --volume, got %v", args)
		}
	}
}

func TestBuildPodLogsArgs_WithTail(t *testing.T) {
	got := buildPodLogsArgs("mypod", 100)
	want := []string{"pod", "logs", "--tail", "100", "mypod"}
	if !slices.Equal(got, want) {
		t.Errorf("buildPodLogsArgs(mypod, 100) = %v, want %v", got, want)
	}
}

func TestBuildPodLogsArgs_NoTail(t *testing.T) {
	got := buildPodLogsArgs("mypod", 0)
	want := []string{"pod", "logs", "mypod"}
	if !slices.Equal(got, want) {
		t.Errorf("buildPodLogsArgs(mypod, 0) = %v, want %v", got, want)
	}
	if slices.Contains(got, "--tail") {
		t.Errorf("expected no --tail flag when tail=0, got %v", got)
	}
}

func TestBuildStreamPodLogsArgs(t *testing.T) {
	got := buildStreamPodLogsArgs("mypod", 50)
	want := []string{"pod", "logs", "--follow", "--tail", "50", "mypod"}
	if !slices.Equal(got, want) {
		t.Errorf("buildStreamPodLogsArgs(mypod, 50) = %v, want %v", got, want)
	}
	if !slices.Contains(got, "--follow") {
		t.Errorf("expected --follow flag in args, got %v", got)
	}
}
