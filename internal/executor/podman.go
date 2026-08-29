package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

// Status represents a pod's runtime status from podman.
type Status struct {
	Running  bool
	ExitCode int
	// Containers holds per-container states when the pod-level state made
	// them worth fetching (Degraded/Exited/Stopped — i.e. at least one
	// container has exited). Empty for pods where every container is up,
	// so the reconciler pays the extra podman call only for degraded pods.
	Containers []ContainerStatus
}

// PodListEntry represents a pod discovered from podman.
type PodListEntry struct {
	Name    string
	Running bool
	// Status is the raw podman pod status string (e.g. "Running",
	// "Exited", "Stopped", "Created"). Empty when not reported.
	Status string
}

// IsTerminal reports whether the entry represents a podman pod in a
// terminal (non-running, non-creating) state — i.e. eligible for orphan
// reaping. Mirrors the podman pod state machine where "Exited", "Stopped",
// "Dead", and "Degraded" are not going to spontaneously start running.
func (e PodListEntry) IsTerminal() bool {
	if e.Running {
		return false
	}
	switch strings.ToLower(e.Status) {
	case "exited", "stopped", "dead", "degraded", "error":
		return true
	default:
		return false
	}
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
	StartContainer(ctx context.Context, containerName string) error
	RemovePod(ctx context.Context, name string) error
	ListPods(ctx context.Context) ([]PodListEntry, error)
	PodStats(ctx context.Context, name string) (PodResourceUsage, error)
	PodLogs(ctx context.Context, name string, tail int) ([]byte, error)
	StreamPodLogs(ctx context.Context, name string, tail int) (io.ReadCloser, error)
	ExecPod(ctx context.Context, podName string, containerName string, command []string) ([]byte, []byte, int, error)
	ListImages(ctx context.Context) ([]ImageInfo, error)
	PullImage(ctx context.Context, name string) error
	PruneImages(ctx context.Context) (int, error)
	ExecProbe(ctx context.Context, podName string, containerName string, command []string, timeout time.Duration) (int, error)
	HTTPProbe(ctx context.Context, port int, path string, timeout time.Duration) error
}

// PodmanExecutor implements Executor using podman CLI.
type PodmanExecutor struct {
	network string

	// cdiOnce ensures the NVIDIA CDI spec is generated at most once per
	// executor lifetime. Triggered on the first GPU pod creation so the
	// CUDA runtime libraries are bind-mounted into the container.
	cdiOnce sync.Once
	cdiErr  error
}

// NewPodmanExecutor creates a new executor with the given network name.
func NewPodmanExecutor(network string) *PodmanExecutor {
	return &PodmanExecutor{network: network}
}

// CreatePod creates a pod and starts all containers defined in the spec.
func (p *PodmanExecutor) CreatePod(ctx context.Context, spec manifest.PodSpec) error {
	// If any container requests a GPU, ensure the NVIDIA CDI spec exists.
	// Without it, `--device nvidia.com/gpu=all` injects only the device
	// node — not the CUDA runtime libraries — and workloads fall back to CPU.
	if podSpecRequestsGPU(spec) {
		p.cdiOnce.Do(func() { p.cdiErr = ensureNvidiaCDI(ctx) })
		if p.cdiErr != nil {
			slog.Warn("nvidia CDI generation failed; GPU workloads may fall back to CPU",
				"error", p.cdiErr)
		}
	}

	// Create the pod.
	args := []string{"pod", "create", "--name", spec.Name, "--network", p.network}

	// Collect port mappings from all containers — ports must be published at pod creation time.
	for _, c := range spec.Containers {
		for _, port := range c.Ports {
			args = append(args, "--publish", formatPublish(port))
		}
	}

	slog.Info("creating pod", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		// If a stale pod exists in podman state, remove it and retry.
		if strings.Contains(string(out), "already exists") || strings.Contains(string(out), "is in use") {
			slog.Warn("stale pod exists, removing before retry", "pod", spec.Name)
			_ = exec.CommandContext(ctx, "podman", "pod", "rm", "-f", spec.Name).Run()
			out, err = exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		}
		if err != nil {
			return fmt.Errorf("podman pod create: %w: %s", err, out)
		}
	}

	// Run init containers sequentially (blocking, not detached).
	for i, ic := range spec.InitContainers {
		icCopy := ic
		icCopy.Name = fmt.Sprintf("init-%d-%s", i, ic.Name)
		// Init containers never received NVIDIA_VISIBLE_DEVICES even when
		// the pod has assigned GPU devices (they run to completion before
		// the main containers start, and typically don't need the GPU) --
		// preserve that by passing no devices here.
		runArgs := buildRunArgs(spec.Name, icCopy, spec.Volumes, p.network, false, spec.CpusetCores, nil)
		slog.Info("running init container", "cmd", "podman", "args", runArgs)
		out, err := exec.CommandContext(ctx, "podman", runArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("init container %s failed: %w: %s", ic.Name, err, out)
		}
	}

	// Start each main container in the pod (detached).
	for _, c := range spec.Containers {
		runArgs := buildRunArgs(spec.Name, c, spec.Volumes, p.network, true, spec.CpusetCores, spec.GPUDevices)
		slog.Info("starting container", "cmd", "podman", "args", runArgs)
		out, err := exec.CommandContext(ctx, "podman", runArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("podman run %s: %w: %s", c.Name, err, out)
		}
	}
	return nil
}

// formatPublish formats a ContainerPort as a --publish value for podman pod create.
func formatPublish(p manifest.ContainerPort) string {
	proto := p.Protocol
	if proto == "" {
		proto = "tcp"
	}
	if p.HostPort == 0 {
		return strconv.Itoa(p.ContainerPort) + "/" + proto
	}
	return strconv.Itoa(p.HostPort) + ":" + strconv.Itoa(p.ContainerPort) + "/" + proto
}

// buildRunArgs constructs the arguments for a podman run command.
// If detach is true, the container runs in the background (-d flag).
// If cpusetCores is non-empty, emits --cpuset-cpus to pin the container
// to those host CPU cores (see ADR-012). If gpuDevices is non-empty, emits
// an NVIDIA_VISIBLE_DEVICES env var scoping the container to those specific
// device IDs.
func buildRunArgs(podName string, container manifest.ContainerSpec, volumes []manifest.VolumeSpec, network string, detach bool, cpusetCores []int, gpuDevices []int) []string {
	args := []string{"run"}
	if detach {
		args = append(args, "-d")
	}
	args = append(args, "--pod", podName, "--name", podName+"-"+container.Name)

	for _, e := range container.Env {
		args = append(args, "--env", e.Name+"="+e.Value)
	}
	// NVIDIA_VISIBLE_DEVICES belongs in the same family as the rest of
	// container.Env above -- emitted once, here, before any positional args
	// (image, entrypoint, command) exist for a later pass to get confused
	// with (issue #85: a previous post-processing pass that spliced this in
	// by scanning for "the position of the image" mis-scanned past
	// --entrypoint's own value token whenever container.Command was set,
	// corrupting the podman invocation).
	if len(gpuDevices) > 0 {
		args = append(args, "--env", "NVIDIA_VISIBLE_DEVICES="+formatDeviceIDs(gpuDevices))
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

	// Security context flags.
	if sc := container.SecurityContext; sc != nil {
		if sc.RunAsUser > 0 {
			args = append(args, "--user", strconv.Itoa(sc.RunAsUser))
		}
		if sc.Privileged {
			args = append(args, "--privileged")
		}
		for _, cap := range sc.AddCaps {
			args = append(args, "--cap-add", cap)
		}
		for _, cap := range sc.DropCaps {
			args = append(args, "--cap-drop", cap)
		}
	}

	limits := container.Resources.Limits
	if limits.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", limits.MemoryMB))
	}
	if limits.CPUMillis > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.1f", float64(limits.CPUMillis)/1000.0))
	}
	if len(cpusetCores) > 0 {
		args = append(args, "--cpuset-cpus", formatCPURange(cpusetCores))
	}
	// The scheduler admits and accounts GPU pods against Requests (see
	// manifest.PodSpec.TotalRequests), not Limits. Gating device attachment
	// on Limits alone let a manifest that sets only requests.nvidia.com/gpu
	// (a valid, common shape -- no limits block at all) hold a reserved
	// device slot while the container never actually received a device
	// (issue #81). Check both so a pod is only ever admitted when this
	// same condition will also attach a device, and vice versa.
	requests := container.Resources.Requests
	if requests.GPUMemoryMB > 0 || requests.GPUCount > 0 || limits.GPUMemoryMB > 0 || limits.GPUCount > 0 {
		args = append(args, "--device", "nvidia.com/gpu=all")
	}

	// K8s pod spec semantics: Container.Command overrides the image's
	// ENTRYPOINT, and Container.Args overrides CMD. Translate to podman:
	//   --entrypoint <cmd>        (single token)
	//   --entrypoint '["a","b"]'  (multi token, JSON array form -- only
	//                              when Args is also set, so Command[1:]
	//                              has somewhere else to go: podman has no
	//                              way to express a multi-token ENTRYPOINT
	//                              other than the JSON-array form)
	// When Args is empty, Command[1:] is appended after the image as
	// plain CMD-tail argv elements instead -- exactly like Args itself
	// always was -- rather than folded into the --entrypoint JSON blob
	// alongside Command[0]. Folding it in there mangled multi-token
	// commands whose trailing token itself contained nested double quotes
	// (issue #73): the quoting needed to survive as its own literal argv
	// element got re-escaped into a single JSON string instead.
	if len(container.Command) > 0 {
		switch {
		case len(container.Command) == 1:
			args = append(args, "--entrypoint", container.Command[0])
		case len(container.Args) > 0:
			encoded, err := json.Marshal(container.Command)
			if err == nil {
				args = append(args, "--entrypoint", string(encoded))
			} else {
				// Fallback: pass first token as entrypoint and the rest
				// as CMD-style args.
				args = append(args, "--entrypoint", container.Command[0])
			}
		default:
			args = append(args, "--entrypoint", container.Command[0])
		}
	}

	args = append(args, normalizeImage(container.Image))

	switch {
	case len(container.Args) > 0:
		args = append(args, container.Args...)
	case len(container.Command) > 1:
		args = append(args, container.Command[1:]...)
	}

	return args
}

// formatDeviceIDs joins GPU device IDs into a comma-separated string.
func formatDeviceIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

// formatCPURange formats a slice of CPU core IDs as a podman --cpuset-cpus
// argument. A contiguous ascending block is rendered as "lo-hi"; otherwise
// the IDs are joined with commas. Empty input returns "".
func formatCPURange(cores []int) string {
	if len(cores) == 0 {
		return ""
	}
	sorted := make([]int, len(cores))
	copy(sorted, cores)
	sort.Ints(sorted)
	contiguous := true
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return fmt.Sprintf("%d-%d", sorted[0], sorted[len(sorted)-1])
	}
	parts := make([]string, len(sorted))
	for i, c := range sorted {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ",")
}

// buildStopArgs constructs the arguments for a podman pod stop command.
func buildStopArgs(name string, gracePeriod int) []string {
	return []string{"pod", "stop", "--time", fmt.Sprintf("%d", gracePeriod), name}
}

// buildRemoveArgs constructs the arguments for a podman pod rm command.
func buildRemoveArgs(name string) []string {
	return []string{"pod", "rm", name}
}

// podmanStopTimeout bounds a single podman invocation issued from the
// stop/delete path, independent of the caller's own context. The DELETE
// HTTP handler passes r.Context() straight through, which carries no
// deadline of its own -- it only ends if the client disconnects. Without
// a bound here, a wedged podman invocation blocks the calling goroutine
// (and the HTTP request) for as long as podman stays wedged (issue #88:
// a real ~7-minute stall, self-resolved, not guaranteed to always be).
const podmanStopTimeout = 20 * time.Second

// podmanWaitDelay bounds how long Cmd.Wait may keep blocking after the
// podman process is known to have exited (or podmanStopTimeout above
// fires), waiting for its stdout/stderr pipes to see EOF. This is the
// other half of issue #88's gap: `exec.CommandContext`'s own cancellation
// only signals the *direct* child. If that child forked a subprocess
// (e.g. podman's own conmon/netavark helpers) that inherited the pipe and
// is itself stuck on a storage/CDI lock, the direct child can already be
// a reaped-pending zombie -- exactly what `ps` showed on the DGX,
// `[podman] <defunct>` parented by spark's own PID -- while
// CombinedOutput() still blocks forever reading for EOF that never comes.
// Cmd.WaitDelay (Go 1.20+) is the stdlib's documented fix for precisely
// this class of hang: it force-closes the pipes after the delay, so Wait
// returns even if a grandchild is still holding them open.
const podmanWaitDelay = 5 * time.Second

// runPodmanBounded runs podman with its own timeout and WaitDelay,
// layered on top of ctx rather than replacing it -- if ctx already carries
// an earlier deadline, that still wins. See podmanStopTimeout and
// podmanWaitDelay for why both are needed: a timeout alone only bounds
// how long Spark waits *before signaling* the direct child; WaitDelay is
// what guarantees Wait() itself returns afterward (issue #88).
func runPodmanBounded(ctx context.Context, timeout, waitDelay time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.WaitDelay = waitDelay
	return cmd.CombinedOutput()
}

// StopPod stops a pod with the given grace period in seconds and removes it.
// StartContainer starts an exited container in place (same config,
// same filesystem) via `podman start`. Used for per-container restarts:
// a crashed container comes back without touching its pod siblings.
func (p *PodmanExecutor) StartContainer(ctx context.Context, containerName string) error {
	args := []string{"start", containerName}
	slog.Info("starting container", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman start %s: %w: %s", containerName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (p *PodmanExecutor) StopPod(ctx context.Context, name string, gracePeriod int) error {
	args := buildStopArgs(name, gracePeriod)
	slog.Info("stopping pod", "cmd", "podman", "args", args)
	out, err := runPodmanBounded(ctx, podmanStopTimeout, podmanWaitDelay, args...)
	if err != nil {
		return fmt.Errorf("podman pod stop: %w: %s", err, out)
	}

	rmArgs := buildRemoveArgs(name)
	slog.Info("removing pod", "cmd", "podman", "args", rmArgs)
	out, err = runPodmanBounded(ctx, podmanStopTimeout, podmanWaitDelay, rmArgs...)
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
	case "Degraded", "Exited", "Stopped":
		// The pod-level state cannot distinguish success from failure, and
		// the always-running infra container means ANY workload container
		// failure leaves the pod "Degraded" rather than "Exited" (issue #52).
		// Derive the verdict from per-container states instead.
		containers, err := p.ContainerStatuses(ctx, name)
		if err != nil {
			return Status{}, fmt.Errorf("pod %s is %s: %w", name, state, err)
		}
		derived := derivePodStatus(containers)
		derived.Containers = containers
		return derived, nil
	case "Dead", "Error":
		return Status{Running: false, ExitCode: 1}, nil
	default:
		return Status{Running: false, ExitCode: 0}, nil
	}
}

// ContainerStatus represents one container's runtime state within a pod.
type ContainerStatus struct {
	Name     string
	Running  bool
	ExitCode int
	IsInfra  bool
}

// ContainerStatuses returns the per-container states of a pod, including
// exited containers.
func (p *PodmanExecutor) ContainerStatuses(ctx context.Context, podName string) ([]ContainerStatus, error) {
	args := []string{"ps", "-a", "--filter", "pod=" + podName, "--format", "json"}
	out, err := exec.CommandContext(ctx, "podman", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	return parseContainerPS(out)
}

// parseContainerPS parses `podman ps -a --format json` output.
func parseContainerPS(out []byte) ([]ContainerStatus, error) {
	var rows []struct {
		Names    []string `json:"Names"`
		State    string   `json:"State"`
		ExitCode int      `json:"ExitCode"`
		IsInfra  bool     `json:"IsInfra"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing podman ps output: %w", err)
	}
	statuses := make([]ContainerStatus, 0, len(rows))
	for _, r := range rows {
		name := ""
		if len(r.Names) > 0 {
			name = r.Names[0]
		}
		statuses = append(statuses, ContainerStatus{
			Name:     name,
			Running:  strings.EqualFold(r.State, "running"),
			ExitCode: r.ExitCode,
			// Belt and braces: older podman versions omit IsInfra from the
			// ps JSON, but the infra container name always ends in "-infra".
			IsInfra: r.IsInfra || strings.HasSuffix(name, "-infra"),
		})
	}
	return statuses, nil
}

// derivePodStatus reduces per-container states to a pod verdict. The infra
// container is ignored: it stays up as long as the pod exists and says
// nothing about the workload. The pod counts as running while any workload
// container runs; once all have exited, the first non-zero exit code wins.
func derivePodStatus(containers []ContainerStatus) Status {
	var sawWorkload bool
	exitCode := 0
	for _, c := range containers {
		if c.IsInfra {
			continue
		}
		sawWorkload = true
		if c.Running {
			return Status{Running: true, ExitCode: 0}
		}
		if exitCode == 0 && c.ExitCode != 0 {
			exitCode = c.ExitCode
		}
	}
	if !sawWorkload {
		// Only the infra container remains — nothing was ever started or
		// everything was removed. Treat as failed rather than succeeded.
		return Status{Running: false, ExitCode: 1}
	}
	return Status{Running: false, ExitCode: exitCode}
}

// RemovePod forcefully removes a pod.
func (p *PodmanExecutor) RemovePod(ctx context.Context, name string) error {
	args := []string{"pod", "rm", "-f", name}
	slog.Info("removing pod", "cmd", "podman", "args", args)
	out, err := runPodmanBounded(ctx, podmanStopTimeout, podmanWaitDelay, args...)
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

// cmdReadCloser wraps a command's stdout pipe so that closing it also
// calls cmd.Wait(), preventing zombie processes.
type cmdReadCloser struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdReadCloser) Close() error {
	readErr := c.ReadCloser.Close()
	waitErr := c.cmd.Wait()
	if readErr != nil {
		return readErr
	}
	return waitErr
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
	return &cmdReadCloser{ReadCloser: stdout, cmd: cmd}, nil
}

// buildExecArgs constructs the arguments for a podman exec command.
func buildExecArgs(podName string, containerName string, command []string) []string {
	target := podName
	if containerName != "" {
		target = podName + "-" + containerName
	}
	args := []string{"exec", target}
	args = append(args, command...)
	return args
}

// ExecPod executes a command in a container within a pod.
// Returns stdout, stderr, exit code, and any error.
func (p *PodmanExecutor) ExecPod(ctx context.Context, podName string, containerName string, command []string) ([]byte, []byte, int, error) {
	args := buildExecArgs(podName, containerName, command)
	slog.Info("exec in pod", "cmd", "podman", "args", args)
	cmd := exec.CommandContext(ctx, "podman", args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("podman exec stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("podman exec stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, 0, fmt.Errorf("podman exec start: %w", err)
	}
	stdout, _ := io.ReadAll(stdoutPipe)
	stderr, _ := io.ReadAll(stderrPipe)
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return stdout, stderr, exitCode, nil
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
			Status:  r.Status,
		}
	}
	return result, nil
}
