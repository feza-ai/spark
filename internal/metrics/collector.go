package metrics

import (
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// SchedulerMetrics provides scheduling and preemption counters.
// If nil is passed to NewCollector, scheduler metrics are omitted.
type SchedulerMetrics interface {
	ScheduleAttempts() int64
	PreemptionCount() int64
}

// HousekeepingMetrics exposes housekeeper counters for /metrics.
// If nil, housekeeping metrics are omitted.
type HousekeepingMetrics interface {
	PodsReaped(reason string) int64
	ImagesPruned() int64
	LastRunSeconds() int64
}

// Collector gathers metrics from the pod store and resource tracker.
type Collector struct {
	store        *state.PodStore
	tracker      *scheduler.ResourceTracker
	scheduler    SchedulerMetrics
	housekeeping HousekeepingMetrics

	// Wave 4 (#22) telemetry: caller pushes fresh samples; emission
	// happens in Collect() so empty samples produce no metric line.
	podThrottle    []PodThrottleSample
	hostLoadavg    [3]float64
	hostLoadavgSet bool
	hostSoftirq    map[string]float64
}

// NewCollector creates a Collector. scheduler may be nil.
func NewCollector(store *state.PodStore, tracker *scheduler.ResourceTracker, sched SchedulerMetrics) *Collector {
	return &Collector{
		store:     store,
		tracker:   tracker,
		scheduler: sched,
	}
}

// SetHousekeeping wires the housekeeping counter source. Pass nil to
// disable housekeeping metric emission.
func (c *Collector) SetHousekeeping(h HousekeepingMetrics) {
	c.housekeeping = h
}

// Collect gathers all metrics at call time and returns them as MetricFamily slices.
func (c *Collector) Collect() []MetricFamily {
	var families []MetricFamily

	// Resource metrics from tracker.
	allocatable := c.tracker.Allocatable()
	available := c.tracker.Available()

	families = append(families, MetricFamily{
		Name: "spark_node_cpu_total_millis",
		Help: "Total allocatable CPU in millicores",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(allocatable.CPUMillis)}},
	})
	families = append(families, MetricFamily{
		Name: "spark_node_cpu_available_millis",
		Help: "Available CPU in millicores",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(available.CPUMillis)}},
	})
	families = append(families, MetricFamily{
		Name: "spark_node_memory_total_mb",
		Help: "Total allocatable memory in MB",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(allocatable.MemoryMB)}},
	})
	families = append(families, MetricFamily{
		Name: "spark_node_memory_available_mb",
		Help: "Available memory in MB",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(available.MemoryMB)}},
	})
	families = append(families, MetricFamily{
		Name: "spark_node_gpu_memory_total_mb",
		Help: "Total allocatable GPU memory in MB",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(allocatable.GPUMemoryMB)}},
	})
	families = append(families, MetricFamily{
		Name: "spark_node_gpu_memory_available_mb",
		Help: "Available GPU memory in MB",
		Type: "gauge",
		Metrics: []Metric{{Value: float64(available.GPUMemoryMB)}},
	})

	// Pod counts by status.
	pods := c.store.List("")
	counts := make(map[string]int)
	for _, pod := range pods {
		counts[string(pod.Status)]++
	}
	var podMetrics []Metric
	for status, count := range counts {
		podMetrics = append(podMetrics, Metric{
			Labels: map[string]string{"status": status},
			Value:  float64(count),
		})
	}
	families = append(families, MetricFamily{
		Name:    "spark_pods_total",
		Help:    "Number of pods by status",
		Type:    "gauge",
		Metrics: podMetrics,
	})

	// Restart counts per pod.
	var restartMetrics []Metric
	for _, pod := range pods {
		if pod.Restarts > 0 {
			restartMetrics = append(restartMetrics, Metric{
				Labels: map[string]string{"pod": pod.Spec.Name},
				Value:  float64(pod.Restarts),
			})
		}
	}
	families = append(families, MetricFamily{
		Name:    "spark_pod_restarts_total",
		Help:    "Total restarts per pod",
		Type:    "counter",
		Metrics: restartMetrics,
	})

	// Wave 4 (#22) telemetry: pod CPU throttling and host metrics.
	if len(c.podThrottle) > 0 {
		families = append(families, renderPodThrottled(c.podThrottle))
	}
	if c.hostLoadavgSet {
		families = append(families, renderLoadavg(c.hostLoadavg))
	}
	if len(c.hostSoftirq) > 0 {
		families = append(families, renderSoftirq(c.hostSoftirq))
	}

	// Scheduler metrics (optional).
	if c.scheduler != nil {
		families = append(families, MetricFamily{
			Name: "spark_scheduling_attempts_total",
			Help: "Total scheduling attempts",
			Type: "counter",
			Metrics: []Metric{{Value: float64(c.scheduler.ScheduleAttempts())}},
		})
		families = append(families, MetricFamily{
			Name: "spark_preemptions_total",
			Help: "Total preemptions",
			Type: "counter",
			Metrics: []Metric{{Value: float64(c.scheduler.PreemptionCount())}},
		})
	}

	// Housekeeping metrics (optional).
	if c.housekeeping != nil {
		reasons := []string{"ttl_completed", "ttl_failed", "orphan"}
		reaped := make([]Metric, 0, len(reasons))
		for _, r := range reasons {
			reaped = append(reaped, Metric{
				Labels: map[string]string{"reason": r},
				Value:  float64(c.housekeeping.PodsReaped(r)),
			})
		}
		families = append(families, MetricFamily{
			Name:    "spark_pods_reaped_total",
			Help:    "Total pods reaped by housekeeping, by reason",
			Type:    "counter",
			Metrics: reaped,
		})
		families = append(families, MetricFamily{
			Name:    "spark_images_pruned_total",
			Help:    "Total images reclaimed by housekeeping",
			Type:    "counter",
			Metrics: []Metric{{Value: float64(c.housekeeping.ImagesPruned())}},
		})
		families = append(families, MetricFamily{
			Name:    "spark_housekeeping_last_run_seconds",
			Help:    "Unix timestamp of the last housekeeping pass (0 if never)",
			Type:    "gauge",
			Metrics: []Metric{{Value: float64(c.housekeeping.LastRunSeconds())}},
		})
	}

	return families
}
