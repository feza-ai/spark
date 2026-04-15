package api

import (
	"encoding/json"
	"io"
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    name,
			"deleted": false,
			"error":   "remove pod: " + err.Error(),
		})
		return
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
