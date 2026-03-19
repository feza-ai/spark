package api

import (
	"net/http"

	"github.com/feza-ai/spark/internal/executor"
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
	mux             *http.ServeMux
}

// NewServer creates a Server and registers all HTTP routes.
func NewServer(store *state.PodStore, tracker *scheduler.ResourceTracker, exec executor.Executor, priorityClasses map[string]int, sqlStore *state.SQLiteStore) *Server {
	s := &Server{
		store:           store,
		tracker:         tracker,
		executor:        exec,
		priorityClasses: priorityClasses,
		sqlStore:        sqlStore,
		mux:             http.NewServeMux(),
	}
	s.registerHealthRoutes()
	s.registerResourceRoutes()
	s.registerPodQueryRoutes()
	s.registerPodMutateRoutes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
