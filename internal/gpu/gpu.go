package gpu

import (
	"errors"
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

	return parseCSV(string(out))
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
		memTotal, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			slog.Warn("gpu: failed to parse memory total", "value", fields[1], "error", err)
			continue
		}
		memUsed, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			slog.Warn("gpu: failed to parse memory used", "value", fields[2], "error", err)
			continue
		}
		util, err := strconv.Atoi(strings.TrimSpace(fields[3]))
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
