package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/feza-ai/spark/internal/state"
)

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

	_, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	data, err := s.executor.PodLogs(r.Context(), name, tail)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
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
