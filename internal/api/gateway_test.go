package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func gatewayPod(backend string) manifest.PodSpec {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(backend, "http://"))
	n, _ := strconv.Atoi(port)
	return manifest.PodSpec{Name: "model", OnDemand: &manifest.OnDemandSpec{HostPort: n, ReadinessPath: "/ready", IdleTimeout: time.Second, StartupTimeout: time.Second, MaxRequests: 64}}
}

func TestGatewayWaitsForReadyAndPreservesRequest(t *testing.T) {
	received := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- r.Method + " " + r.URL.RequestURI() + " " + string(b)
		w.WriteHeader(201)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()
	s := newTestServer(t)
	s.store.Apply(gatewayPod(backend.URL))
	var once sync.Once
	wake := make(chan struct{})
	s.SetWorkloadWake(func() { once.Do(func() { close(wake) }) })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/workloads/model/v1/a%2Fb?q=hello%20world", strings.NewReader("prompt")))
		done <- w
	}()
	<-wake
	select {
	case <-received:
		t.Fatal("forwarded before ready")
	default:
	}
	s.store.UpdateStatus("model", state.StatusRunning, "started")
	select {
	case <-received:
		t.Fatal("running was treated as ready")
	default:
	}
	s.store.SetReady("model", true)
	select {
	case w := <-done:
		if w.Code != 201 || w.Body.String() != "ok" {
			t.Fatal(w)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request hung")
	}
	if got := <-received; got != "POST /v1/a%2Fb?q=hello%20world prompt" {
		t.Fatal(got)
	}
	p, _ := s.store.Get("model")
	if p.InFlight != 0 {
		t.Fatal("request lease leaked")
	}
}

func TestGatewayStreamingPinsWorkload(t *testing.T) {
	finish := make(chan struct{})
	defer close(finish)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer backend.Close()
	s := newTestServer(t)
	s.store.Apply(gatewayPod(backend.URL))
	s.store.UpdateStatus("model", state.StatusRunning, "started")
	s.store.SetReady("model", true)
	front := httptest.NewServer(s)
	defer front.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", front.URL+"/workloads/model/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, b); err != nil {
		t.Fatal(err)
	}
	if s.store.TryIdleStop("model", time.Now().Add(time.Hour)) {
		t.Fatal("evicted live stream")
	}
	cancel()
}

func TestGatewayTimeoutCancellationAndLimit(t *testing.T) {
	s := newTestServer(t)
	p := gatewayPod("http://127.0.0.1:1")
	p.OnDemand.StartupTimeout = 20 * time.Millisecond
	p.OnDemand.MaxRequests = 1
	s.store.Apply(p)
	_, release, _ := s.store.AcquireRequest("model")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/workloads/model/", nil))
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	release()
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/workloads/model/", nil))
	if w.Code != 504 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/workloads/model/", nil).WithContext(ctx))
	rec, _ := s.store.Get("model")
	if rec.InFlight != 0 {
		t.Fatal("cancelled request leaked")
	}
}

func TestGatewayAuthenticationDoesNotLeakToken(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Spark token leaked")
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	store := state.NewPodStore()
	store.Apply(gatewayPod(backend.URL))
	store.UpdateStatus("model", state.StatusRunning, "started")
	store.SetReady("model", true)
	s := NewServer(store, nil, nil, nil, nil, nil, nil, "secret", nil, nil, nil, "test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/workloads/model/", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	req := httptest.NewRequest("GET", "/workloads/model/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
