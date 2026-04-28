// Package housekeeper implements automatic cleanup of finished pods,
// orphaned podman containers, and unused images. It runs as a single
// background goroutine alongside the reconciler.
//
// The housekeeper has three independent responsibilities:
//
//  1. TTL-based pod reaping. Pods that have been in the Completed or
//     Failed state for longer than their configured TTL are removed
//     from the store (and from podman best-effort). The TTL is
//     configurable per-pod via the spark.feza.ai/ttl-after-finished
//     annotation, with separate daemon-wide defaults for completed and
//     failed pods.
//
//  2. Orphan reaping. Pods that exist in podman but not in the Spark
//     store are reaped after orphanReapTTL when they are in a terminal
//     state (exited, stopped, dead). Running orphans are warned about
//     but never killed — that is the operator's call.
//
//  3. Image pruning. Periodically runs `podman image prune -f` to free
//     space taken by dangling images.
//
// Each cleanup type is disabled by setting its TTL/interval to zero.
package housekeeper

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

// TTLAnnotation is the manifest annotation that overrides the daemon-wide
// completed/failed TTL on a per-pod basis. Value must be a duration
// string parseable by time.ParseDuration (e.g. "30m", "2h", "0s" to
// disable cleanup for that pod).
const TTLAnnotation = "spark.feza.ai/ttl-after-finished"

// store is the subset of state.PodStore the housekeeper needs.
// Defining it as an interface keeps the unit tests independent of the
// SQLite-backed pruning behavior on PodStore.
type store interface {
	List(state.PodStatus) []state.PodRecord
	Delete(name string) bool
}

// reaper is the subset of executor.Executor the housekeeper needs.
type reaper interface {
	ListPods(ctx context.Context) ([]executor.PodListEntry, error)
	RemovePod(ctx context.Context, name string) error
	PruneImages(ctx context.Context) (int, error)
}

// Config controls the housekeeping intervals and TTLs. A zero value for
// any TTL or interval disables that cleanup. Interval defaults to one
// minute when zero.
type Config struct {
	// Interval between housekeeping passes (TTL + orphan checks).
	// If zero, defaults to 1 minute.
	Interval time.Duration

	// CompletedPodTTL is how long a Completed pod is retained before
	// removal. Zero disables.
	CompletedPodTTL time.Duration

	// FailedPodTTL is how long a Failed pod is retained before
	// removal. Zero disables.
	FailedPodTTL time.Duration

	// OrphanReapTTL is how long a podman pod with no Spark store
	// record must remain in a terminal state before being removed.
	// Zero disables orphan reaping.
	OrphanReapTTL time.Duration

	// ImagePruneInterval is how often `podman image prune -f` runs.
	// Zero disables.
	ImagePruneInterval time.Duration
}

// Counters collects monotonic counters and the last-run timestamp for
// /metrics. All accessors are safe for concurrent reads.
type Counters struct {
	ttlCompleted    atomic.Int64
	ttlFailed       atomic.Int64
	orphan          atomic.Int64
	imagesPruned    atomic.Int64
	lastRunUnixSecs atomic.Int64
}

// PodsReaped returns the count of pods reaped for the given reason
// label. Reasons: ttl_completed, ttl_failed, orphan. Unknown reasons
// return 0.
func (c *Counters) PodsReaped(reason string) int64 {
	switch reason {
	case "ttl_completed":
		return c.ttlCompleted.Load()
	case "ttl_failed":
		return c.ttlFailed.Load()
	case "orphan":
		return c.orphan.Load()
	default:
		return 0
	}
}

// ImagesPruned returns the cumulative number of images reclaimed.
func (c *Counters) ImagesPruned() int64 { return c.imagesPruned.Load() }

// LastRunSeconds returns the wall-clock unix seconds at which the last
// housekeeping pass completed. Zero if it has not yet run.
func (c *Counters) LastRunSeconds() int64 { return c.lastRunUnixSecs.Load() }

// Housekeeper drives the periodic cleanup loop.
type Housekeeper struct {
	store    store
	executor reaper
	cfg      Config
	counters *Counters

	// nowFunc returns the current time. Tests inject a fake clock.
	nowFunc func() time.Time

	// orphanFirstSeen tracks when an orphan in a terminal state was
	// first observed. Names are removed once reaped or when they
	// re-enter the store.
	orphanFirstSeen map[string]time.Time

	// lastImagePrune tracks the last time the image prune ran.
	lastImagePrune time.Time
}

// New constructs a Housekeeper. The returned counters value is usable
// for metric emission; it is shared with the housekeeper goroutine.
func New(s store, e reaper, cfg Config) (*Housekeeper, *Counters) {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	c := &Counters{}
	return &Housekeeper{
		store:           s,
		executor:        e,
		cfg:             cfg,
		counters:        c,
		nowFunc:         time.Now,
		orphanFirstSeen: make(map[string]time.Time),
	}, c
}

// SetClock overrides the time source for tests.
func (h *Housekeeper) SetClock(f func() time.Time) { h.nowFunc = f }

// Run blocks running the housekeeping loop until ctx is cancelled.
func (h *Housekeeper) Run(ctx context.Context) {
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("housekeeper stopped")
			return
		case <-ticker.C:
			h.RunOnce(ctx)
		}
	}
}

// RunOnce executes a single housekeeping pass. Exported for tests and
// for callers that want to force a sweep on shutdown.
func (h *Housekeeper) RunOnce(ctx context.Context) {
	h.reapTTL(ctx)
	h.reapOrphans(ctx)
	h.maybePruneImages(ctx)
	h.counters.lastRunUnixSecs.Store(h.nowFunc().Unix())
}

// reapTTL removes pods whose terminal state has aged past their TTL.
// The per-pod annotation, if present and parseable, overrides the
// daemon default. A zero TTL (either configured or annotated) disables
// cleanup for that pod.
func (h *Housekeeper) reapTTL(ctx context.Context) {
	now := h.nowFunc()
	for _, rec := range h.store.List("") {
		if rec.Status != state.StatusCompleted && rec.Status != state.StatusFailed {
			continue
		}
		if rec.FinishedAt.IsZero() {
			continue
		}

		ttl := h.cfg.CompletedPodTTL
		if rec.Status == state.StatusFailed {
			ttl = h.cfg.FailedPodTTL
		}
		if override, ok := annotationTTL(rec.Spec.Annotations); ok {
			ttl = override
		}
		if ttl <= 0 {
			continue
		}
		if now.Sub(rec.FinishedAt) < ttl {
			continue
		}

		// Best-effort remove from podman; the pod may already be gone.
		if err := h.executor.RemovePod(ctx, rec.Spec.Name); err != nil {
			slog.Debug("housekeeper: best-effort RemovePod failed",
				"pod", rec.Spec.Name, "err", err)
		}
		if !h.store.Delete(rec.Spec.Name) {
			continue
		}
		if rec.Status == state.StatusCompleted {
			h.counters.ttlCompleted.Add(1)
			slog.Info("housekeeper reaped completed pod",
				"pod", rec.Spec.Name, "age", now.Sub(rec.FinishedAt))
		} else {
			h.counters.ttlFailed.Add(1)
			slog.Info("housekeeper reaped failed pod",
				"pod", rec.Spec.Name, "age", now.Sub(rec.FinishedAt))
		}
	}
}

// reapOrphans removes podman pods that have no corresponding Spark
// store record and have been in a terminal state for at least
// OrphanReapTTL. Running orphans produce a warning but are left alone.
func (h *Housekeeper) reapOrphans(ctx context.Context) {
	if h.cfg.OrphanReapTTL <= 0 {
		return
	}
	pods, err := h.executor.ListPods(ctx)
	if err != nil {
		slog.Warn("housekeeper: ListPods failed", "err", err)
		return
	}
	known := make(map[string]struct{})
	for _, rec := range h.store.List("") {
		known[rec.Spec.Name] = struct{}{}
	}

	now := h.nowFunc()
	currentOrphans := make(map[string]struct{})
	for _, p := range pods {
		if _, inStore := known[p.Name]; inStore {
			continue
		}
		// Running orphan: warn, don't kill.
		if p.Running {
			slog.Warn("housekeeper: running orphan pod (not killed)",
				"pod", p.Name, "status", p.Status)
			continue
		}
		// Terminal-only orphans are eligible for reap.
		if !p.IsTerminal() {
			continue
		}
		currentOrphans[p.Name] = struct{}{}
		first, seen := h.orphanFirstSeen[p.Name]
		if !seen {
			h.orphanFirstSeen[p.Name] = now
			slog.Info("housekeeper: orphan observed", "pod", p.Name, "status", p.Status)
			continue
		}
		if now.Sub(first) < h.cfg.OrphanReapTTL {
			continue
		}
		if err := h.executor.RemovePod(ctx, p.Name); err != nil {
			slog.Warn("housekeeper: failed to remove orphan", "pod", p.Name, "err", err)
			continue
		}
		delete(h.orphanFirstSeen, p.Name)
		h.counters.orphan.Add(1)
		slog.Info("housekeeper reaped orphan pod", "pod", p.Name, "age", now.Sub(first))
	}
	// Forget orphans that have disappeared.
	for name := range h.orphanFirstSeen {
		if _, still := currentOrphans[name]; !still {
			delete(h.orphanFirstSeen, name)
		}
	}
}

// maybePruneImages runs `podman image prune -f` if the configured
// interval has elapsed since the last prune.
func (h *Housekeeper) maybePruneImages(ctx context.Context) {
	if h.cfg.ImagePruneInterval <= 0 {
		return
	}
	now := h.nowFunc()
	if !h.lastImagePrune.IsZero() && now.Sub(h.lastImagePrune) < h.cfg.ImagePruneInterval {
		return
	}
	count, err := h.executor.PruneImages(ctx)
	h.lastImagePrune = now
	if err != nil {
		slog.Warn("housekeeper: image prune failed", "err", err)
		return
	}
	if count > 0 {
		h.counters.imagesPruned.Add(int64(count))
		slog.Info("housekeeper pruned images", "count", count)
	}
}

// annotationTTL parses TTLAnnotation from the given map. Returns
// (duration, true) if the annotation is present and parseable; an
// empty value or whitespace returns (0, false) so the daemon default
// applies. A negative duration is treated as "disable" (0, true).
func annotationTTL(annotations map[string]string) (time.Duration, bool) {
	if len(annotations) == 0 {
		return 0, false
	}
	raw, ok := annotations[TTLAnnotation]
	if !ok {
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("housekeeper: invalid TTL annotation", "value", raw, "err", err)
		return 0, false
	}
	if d < 0 {
		return 0, true
	}
	return d, true
}
