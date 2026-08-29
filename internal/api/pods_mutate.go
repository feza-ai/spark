package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

func isNoSuchPod(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such pod")
}

// isCgroupCleanupRace reports whether err is podman's benign
// "cgroup: Unit machine-libpod_pod_<id>.slice not loaded" error from
// `podman pod rm` (issue #71). It surfaces when the pod's containers were
// already torn down (e.g. by a preceding stop) and systemd/crun reaped the
// pod's cgroup slice before rm got to it -- podman reports the absence of
// the thing it's cleaning up as a hard error even though the desired end
// state (pod gone) is already true.
func isCgroupCleanupRace(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cgroup") && strings.Contains(msg, "not loaded")
}

// isPodAlreadyGone reports whether err indicates the pod is already absent
// from podman state -- either explicitly ("no such pod") or via the
// cgroup-cleanup race in isCgroupCleanupRace (issue #71). Callers classify
// both the same way: proceed with cleanup instead of aborting.
func isPodAlreadyGone(err error) bool {
	return isNoSuchPod(err) || isCgroupCleanupRace(err)
}

// podConfirmedGone reports whether a pod is confirmed absent from podman,
// used to distinguish a spurious RemovePod error (the pod was actually
// removed despite the error) from a genuine failure that left it alive.
// Only isPodAlreadyGone from a fresh status check counts as confirmation;
// any other outcome -- the pod still reports a status, or the status
// check itself errors some other way -- is treated conservatively as
// "still there", preserving the existing safe (no release) behavior.
func (s *Server) podConfirmedGone(ctx context.Context, name string) bool {
	_, err := s.executor.PodStatus(ctx, name)
	return err != nil && isPodAlreadyGone(err)
}

// removePodMaxAttempts bounds the retries around executor.RemovePod when it
// hits the podman cgroup-cleanup race (issue #71): "podman pod rm" can
// report the slice-not-loaded error even though the pod is already torn
// down, and an immediate retry commonly succeeds outright once the race
// window has passed.
const removePodMaxAttempts = 3

// removePodRetryDelay is the pause between retries in removePodWithRetry.
var removePodRetryDelay = 20 * time.Millisecond

// removePodWithRetry calls executor.RemovePod, retrying up to
// removePodMaxAttempts times when the failure is the cgroup-cleanup race
// (isCgroupCleanupRace) rather than giving up on the first attempt. Any
// other error, a "no such pod" result, or success returns immediately:
// retrying either wastes time (no such pod never becomes "found") or has
// already achieved the state the caller wants.
func (s *Server) removePodWithRetry(ctx context.Context, name string) error {
	var err error
	for attempt := 1; attempt <= removePodMaxAttempts; attempt++ {
		err = s.executor.RemovePod(ctx, name)
		if err == nil || !isCgroupCleanupRace(err) {
			return err
		}
		if attempt < removePodMaxAttempts {
			time.Sleep(removePodRetryDelay)
		}
	}
	return err
}

func (s *Server) registerPodMutateRoutes() {
	s.mux.HandleFunc("POST /api/v1/pods", s.handleApplyPod)
	s.mux.HandleFunc("DELETE /api/v1/pods/{name}", s.handleDeletePod)
}

func (s *Server) handleApplyPod(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "request body too large"})
		return
	}

	result, err := manifest.Parse(body, s.priorityClasses)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Up-front structural guard: reject pods whose total CPU request exceeds
	// the host's allocatable cores. Distinct from the "queue for later"
	// admission path that handles transient unavailability.
	if s.tracker != nil {
		if cores := s.tracker.Allocatable().Cores; len(cores) > 0 {
			for _, pod := range result.Pods {
				totalCPU := pod.TotalRequests().CPUMillis
				if totalCPU >= 1000 && totalCPU%1000 == 0 {
					n := totalCPU / 1000
					if n > len(cores) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						json.NewEncoder(w).Encode(map[string]string{
							"error": fmt.Sprintf("limits.cpu %d exceeds allocatable cores %d", n, len(cores)),
						})
						return
					}
				}
			}
		}
	}

	type podStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var pods []podStatus

	for _, pod := range result.Pods {
		s.store.Apply(pod)
		s.store.SetRawManifest(pod.Name, body)
		if s.sqlStore != nil {
			if rec, ok := s.store.Get(pod.Name); ok {
				s.sqlStore.SavePod(&rec)
			}
		}
		pods = append(pods, podStatus{Name: pod.Name, Status: "pending"})
	}

	if s.cronSched != nil {
		for _, cj := range result.CronJobs {
			if err := s.cronSched.Register(cj); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "cronjob registration failed: " + err.Error()})
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{"pods": pods})
}

func (s *Server) handleDeletePod(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	_, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    name,
			"deleted": false,
			"error":   "pod not found",
		})
		return
	}

	if err := s.executor.StopPod(r.Context(), name, 10); err != nil && !isPodAlreadyGone(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    name,
			"deleted": false,
			"error":   "stop pod: " + err.Error(),
		})
		return
	}

	if err := s.removePodWithRetry(r.Context(), name); err != nil && !isPodAlreadyGone(err) {
		// podman occasionally reports a non-fatal error (e.g. a network
		// cleanup warning, or -- once retries above are exhausted -- the
		// cgroup-cleanup race) after it has already removed the pod, whose
		// message doesn't match isPodAlreadyGone so it isn't caught above.
		// Trusting the error at face value here left the store record and
		// the scheduler's resource reservation (including any GPU device
		// slot) intact for a pod that no longer existed, leaking that
		// reservation forever since nothing ever revisits a deleted-but-
		// not-really pod again (issue #81). Confirm against actual state
		// before deciding: only keep the record and reservation when the
		// pod is confirmed to still exist.
		if !s.podConfirmedGone(r.Context(), name) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":    name,
				"deleted": false,
				"error":   "remove pod: " + err.Error(),
			})
			return
		}
		slog.Warn("podman pod rm reported an error but the pod is confirmed gone; releasing reservation anyway",
			"pod", name, "err", err)
	}

	if s.scheduler != nil {
		s.scheduler.RemovePod(name)
	}

	s.store.Delete(name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"name":    name,
		"deleted": true,
	})
}
