package bus

import (
	"encoding/json"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// ApplyResponse is returned to clients after applying a manifest.
type ApplyResponse struct {
	Pods     []PodStatus `json:"pods"`
	CronJobs []string    `json:"cronJobs,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// PodStatus reports a pod's name and current status.
type PodStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// RegisterApplyHandler registers the req.spark.apply handler.
func RegisterApplyHandler(b Bus, store *state.PodStore, priorityClasses map[string]int) {
	b.HandleRequest("req.spark.apply", func(_ string, data []byte) ([]byte, error) {
		result, err := manifest.Parse(data, priorityClasses)
		if err != nil {
			return json.Marshal(ApplyResponse{Error: err.Error()})
		}

		resp := ApplyResponse{}

		for _, pod := range result.Pods {
			store.Apply(pod)
			resp.Pods = append(resp.Pods, PodStatus{
				Name:   pod.Name,
				Status: "pending",
			})
		}

		for _, cj := range result.CronJobs {
			resp.CronJobs = append(resp.CronJobs, cj.Name)
		}

		return json.Marshal(resp)
	})
}
