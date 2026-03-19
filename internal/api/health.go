package api

import "net/http"

func (s *Server) registerHealthRoutes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}
