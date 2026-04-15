package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

func (s *Server) registerPodQueryRoutes() {
	s.mux.HandleFunc("GET /api/v1/pods", s.handleListPods)
	s.mux.HandleFunc("GET /api/v1/pods/{name}", s.handleGetPod)
}

type podSummary struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

type listPodsResponse struct {
	Pods []podSummary `json:"pods"`
}

type podEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

type containerPortResponse struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol"`
}

type containerResponse struct {
	Name  string                  `json:"name"`
	Image string                  `json:"image"`
	Ports []containerPortResponse `json:"ports,omitempty"`
}

type getPodResponse struct {
	Name          string              `json:"name"`
	Status        string              `json:"status"`
	Priority      int                 `json:"priority"`
	StartedAt     time.Time           `json:"startedAt"`
	FinishedAt    time.Time           `json:"finishedAt"`
	Restarts      int                 `json:"restarts"`
	StartAttempts int                 `json:"startAttempts,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	Containers    []containerResponse `json:"containers,omitempty"`
	Events        []podEvent          `json:"events"`
}

func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	records := s.store.List(state.PodStatus(status))

	pods := make([]podSummary, 0, len(records))
	for _, rec := range records {
		pods = append(pods, podSummary{
			Name:     rec.Spec.Name,
			Status:   string(rec.Status),
			Priority: rec.Spec.Priority,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listPodsResponse{Pods: pods})
}

func (s *Server) handleGetPod(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rec, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	events := make([]podEvent, 0, len(rec.Events))
	for _, e := range rec.Events {
		events = append(events, podEvent{
			Time:    e.Time,
			Type:    e.Type,
			Message: e.Message,
		})
	}

	containers := make([]containerResponse, 0, len(rec.Spec.Containers))
	for _, c := range rec.Spec.Containers {
		cr := containerResponse{
			Name:  c.Name,
			Image: c.Image,
		}
		for _, p := range c.Ports {
			cr.Ports = append(cr.Ports, containerPortResponse{
				ContainerPort: p.ContainerPort,
				HostPort:      p.HostPort,
				Protocol:      p.Protocol,
			})
		}
		containers = append(containers, cr)
	}

	resp := getPodResponse{
		Name:          rec.Spec.Name,
		Status:        string(rec.Status),
		Priority:      rec.Spec.Priority,
		StartedAt:     rec.StartedAt,
		FinishedAt:    rec.FinishedAt,
		Restarts:      rec.Restarts,
		StartAttempts: rec.StartAttempts,
		Reason:        rec.Reason,
		Containers:    containers,
		Events:        events,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
