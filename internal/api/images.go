package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) registerImageRoutes() {
	s.mux.HandleFunc("GET /api/v1/images", s.handleListImages)
	s.mux.HandleFunc("POST /api/v1/images/pull", s.handlePullImage)
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.executor.ListImages(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	type imageEntry struct {
		ID      string   `json:"id"`
		Names   []string `json:"names"`
		Size    string   `json:"size"`
		Created string   `json:"created"`
	}

	entries := make([]imageEntry, len(images))
	for i, img := range images {
		entries[i] = imageEntry{
			ID:      img.ID,
			Names:   img.Names,
			Size:    img.Size,
			Created: img.Created,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"images": entries})
}

func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	if body.Image == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "image name is required"})
		return
	}

	if err := s.executor.PullImage(r.Context(), body.Image); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "pull failed: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"pulled": body.Image})
}
