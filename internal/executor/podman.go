package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/feza-ai/spark/internal/manifest"
)

// Status represents a pod's runtime status from podman.
type Status struct {
	Running  bool
	ExitCode int
}

// Executor defines the interface for pod lifecycle management.
type Executor interface {
	CreatePod(ctx context.Context, spec manifest.PodSpec) error
	StopPod(ctx context.Context, name string, gracePeriod int) error
	PodStatus(ctx context.Context, name string) (Status, error)
	RemovePod(ctx context.Context, name string) error
}

// PodmanExecutor implements Executor using podman CLI.
type PodmanExecutor struct {
	network string
}

// NewPodmanExecutor creates a new executor with the given network name.
func NewPodmanExecutor(network string) *PodmanExecutor {
	return &PodmanExecutor{network: network}
}

// CreatePod creates a pod and starts all containers defined in the spec.
func (p *PodmanExecutor) CreatePod(ctx context.Context, spec manifest.PodSpec) error {
	// Create the pod.
	args := []string{"pod", "create", "--name", spec.Name, "--network", p.network}
	slog.Info("creating pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pod create: %w: %s", err, out)
	}

	// Start each container in the pod.
	for _, c := range spec.Containers {
		runArgs := buildRunArgs(spec.Name, c, spec.Volumes, p.network)
		slog.Info("starting container", "cmd", "podman", "args", runArgs)
		out, err := exec.CommandContext(ctx, "podman", runArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("podman run %s: %w: %s", c.Name, err, out)
		}
	}
	return nil
}

// buildRunArgs constructs the arguments for a podman run command.
func buildRunArgs(podName string, container manifest.ContainerSpec, volumes []manifest.VolumeSpec, network string) []string {
	args := []string{"run", "-d", "--pod", podName, "--name", podName + "-" + container.Name}

	for _, e := range container.Env {
		args = append(args, "--env", e.Name+"="+e.Value)
	}

	// Build a lookup from volume name to VolumeSpec.
	volMap := make(map[string]manifest.VolumeSpec, len(volumes))
	for _, v := range volumes {
		volMap[v.Name] = v
	}

	for _, m := range container.VolumeMounts {
		vol, ok := volMap[m.Name]
		if !ok {
			continue
		}
		mount := vol.HostPath + ":" + m.MountPath
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "--volume", mount)
	}

	limits := container.Resources.Limits
	if limits.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", limits.MemoryMB))
	}
	if limits.CPUMillis > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.1f", float64(limits.CPUMillis)/1000.0))
	}
	if limits.GPUMemoryMB > 0 {
		args = append(args, "--device", "nvidia.com/gpu=all")
	}

	args = append(args, container.Image)

	if len(container.Command) > 0 {
		args = append(args, container.Command...)
	}
	if len(container.Args) > 0 {
		args = append(args, container.Args...)
	}

	return args
}

// StopPod stops a pod with the given grace period in seconds.
func (p *PodmanExecutor) StopPod(ctx context.Context, name string, gracePeriod int) error {
	args := []string{"pod", "stop", "--time", fmt.Sprintf("%d", gracePeriod), name}
	slog.Info("stopping pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pod stop: %w: %s", err, out)
	}
	return nil
}

// PodStatus inspects a pod and returns its status.
func (p *PodmanExecutor) PodStatus(ctx context.Context, name string) (Status, error) {
	args := []string{"pod", "inspect", name, "--format", "{{.State}}"}
	slog.Info("inspecting pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return Status{}, fmt.Errorf("podman pod inspect: %w: %s", err, out)
	}

	state := strings.TrimSpace(string(out))
	switch state {
	case "Running":
		return Status{Running: true, ExitCode: 0}, nil
	case "Exited":
		return Status{Running: false, ExitCode: 0}, nil
	case "Dead", "Error":
		return Status{Running: false, ExitCode: 1}, nil
	default:
		return Status{Running: false, ExitCode: 0}, nil
	}
}

// RemovePod forcefully removes a pod.
func (p *PodmanExecutor) RemovePod(ctx context.Context, name string) error {
	args := []string{"pod", "rm", "-f", name}
	slog.Info("removing pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pod rm: %w: %s", err, out)
	}
	return nil
}
