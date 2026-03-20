package api

import (
	"encoding/json"
	"net/http"

	"github.com/feza-ai/spark/internal/state"
)

type execRequest struct {
	Command   []string `json:"command"`
	Container string   `json:"container"`
}

type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (s *Server) handlePodExec(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	if len(req.Command) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "command is required"})
		return
	}

	rec, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "pod not found"})
		return
	}

	if rec.Status != state.StatusRunning {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "pod not running"})
		return
	}

	stdout, stderr, exitCode, err := s.executor.ExecPod(r.Context(), name, req.Container, req.Command)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(execResponse{
		Stdout:   string(stdout),
		Stderr:   string(stderr),
		ExitCode: exitCode,
	})
}
