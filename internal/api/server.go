package api

import (
	"net/http"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/metrics"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// Server provides HTTP endpoints for health, resources, and pod management.
type Server struct {
	store           *state.PodStore
	tracker         *scheduler.ResourceTracker
	executor        executor.Executor
	priorityClasses map[string]int
	sqlStore        *state.SQLiteStore
	collector       *metrics.Collector
	mux             *http.ServeMux
	handler         http.Handler
}

// NewServer creates a Server and registers all HTTP routes.
// If token is non-empty, all routes (except /healthz and /metrics) require
// Bearer token authentication.
func NewServer(store *state.PodStore, tracker *scheduler.ResourceTracker, exec executor.Executor, priorityClasses map[string]int, sqlStore *state.SQLiteStore, collector *metrics.Collector, token string) *Server {
	s := &Server{
		store:           store,
		tracker:         tracker,
		executor:        exec,
		priorityClasses: priorityClasses,
		sqlStore:        sqlStore,
		collector:       collector,
		mux:             http.NewServeMux(),
	}
	s.registerHealthRoutes()
	s.registerResourceRoutes()
	s.registerPodQueryRoutes()
	s.registerPodMutateRoutes()
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)

	if token != "" {
		s.handler = NewAuthMiddleware(token).Wrap(s.mux)
	} else {
		s.handler = s.mux
	}
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
