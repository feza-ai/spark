package main

import (
	"log/slog"
	"sync"

	"github.com/feza-ai/spark/internal/metrics"
)

// hostMemoryAdapter implements scheduler.HostMemorySource by reading
// /proc/meminfo's MemAvailable at the moment admission asks for it. It
// answers the half of issue #121 that a declared-request ledger cannot:
// the ledger sums what pods said they would use, so containers that
// declared nothing leave it reading empty on a node that is full.
//
// Unlike hostLoadAdapter, this samples on demand rather than on a ticker.
// The CPU source has no choice -- a load average is a trailing statistic --
// but memory can collapse in seconds, and admitting a 100GiB pod against a
// 15-second-old reading is the failure this guard exists to stop. A
// procfs read costs microseconds and runs once per admission attempt, so
// there is nothing to amortise.
//
// readErrs counts consecutive read failures purely so the operator sees
// the guard silently disable itself: the first failure logs at Warn, the
// rest at Debug, since a host without /proc/meminfo (a macOS dev box)
// would otherwise log on every scheduling attempt forever.
type hostMemoryAdapter struct {
	read func() (int, error)

	mu     sync.Mutex
	warned bool
}

func newHostMemoryAdapter() *hostMemoryAdapter {
	return &hostMemoryAdapter{read: metrics.ReadMemAvailableMB}
}

// AvailableMemoryMB implements scheduler.HostMemorySource. A read failure
// reports ok=false, which disables the guard for that admission rather
// than refusing on an unknown: an unreadable host is not evidence of an
// exhausted one.
func (h *hostMemoryAdapter) AvailableMemoryMB() (int, bool) {
	mb, err := h.read()
	if err != nil {
		h.logReadFailure(err)
		return 0, false
	}
	h.mu.Lock()
	h.warned = false
	h.mu.Unlock()
	return mb, true
}

func (h *hostMemoryAdapter) logReadFailure(err error) {
	h.mu.Lock()
	first := !h.warned
	h.warned = true
	h.mu.Unlock()

	if first {
		slog.Warn("live memory guard disabled: cannot read host memory; admission falls back to declared-request accounting alone", "error", err)
		return
	}
	slog.Debug("host memory read failed", "error", err)
}
