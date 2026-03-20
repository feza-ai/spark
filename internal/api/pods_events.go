package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type podEventDetail struct {
	PodName string    `json:"pod_name"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

type podEventsResponse struct {
	Events []podEventDetail `json:"events"`
}

func (s *Server) handlePodEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if _, ok := s.store.Get(name); !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	var since time.Time
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		var err error
		since, err = time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("invalid since parameter: %s", err.Error())})
			return
		}
	}

	events, err := s.sqlStore.ListEvents(name, since)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	resp := podEventsResponse{
		Events: make([]podEventDetail, 0, len(events)),
	}
	for _, e := range events {
		resp.Events = append(resp.Events, podEventDetail{
			PodName: name,
			Time:    e.Time,
			Type:    e.Type,
			Message: e.Message,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
