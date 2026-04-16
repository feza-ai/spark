package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func TestParseCpuStatThrottled_HappyPath(t *testing.T) {
	in := "usage_usec 12345\nuser_usec 1000\nthrottled_usec 1234567\nnr_throttled 5\n"
	got, err := ParseCpuStatThrottled(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := 1.234567
	if got < want-1e-9 || got > want+1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseCpuStatThrottled_MissingThrottled(t *testing.T) {
	in := "usage_usec 12345\nuser_usec 1000\nnr_throttled 5\n"
	if _, err := ParseCpuStatThrottled(in); err == nil {
		t.Fatal("expected error for missing throttled_usec")
	}
}

func TestParseCpuStatThrottled_Malformed(t *testing.T) {
	in := "throttled_usec abc\n"
	if _, err := ParseCpuStatThrottled(in); err == nil {
		t.Fatal("expected error for non-integer throttled_usec")
	}
}

func TestReadPodThrottledSeconds_FromFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cpu.stat")
	if err := os.WriteFile(path, []byte("throttled_usec 500000\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := ReadPodThrottledSeconds(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0.5 {
		t.Errorf("got %v, want 0.5", got)
	}
}

func TestRenderPodThrottled(t *testing.T) {
	mf := renderPodThrottled([]PodThrottleSample{
		{Pod: "a", Seconds: 1.5},
		{Pod: "b", Seconds: 0},
	})
	if mf.Name != "spark_pod_cpu_throttled_seconds" {
		t.Errorf("name: got %q", mf.Name)
	}
	if mf.Type != "counter" {
		t.Errorf("type: got %q", mf.Type)
	}
	if len(mf.Metrics) != 2 {
		t.Fatalf("metrics: got %d, want 2", len(mf.Metrics))
	}
	if mf.Metrics[0].Labels["pod"] != "a" || mf.Metrics[0].Value != 1.5 {
		t.Errorf("metric 0: got %+v", mf.Metrics[0])
	}
	if mf.Metrics[1].Labels["pod"] != "b" || mf.Metrics[1].Value != 0 {
		t.Errorf("metric 1: got %+v", mf.Metrics[1])
	}
}

func TestCollector_EmitsPodThrottled(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
		nil, 0,
	)
	c := NewCollector(store, tracker, nil)
	c.SetPodThrottleSamples([]PodThrottleSample{{Pod: "p1", Seconds: 2.5}})
	families := c.Collect()

	var found bool
	for _, mf := range families {
		if mf.Name == "spark_pod_cpu_throttled_seconds" {
			found = true
			if len(mf.Metrics) != 1 || mf.Metrics[0].Labels["pod"] != "p1" || mf.Metrics[0].Value != 2.5 {
				t.Errorf("unexpected metric: %+v", mf.Metrics)
			}
		}
	}
	if !found {
		t.Fatalf("expected spark_pod_cpu_throttled_seconds in families: %v",
			strings.Join(metricNames(families), ", "))
	}
}

func metricNames(fs []MetricFamily) []string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return names
}
