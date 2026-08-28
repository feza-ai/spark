package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

		if err := exec.StopPod(ctx, req.Name, gracePeriod); err != nil && !isNoSuchPod(err) {
			return json.Marshal(DeleteResponse{
				Name:    req.Name,
				Deleted: false,
				Error:   fmt.Sprintf("stop pod: %v", err),
			})
		}

		if err := exec.RemovePod(ctx, req.Name); err != nil && !isNoSuchPod(err) {
			// podman occasionally reports an error (e.g. a network cleanup
			// warning) after already removing the pod. Trusting it at face
			// value leaves the store record and the scheduler's resource
			// reservation -- including any GPU device slot -- intact for a
			// pod that no longer exists, with nothing left to ever release
			// it (issue #81). Confirm via a fresh status check before
			// giving up: only keep the record and reservation when the pod
			// is confirmed to still exist.
			if _, statusErr := exec.PodStatus(ctx, req.Name); !(statusErr != nil && isNoSuchPod(statusErr)) {
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
