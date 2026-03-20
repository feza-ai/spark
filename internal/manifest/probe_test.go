package manifest

import (
	"testing"
)

func TestParseProbe(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		check  func(t *testing.T, cs ContainerSpec)
	}{
		{
			name: "exec probe with all fields",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: probe-exec
spec:
  containers:
    - name: app
      image: busybox
      livenessProbe:
        exec:
          command:
            - cat
            - /tmp/healthy
        initialDelaySeconds: 5
        periodSeconds: 10
        failureThreshold: 3
        timeoutSeconds: 2`,
			check: func(t *testing.T, cs ContainerSpec) {
				if cs.LivenessProbe == nil {
					t.Fatal("expected LivenessProbe to be set")
				}
				p := cs.LivenessProbe
				if p.Exec == nil {
					t.Fatal("expected Exec probe")
				}
				if len(p.Exec.Command) != 2 || p.Exec.Command[0] != "cat" || p.Exec.Command[1] != "/tmp/healthy" {
					t.Fatalf("unexpected exec command: %v", p.Exec.Command)
				}
				if p.InitialDelaySeconds != 5 {
					t.Fatalf("expected InitialDelaySeconds=5, got %d", p.InitialDelaySeconds)
				}
				if p.PeriodSeconds != 10 {
					t.Fatalf("expected PeriodSeconds=10, got %d", p.PeriodSeconds)
				}
				if p.FailureThreshold != 3 {
					t.Fatalf("expected FailureThreshold=3, got %d", p.FailureThreshold)
				}
				if p.TimeoutSeconds != 2 {
					t.Fatalf("expected TimeoutSeconds=2, got %d", p.TimeoutSeconds)
				}
				if p.HTTPGet != nil {
					t.Fatal("expected HTTPGet to be nil")
				}
			},
		},
		{
			name: "httpGet probe",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: probe-http
spec:
  containers:
    - name: web
      image: nginx
      livenessProbe:
        httpGet:
          path: /healthz
          port: 8080
        periodSeconds: 15`,
			check: func(t *testing.T, cs ContainerSpec) {
				if cs.LivenessProbe == nil {
					t.Fatal("expected LivenessProbe to be set")
				}
				p := cs.LivenessProbe
				if p.HTTPGet == nil {
					t.Fatal("expected HTTPGet probe")
				}
				if p.HTTPGet.Path != "/healthz" {
					t.Fatalf("expected path=/healthz, got %s", p.HTTPGet.Path)
				}
				if p.HTTPGet.Port != 8080 {
					t.Fatalf("expected port=8080, got %d", p.HTTPGet.Port)
				}
				if p.PeriodSeconds != 15 {
					t.Fatalf("expected PeriodSeconds=15, got %d", p.PeriodSeconds)
				}
				if p.Exec != nil {
					t.Fatal("expected Exec to be nil")
				}
			},
		},
		{
			name: "missing probe returns nil",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: probe-none
spec:
  containers:
    - name: app
      image: busybox`,
			check: func(t *testing.T, cs ContainerSpec) {
				if cs.LivenessProbe != nil {
					t.Fatal("expected LivenessProbe to be nil")
				}
			},
		},
		{
			name: "defaults applied when fields omitted",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: probe-defaults
spec:
  containers:
    - name: app
      image: busybox
      livenessProbe:
        exec:
          command:
            - cat
            - /tmp/healthy`,
			check: func(t *testing.T, cs ContainerSpec) {
				if cs.LivenessProbe == nil {
					t.Fatal("expected LivenessProbe to be set")
				}
				p := cs.LivenessProbe
				if p.PeriodSeconds != 10 {
					t.Fatalf("expected default PeriodSeconds=10, got %d", p.PeriodSeconds)
				}
				if p.FailureThreshold != 3 {
					t.Fatalf("expected default FailureThreshold=3, got %d", p.FailureThreshold)
				}
				if p.TimeoutSeconds != 1 {
					t.Fatalf("expected default TimeoutSeconds=1, got %d", p.TimeoutSeconds)
				}
				if p.InitialDelaySeconds != 0 {
					t.Fatalf("expected InitialDelaySeconds=0, got %d", p.InitialDelaySeconds)
				}
			},
		},
		{
			name: "partial fields with httpGet",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: probe-partial
spec:
  containers:
    - name: web
      image: nginx
      livenessProbe:
        httpGet:
          path: /ready
          port: 3000
        initialDelaySeconds: 10
        failureThreshold: 5`,
			check: func(t *testing.T, cs ContainerSpec) {
				if cs.LivenessProbe == nil {
					t.Fatal("expected LivenessProbe to be set")
				}
				p := cs.LivenessProbe
				if p.HTTPGet == nil {
					t.Fatal("expected HTTPGet probe")
				}
				if p.HTTPGet.Path != "/ready" {
					t.Fatalf("expected path=/ready, got %s", p.HTTPGet.Path)
				}
				if p.HTTPGet.Port != 3000 {
					t.Fatalf("expected port=3000, got %d", p.HTTPGet.Port)
				}
				if p.InitialDelaySeconds != 10 {
					t.Fatalf("expected InitialDelaySeconds=10, got %d", p.InitialDelaySeconds)
				}
				if p.FailureThreshold != 5 {
					t.Fatalf("expected FailureThreshold=5, got %d", p.FailureThreshold)
				}
				// Defaults for omitted fields.
				if p.PeriodSeconds != 10 {
					t.Fatalf("expected default PeriodSeconds=10, got %d", p.PeriodSeconds)
				}
				if p.TimeoutSeconds != 1 {
					t.Fatalf("expected default TimeoutSeconds=1, got %d", p.TimeoutSeconds)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse([]byte(tt.yaml), nil)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if len(result.Pods) == 0 {
				t.Fatal("expected at least one pod")
			}
			if len(result.Pods[0].Containers) == 0 {
				t.Fatal("expected at least one container")
			}
			tt.check(t, result.Pods[0].Containers[0])
		})
	}
}
