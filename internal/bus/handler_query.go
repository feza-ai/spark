package bus

import (
	"encoding/json"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

// GetRequest is the payload for req.spark.get.
type GetRequest struct {
	Name string `json:"name"`
}

// GetResponse is the full detail response for a single pod.
type GetResponse struct {
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Priority   int         `json:"priority"`
	StartedAt  string      `json:"startedAt,omitempty"`
	FinishedAt string      `json:"finishedAt,omitempty"`
	Restarts   int         `json:"restarts"`
	Events     []EventInfo `json:"events"`
	Error      string      `json:"error,omitempty"`
}

// EventInfo describes a single pod lifecycle event.
type EventInfo struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ListRequest is the payload for req.spark.list.
type ListRequest struct {
	Status string `json:"status,omitempty"`
}

// ListResponse contains a list of pod summaries.
type ListResponse struct {
	Pods []PodSummary `json:"pods"`
}

// PodSummary is a brief view of a pod for list results.
type PodSummary struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

// RegisterGetHandler registers the req.spark.get handler.
func RegisterGetHandler(b Bus, store *state.PodStore) {
	b.HandleRequest("req.spark.get", func(_ string, data []byte) ([]byte, error) {
		var req GetRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return json.Marshal(GetResponse{Error: "invalid request: " + err.Error()})
		}

		rec, ok := store.Get(req.Name)
		if !ok {
			return json.Marshal(GetResponse{Error: "pod not found: " + req.Name})
		}

		resp := GetResponse{
			Name:     rec.Spec.Name,
			Status:   string(rec.Status),
			Priority: rec.Spec.Priority,
			Restarts: rec.Restarts,
		}

		if !rec.StartedAt.IsZero() {
			resp.StartedAt = rec.StartedAt.Format(time.RFC3339)
		}
		if !rec.FinishedAt.IsZero() {
			resp.FinishedAt = rec.FinishedAt.Format(time.RFC3339)
		}

		resp.Events = make([]EventInfo, len(rec.Events))
		for i, e := range rec.Events {
			resp.Events[i] = EventInfo{
				Time:    e.Time.Format(time.RFC3339),
				Type:    e.Type,
				Message: e.Message,
			}
		}

		return json.Marshal(resp)
	})
}

// RegisterListHandler registers the req.spark.list handler.
func RegisterListHandler(b Bus, store *state.PodStore) {
	b.HandleRequest("req.spark.list", func(_ string, data []byte) ([]byte, error) {
		var req ListRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return json.Marshal(ListResponse{})
		}

		records := store.List(state.PodStatus(req.Status))

		resp := ListResponse{
			Pods: make([]PodSummary, len(records)),
		}
		for i, rec := range records {
			resp.Pods[i] = PodSummary{
				Name:     rec.Spec.Name,
				Status:   string(rec.Status),
				Priority: rec.Spec.Priority,
			}
		}

		return json.Marshal(resp)
	})
}
