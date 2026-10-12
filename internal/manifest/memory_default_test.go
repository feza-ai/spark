package manifest

import (
	"strings"
	"testing"
)

// podWithResources builds a single-container Pod manifest whose resources:
// block is exactly the YAML passed in, so each case states only the thing
// under test. resourcesYAML is indented to sit under the container.
func podWithResources(resourcesYAML string) []byte {
	var b strings.Builder
	b.WriteString(`apiVersion: v1
kind: Pod
metadata:
  name: p
spec:
  containers:
  - name: main
    image: test
`)
	b.WriteString(resourcesYAML)
	return []byte(b.String())
}

// TestParse_DefaultMemoryRequest covers the defect in issue #121: a
// container that declares no memory was accounted at 0MB forever, so a
// node packed with such containers reported an empty memory ledger while
// it was full. Each case asserts both what the ledger charges the pod
// (TotalRequests) and whether the container is marked as having declared
// nothing, since the WARN log and the metric both key off that mark.
func TestParse_DefaultMemoryRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// resources is the YAML block appended under the container.
		resources string
		// defaultMB is passed via WithDefaultMemoryRequestMB; 0 means the
		// option is omitted entirely.
		defaultMB      int
		wantMemoryMB   int
		wantUndeclared bool
	}{
		{
			name:           "no resources block at all is defaulted",
			resources:      "",
			defaultMB:      2048,
			wantMemoryMB:   2048,
			wantUndeclared: true,
		},
		{
			name: "cpu declared but memory absent is defaulted",
			resources: `    resources:
      requests:
        cpu: "2"
`,
			defaultMB:      2048,
			wantMemoryMB:   2048,
			wantUndeclared: true,
		},
		{
			name: "limits declaring only cpu is defaulted",
			resources: `    resources:
      limits:
        cpu: "2"
`,
			defaultMB:      2048,
			wantMemoryMB:   2048,
			wantUndeclared: true,
		},
		{
			name: "a valueless memory key declares nothing and is defaulted",
			resources: `    resources:
      requests:
        memory:
`,
			defaultMB:      2048,
			wantMemoryMB:   2048,
			wantUndeclared: true,
		},
		{
			name: "an explicit zero is a declaration and is honored",
			resources: `    resources:
      requests:
        memory: "0"
`,
			defaultMB:      2048,
			wantMemoryMB:   0,
			wantUndeclared: false,
		},
		{
			name: "a declared request is never overwritten",
			resources: `    resources:
      requests:
        memory: 512Mi
`,
			defaultMB:      2048,
			wantMemoryMB:   512,
			wantUndeclared: false,
		},
		{
			name: "a limits-only memory declaration still defaults the request to the limit, not to the configured default",
			resources: `    resources:
      limits:
        memory: 512Mi
`,
			defaultMB:      2048,
			wantMemoryMB:   512,
			wantUndeclared: false,
		},
		{
			name:           "no default configured leaves an undeclared container at zero",
			resources:      "",
			defaultMB:      0,
			wantMemoryMB:   0,
			wantUndeclared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []ParseOption
			if tt.defaultMB != 0 {
				opts = append(opts, WithDefaultMemoryRequestMB(tt.defaultMB))
			}
			result, err := Parse(podWithResources(tt.resources), nil, opts...)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(result.Pods) != 1 {
				t.Fatalf("expected 1 pod, got %d", len(result.Pods))
			}
			pod := result.Pods[0]
			if got := pod.TotalRequests().MemoryMB; got != tt.wantMemoryMB {
				t.Errorf("TotalRequests().MemoryMB = %d, want %d", got, tt.wantMemoryMB)
			}
			if got := pod.Containers[0].Resources.MemoryRequestUndeclared; got != tt.wantUndeclared {
				t.Errorf("MemoryRequestUndeclared = %v, want %v", got, tt.wantUndeclared)
			}
			undeclared := pod.UndeclaredMemoryContainers()
			if tt.wantUndeclared && len(undeclared) != 1 {
				t.Errorf("UndeclaredMemoryContainers() = %v, want [main]", undeclared)
			}
			if !tt.wantUndeclared && len(undeclared) != 0 {
				t.Errorf("UndeclaredMemoryContainers() = %v, want none", undeclared)
			}
		})
	}
}

// TestParse_DefaultMemoryRequest_SumsAcrossContainers checks that the
// ledger charges every undeclared container, not just the first. This is
// the shape that made issue #121 severe: the figure admission trusts is a
// sum, so one uncharged container per pod is enough to understate a node.
func TestParse_DefaultMemoryRequest_SumsAcrossContainers(t *testing.T) {
	t.Parallel()

	data := []byte(`apiVersion: v1
kind: Pod
metadata:
  name: p
spec:
  containers:
  - name: undeclared-a
    image: test
  - name: declared
    image: test
    resources:
      requests:
        memory: 512Mi
  - name: undeclared-b
    image: test
`)

	result, err := Parse(data, nil, WithDefaultMemoryRequestMB(2048))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := result.Pods[0].TotalRequests().MemoryMB, 2048+512+2048; got != want {
		t.Fatalf("TotalRequests().MemoryMB = %d, want %d", got, want)
	}
	got := result.Pods[0].UndeclaredMemoryContainers()
	if len(got) != 2 || got[0] != "undeclared-a" || got[1] != "undeclared-b" {
		t.Fatalf("UndeclaredMemoryContainers() = %v, want [undeclared-a undeclared-b]", got)
	}
}

// TestParse_DefaultMemoryRequest_AppliesToJobAndCronJob checks the default
// reaches the kinds that actually run workloads here. A CronJob's
// JobTemplate is materialized into pods later, so defaulting only the
// top-level Pod kind would leave the recurring workloads -- the ones most
// likely to be resident in bulk -- accounted at zero.
func TestParse_DefaultMemoryRequest_AppliesToJobAndCronJob(t *testing.T) {
	t.Parallel()

	job := []byte(`apiVersion: batch/v1
kind: Job
metadata:
  name: j
spec:
  template:
    spec:
      containers:
      - name: main
        image: test
`)
	result, err := Parse(job, nil, WithDefaultMemoryRequestMB(2048))
	if err != nil {
		t.Fatalf("Parse job: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("expected 1 pod from Job, got %d", len(result.Pods))
	}
	if got := result.Pods[0].TotalRequests().MemoryMB; got != 2048 {
		t.Errorf("Job pod TotalRequests().MemoryMB = %d, want 2048", got)
	}

	cronjob := []byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: cj
spec:
  schedule: "* * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: main
            image: test
`)
	result, err = Parse(cronjob, nil, WithDefaultMemoryRequestMB(2048))
	if err != nil {
		t.Fatalf("Parse cronjob: %v", err)
	}
	if len(result.CronJobs) != 1 {
		t.Fatalf("expected 1 cronjob, got %d", len(result.CronJobs))
	}
	if got := result.CronJobs[0].JobTemplate.TotalRequests().MemoryMB; got != 2048 {
		t.Errorf("CronJob JobTemplate TotalRequests().MemoryMB = %d, want 2048", got)
	}
}

// TestParse_DefaultMemoryRequest_NeverMasksAParseError guards the line
// issue #66 drew: absent input may be defaulted, malformed input must be
// rejected. Defaulting runs after parsing, so a bad quantity still fails
// rather than quietly becoming the default.
func TestParse_DefaultMemoryRequest_NeverMasksAParseError(t *testing.T) {
	t.Parallel()

	data := podWithResources(`    resources:
      requests:
        memory: 512Zi
`)
	_, err := Parse(data, nil, WithDefaultMemoryRequestMB(2048))
	if err == nil {
		t.Fatal("expected an unparseable memory quantity to be an error, got nil")
	}
	if !strings.Contains(err.Error(), "512Zi") {
		t.Fatalf("expected the error to name the bad quantity, got: %v", err)
	}
}
