package executor

import (
	"context"
	"io"
	"os/exec"
	"slices"
	"testing"
	"time"

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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)

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
	args := buildRunArgs("mypod", container, volumes, "spark-net", true)

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
			args := buildRunArgs("mypod", container, nil, "spark-net", true)
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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)
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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)

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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)

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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)

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
	args := buildRunArgs("mypod", container, nil, "spark-net", true)

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
			args := buildRunArgs("mypod", container, tt.volumes, "spark-net", true)
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
	args := buildRunArgs("mypod", container, volumes, "spark-net", true)

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

func TestCmdReadCloser_WaitsOnProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "hello")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	rc := &cmdReadCloser{ReadCloser: stdout, cmd: cmd}

	buf, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := string(buf); got != "hello\n" {
		t.Errorf("got %q, want %q", got, "hello\n")
	}

	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if cmd.ProcessState == nil {
		t.Error("cmd.ProcessState is nil after Close; process was not waited on")
	}
}

func TestCmdReadCloser_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, "sleep", "60")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	rc := &cmdReadCloser{ReadCloser: stdout, cmd: cmd}

	cancel()

	done := make(chan error, 1)
	go func() { done <- rc.Close() }()

	select {
	case <-done:
		// Error from killed process is expected.
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s after context cancel")
	}

	if cmd.ProcessState == nil {
		t.Error("cmd.ProcessState is nil after Close; zombie process not reaped")
	}
}

func TestFormatPublish(t *testing.T) {
	tests := []struct {
		name string
		port manifest.ContainerPort
		want string
	}{
		{
			name: "explicit host port and protocol",
			port: manifest.ContainerPort{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
			want: "8080:80/tcp",
		},
		{
			name: "udp protocol",
			port: manifest.ContainerPort{ContainerPort: 53, HostPort: 5353, Protocol: "udp"},
			want: "5353:53/udp",
		},
		{
			name: "random host port",
			port: manifest.ContainerPort{ContainerPort: 3000, HostPort: 0, Protocol: "tcp"},
			want: "3000/tcp",
		},
		{
			name: "default protocol",
			port: manifest.ContainerPort{ContainerPort: 443, HostPort: 8443},
			want: "8443:443/tcp",
		},
		{
			name: "random host port default protocol",
			port: manifest.ContainerPort{ContainerPort: 9090},
			want: "9090/tcp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPublish(tt.port)
			if got != tt.want {
				t.Errorf("formatPublish(%+v) = %q, want %q", tt.port, got, tt.want)
			}
		})
	}
}

func TestCreatePod_WithPorts(t *testing.T) {
	spec := manifest.PodSpec{
		Name: "web",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "nginx",
				Image: "nginx:latest",
				Ports: []manifest.ContainerPort{
					{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
					{ContainerPort: 443, HostPort: 8443, Protocol: "tcp"},
				},
			},
			{
				Name:  "sidecar",
				Image: "sidecar:latest",
				Ports: []manifest.ContainerPort{
					{ContainerPort: 9090, HostPort: 9090, Protocol: "tcp"},
				},
			},
		},
	}

	// Build the pod create args the same way CreatePod does.
	args := []string{"pod", "create", "--name", spec.Name, "--network", "spark-net"}
	for _, c := range spec.Containers {
		for _, port := range c.Ports {
			args = append(args, "--publish", formatPublish(port))
		}
	}

	// Verify all three --publish flags are present with correct values.
	wantPublish := []string{"8080:80/tcp", "8443:443/tcp", "9090:9090/tcp"}
	for _, want := range wantPublish {
		idx := slices.Index(args, want)
		if idx < 1 || args[idx-1] != "--publish" {
			t.Errorf("expected --publish %s in args, got %v", want, args)
		}
	}
}

func TestCreatePod_PortRandomHost(t *testing.T) {
	spec := manifest.PodSpec{
		Name: "app",
		Containers: []manifest.ContainerSpec{
			{
				Name:  "api",
				Image: "api:latest",
				Ports: []manifest.ContainerPort{
					{ContainerPort: 3000, HostPort: 0},
					{ContainerPort: 8080, HostPort: 0, Protocol: "udp"},
				},
			},
		},
	}

	args := []string{"pod", "create", "--name", spec.Name, "--network", "spark-net"}
	for _, c := range spec.Containers {
		for _, port := range c.Ports {
			args = append(args, "--publish", formatPublish(port))
		}
	}

	// HostPort=0 should omit the host port portion.
	wantPublish := []string{"3000/tcp", "8080/udp"}
	for _, want := range wantPublish {
		idx := slices.Index(args, want)
		if idx < 1 || args[idx-1] != "--publish" {
			t.Errorf("expected --publish %s in args, got %v", want, args)
		}
	}

	// Ensure no colon-prefixed format (like ":3000/tcp") leaked in.
	for _, a := range args {
		if len(a) > 0 && a[0] == ':' {
			t.Errorf("unexpected colon-prefixed publish value: %s", a)
		}
	}
}

func TestBuildRunArgs_NoDetach(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "setup",
		Image: "busybox:latest",
	}
	args := buildRunArgs("mypod", container, nil, "spark-net", false)

	if slices.Contains(args, "-d") {
		t.Errorf("expected no -d flag when detach=false, got %v", args)
	}
	if args[0] != "run" {
		t.Errorf("expected first arg to be 'run', got %q", args[0])
	}
	// --pod should still be present
	podIdx := slices.Index(args, "--pod")
	if podIdx < 0 || args[podIdx+1] != "mypod" {
		t.Errorf("expected --pod mypod in args, got %v", args)
	}
}

func TestBuildRunArgs_InitContainerNaming(t *testing.T) {
	container := manifest.ContainerSpec{
		Name:  "init-0-setup",
		Image: "busybox:latest",
	}
	args := buildRunArgs("mypod", container, nil, "spark-net", false)

	nameIdx := slices.Index(args, "--name")
	if nameIdx < 0 || args[nameIdx+1] != "mypod-init-0-setup" {
		t.Errorf("expected --name mypod-init-0-setup in args, got %v", args)
	}
}

func TestBuildRunArgs_SecurityContext(t *testing.T) {
	tests := []struct {
		name     string
		sc       *manifest.SecurityContext
		wantArgs []string // flag-value pairs or standalone flags expected in args
		notArgs  []string // flags that should NOT appear
	}{
		{
			name:    "nil security context",
			sc:      nil,
			notArgs: []string{"--user", "--privileged", "--cap-add", "--cap-drop"},
		},
		{
			name: "runAsUser only",
			sc:   &manifest.SecurityContext{RunAsUser: 1000},
			wantArgs: []string{"--user", "1000"},
			notArgs:  []string{"--privileged", "--cap-add", "--cap-drop"},
		},
		{
			name: "privileged only",
			sc:   &manifest.SecurityContext{Privileged: true},
			wantArgs: []string{"--privileged"},
			notArgs:  []string{"--user", "--cap-add", "--cap-drop"},
		},
		{
			name: "add capabilities",
			sc:   &manifest.SecurityContext{AddCaps: []string{"NET_ADMIN", "SYS_PTRACE"}},
			wantArgs: []string{"--cap-add", "NET_ADMIN", "--cap-add", "SYS_PTRACE"},
			notArgs:  []string{"--user", "--privileged", "--cap-drop"},
		},
		{
			name: "drop capabilities",
			sc:   &manifest.SecurityContext{DropCaps: []string{"ALL"}},
			wantArgs: []string{"--cap-drop", "ALL"},
			notArgs:  []string{"--user", "--privileged", "--cap-add"},
		},
		{
			name: "all fields combined",
			sc: &manifest.SecurityContext{
				RunAsUser:  65534,
				Privileged: true,
				AddCaps:    []string{"SYS_ADMIN"},
				DropCaps:   []string{"NET_RAW"},
			},
			wantArgs: []string{"--user", "65534", "--privileged", "--cap-add", "SYS_ADMIN", "--cap-drop", "NET_RAW"},
		},
		{
			name:    "zero runAsUser not added",
			sc:      &manifest.SecurityContext{RunAsUser: 0},
			notArgs: []string{"--user", "--privileged", "--cap-add", "--cap-drop"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := manifest.ContainerSpec{
				Name:            "app",
				Image:           "myimage:latest",
				SecurityContext: tt.sc,
			}
			args := buildRunArgs("mypod", container, nil, "spark-net", true)

			// Check expected flag-value pairs.
			for i := 0; i < len(tt.wantArgs); i++ {
				flag := tt.wantArgs[i]
				if flag == "--privileged" {
					if !slices.Contains(args, "--privileged") {
						t.Errorf("expected --privileged in args, got %v", args)
					}
					continue
				}
				// It's a flag with a value: --flag value
				if i+1 < len(tt.wantArgs) && !isFlag(tt.wantArgs[i+1]) {
					value := tt.wantArgs[i+1]
					idx := -1
					for j, a := range args {
						if a == flag && j+1 < len(args) && args[j+1] == value {
							idx = j
							break
						}
					}
					if idx < 0 {
						t.Errorf("expected %s %s in args, got %v", flag, value, args)
					}
					i++ // skip value
				}
			}

			// Check flags that should NOT appear.
			for _, flag := range tt.notArgs {
				if slices.Contains(args, flag) {
					t.Errorf("unexpected %s in args, got %v", flag, args)
				}
			}
		})
	}
}

func isFlag(s string) bool {
	return len(s) > 1 && s[0] == '-'
}
