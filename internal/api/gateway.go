package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

// SetWorkloadWake connects incoming demand to the single reconciliation loop.
// Configure before serving HTTP. Polling reconciliation also works without it.
func (s *Server) SetWorkloadWake(wake func()) { s.workloadWake = wake }

func (s *Server) handleWorkload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pod, release, err := s.store.AcquireRequest(name)
	if err != nil {
		code := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, state.ErrPodNotFound):
			code = http.StatusNotFound
		case errors.Is(err, state.ErrNotOnDemand):
			code = http.StatusBadRequest
		case errors.Is(err, state.ErrRequestLimit):
			code = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "1")
		}
		http.Error(w, err.Error(), code)
		return
	}
	defer release()
	if pod.Spec.OnDemand.ActivationMode() != "http" {
		http.Error(w, "workload requires its configured activation protocol", http.StatusBadRequest)
		return
	}
	if s.workloadWake != nil && (pod.Status != state.StatusRunning || !pod.Ready) {
		s.workloadWake()
	}
	// Limit only the wait for startup, never the duration of an active stream.
	waitCtx, cancel := context.WithTimeout(r.Context(), pod.Spec.OnDemand.StartupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, ok := s.store.Get(name)
		if !ok || current.Generation != pod.Generation {
			http.Error(w, "workload deleted", http.StatusNotFound)
			return
		}
		if current.Spec.OnDemand == nil {
			http.Error(w, "on-demand mode disabled", http.StatusServiceUnavailable)
			return
		}
		if current.Status == state.StatusFailed || current.Status == state.StatusCompleted {
			http.Error(w, "workload failed to become ready; inspect pod events", http.StatusServiceUnavailable)
			return
		}
		if current.Status == state.StatusRunning && current.Ready {
			target, err := url.Parse(current.Spec.OnDemand.BackendURL())
			if err != nil {
				http.Error(w, "invalid backend", http.StatusServiceUnavailable)
				return
			}
			// Strip the gateway prefix using escaped paths, preserving encoded slashes
			// and the caller's query. Rewrite drops untrusted forwarding headers.
			prefix := "/workloads/" + url.PathEscape(name)
			path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
			if path == "" {
				path = "/"
			}
			decoded, err := url.PathUnescape(path)
			if err != nil {
				http.Error(w, "invalid path", http.StatusBadRequest)
				return
			}
			proxy := &httputil.ReverseProxy{
				Transport:     s.gatewayTransport,
				FlushInterval: -1,
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.SetURL(target)
					pr.Out.URL.Path = decoded
					pr.Out.URL.RawPath = path
					// Preserve the application's query without adding gateway parameters.
					pr.Out.URL.RawQuery = r.URL.RawQuery
					pr.SetXForwarded()
					// The bearer token authorizes Spark; never expose it to a workload.
					if s.gatewayUsesToken {
						pr.Out.Header.Del("Authorization")
					}
				},
				ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
					http.Error(w, "workload connection failed", http.StatusBadGateway)
				},
			}
			proxy.ServeHTTP(w, r)
			return
		}
		select {
		case <-waitCtx.Done():
			if r.Context().Err() == nil {
				http.Error(w, "workload startup timed out", http.StatusGatewayTimeout)
			}
			return
		case <-ticker.C:
		}
	}
}
