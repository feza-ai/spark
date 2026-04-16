package metrics

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PodThrottleSample is one pod's cumulative CPU-throttled time in seconds.
// Cgroup-path discovery (mapping pod name -> cgroup path) is wired in a
// follow-up; this file provides the parser, reader, and renderer in
// isolation so unit tests can validate the metric format without a live
// cgroupfs.
type PodThrottleSample struct {
	Pod     string
	Seconds float64
}

// ParseCpuStatThrottled parses cgroup v2 cpu.stat content and returns the
// throttled_usec value converted to seconds. Returns an error if the field
// is missing or unparseable.
func ParseCpuStatThrottled(content string) (float64, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "throttled_usec" {
			continue
		}
		usec, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse throttled_usec %q: %w", fields[1], err)
		}
		return float64(usec) / 1_000_000, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("throttled_usec not found in cpu.stat")
}

// ReadPodThrottledSeconds reads cpu.stat from a cgroup path and returns the
// throttled_usec value converted to seconds.
func ReadPodThrottledSeconds(cgroupPath string) (float64, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "cpu.stat"))
	if err != nil {
		return 0, err
	}
	return ParseCpuStatThrottled(string(data))
}

// SetPodThrottleSamples records the latest per-pod throttling values for
// inclusion in the next Collect() call.
func (c *Collector) SetPodThrottleSamples(samples []PodThrottleSample) {
	c.podThrottle = samples
}

func renderPodThrottled(samples []PodThrottleSample) MetricFamily {
	out := MetricFamily{
		Name: "spark_pod_cpu_throttled_seconds",
		Help: "Total CPU time the pod was throttled by the cgroup CPU quota.",
		Type: "counter",
	}
	for _, s := range samples {
		out.Metrics = append(out.Metrics, Metric{
			Labels: map[string]string{"pod": s.Pod},
			Value:  s.Seconds,
		})
	}
	return out
}
