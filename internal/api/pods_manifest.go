package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) registerPodManifestRoutes() {
	s.mux.HandleFunc("GET /api/v1/pods/{name}/manifest", s.handleGetPodManifest)
}

// handleGetPodManifest implements GET /api/v1/pods/{name}/manifest (issue
// #80, quick win 1): after a state-divergence incident (a pod reports
// pending while its container is actually alive), GET /api/v1/pods/{name}
// only returns a status summary, leaving an operator with no way to
// recreate the pod faithfully. This returns the manifest bytes exactly as
// they were submitted -- via POST /api/v1/pods, the req.spark.apply NATS
// handler, or the manifest directory watcher -- so it round-trips through
// `curl .../manifest | curl -d @- .../pods` regardless of whether the
// original request was YAML or JSON.
func (s *Server) handleGetPodManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rec, ok := s.store.Get(name)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("pod not found: %s", name)})
		return
	}

	// No status filter: this must work for a pod stuck in any state,
	// including a divergent/pending one -- that's the whole point of the
	// issue. An empty RawManifest (a pod loaded from a pre-migration store,
	// or applied through a path that predates this field) is not an error,
	// just an empty body.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(rec.RawManifest)
}
