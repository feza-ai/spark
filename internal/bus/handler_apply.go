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

// CronRegisterer registers parsed CronJob specs for scheduled execution.
type CronRegisterer interface {
	Register(manifest.CronJobSpec) error
}

// RegisterApplyHandler registers the req.spark.apply handler.
// An optional CronRegisterer may be passed; when provided, each parsed
// CronJob is registered for scheduled execution.
func RegisterApplyHandler(b Bus, store *state.PodStore, priorityClasses map[string]int, opts ...CronRegisterer) {
	var cronReg CronRegisterer
	if len(opts) > 0 {
		cronReg = opts[0]
	}

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
			if cronReg != nil {
				if err := cronReg.Register(cj); err != nil {
					return json.Marshal(ApplyResponse{Error: "cron register: " + err.Error()})
				}
			}
			resp.CronJobs = append(resp.CronJobs, cj.Name)
		}

		return json.Marshal(resp)
	})
}
