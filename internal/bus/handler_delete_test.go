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
	stopErr      error
	removeErr    error
	stopped      []string
	removed      []string
	podStatus    executor.Status
	podStatusErr error

	// removeErrSequence, when non-nil, scripts a per-call result for
	// RemovePod: entry i is returned on the (i+1)th call (a nil entry
	// means success on that call). Once the sequence is exhausted,
	// RemovePod returns nil. Used to test retry behavior; removeErr keeps
	// its existing single-error-every-call meaning when this is nil.
	removeErrSequence []error
	removeCallCount   int
}

func (e *stubExecutor) CreatePod(_ context.Context, _ manifest.PodSpec) error {
	return nil
}

func (e *stubExecutor) StartContainer(_ context.Context, _ string) error { return nil }

func (e *stubExecutor) StopPod(_ context.Context, name string, _ int) error {
	e.stopped = append(e.stopped, name)
	return e.stopErr
}

func (e *stubExecutor) PodStatus(_ context.Context, _ string) (executor.Status, error) {
	return e.podStatus, e.podStatusErr
}

func (e *stubExecutor) RemovePod(_ context.Context, name string) error {
	e.removed = append(e.removed, name)
	if e.removeErrSequence != nil {
		idx := e.removeCallCount
		e.removeCallCount++
		if idx < len(e.removeErrSequence) {
			return e.removeErrSequence[idx]
		}
		return nil
	}
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

func (e *stubExecutor) PruneImages(_ context.Context) (int, error) {
	return 0, nil
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
		podStatusErr    error
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
		{
			// issue #81: podman can report an error on removal that isn't
			// "no such pod" even though it already removed the pod. A
			// follow-up status check confirming "no such pod" must let the
			// delete succeed rather than leaking the scheduler reservation.
			name:            "remove error but pod confirmed gone",
			podName:         "ghost-pod",
			podExists:       true,
			removeErr:       fmt.Errorf("Error: unable to clean up network for pod: network not found"),
			podStatusErr:    fmt.Errorf("Error: no such pod ghost-pod"),
			scheduler:       &stubPodRemover{},
			wantDeleted:     true,
			wantSchedulerRM: []string{"ghost-pod"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewStubBus()
			store := state.NewPodStore()
			exec := &stubExecutor{stopErr: tc.stopErr, removeErr: tc.removeErr, podStatusErr: tc.podStatusErr}

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

// TestDeleteHandler_CgroupRaceTreatedAsSuccess reproduces issue #71 for the
// NATS delete handler: podman pod rm can report "cgroup: Unit
// machine-libpod_pod_<id>.slice not loaded" when the pod's containers were
// already torn down and its cgroup slice was reaped before rm got to it --
// the desired end state (pod gone) is already true, so delete must still
// succeed and release the scheduler reservation, even once the bounded
// retry is exhausted (every attempt returns the same error here).
func TestDeleteHandler_CgroupRaceTreatedAsSuccess(t *testing.T) {
	b := NewStubBus()
	store := state.NewPodStore()
	exec := &stubExecutor{
		removeErr: fmt.Errorf("Error: removing pod a3f9c21b: cgroup: Unit machine-libpod_pod_a3f9c21b.slice not loaded."),
	}
	sched := &stubPodRemover{}

	store.Apply(manifest.PodSpec{Name: "race-pod", TerminationGracePeriodSeconds: 10})
	RegisterDeleteHandler(b, store, exec, sched)

	reqData, _ := json.Marshal(DeleteRequest{Name: "race-pod"})
	resp, err := b.Request(context.Background(), "req.spark.delete", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var dr DeleteResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !dr.Deleted {
		t.Errorf("expected Deleted=true for the cgroup-cleanup race error, got response %+v", dr)
	}
	if len(sched.removed) != 1 || sched.removed[0] != "race-pod" {
		t.Errorf("expected scheduler.RemovePod(race-pod) once, got %v", sched.removed)
	}
}

// TestDeleteHandler_CgroupRaceRetrySucceeds reproduces the exact issue #71
// repro for the NATS delete handler: the first podman pod rm hits the
// cgroup-cleanup race and errors, but an immediate retry succeeds outright
// once the race window has passed. RemovePod must be retried, and the
// second call succeeding must not be treated as a failure.
func TestDeleteHandler_CgroupRaceRetrySucceeds(t *testing.T) {
	b := NewStubBus()
	store := state.NewPodStore()
	exec := &stubExecutor{
		removeErrSequence: []error{
			fmt.Errorf("Error: removing pod issue71: cgroup: Unit machine-libpod_pod_issue71.slice not loaded."),
			nil,
		},
	}
	sched := &stubPodRemover{}

	store.Apply(manifest.PodSpec{Name: "issue71", TerminationGracePeriodSeconds: 10})
	RegisterDeleteHandler(b, store, exec, sched)

	reqData, _ := json.Marshal(DeleteRequest{Name: "issue71"})
	resp, err := b.Request(context.Background(), "req.spark.delete", reqData)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	var dr DeleteResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !dr.Deleted {
		t.Errorf("expected Deleted=true once the retried RemovePod succeeds, got response %+v", dr)
	}
	if len(exec.removed) != 2 {
		t.Errorf("expected RemovePod to be retried once (2 calls total), got %d", len(exec.removed))
	}
}
