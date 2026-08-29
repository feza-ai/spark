package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

// isNoSuchPod reports whether err is the podman "no such pod" error,
// meaning the pod does not exist in podman state. Mirrors the identical
// helper in internal/api and internal/reconciler -- each package keeps
// its own copy rather than sharing one across an import boundary.
func isNoSuchPod(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such pod")
}

// isCgroupCleanupRace reports whether err is podman's benign
// "cgroup: Unit machine-libpod_pod_<id>.slice not loaded" error from
// `podman pod rm` (issue #71). It surfaces when the pod's containers were
// already torn down (e.g. by a preceding stop) and systemd/crun reaped the
// pod's cgroup slice before rm got to it -- podman reports the absence of
// the thing it's cleaning up as a hard error even though the desired end
// state (pod gone) is already true.
func isCgroupCleanupRace(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cgroup") && strings.Contains(msg, "not loaded")
}

// isPodAlreadyGone reports whether err indicates the pod is already absent
// from podman state -- either explicitly ("no such pod") or via the
// cgroup-cleanup race in isCgroupCleanupRace (issue #71). Callers classify
// both the same way: proceed with cleanup instead of aborting.
func isPodAlreadyGone(err error) bool {
	return isNoSuchPod(err) || isCgroupCleanupRace(err)
}

// removePodMaxAttempts bounds the retries around exec.RemovePod when it
// hits the podman cgroup-cleanup race (issue #71): "podman pod rm" can
// report the slice-not-loaded error even though the pod is already torn
// down, and an immediate retry commonly succeeds outright once the race
// window has passed.
const removePodMaxAttempts = 3

// removePodRetryDelay is the pause between retries in removePodWithRetry.
var removePodRetryDelay = 20 * time.Millisecond

// removePodWithRetry calls exec.RemovePod, retrying up to
// removePodMaxAttempts times when the failure is the cgroup-cleanup race
// (isCgroupCleanupRace) rather than giving up on the first attempt. Any
// other error, a "no such pod" result, or success returns immediately:
// retrying either wastes time (no such pod never becomes "found") or has
// already achieved the state the caller wants.
func removePodWithRetry(ctx context.Context, exec executor.Executor, name string) error {
	var err error
	for attempt := 1; attempt <= removePodMaxAttempts; attempt++ {
		err = exec.RemovePod(ctx, name)
		if err == nil || !isCgroupCleanupRace(err) {
			return err
		}
		if attempt < removePodMaxAttempts {
			time.Sleep(removePodRetryDelay)
		}
	}
	return err
}

// PodRemover releases scheduler resources for a pod.
type PodRemover interface {
	RemovePod(name string)
}

// DeleteRequest is the payload for a delete request.
type DeleteRequest struct {
	Name string `json:"name"`
}

// DeleteResponse is returned to clients after deleting a pod.
type DeleteResponse struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// RegisterDeleteHandler registers the req.spark.delete handler.
// The scheduler parameter may be nil if scheduling is not enabled.
func RegisterDeleteHandler(b Bus, store *state.PodStore, exec executor.Executor, scheduler PodRemover) {
	b.HandleRequest("req.spark.delete", func(_ string, data []byte) ([]byte, error) {
		var req DeleteRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return json.Marshal(DeleteResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		}

		rec, ok := store.Get(req.Name)
		if !ok {
			return json.Marshal(DeleteResponse{
				Name:    req.Name,
				Deleted: false,
				Error:   fmt.Sprintf("pod %q not found", req.Name),
			})
		}

		gracePeriod := rec.Spec.TerminationGracePeriodSeconds
		if gracePeriod <= 0 {
			gracePeriod = 30
		}

		ctx := context.Background()

		if err := exec.StopPod(ctx, req.Name, gracePeriod); err != nil && !isPodAlreadyGone(err) {
			return json.Marshal(DeleteResponse{
				Name:    req.Name,
				Deleted: false,
				Error:   fmt.Sprintf("stop pod: %v", err),
			})
		}

		if err := removePodWithRetry(ctx, exec, req.Name); err != nil && !isPodAlreadyGone(err) {
			// podman occasionally reports an error (e.g. a network cleanup
			// warning, or -- once retries above are exhausted -- the
			// cgroup-cleanup race) after already removing the pod. Trusting
			// it at face value leaves the store record and the scheduler's
			// resource reservation -- including any GPU device slot --
			// intact for a pod that no longer exists, with nothing left to
			// ever release it (issue #81). Confirm via a fresh status check
			// before giving up: only keep the record and reservation when
			// the pod is confirmed to still exist.
			if _, statusErr := exec.PodStatus(ctx, req.Name); !(statusErr != nil && isPodAlreadyGone(statusErr)) {
				return json.Marshal(DeleteResponse{
					Name:    req.Name,
					Deleted: false,
					Error:   fmt.Sprintf("remove pod: %v", err),
				})
			}
		}

		store.Delete(req.Name)

		if scheduler != nil {
			scheduler.RemovePod(req.Name)
		}

		return json.Marshal(DeleteResponse{
			Name:    req.Name,
			Deleted: true,
		})
	})
}
