package metrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParseMemAvailable parses /proc/meminfo content and returns the kernel's
// MemAvailable figure in MB.
//
// MemAvailable, not MemFree: MemFree excludes reclaimable page cache and
// slab, so it reads catastrophically low on any host that has been up long
// enough to fill its cache. MemAvailable is the kernel's own estimate of
// what a new workload can obtain without pushing the system into swap or
// reclaim pressure, which is exactly the question admission asks.
//
// Returns an error when the field is absent: an older kernel without
// MemAvailable (pre-3.14) must fail loudly rather than be silently treated
// as a host with zero free memory, or with infinite free memory. Silent
// defaults in this path are the failure mode this repo keeps re-learning
// (issues #43, #66, #121).
func ParseMemAvailable(content string) (int, error) {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected MemAvailable format: %s", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", err)
		}
		if kb < 0 {
			return 0, fmt.Errorf("negative MemAvailable: %s", line)
		}
		return int(kb / 1024), nil
	}
	return 0, fmt.Errorf("MemAvailable not found in meminfo")
}

// ReadMemAvailableMB reads /proc/meminfo and returns MemAvailable in MB.
// Returns a benign error on platforms where /proc/meminfo is absent, which
// callers treat as "no reading" rather than as an exhausted host.
func ReadMemAvailableMB() (int, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("meminfo not available: %w", err)
	}
	return ParseMemAvailable(string(data))
}
