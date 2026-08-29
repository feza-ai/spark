package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

// defaultPendingLogTimeout is how long a Pending pod's GET /logs treats
// podman's "no such pod" error as "not started yet" before switching to
// reporting the shortfall directly (issue #78). Overridable per-Server via
// SetPendingLogTimeout.
const defaultPendingLogTimeout = 10 * time.Minute

func (s *Server) registerPodLogRoutes() {
	s.mux.HandleFunc("GET /api/v1/pods/{name}/logs", s.handlePodLogs)
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	tailStr := r.URL.Query().Get("tail")
	tail := 100
	if tailStr != "" {
		n, err := strconv.Atoi(tailStr)
		if err == nil && n > 0 {
			tail = n
		}
	}

	follow := r.URL.Query().Get("follow") == "true"

	if follow {
		s.handlePodLogsFollow(w, r, name, tail)
		return
	}

	rec, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	data, err := s.executor.PodLogs(r.Context(), name, tail)
	if err != nil {
		// Issue #78: a pod that is legitimately Pending (queued, awaiting
		// resources) was never created in podman, so "no such pod" here
		// does not mean podman lost it -- it means it hasn't started yet.
		// Forwarding the raw podman error as a 500 misleads callers (CI
		// deploy scripts) into failing fast on a pod that is still queued.
		if rec.Status == state.StatusPending && isNoSuchPod(err) {
			s.writePendingLogs(w, rec)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
}

// writePendingLogs responds to GET /logs for a Pending pod whose podman
// backing does not exist yet (issue #78). Within pendingLogTimeout of the
// pod's most recent uninterrupted run of Pending status, it reports "not
// started yet" (200, empty body) rather than an error implying the pod
// vanished. Past the timeout, it reports the real shortfall (503) instead
// of staying silent forever -- so callers can tell "still queued" from
// "gave up".
func (s *Server) writePendingLogs(w http.ResponseWriter, rec state.PodRecord) {
	timeout := s.pendingLogTimeout
	if timeout <= 0 {
		timeout = defaultPendingLogTimeout
	}

	if time.Since(pendingSince(rec)) < timeout {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf("pod has been pending longer than %s: %s", timeout, pendingReason(rec)),
	})
}

// pendingSince returns when a pod most recently entered its current
// uninterrupted run of Pending status, derived from its event history
// rather than a separate persisted field. The reconciler appends a
// "pending"-type event (state.StatusPending, via PodStore.UpdateStatus) or
// a "PendingWatchdog" event (via PodStore.AddEvent) on every reconcile
// tick a pod stays queued (see reconcilePending in
// internal/reconciler/reconciler.go). Walking the event history backward,
// the run's start is the oldest trailing event whose Type is one of
// those two; a pod with no events yet (freshly applied, not yet
// reconciled) counts as having just become pending.
func pendingSince(rec state.PodRecord) time.Time {
	since := time.Now()
	for i := len(rec.Events) - 1; i >= 0; i-- {
		e := rec.Events[i]
		if !isPendingEvent(e) {
			break
		}
		since = e.Time
	}
	return since
}

// pendingReason returns the most recent non-empty message recorded during
// the pod's current run of Pending status (typically the reconciler's
// "awaiting-resources: ..." shortfall text), or a generic fallback if none
// was recorded.
func pendingReason(rec state.PodRecord) string {
	for i := len(rec.Events) - 1; i >= 0; i-- {
		e := rec.Events[i]
		if !isPendingEvent(e) {
			break
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return "still awaiting resources"
}

// isPendingEvent reports whether an event was recorded while a pod stayed
// in Pending status -- either the status-change event itself or a
// PendingWatchdog progress note (see pendingSince).
func isPendingEvent(e state.PodEvent) bool {
	return e.Type == string(state.StatusPending) || e.Type == "PendingWatchdog"
}

func (s *Server) handlePodLogsFollow(w http.ResponseWriter, r *http.Request, name string, tail int) {
	rec, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	if rec.Status != state.StatusRunning {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "pod not running"})
		return
	}

	reader, err := s.executor.StreamPodLogs(r.Context(), name, tail)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer reader.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		fmt.Fprintf(w, "data: %s\n\n", scanner.Text())
		flusher.Flush()
	}
}
