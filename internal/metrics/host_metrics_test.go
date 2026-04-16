package metrics

import (
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func TestParseLoadavg_HappyPath(t *testing.T) {
	one, five, fifteen, err := ParseLoadavg("0.50 0.40 0.30 1/200 12345\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if one != 0.5 || five != 0.4 || fifteen != 0.3 {
		t.Errorf("got (%v, %v, %v), want (0.5, 0.4, 0.3)", one, five, fifteen)
	}
}

func TestParseLoadavg_Malformed(t *testing.T) {
	cases := []string{
		"",
		"abc def ghi",
		"0.5",
		"0.5 0.4",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, _, _, err := ParseLoadavg(in); err == nil {
				t.Errorf("expected error for %q", in)
			}
		})
	}
}

func TestParseSoftirq_HappyPath(t *testing.T) {
	// Canonical /proc/stat softirq row (counts, not seconds).
	in := strings.Join([]string{
		"cpu  123 0 456 789 0 0 0 0 0 0",
		"softirq 300 10 20 30 40 50 60 70 80 90 100",
		"intr 0",
	}, "\n")
	got, err := ParseSoftirq(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantKeys := []string{"hi", "timer", "net_tx", "net_rx", "block", "irq_poll", "tasklet", "sched", "hrtimer", "rcu"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	// 10/100 = 0.1 seconds for hi.
	if got["hi"] != 0.1 {
		t.Errorf("hi: got %v, want 0.1", got["hi"])
	}
	// 100/100 = 1.0 for rcu.
	if got["rcu"] != 1.0 {
		t.Errorf("rcu: got %v, want 1.0", got["rcu"])
	}
}

func TestParseSoftirq_MissingRow(t *testing.T) {
	if _, err := ParseSoftirq("cpu  1 2 3\nintr 0\n"); err == nil {
		t.Fatal("expected error when softirq row absent")
	}
}

func TestRenderLoadavg(t *testing.T) {
	mf := renderLoadavg([3]float64{0.5, 0.4, 0.3})
	if mf.Name != "spark_host_loadavg" {
		t.Errorf("name: got %q", mf.Name)
	}
	if mf.Type != "gauge" {
		t.Errorf("type: got %q", mf.Type)
	}
	if len(mf.Metrics) != 3 {
		t.Fatalf("metrics count: got %d", len(mf.Metrics))
	}
	wantWindows := []string{"1m", "5m", "15m"}
	wantValues := []float64{0.5, 0.4, 0.3}
	for i, m := range mf.Metrics {
		if m.Labels["window"] != wantWindows[i] {
			t.Errorf("metric %d window: got %q, want %q", i, m.Labels["window"], wantWindows[i])
		}
		if m.Value != wantValues[i] {
			t.Errorf("metric %d value: got %v, want %v", i, m.Value, wantValues[i])
		}
	}
}

func TestRenderSoftirq(t *testing.T) {
	m := map[string]float64{"net_rx": 12.34, "timer": 5.67}
	mf := renderSoftirq(m)
	if mf.Name != "spark_host_softirq_seconds" {
		t.Errorf("name: got %q", mf.Name)
	}
	// Renders in canonical label order; skip labels not in input map.
	if len(mf.Metrics) != 2 {
		t.Fatalf("metrics count: got %d", len(mf.Metrics))
	}
	got := make(map[string]float64, len(mf.Metrics))
	for _, met := range mf.Metrics {
		got[met.Labels["type"]] = met.Value
	}
	if got["net_rx"] != 12.34 || got["timer"] != 5.67 {
		t.Errorf("got %+v, want net_rx=12.34 timer=5.67", got)
	}
}

func TestCollector_EmitsHostMetrics(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 4000, MemoryMB: 8192},
		scheduler.Resources{},
		nil, 0,
	)
	c := NewCollector(store, tracker, nil)

	// Without samples, no emission.
	families := c.Collect()
	for _, mf := range families {
		if mf.Name == "spark_host_loadavg" || mf.Name == "spark_host_softirq_seconds" {
			t.Errorf("unexpected emission without samples: %s", mf.Name)
		}
	}

	// With samples, both metrics present.
	c.SetHostLoadavg(0.5, 0.4, 0.3, true)
	c.SetHostSoftirq(map[string]float64{"net_rx": 1.0, "timer": 2.0})
	families = c.Collect()
	var haveLoad, haveSoft bool
	for _, mf := range families {
		if mf.Name == "spark_host_loadavg" {
			haveLoad = true
		}
		if mf.Name == "spark_host_softirq_seconds" {
			haveSoft = true
		}
	}
	if !haveLoad {
		t.Error("spark_host_loadavg not emitted")
	}
	if !haveSoft {
		t.Error("spark_host_softirq_seconds not emitted")
	}
}
