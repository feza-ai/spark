package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

// cdiSpecPath is the standard location for the NVIDIA CDI spec. podman reads
// CDI specs from /etc/cdi/ (system) and $XDG_CONFIG_HOME/cdi/ (user).
// Generating nvidia.yaml here makes `--device nvidia.com/gpu=all` bind-mount
// the CUDA driver libraries (libcuda.so, libcudart.so, etc.) from the host
// into the container — required for any workload that calls into CUDA.
const cdiSpecPath = "/etc/cdi/nvidia.yaml"

// ensureNvidiaCDI regenerates the NVIDIA CDI spec if it is missing. Without
// it, `podman run --device nvidia.com/gpu=all` injects the device node but
// not the CUDA runtime libraries, so GPU workloads fall back to CPU.
//
// Idempotent: if /etc/cdi/nvidia.yaml already exists, this is a no-op.
// Requires `nvidia-ctk` on PATH (part of nvidia-container-toolkit). If the
// tool is missing, logs a warning and returns nil so non-GPU work is not
// blocked.
func ensureNvidiaCDI(ctx context.Context) error {
	if fi, err := os.Stat(cdiSpecPath); err == nil && fi.Size() > 0 {
		return nil
	}

	if _, err := exec.LookPath("nvidia-ctk"); err != nil {
		slog.Warn("nvidia-ctk not found on PATH; GPU workloads will lack CUDA runtime libs",
			"hint", "install nvidia-container-toolkit on the host")
		return nil
	}

	if err := os.MkdirAll("/etc/cdi", 0o755); err != nil {
		return fmt.Errorf("nvidia CDI: create /etc/cdi: %w", err)
	}

	args := []string{"cdi", "generate", "--output=" + cdiSpecPath}
	slog.Info("generating nvidia CDI spec", "cmd", "nvidia-ctk", "args", args)

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cmdCtx, "nvidia-ctk", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nvidia-ctk cdi generate: %w: %s", err, out)
	}
	slog.Info("nvidia CDI spec generated", "path", cdiSpecPath)
	return nil
}

// podSpecRequestsGPU reports whether any container in the spec requests a GPU.
func podSpecRequestsGPU(spec manifest.PodSpec) bool {
	for _, c := range spec.Containers {
		l := c.Resources.Limits
		if l.GPUMemoryMB > 0 || l.GPUCount > 0 {
			return true
		}
	}
	for _, c := range spec.InitContainers {
		l := c.Resources.Limits
		if l.GPUMemoryMB > 0 || l.GPUCount > 0 {
			return true
		}
	}
	return false
}
