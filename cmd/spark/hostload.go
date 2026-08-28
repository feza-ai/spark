package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/feza-ai/spark/internal/metrics"
)

// hostLoadAdapter implements scheduler.HostLoadSource by periodically
// sampling /proc/loadavg (via metrics.ReadLoadavg) and converting the
// 5-minute load average into free CPU millicores against the host's total
// CPU capacity. It exists to answer issue #76: resource accounting alone
// can starve scheduling when reservations sit idle; this gives the
// scheduler a live reality check to admit against instead.
//
// The 5-minute window is a deliberate middle ground: 1-minute is noisy
// enough to flap admission decisions on a brief spike, 15-minute reacts
// too slowly to genuine sustained idle. marginMillis is subtracted from
// the computed headroom to cover the trailing average's inherent lag
// (load can rise between samples before the average catches up).
//
// Neither the issue nor any ADR prescribes a specific window or margin,
// so both are operator-tunable (--host-load-sample-interval,
// --cpu-overcommit-margin-millis) rather than hardcoded — see
// docs/adr/013-utilization-aware-admission.md.
type hostLoadAdapter struct {
	totalCPUMillis int
	marginMillis   int

	mu     sync.RWMutex
	millis int
	ok     bool
}

func newHostLoadAdapter(totalCPUMillis, marginMillis int) *hostLoadAdapter {
	return &hostLoadAdapter{totalCPUMillis: totalCPUMillis, marginMillis: marginMillis}
}

// AvailableCPUMillis implements scheduler.HostLoadSource.
func (h *hostLoadAdapter) AvailableCPUMillis() (int, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.millis, h.ok
}

// sample reads /proc/loadavg once and updates the live estimate. It
// returns the parsed load averages so the caller can also feed the
// metrics collector without a second read of /proc/loadavg.
func (h *hostLoadAdapter) sample() (one, five, fifteen float64, err error) {
	one, five, fifteen, err = metrics.ReadLoadavg()
	if err != nil {
		return 0, 0, 0, err
	}
	freeMillis := h.totalCPUMillis - int(five*1000) - h.marginMillis
	if freeMillis < 0 {
		freeMillis = 0
	}
	h.mu.Lock()
	h.millis, h.ok = freeMillis, true
	h.mu.Unlock()
	return one, five, fifteen, nil
}

// runHostLoadSampling starts a goroutine that samples /proc/loadavg on the
// given interval until ctx is cancelled, feeding both the scheduler (via
// hostLoad) and the metrics collector (via SetHostLoadavg) from the same
// read -- activating the previously-dead spark_host_loadavg metric wiring
// (docs/design.md noted this as a follow-up). Sampling errors (e.g.
// /proc/loadavg absent on non-Linux dev machines) are logged at Debug and
// otherwise ignored: the feature simply stays disabled (AvailableCPUMillis
// reports ok=false) until a sample succeeds.
func runHostLoadSampling(ctx context.Context, interval time.Duration, hostLoad *hostLoadAdapter, mc *metrics.Collector) {
	sampleOnce := func() {
		one, five, fifteen, err := hostLoad.sample()
		if err != nil {
			slog.Debug("host load sample failed", "error", err)
			return
		}
		mc.SetHostLoadavg(one, five, fifteen, true)
	}

	go func() {
		sampleOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sampleOnce()
			}
		}
	}()
}
