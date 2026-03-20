package manifest

import "testing"

func TestParseInitContainers(t *testing.T) {
	yaml := `
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  initContainers:
    - name: init-db
      image: busybox:latest
      command:
        - sh
        - -c
        - echo init
      args:
        - --verbose
      env:
        - name: DB_HOST
          value: localhost
      volumeMounts:
        - name: data
          mountPath: /data
          readOnly: true
      resources:
        requests:
          cpu: "100m"
          memory: "64Mi"
  containers:
    - name: app
      image: myapp:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(result.Pods))
	}
	pod := result.Pods[0]
	if len(pod.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(pod.InitContainers))
	}

	ic := pod.InitContainers[0]
	if ic.Name != "init-db" {
		t.Errorf("init container name = %q, want %q", ic.Name, "init-db")
	}
	if ic.Image != "busybox:latest" {
		t.Errorf("init container image = %q, want %q", ic.Image, "busybox:latest")
	}
	if len(ic.Command) != 3 || ic.Command[0] != "sh" {
		t.Errorf("init container command = %v, want [sh -c echo init]", ic.Command)
	}
	if len(ic.Args) != 1 || ic.Args[0] != "--verbose" {
		t.Errorf("init container args = %v, want [--verbose]", ic.Args)
	}
	if len(ic.Env) != 1 || ic.Env[0].Name != "DB_HOST" || ic.Env[0].Value != "localhost" {
		t.Errorf("init container env = %v, want [{DB_HOST localhost}]", ic.Env)
	}
	if len(ic.VolumeMounts) != 1 || ic.VolumeMounts[0].Name != "data" || !ic.VolumeMounts[0].ReadOnly {
		t.Errorf("init container volumeMounts = %v, want [{data /data true}]", ic.VolumeMounts)
	}
	if ic.Resources.Requests.CPUMillis != 100 {
		t.Errorf("init container CPU requests = %d, want 100", ic.Resources.Requests.CPUMillis)
	}
	if ic.Resources.Requests.MemoryMB != 64 {
		t.Errorf("init container memory requests = %d, want 64", ic.Resources.Requests.MemoryMB)
	}
}

func TestParseInitContainers_Empty(t *testing.T) {
	yaml := `
apiVersion: v1
kind: Pod
metadata:
  name: no-init
spec:
  containers:
    - name: app
      image: myapp:latest
`
	result, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(result.Pods))
	}
	if len(result.Pods[0].InitContainers) != 0 {
		t.Errorf("expected 0 init containers, got %d", len(result.Pods[0].InitContainers))
	}
}

func TestParseInitAndMainContainers(t *testing.T) {
	tests := []struct {
		name           string
		yaml           string
		wantInit       int
		wantContainers int
		wantInitNames  []string
		wantMainNames  []string
	}{
		{
			name: "one init one main",
			yaml: `
apiVersion: v1
kind: Pod
metadata:
  name: mixed
spec:
  initContainers:
    - name: setup
      image: busybox:latest
  containers:
    - name: app
      image: myapp:latest
`,
			wantInit:       1,
			wantContainers: 1,
			wantInitNames:  []string{"setup"},
			wantMainNames:  []string{"app"},
		},
		{
			name: "two init two main",
			yaml: `
apiVersion: v1
kind: Pod
metadata:
  name: multi
spec:
  initContainers:
    - name: init-1
      image: busybox:latest
    - name: init-2
      image: alpine:latest
  containers:
    - name: web
      image: nginx:latest
    - name: sidecar
      image: fluentd:latest
`,
			wantInit:       2,
			wantContainers: 2,
			wantInitNames:  []string{"init-1", "init-2"},
			wantMainNames:  []string{"web", "sidecar"},
		},
		{
			name: "no init containers",
			yaml: `
apiVersion: v1
kind: Pod
metadata:
  name: no-init
spec:
  containers:
    - name: app
      image: myapp:latest
`,
			wantInit:       0,
			wantContainers: 1,
			wantInitNames:  nil,
			wantMainNames:  []string{"app"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse([]byte(tt.yaml), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Pods) != 1 {
				t.Fatalf("expected 1 pod, got %d", len(result.Pods))
			}
			pod := result.Pods[0]
			if len(pod.InitContainers) != tt.wantInit {
				t.Errorf("init containers = %d, want %d", len(pod.InitContainers), tt.wantInit)
			}
			if len(pod.Containers) != tt.wantContainers {
				t.Errorf("containers = %d, want %d", len(pod.Containers), tt.wantContainers)
			}
			for i, name := range tt.wantInitNames {
				if i >= len(pod.InitContainers) {
					break
				}
				if pod.InitContainers[i].Name != name {
					t.Errorf("init container[%d].Name = %q, want %q", i, pod.InitContainers[i].Name, name)
				}
			}
			for i, name := range tt.wantMainNames {
				if i >= len(pod.Containers) {
					break
				}
				if pod.Containers[i].Name != name {
					t.Errorf("container[%d].Name = %q, want %q", i, pod.Containers[i].Name, name)
				}
			}
		})
	}
}
