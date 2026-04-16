package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/cron"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

type mockCronRegisterer struct {
	mu        sync.Mutex
	calls     []manifest.CronJobSpec
	returnErr error
}

func (m *mockCronRegisterer) Register(spec manifest.CronJobSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spec)
	return m.returnErr
}

const testCronJobYAML = `apiVersion: batch/v1
kind: CronJob
metadata:
  name: hello-cron
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: hello
            image: busybox:latest
            command: ["echo", "hello"]
`

func TestApplyCronJob(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		cronSched     *mockCronRegisterer
		wantCode      int
		wantRegisters int
		wantSchedule  string
	}{
		{
			name:          "cronjob registered",
			body:          testCronJobYAML,
			cronSched:     &mockCronRegisterer{},
			wantCode:      http.StatusCreated,
			wantRegisters: 1,
			wantSchedule:  "*/5 * * * *",
		},
		{
			name:          "cronjob with nil scheduler",
			body:          testCronJobYAML,
			cronSched:     nil,
			wantCode:      http.StatusCreated,
			wantRegisters: 0,
		},
		{
			name:          "register error returns 500",
			body:          testCronJobYAML,
			cronSched:     &mockCronRegisterer{returnErr: errors.New("schedule conflict")},
			wantCode:      http.StatusInternalServerError,
			wantRegisters: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
				scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
				nil, 0,
			)
			exec := &stubExecutor{}

			var cron CronRegisterer
			if tt.cronSched != nil {
				cron = tt.cronSched
			}
			srv := NewServer(store, tracker, exec, nil, nil, nil, cron, "", nil, nil, nil, "test")

			req := httptest.NewRequest(http.MethodPost, "/api/v1/pods", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}

			if tt.cronSched != nil {
				tt.cronSched.mu.Lock()
				got := len(tt.cronSched.calls)
				tt.cronSched.mu.Unlock()
				if got != tt.wantRegisters {
					t.Fatalf("expected %d Register calls, got %d", tt.wantRegisters, got)
				}
				if tt.wantSchedule != "" && got > 0 {
					tt.cronSched.mu.Lock()
					schedule := tt.cronSched.calls[0].Schedule
					tt.cronSched.mu.Unlock()
					if schedule != tt.wantSchedule {
						t.Errorf("expected schedule %q, got %q", tt.wantSchedule, schedule)
					}
				}
			}

			if tt.wantCode == http.StatusInternalServerError {
				var body struct {
					Error string `json:"error"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				if body.Error == "" {
					t.Error("expected non-empty error message")
				}
			}
		})
	}
}

// mockCronManager implements CronManager for testing the HTTP endpoints.
type mockCronManager struct {
	mu           sync.Mutex
	calls        []manifest.CronJobSpec
	returnErr    error
	jobs         []cron.CronJobStatus
	unregistered []string
}

func (m *mockCronManager) Register(spec manifest.CronJobSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spec)
	return m.returnErr
}

func (m *mockCronManager) List() []cron.CronJobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs
}

func (m *mockCronManager) Get(name string) (cron.CronJobStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Name == name {
			return j, true
		}
	}
	return cron.CronJobStatus{}, false
}

func (m *mockCronManager) Unregister(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregistered = append(m.unregistered, name)
}

func newCronTestServer(t *testing.T, mgr CronManager) *Server {
	t.Helper()
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
		nil, 0,
	)
	var cronSched CronRegisterer
	if mgr != nil {
		cronSched = mgr
	}
	return NewServer(store, tracker, nil, nil, nil, nil, cronSched, "", nil, nil, nil, "test")
}

func TestListCronJobs(t *testing.T) {
	now := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	next := time.Date(2026, 3, 20, 10, 5, 0, 0, time.UTC)

	tests := []struct {
		name     string
		mgr      CronManager
		wantCode int
		wantLen  int
	}{
		{
			name:     "nil cron scheduler returns empty array",
			mgr:      nil,
			wantCode: http.StatusOK,
			wantLen:  0,
		},
		{
			name: "returns registered jobs",
			mgr: &mockCronManager{
				jobs: []cron.CronJobStatus{
					{Name: "alpha", Schedule: "*/5 * * * *", LastRun: now, NextRun: next, RunCount: 3},
					{Name: "beta", Schedule: "0 * * * *", NextRun: next, RunCount: 0},
				},
			},
			wantCode: http.StatusOK,
			wantLen:  2,
		},
		{
			name:     "empty list",
			mgr:      &mockCronManager{},
			wantCode: http.StatusOK,
			wantLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCronTestServer(t, tt.mgr)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/cronjobs", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}

			var jobs []cronJobResponse
			if err := json.NewDecoder(rec.Body).Decode(&jobs); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if len(jobs) != tt.wantLen {
				t.Fatalf("expected %d jobs, got %d", tt.wantLen, len(jobs))
			}
		})
	}
}

func TestGetCronJob(t *testing.T) {
	now := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	next := time.Date(2026, 3, 20, 10, 5, 0, 0, time.UTC)

	mgr := &mockCronManager{
		jobs: []cron.CronJobStatus{
			{Name: "myjob", Schedule: "*/5 * * * *", LastRun: now, NextRun: next, RunCount: 5},
		},
	}

	tests := []struct {
		name     string
		jobName  string
		wantCode int
	}{
		{
			name:     "found",
			jobName:  "myjob",
			wantCode: http.StatusOK,
		},
		{
			name:     "not found",
			jobName:  "missing",
			wantCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCronTestServer(t, mgr)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/cronjobs/"+tt.jobName, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var job cronJobResponse
				if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				if job.Name != tt.jobName {
					t.Fatalf("expected name %q, got %q", tt.jobName, job.Name)
				}
				if job.Schedule != "*/5 * * * *" {
					t.Fatalf("expected schedule */5 * * * *, got %s", job.Schedule)
				}
				if job.RunCount != 5 {
					t.Fatalf("expected RunCount 5, got %d", job.RunCount)
				}
			}
		})
	}
}

func TestDeleteCronJob(t *testing.T) {
	mgr := &mockCronManager{
		jobs: []cron.CronJobStatus{
			{Name: "deljob", Schedule: "* * * * *"},
		},
	}
	srv := newCronTestServer(t, mgr)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/cronjobs/deljob", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.unregistered) != 1 || mgr.unregistered[0] != "deljob" {
		t.Fatalf("expected Unregister called with deljob, got %v", mgr.unregistered)
	}
}

func TestDeleteCronJobNilManager(t *testing.T) {
	srv := newCronTestServer(t, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/cronjobs/anything", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestGetCronJobNilManager(t *testing.T) {
	srv := newCronTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cronjobs/anything", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}
