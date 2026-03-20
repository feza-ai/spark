package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type imageStubExecutor struct {
	stubExecutor
	images   []executor.ImageInfo
	listErr  error
	pullErr  error
	pulled   string
}

func (e *imageStubExecutor) ListImages(_ context.Context) ([]executor.ImageInfo, error) {
	return e.images, e.listErr
}

func (e *imageStubExecutor) PullImage(_ context.Context, name string) error {
	e.pulled = name
	return e.pullErr
}

func newImageTestServer(t *testing.T, exec *imageStubExecutor) *Server {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
	return NewServer(store, tracker, exec, nil, nil, nil, nil, "")
}

func TestListImages(t *testing.T) {
	exec := &imageStubExecutor{
		images: []executor.ImageInfo{
			{ID: "abc123", Names: []string{"alpine:latest"}, Size: "5.0 MB", Created: "1710000000"},
			{ID: "def456", Names: []string{"nginx:1.25"}, Size: "50.0 MB", Created: "1710100000"},
		},
	}
	srv := newImageTestServer(t, exec)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var resp struct {
		Images []struct {
			ID      string   `json:"id"`
			Names   []string `json:"names"`
			Size    string   `json:"size"`
			Created string   `json:"created"`
		} `json:"images"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(resp.Images))
	}
	if resp.Images[0].ID != "abc123" {
		t.Errorf("expected id abc123, got %s", resp.Images[0].ID)
	}
	if resp.Images[1].Names[0] != "nginx:1.25" {
		t.Errorf("expected name nginx:1.25, got %s", resp.Images[1].Names[0])
	}
}

func TestPullImage_Success(t *testing.T) {
	exec := &imageStubExecutor{}
	srv := newImageTestServer(t, exec)

	body := `{"image":"alpine:latest"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/pull", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["pulled"] != "alpine:latest" {
		t.Errorf("expected pulled alpine:latest, got %s", resp["pulled"])
	}
	if exec.pulled != "alpine:latest" {
		t.Errorf("expected executor to receive alpine:latest, got %s", exec.pulled)
	}
}

func TestPullImage_EmptyName(t *testing.T) {
	exec := &imageStubExecutor{}
	srv := newImageTestServer(t, exec)

	body := `{"image":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/pull", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "image name is required" {
		t.Errorf("expected 'image name is required', got %s", resp["error"])
	}
}

func TestPullImage_Failure(t *testing.T) {
	exec := &imageStubExecutor{
		pullErr: fmt.Errorf("connection refused"),
	}
	srv := newImageTestServer(t, exec)

	body := `{"image":"alpine:latest"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/pull", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp["error"], "pull failed:") {
		t.Errorf("expected error to contain 'pull failed:', got %s", resp["error"])
	}
}
