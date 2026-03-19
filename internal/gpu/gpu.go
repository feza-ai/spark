package gpu

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoGPU is returned when nvidia-smi is not found or no GPU is detected.
var ErrNoGPU = errors.New("gpu: nvidia-smi not found or no GPU detected")

// GPUInfo holds aggregated GPU information from nvidia-smi.
type GPUInfo struct {
	Model              string
	MemoryTotalMB      int
	MemoryUsedMB       int
	UtilizationPercent int
	GPUCount           int
}

// Detect runs nvidia-smi and returns aggregated GPU information.
func Detect() (GPUInfo, error) {
	return DetectWithFallback(nil)
}

// DetectWithFallback runs nvidia-smi and returns aggregated GPU information.
// When memory values are unavailable (unified memory GPUs like NVIDIA GB10),
// it calls fallbackMemoryMB to obtain total system memory. Pass nil to skip
// the fallback.
func DetectWithFallback(fallbackMemoryMB func() (int, error)) (GPUInfo, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return GPUInfo{}, ErrNoGPU
	}

	cmd := exec.Command(path,
		"--query-gpu=name,memory.total,memory.used,utilization.gpu",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return GPUInfo{}, ErrNoGPU
	}

	info, err := parseCSV(string(out))
	if err != nil {
		return GPUInfo{}, err
	}

	// For unified-memory GPUs (e.g. NVIDIA GB10), memory.total reports [N/A].
	// Fall back to system memory when GPU was detected but memory is unknown.
	if info.GPUCount > 0 && info.MemoryTotalMB == 0 && fallbackMemoryMB != nil {
		sysMB, fbErr := fallbackMemoryMB()
		if fbErr != nil {
			slog.Warn("gpu: fallback memory detection failed", "error", fbErr)
		} else {
			info.MemoryTotalMB = sysMB
			slog.Info("gpu: using system memory for unified-memory GPU",
				"memoryTotalMB", sysMB, "model", info.Model)
		}
	}

	return info, nil
}

// isNA returns true when a nvidia-smi field value indicates the metric is
// not available. Unified-memory GPUs such as the NVIDIA GB10 report "[N/A]"
// for dedicated memory fields.
func isNA(s string) bool {
	v := strings.TrimSpace(s)
	return v == "[N/A]" || v == "N/A" || strings.EqualFold(v, "not available")
}

// parseIntOrNA parses s as an integer. If the value is a recognized N/A
// sentinel, it returns 0 and a nil error.
func parseIntOrNA(s string) (int, error) {
	v := strings.TrimSpace(s)
	if isNA(v) {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", v, err)
	}
	return n, nil
}

// parseCSV parses nvidia-smi CSV output into GPUInfo.
func parseCSV(output string) (GPUInfo, error) {
	var info GPUInfo

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return GPUInfo{}, ErrNoGPU
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Split(line, ", ")
		if len(fields) != 4 {
			slog.Warn("gpu: unexpected nvidia-smi output", "line", line)
			continue
		}

		name := strings.TrimSpace(fields[0])
		memTotal, err := parseIntOrNA(fields[1])
		if err != nil {
			slog.Warn("gpu: failed to parse memory total", "value", fields[1], "error", err)
			continue
		}
		memUsed, err := parseIntOrNA(fields[2])
		if err != nil {
			slog.Warn("gpu: failed to parse memory used", "value", fields[2], "error", err)
			continue
		}
		util, err := parseIntOrNA(fields[3])
		if err != nil {
			slog.Warn("gpu: failed to parse utilization", "value", fields[3], "error", err)
			continue
		}

		if info.GPUCount == 0 {
			info.Model = name
		}
		info.MemoryTotalMB += memTotal
		info.MemoryUsedMB += memUsed
		info.UtilizationPercent += util
		info.GPUCount++
	}

	if info.GPUCount == 0 {
		return GPUInfo{}, ErrNoGPU
	}

	// Average utilization across GPUs.
	info.UtilizationPercent = info.UtilizationPercent / info.GPUCount

	return info, nil
}
