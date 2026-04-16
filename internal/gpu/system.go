package gpu

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// SystemInfo holds detected CPU and memory resources.
type SystemInfo struct {
	CPUMillis     int
	MemoryTotalMB int
	CoreIDs       []int
}

// DetectSystem detects the system's CPU and RAM resources.
func DetectSystem() (SystemInfo, error) {
	numCPU := runtime.NumCPU()
	cpuMillis := numCPU * 1000

	memMB, err := detectMemory()
	if err != nil {
		return SystemInfo{}, fmt.Errorf("detect memory: %w", err)
	}

	coreIDs := make([]int, numCPU)
	for i := 0; i < numCPU; i++ {
		coreIDs[i] = i
	}

	return SystemInfo{
		CPUMillis:     cpuMillis,
		MemoryTotalMB: memMB,
		CoreIDs:       coreIDs,
	}, nil
}

func detectMemory() (int, error) {
	switch runtime.GOOS {
	case "darwin":
		return detectMemoryDarwin()
	case "linux":
		return detectMemoryLinux()
	default:
		return 0, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func detectMemoryDarwin() (int, error) {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, fmt.Errorf("sysctl: %w", err)
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse sysctl output: %w", err)
	}
	return int(bytes / (1024 * 1024)), nil
}

func detectMemoryLinux() (int, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("unexpected MemTotal format: %s", line)
			}
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse MemTotal: %w", err)
			}
			return int(kb / 1024), nil
		}
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}
