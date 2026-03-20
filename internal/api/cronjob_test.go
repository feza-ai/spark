package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
			srv := NewServer(store, tracker, exec, nil, nil, nil, cron, "")

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
