package manifest

import "testing"

func TestParseCronJob_Valid(t *testing.T) {
	root, err := ParseYAML([]byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: wolf-weekly-retrain
spec:
  schedule: "0 2 * * 0"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      backoffLimit: 1
      template:
        spec:
          priorityClassName: normal
          restartPolicy: Never
          containers:
            - name: retrain
              image: localhost:5000/wolf-train:latest
              command:
                - python
                - train.py
                - --mode
                - finetune
              resources:
                requests:
                  cpu: "4"
                  memory: 32Gi
                  nvidia.com/gpu: "1"
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	pc := DefaultPriorityClasses()
	cj, err := parseCronJob(root, pc)
	if err != nil {
		t.Fatalf("parseCronJob: %v", err)
	}

	if cj.Name != "wolf-weekly-retrain" {
		t.Errorf("Name = %q, want %q", cj.Name, "wolf-weekly-retrain")
	}
	if cj.Schedule != "0 2 * * 0" {
		t.Errorf("Schedule = %q, want %q", cj.Schedule, "0 2 * * 0")
	}
	if cj.ConcurrencyPolicy != "Forbid" {
		t.Errorf("ConcurrencyPolicy = %q, want %q", cj.ConcurrencyPolicy, "Forbid")
	}
	if cj.SuccessfulJobsHistoryLimit != 3 {
		t.Errorf("SuccessfulJobsHistoryLimit = %d, want 3", cj.SuccessfulJobsHistoryLimit)
	}
	if cj.FailedJobsHistoryLimit != 1 {
		t.Errorf("FailedJobsHistoryLimit = %d, want 1", cj.FailedJobsHistoryLimit)
	}
	if cj.BackoffLimit != 1 {
		t.Errorf("BackoffLimit = %d, want 1", cj.BackoffLimit)
	}

	pod := cj.JobTemplate
	if pod.SourceKind != "CronJob" {
		t.Errorf("SourceKind = %q, want %q", pod.SourceKind, "CronJob")
	}
	if pod.SourceName != "wolf-weekly-retrain" {
		t.Errorf("SourceName = %q, want %q", pod.SourceName, "wolf-weekly-retrain")
	}
	if pod.RestartPolicy != "Never" {
		t.Errorf("RestartPolicy = %q, want %q", pod.RestartPolicy, "Never")
	}
	if pod.PriorityClassName != "normal" {
		t.Errorf("PriorityClassName = %q, want %q", pod.PriorityClassName, "normal")
	}
	if pod.Priority != 1000 {
		t.Errorf("Priority = %d, want 1000", pod.Priority)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("len(Containers) = %d, want 1", len(pod.Containers))
	}

	c := pod.Containers[0]
	if c.Name != "retrain" {
		t.Errorf("Container.Name = %q, want %q", c.Name, "retrain")
	}
	if c.Image != "localhost:5000/wolf-train:latest" {
		t.Errorf("Container.Image = %q, want %q", c.Image, "localhost:5000/wolf-train:latest")
	}
	if len(c.Command) != 4 {
		t.Fatalf("len(Command) = %d, want 4", len(c.Command))
	}
	wantCmd := []string{"python", "train.py", "--mode", "finetune"}
	for i, w := range wantCmd {
		if c.Command[i] != w {
			t.Errorf("Command[%d] = %q, want %q", i, c.Command[i], w)
		}
	}
	if c.Resources.Requests.CPUMillis != 4000 {
		t.Errorf("CPU = %d, want 4000", c.Resources.Requests.CPUMillis)
	}
	if c.Resources.Requests.MemoryMB != 32768 {
		t.Errorf("Memory = %d, want 32768", c.Resources.Requests.MemoryMB)
	}
	if c.Resources.Requests.GPUCount != 1 {
		t.Errorf("GPUCount = %d, want 1", c.Resources.Requests.GPUCount)
	}
}

func TestParseCronJob_Defaults(t *testing.T) {
	root, err := ParseYAML([]byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: minimal-job
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: worker
              image: busybox
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	cj, err := parseCronJob(root, DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("parseCronJob: %v", err)
	}

	if cj.ConcurrencyPolicy != "Forbid" {
		t.Errorf("ConcurrencyPolicy = %q, want default %q", cj.ConcurrencyPolicy, "Forbid")
	}
	if cj.SuccessfulJobsHistoryLimit != 3 {
		t.Errorf("SuccessfulJobsHistoryLimit = %d, want default 3", cj.SuccessfulJobsHistoryLimit)
	}
	if cj.FailedJobsHistoryLimit != 1 {
		t.Errorf("FailedJobsHistoryLimit = %d, want default 1", cj.FailedJobsHistoryLimit)
	}
	if cj.BackoffLimit != 1 {
		t.Errorf("BackoffLimit = %d, want default 1", cj.BackoffLimit)
	}
	if cj.JobTemplate.RestartPolicy != "Never" {
		t.Errorf("RestartPolicy = %q, want default %q", cj.JobTemplate.RestartPolicy, "Never")
	}
}

func TestParseCronJob_MissingSchedule(t *testing.T) {
	root, err := ParseYAML([]byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: no-schedule
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: worker
              image: busybox
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	_, err = parseCronJob(root, DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected error for missing schedule, got nil")
	}
}

func TestParseCronJob_MissingPodTemplate(t *testing.T) {
	root, err := ParseYAML([]byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: no-template
spec:
  schedule: "0 0 * * *"
`))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	_, err = parseCronJob(root, DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected error for missing pod template, got nil")
	}
}
