package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/feza-ai/spark/internal/manifest"
)

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

	// Best-effort stop and remove via executor.
	s.executor.StopPod(r.Context(), name, 10)
	s.executor.RemovePod(r.Context(), name)

	s.store.Delete(name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"name":    name,
		"deleted": true,
	})
}
