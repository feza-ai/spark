package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// userHZ is the Linux scheduler tick used to convert /proc/stat counts to
// seconds. 100 is the standard CLK_TCK value on Linux x86_64 and arm64
// (including the DGX Grace-Hopper).
const userHZ = 100

// softirqLabels names each softirq type in the order they appear in the
// /proc/stat softirq row (after the leading total field).
var softirqLabels = []string{
	"hi", "timer", "net_tx", "net_rx", "block",
	"irq_poll", "tasklet", "sched", "hrtimer", "rcu",
}

// ParseLoadavg parses the first three fields of a /proc/loadavg line as
// 1/5/15-minute load averages.
func ParseLoadavg(content string) (one, five, fifteen float64, err error) {
	fields := strings.Fields(content)
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("loadavg: expected 3+ fields, got %d", len(fields))
	}
	for i, target := range []*float64{&one, &five, &fifteen} {
		v, perr := strconv.ParseFloat(fields[i], 64)
		if perr != nil {
			return 0, 0, 0, fmt.Errorf("loadavg field %d %q: %w", i, fields[i], perr)
		}
		*target = v
	}
	return one, five, fifteen, nil
}

// ReadLoadavg reads /proc/loadavg and returns the three load averages.
// Returns a benign error on platforms where /proc/loadavg is absent.
func ReadLoadavg() (one, five, fifteen float64, err error) {
	data, rerr := os.ReadFile("/proc/loadavg")
	if rerr != nil {
		return 0, 0, 0, fmt.Errorf("loadavg not available: %w", rerr)
	}
	return ParseLoadavg(string(data))
}

// ParseSoftirq parses /proc/stat content and returns per-softirq-type
// cumulative counts converted to seconds (counts / userHZ).
func ParseSoftirq(content string) (map[string]float64, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "softirq ") {
			continue
		}
		fields := strings.Fields(line)
		// softirq <total> <hi> <timer> <net_tx> <net_rx> <block> ...
		// Skip the leading "softirq" word and the total.
		if len(fields) < 2 {
			return nil, fmt.Errorf("softirq row malformed: %q", line)
		}
		out := make(map[string]float64, len(softirqLabels))
		for i, label := range softirqLabels {
			idx := i + 2 // skip "softirq" and total
			if idx >= len(fields) {
				break
			}
			v, perr := strconv.ParseUint(fields[idx], 10, 64)
			if perr != nil {
				return nil, fmt.Errorf("softirq field %s %q: %w", label, fields[idx], perr)
			}
			out[label] = float64(v) / float64(userHZ)
		}
		return out, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("softirq row not found in /proc/stat")
}

// ReadSoftirqSeconds reads /proc/stat and returns per-softirq-type seconds.
// Returns a benign error on platforms where /proc/stat is absent.
func ReadSoftirqSeconds() (map[string]float64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, fmt.Errorf("/proc/stat not available: %w", err)
	}
	return ParseSoftirq(string(data))
}

// SetHostLoadavg stores the latest load averages for emission.
func (c *Collector) SetHostLoadavg(one, five, fifteen float64, set bool) {
	c.hostLoadavg = [3]float64{one, five, fifteen}
	c.hostLoadavgSet = set
}

// SetHostSoftirq stores the latest per-softirq seconds map for emission.
// A nil/empty map disables emission.
func (c *Collector) SetHostSoftirq(m map[string]float64) {
	c.hostSoftirq = m
}

func renderLoadavg(values [3]float64) MetricFamily {
	out := MetricFamily{
		Name: "spark_host_loadavg",
		Help: "System load average over the given window.",
		Type: "gauge",
	}
	for i, w := range []string{"1m", "5m", "15m"} {
		out.Metrics = append(out.Metrics, Metric{
			Labels: map[string]string{"window": w},
			Value:  values[i],
		})
	}
	return out
}

func renderSoftirq(m map[string]float64) MetricFamily {
	out := MetricFamily{
		Name: "spark_host_softirq_seconds",
		Help: "Cumulative time spent servicing each softirq type.",
		Type: "counter",
	}
	for _, label := range softirqLabels {
		v, ok := m[label]
		if !ok {
			continue
		}
		out.Metrics = append(out.Metrics, Metric{
			Labels: map[string]string{"type": label},
			Value:  v,
		})
	}
	return out
}
