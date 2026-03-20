package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/feza-ai/spark/internal/cron"
)

// CronManager extends CronRegisterer with query and lifecycle methods.
type CronManager interface {
	CronRegisterer
	List() []cron.CronJobStatus
	Get(name string) (cron.CronJobStatus, bool)
	Unregister(name string)
}

type cronJobResponse struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"`
	LastRun  time.Time `json:"lastRun,omitempty"`
	NextRun  time.Time `json:"nextRun"`
	RunCount int       `json:"runCount"`
}

func cronJobFromStatus(s cron.CronJobStatus) cronJobResponse {
	return cronJobResponse{
		Name:     s.Name,
		Schedule: s.Schedule,
		LastRun:  s.LastRun,
		NextRun:  s.NextRun,
		RunCount: s.RunCount,
	}
}

func (s *Server) registerCronJobRoutes() {
	s.mux.HandleFunc("GET /api/v1/cronjobs", s.handleListCronJobs)
	s.mux.HandleFunc("GET /api/v1/cronjobs/{name}", s.handleGetCronJob)
	s.mux.HandleFunc("DELETE /api/v1/cronjobs/{name}", s.handleDeleteCronJob)
}

func (s *Server) handleListCronJobs(w http.ResponseWriter, r *http.Request) {
	mgr, ok := s.cronSched.(CronManager)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]cronJobResponse{})
		return
	}

	statuses := mgr.List()
	resp := make([]cronJobResponse, len(statuses))
	for i, st := range statuses {
		resp[i] = cronJobFromStatus(st)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetCronJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	mgr, ok := s.cronSched.(CronManager)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "cronjob not found"})
		return
	}

	status, found := mgr.Get(name)
	if !found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "cronjob not found"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cronJobFromStatus(status))
}

func (s *Server) handleDeleteCronJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	mgr, ok := s.cronSched.(CronManager)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "cron manager not available"})
		return
	}

	mgr.Unregister(name)
	w.WriteHeader(http.StatusNoContent)
}
