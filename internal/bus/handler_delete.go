package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/state"
)

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
func RegisterDeleteHandler(b Bus, store *state.PodStore, exec executor.Executor) {
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

		ctx := context.TODO()

		if err := exec.StopPod(ctx, req.Name, gracePeriod); err != nil {
			return json.Marshal(DeleteResponse{
				Name:    req.Name,
				Deleted: false,
				Error:   fmt.Sprintf("stop pod: %v", err),
			})
		}

		if err := exec.RemovePod(ctx, req.Name); err != nil {
			return json.Marshal(DeleteResponse{
				Name:    req.Name,
				Deleted: false,
				Error:   fmt.Sprintf("remove pod: %v", err),
			})
		}

		store.Delete(req.Name)

		return json.Marshal(DeleteResponse{
			Name:    req.Name,
			Deleted: true,
		})
	})
}
