package api

import (
	"net/http"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/metrics"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

// CronRegisterer registers parsed CronJob specs with a cron scheduler.
type CronRegisterer interface {
	Register(manifest.CronJobSpec) error
}

// PodRemover releases scheduler resources for a removed pod.
type PodRemover interface {
	RemovePod(name string)
}

// Server provides HTTP endpoints for health, resources, and pod management.
type Server struct {
	store           *state.PodStore
	tracker         *scheduler.ResourceTracker
	executor        executor.Executor
	priorityClasses map[string]int
	sqlStore        *state.SQLiteStore
	collector       *metrics.Collector
	cronSched       CronRegisterer
	gpuInfo         *gpu.GPUInfo
	sysInfo         *gpu.SystemInfo
	mux             *http.ServeMux
	handler         http.Handler
	scheduler       PodRemover
}

// NewServer creates a Server and registers all HTTP routes.
// If token is non-empty, all routes (except /healthz and /metrics) require
// Bearer token authentication.
func NewServer(store *state.PodStore, tracker *scheduler.ResourceTracker, exec executor.Executor, priorityClasses map[string]int, sqlStore *state.SQLiteStore, collector *metrics.Collector, cronSched CronRegisterer, token string, sched PodRemover, gpuInfo *gpu.GPUInfo, sysInfo *gpu.SystemInfo) *Server {
	s := &Server{
		store:           store,
		tracker:         tracker,
		executor:        exec,
		priorityClasses: priorityClasses,
		sqlStore:        sqlStore,
		collector:       collector,
		cronSched:       cronSched,
		gpuInfo:         gpuInfo,
		sysInfo:         sysInfo,
		mux:             http.NewServeMux(),
		scheduler:       sched,
	}
	s.registerHealthRoutes()
	s.registerResourceRoutes()
	s.registerNodeRoutes()
	s.registerPodQueryRoutes()
	s.registerPodMutateRoutes()
	s.registerPodLogRoutes()
	s.registerImageRoutes()
	s.mux.HandleFunc("GET /api/v1/pods/{name}/events", s.handlePodEvents)
	s.mux.HandleFunc("POST /api/v1/pods/{name}/exec", s.handlePodExec)
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
