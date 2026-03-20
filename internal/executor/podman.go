package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/feza-ai/spark/internal/manifest"
)

// Status represents a pod's runtime status from podman.
type Status struct {
	Running  bool
	ExitCode int
}

// PodListEntry represents a pod discovered from podman.
type PodListEntry struct {
	Name    string
	Running bool
}

// PodResourceUsage represents actual resource usage of a running pod.
type PodResourceUsage struct {
	CPUPercent float64
	MemoryMB   int
}

// ImageInfo represents a container image stored locally.
type ImageInfo struct {
	ID      string
	Names   []string
	Size    string
	Created string
}

// Executor defines the interface for pod lifecycle management.
type Executor interface {
	CreatePod(ctx context.Context, spec manifest.PodSpec) error
	StopPod(ctx context.Context, name string, gracePeriod int) error
	PodStatus(ctx context.Context, name string) (Status, error)
	RemovePod(ctx context.Context, name string) error
	ListPods(ctx context.Context) ([]PodListEntry, error)
	PodStats(ctx context.Context, name string) (PodResourceUsage, error)
	PodLogs(ctx context.Context, name string, tail int) ([]byte, error)
	StreamPodLogs(ctx context.Context, name string, tail int) (io.ReadCloser, error)
	ListImages(ctx context.Context) ([]ImageInfo, error)
	PullImage(ctx context.Context, name string) error
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
		if vol.EmptyDir {
			mount := "type=tmpfs,destination=" + m.MountPath
			if m.ReadOnly {
				mount += ",ro"
			}
			args = append(args, "--mount", mount)
		} else {
			mount := vol.HostPath + ":" + m.MountPath
			if m.ReadOnly {
				mount += ":ro"
			}
			args = append(args, "--volume", mount)
		}
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

// buildStopArgs constructs the arguments for a podman pod stop command.
func buildStopArgs(name string, gracePeriod int) []string {
	return []string{"pod", "stop", "--time", fmt.Sprintf("%d", gracePeriod), name}
}

// buildRemoveArgs constructs the arguments for a podman pod rm command.
func buildRemoveArgs(name string) []string {
	return []string{"pod", "rm", name}
}

// StopPod stops a pod with the given grace period in seconds and removes it.
func (p *PodmanExecutor) StopPod(ctx context.Context, name string, gracePeriod int) error {
	args := buildStopArgs(name, gracePeriod)
	slog.Info("stopping pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pod stop: %w: %s", err, out)
	}

	rmArgs := buildRemoveArgs(name)
	slog.Info("removing pod", "cmd", "podman", "args", rmArgs)
	out, err = exec.CommandContext(ctx, "podman", rmArgs...).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "no such pod") {
			return nil
		}
		return fmt.Errorf("podman pod rm: %w: %s", err, out)
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

// ListPods returns all pods known to podman.
func (p *PodmanExecutor) ListPods(ctx context.Context) ([]PodListEntry, error) {
	args := []string{"pod", "ls", "--format", "json"}
	slog.Info("listing pods", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman pod ls: %w: %s", err, out)
	}
	return parsePodsJSON(out)
}

// PodStats queries resource usage for a running pod.
func (p *PodmanExecutor) PodStats(ctx context.Context, name string) (PodResourceUsage, error) {
	args := []string{"pod", "stats", "--no-stream", "--format", "json", name}
	slog.Info("querying pod stats", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return PodResourceUsage{}, fmt.Errorf("podman pod stats: %w: %s", err, out)
	}
	return parsePodStats(out)
}

// parsePodStats parses the JSON output of podman pod stats.
func parsePodStats(data []byte) (PodResourceUsage, error) {
	var containers []struct {
		CPU    string `json:"cpu_percent"`
		MemRaw string `json:"mem_usage"`
	}
	if err := json.Unmarshal(data, &containers); err != nil {
		return PodResourceUsage{}, fmt.Errorf("parse pod stats: %w", err)
	}
	var totalCPU float64
	var totalMemMB int
	for _, c := range containers {
		cpuStr := strings.TrimSuffix(c.CPU, "%")
		cpu, _ := strconv.ParseFloat(cpuStr, 64)
		totalCPU += cpu
		totalMemMB += parseMemUsage(c.MemRaw)
	}
	return PodResourceUsage{CPUPercent: totalCPU, MemoryMB: totalMemMB}, nil
}

// parseMemUsage extracts memory in MB from a "used / limit" string.
func parseMemUsage(raw string) int {
	parts := strings.Split(raw, "/")
	if len(parts) == 0 {
		return 0
	}
	used := strings.TrimSpace(parts[0])
	used = strings.ToLower(used)
	if strings.HasSuffix(used, "gib") {
		val, _ := strconv.ParseFloat(strings.TrimSuffix(used, "gib"), 64)
		return int(val * 1024)
	}
	if strings.HasSuffix(used, "mib") {
		val, _ := strconv.ParseFloat(strings.TrimSuffix(used, "mib"), 64)
		return int(val)
	}
	if strings.HasSuffix(used, "kib") {
		val, _ := strconv.ParseFloat(strings.TrimSuffix(used, "kib"), 64)
		return int(val / 1024)
	}
	return 0
}

// buildPodLogsArgs constructs the arguments for a podman pod logs command.
func buildPodLogsArgs(name string, tail int) []string {
	args := []string{"pod", "logs"}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, name)
	return args
}

// buildStreamPodLogsArgs constructs the arguments for a streaming podman pod logs command.
func buildStreamPodLogsArgs(name string, tail int) []string {
	args := []string{"pod", "logs", "--follow"}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, name)
	return args
}

// PodLogs returns the combined log output for a pod.
func (p *PodmanExecutor) PodLogs(ctx context.Context, name string, tail int) ([]byte, error) {
	args := buildPodLogsArgs(name, tail)
	slog.Info("fetching pod logs", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman pod logs: %w: %s", err, out)
	}
	return out, nil
}

// StreamPodLogs returns a streaming reader for pod logs.
// The caller must close the returned reader; cancelling the context stops the process.
func (p *PodmanExecutor) StreamPodLogs(ctx context.Context, name string, tail int) (io.ReadCloser, error) {
	args := buildStreamPodLogsArgs(name, tail)
	slog.Info("streaming pod logs", "cmd", "podman", "args", args)
	cmd := exec.CommandContext(ctx, "podman", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("podman pod logs pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("podman pod logs start: %w", err)
	}
	return stdout, nil
}

// parsePodsJSON parses the JSON output of podman pod ls.
func parsePodsJSON(data []byte) ([]PodListEntry, error) {
	var raw []struct {
		Name   string `json:"Name"`
		Status string `json:"Status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse pod list: %w", err)
	}
	result := make([]PodListEntry, len(raw))
	for i, r := range raw {
		result[i] = PodListEntry{
			Name:    r.Name,
			Running: r.Status == "Running",
		}
	}
	return result, nil
}
