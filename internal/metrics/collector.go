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

// Collector gathers metrics from the pod store and resource tracker.
type Collector struct {
	store     *state.PodStore
	tracker   *scheduler.ResourceTracker
	scheduler SchedulerMetrics
}

// NewCollector creates a Collector. scheduler may be nil.
func NewCollector(store *state.PodStore, tracker *scheduler.ResourceTracker, sched SchedulerMetrics) *Collector {
	return &Collector{
		store:     store,
		tracker:   tracker,
		scheduler: sched,
	}
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

	return families
}
