package metrics

import (
	"sort"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type fakeSchedulerMetrics struct {
	attempts    int64
	preemptions int64
}

func (f *fakeSchedulerMetrics) ScheduleAttempts() int64 { return f.attempts }
func (f *fakeSchedulerMetrics) PreemptionCount() int64  { return f.preemptions }

func findFamily(families []MetricFamily, name string) (MetricFamily, bool) {
	for _, f := range families {
		if f.Name == name {
			return f, true
		}
	}
	return MetricFamily{}, false
}

func TestCollect_PodCounts(t *testing.T) {
	store := state.NewPodStore()
	store.Apply(manifest.PodSpec{Name: "pod-a"})
	store.Apply(manifest.PodSpec{Name: "pod-b"})
	store.Apply(manifest.PodSpec{Name: "pod-c"})
	store.UpdateStatus("pod-b", state.StatusRunning, "started")
	store.UpdateStatus("pod-c", state.StatusRunning, "started")

	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192, GPUMemoryMB: 16384},
		scheduler.Resources{},
	nil, 0,
	)

	c := NewCollector(store, tracker, nil)
	families := c.Collect()

	f, ok := findFamily(families, "spark_pods_total")
	if !ok {
		t.Fatal("spark_pods_total not found")
	}

	got := make(map[string]float64)
	for _, m := range f.Metrics {
		got[m.Labels["status"]] = m.Value
	}

	tests := []struct {
		status string
		want   float64
	}{
		{"pending", 1},
		{"running", 2},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got[tt.status] != tt.want {
				t.Errorf("status %s: got %g, want %g", tt.status, got[tt.status], tt.want)
			}
		})
	}
}

func TestCollect_Resources(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 500, MemoryMB: 1024, GPUMemoryMB: 0},
	nil, 0,
	)
	// Allocate some resources to make available != total.
	tracker.Allocate("test-pod", manifest.ResourceList{CPUMillis: 1000, MemoryMB: 2048, GPUMemoryMB: 4096})

	c := NewCollector(store, tracker, nil)
	families := c.Collect()

	tests := []struct {
		name string
		want float64
	}{
		{"spark_node_cpu_total_millis", 7500},
		{"spark_node_cpu_available_millis", 6500},
		{"spark_node_memory_total_mb", 15360},
		{"spark_node_memory_available_mb", 13312},
		{"spark_node_gpu_memory_total_mb", 32768},
		{"spark_node_gpu_memory_available_mb", 28672},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := findFamily(families, tt.name)
			if !ok {
				t.Fatalf("%s not found", tt.name)
			}
			if len(f.Metrics) != 1 {
				t.Fatalf("expected 1 metric, got %d", len(f.Metrics))
			}
			if f.Metrics[0].Value != tt.want {
				t.Errorf("got %g, want %g", f.Metrics[0].Value, tt.want)
			}
		})
	}
}

func TestCollect_SchedulerMetrics(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
	nil, 0,
	)
	sched := &fakeSchedulerMetrics{attempts: 42, preemptions: 7}
	c := NewCollector(store, tracker, sched)
	families := c.Collect()

	tests := []struct {
		name string
		want float64
	}{
		{"spark_scheduling_attempts_total", 42},
		{"spark_preemptions_total", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := findFamily(families, tt.name)
			if !ok {
				t.Fatalf("%s not found", tt.name)
			}
			if f.Metrics[0].Value != tt.want {
				t.Errorf("got %g, want %g", f.Metrics[0].Value, tt.want)
			}
		})
	}
}

func TestCollect_SchedulerNil(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
	nil, 0,
	)
	c := NewCollector(store, tracker, nil)
	families := c.Collect()

	for _, f := range families {
		if f.Name == "spark_scheduling_attempts_total" || f.Name == "spark_preemptions_total" {
			t.Errorf("scheduler metric %s should not be present when scheduler is nil", f.Name)
		}
	}
}

func TestRender_Format(t *testing.T) {
	families := []MetricFamily{
		{
			Name: "spark_node_cpu_total_millis",
			Help: "Total allocatable CPU in millicores",
			Type: "gauge",
			Metrics: []Metric{{Value: 8000}},
		},
		{
			Name: "spark_pods_total",
			Help: "Number of pods by status",
			Type: "gauge",
			Metrics: []Metric{
				{Labels: map[string]string{"status": "running"}, Value: 3},
			},
		},
	}

	got := string(Render(families))
	want := strings.Join([]string{
		"# HELP spark_node_cpu_total_millis Total allocatable CPU in millicores",
		"# TYPE spark_node_cpu_total_millis gauge",
		"spark_node_cpu_total_millis 8000",
		"# HELP spark_pods_total Number of pods by status",
		"# TYPE spark_pods_total gauge",
		`spark_pods_total{status="running"} 3`,
		"",
	}, "\n")

	if got != want {
		t.Errorf("render mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRender_LabelEscaping(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"backslash", `a\b`, `a\\b`},
		{"quote", `a"b`, `a\"b`},
		{"newline", "a\nb", `a\nb`},
		{"combined", "a\\\"\n", `a\\\"\n`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := []MetricFamily{{
				Name: "test_metric",
				Help: "test",
				Type: "gauge",
				Metrics: []Metric{{
					Labels: map[string]string{"label": tt.value},
					Value:  1,
				}},
			}}
			got := string(Render(families))
			wantLine := `test_metric{label="` + tt.want + `"} 1`
			if !strings.Contains(got, wantLine) {
				t.Errorf("expected line %q in output:\n%s", wantLine, got)
			}
		})
	}
}

func TestRender_MultipleLabels_Sorted(t *testing.T) {
	families := []MetricFamily{{
		Name: "test_metric",
		Help: "test",
		Type: "gauge",
		Metrics: []Metric{{
			Labels: map[string]string{"z": "1", "a": "2"},
			Value:  42,
		}},
	}}
	got := string(Render(families))
	want := `test_metric{a="2",z="1"} 42`
	if !strings.Contains(got, want) {
		t.Errorf("expected sorted labels %q in output:\n%s", want, got)
	}
}

func TestCollect_PodRestarts(t *testing.T) {
	store := state.NewPodStore()
	store.LoadFrom(map[string]*state.PodRecord{
		"pod-a": {Spec: manifest.PodSpec{Name: "pod-a"}, Status: state.StatusRunning, Restarts: 3},
		"pod-b": {Spec: manifest.PodSpec{Name: "pod-b"}, Status: state.StatusRunning, Restarts: 0},
	})

	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
	nil, 0,
	)
	c := NewCollector(store, tracker, nil)
	families := c.Collect()

	f, ok := findFamily(families, "spark_pod_restarts_total")
	if !ok {
		t.Fatal("spark_pod_restarts_total not found")
	}

	// Only pod-a should appear (restarts > 0).
	if len(f.Metrics) != 1 {
		t.Fatalf("expected 1 restart metric, got %d", len(f.Metrics))
	}
	if f.Metrics[0].Labels["pod"] != "pod-a" {
		t.Errorf("expected pod-a, got %s", f.Metrics[0].Labels["pod"])
	}
	if f.Metrics[0].Value != 3 {
		t.Errorf("expected 3 restarts, got %g", f.Metrics[0].Value)
	}
}

func TestCollect_FamilyTypes(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
	nil, 0,
	)
	sched := &fakeSchedulerMetrics{}
	c := NewCollector(store, tracker, sched)
	families := c.Collect()

	gauges := []string{
		"spark_node_cpu_total_millis",
		"spark_node_cpu_available_millis",
		"spark_node_memory_total_mb",
		"spark_node_memory_available_mb",
		"spark_node_gpu_memory_total_mb",
		"spark_node_gpu_memory_available_mb",
		"spark_pods_total",
	}
	counters := []string{
		"spark_pod_restarts_total",
		"spark_scheduling_attempts_total",
		"spark_preemptions_total",
	}

	familyMap := make(map[string]MetricFamily)
	for _, f := range families {
		familyMap[f.Name] = f
	}

	for _, name := range gauges {
		f, ok := familyMap[name]
		if !ok {
			t.Errorf("missing family %s", name)
			continue
		}
		if f.Type != "gauge" {
			t.Errorf("%s: type = %s, want gauge", name, f.Type)
		}
	}
	for _, name := range counters {
		f, ok := familyMap[name]
		if !ok {
			t.Errorf("missing family %s", name)
			continue
		}
		if f.Type != "counter" {
			t.Errorf("%s: type = %s, want counter", name, f.Type)
		}
	}

	for _, f := range families {
		if f.Help == "" {
			t.Errorf("%s: Help is empty", f.Name)
		}
	}

	_ = sort.Strings
}
