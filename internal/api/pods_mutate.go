package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/feza-ai/spark/internal/manifest"
)

func isNoSuchPod(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such pod")
}

// podConfirmedGone reports whether a pod is confirmed absent from podman,
// used to distinguish a spurious RemovePod error (the pod was actually
// removed despite the error) from a genuine failure that left it alive.
// Only "no such pod" from a fresh status check counts as confirmation;
// any other outcome -- the pod still reports a status, or the status
// check itself errors some other way -- is treated conservatively as
// "still there", preserving the existing safe (no release) behavior.
func (s *Server) podConfirmedGone(ctx context.Context, name string) bool {
	_, err := s.executor.PodStatus(ctx, name)
	return err != nil && isNoSuchPod(err)
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

	if err := s.executor.StopPod(r.Context(), name, 10); err != nil && !isNoSuchPod(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    name,
			"deleted": false,
			"error":   "stop pod: " + err.Error(),
		})
		return
	}

	if err := s.executor.RemovePod(r.Context(), name); err != nil && !isNoSuchPod(err) {
		// podman occasionally reports a non-fatal error (e.g. a network or
		// cgroup cleanup warning) after it has already removed the pod --
		// the message doesn't match "no such pod" so it isn't caught above.
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
