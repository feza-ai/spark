package manifest

import "fmt"

// parseCronJob extracts a CronJobSpec from a parsed CronJob manifest.
func parseCronJob(root map[string]interface{}, priorityClasses map[string]int) (CronJobSpec, error) {
	name := getString(root, "metadata", "name")

	schedule := getString(root, "spec", "schedule")
	if schedule == "" {
		return CronJobSpec{}, fmt.Errorf("cronjob %q: spec.schedule is required", name)
	}

	concurrencyPolicy := getString(root, "spec", "concurrencyPolicy")
	if concurrencyPolicy == "" {
		concurrencyPolicy = "Forbid"
	}

	successLimit := getInt(root, "spec", "successfulJobsHistoryLimit")
	if successLimit == 0 {
		successLimit = 3
	}

	failedLimit := getInt(root, "spec", "failedJobsHistoryLimit")
	if failedLimit == 0 {
		failedLimit = 1
	}

	backoffLimit := getInt(root, "spec", "jobTemplate", "spec", "backoffLimit")
	if backoffLimit == 0 {
		backoffLimit = 1
	}

	podSpecMap := getMap(root, "spec", "jobTemplate", "spec", "template", "spec")
	if podSpecMap == nil {
		return CronJobSpec{}, fmt.Errorf("cronjob %q: spec.jobTemplate.spec.template.spec is required", name)
	}

	pod, err := parsePodFromMap(podSpecMap, priorityClasses)
	if err != nil {
		return CronJobSpec{}, fmt.Errorf("cronjob %q: %w", name, err)
	}
	pod.Name = name
	pod.SourceKind = "CronJob"
	pod.SourceName = name
	pod.BackoffLimit = backoffLimit

	if pod.RestartPolicy == "" {
		pod.RestartPolicy = "Never"
	}

	return CronJobSpec{
		Name:                       name,
		Schedule:                   schedule,
		ConcurrencyPolicy:          concurrencyPolicy,
		SuccessfulJobsHistoryLimit: successLimit,
		FailedJobsHistoryLimit:     failedLimit,
		JobTemplate:                pod,
		BackoffLimit:               backoffLimit,
	}, nil
}
