package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// stubExecutor implements executor.Executor for testing.
type stubExecutor struct {
	stopErr   error
	removeErr error
	stopped   []string
	removed   []string
}

func (e *stubExecutor) CreatePod(_ context.Context, _ manifest.PodSpec) error {
	return nil
}

func (e *stubExecutor) StopPod(_ context.Context, name string, _ int) error {
	e.stopped = append(e.stopped, name)
	return e.stopErr
}

func (e *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return executor.Status{}, nil
}

func (e *stubExecutor) RemovePod(_ context.Context, name string) error {
	e.removed = append(e.removed, name)
	return e.removeErr
}

func (e *stubExecutor) ListPods(_ context.Context) ([]executor.PodListEntry, error) {
	return nil, nil
}

func (e *stubExecutor) PodStats(_ context.Context, _ string) (executor.PodResourceUsage, error) {
	return executor.PodResourceUsage{}, nil
}

func (e *stubExecutor) PodLogs(_ context.Context, _ string, _ int) ([]byte, error) {
	return nil, nil
}

func (e *stubExecutor) StreamPodLogs(_ context.Context, _ string, _ int) (io.ReadCloser, error) {
	return nil, nil
}

func (e *stubExecutor) ExecPod(_ context.Context, _ string, _ string, _ []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}

func (e *stubExecutor) ListImages(_ context.Context) ([]executor.ImageInfo, error) {
	return nil, nil
}

func (e *stubExecutor) PullImage(_ context.Context, _ string) error {
	return nil
}

func (e *stubExecutor) ExecProbe(_ context.Context, _ string, _ string, _ []string, _ time.Duration) (int, error) {
	return 0, nil
}

func (e *stubExecutor) HTTPProbe(_ context.Context, _ int, _ string, _ time.Duration) error {
	return nil
}

// stubPodRemover implements PodRemover for testing.
type stubPodRemover struct {
	removed []string
}

func (s *stubPodRemover) RemovePod(name string) {
	s.removed = append(s.removed, name)
}

func TestDeleteHandler(t *testing.T) {
	tests := []struct {
		name            string
		podName         string
		podExists       bool
		stopErr         error
		removeErr       error
		scheduler       PodRemover
		wantDeleted     bool
		wantError       bool
		wantSchedulerRM []string
	}{
		{
			name:            "existing pod with scheduler",
			podName:         "test-pod",
			podExists:       true,
			scheduler:       &stubPodRemover{},
			wantDeleted:     true,
			wantSchedulerRM: []string{"test-pod"},
		},
		{
			name:        "existing pod nil scheduler",
			podName:     "test-pod",
			podExists:   true,
			scheduler:   nil,
			wantDeleted: true,
		},
		{
			name:      "non-existent pod",
			podName:   "missing-pod",
			podExists: false,
			scheduler: &stubPodRemover{},
			wantError: true,
		},
		{
			name:      "stop error",
			podName:   "fail-pod",
			podExists: true,
			stopErr:   fmt.Errorf("stop failed"),
			scheduler: &stubPodRemover{},
			wantError: true,
		},
		{
			name:      "remove error",
			podName:   "fail-pod",
			podExists: true,
			removeErr: fmt.Errorf("remove failed"),
			scheduler: &stubPodRemover{},
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewStubBus()
			store := state.NewPodStore()
			exec := &stubExecutor{stopErr: tc.stopErr, removeErr: tc.removeErr}

			if tc.podExists {
				store.Apply(manifest.PodSpec{
					Name:                          tc.podName,
					TerminationGracePeriodSeconds: 10,
				})
			}

			RegisterDeleteHandler(b, store, exec, tc.scheduler)

			reqData, _ := json.Marshal(DeleteRequest{Name: tc.podName})
			resp, err := b.Request(context.Background(), "req.spark.delete", reqData)
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}

			var dr DeleteResponse
			if err := json.Unmarshal(resp, &dr); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}

			if dr.Deleted != tc.wantDeleted {
				t.Errorf("Deleted = %v, want %v", dr.Deleted, tc.wantDeleted)
			}
			if tc.wantError && dr.Error == "" {
				t.Error("expected error message, got empty")
			}
			if !tc.wantError && dr.Error != "" {
				t.Errorf("unexpected error: %q", dr.Error)
			}

			// Check scheduler was called on successful delete.
			if sr, ok := tc.scheduler.(*stubPodRemover); ok {
				if len(tc.wantSchedulerRM) == 0 && len(sr.removed) != 0 {
					t.Errorf("scheduler.RemovePod called unexpectedly: %v", sr.removed)
				}
				for i, want := range tc.wantSchedulerRM {
					if i >= len(sr.removed) {
						t.Errorf("scheduler.RemovePod not called for %q", want)
						continue
					}
					if sr.removed[i] != want {
						t.Errorf("scheduler.RemovePod[%d] = %q, want %q", i, sr.removed[i], want)
					}
				}
			}
		})
	}
}
