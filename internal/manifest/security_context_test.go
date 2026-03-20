package manifest

import (
	"reflect"
	"testing"
)

func TestParseSecurityContext(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want *SecurityContext
	}{
		{
			name: "all fields",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: secure-pod
spec:
  containers:
    - name: app
      image: nginx
      securityContext:
        runAsUser: 1000
        runAsNonRoot: true
        privileged: false
        capabilities:
          add:
            - NET_ADMIN
            - SYS_TIME
          drop:
            - ALL
`,
			want: &SecurityContext{
				RunAsUser:    1000,
				RunAsNonRoot: true,
				Privileged:   false,
				AddCaps:      []string{"NET_ADMIN", "SYS_TIME"},
				DropCaps:     []string{"ALL"},
			},
		},
		{
			name: "only runAsUser",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: partial-pod
spec:
  containers:
    - name: app
      image: nginx
      securityContext:
        runAsUser: 500
`,
			want: &SecurityContext{
				RunAsUser: 500,
			},
		},
		{
			name: "missing securityContext",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: no-sc-pod
spec:
  containers:
    - name: app
      image: nginx
`,
			want: nil,
		},
		{
			name: "capabilities add only",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: caps-add-pod
spec:
  containers:
    - name: app
      image: nginx
      securityContext:
        capabilities:
          add:
            - NET_RAW
`,
			want: &SecurityContext{
				AddCaps: []string{"NET_RAW"},
			},
		},
		{
			name: "capabilities drop only",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: caps-drop-pod
spec:
  containers:
    - name: app
      image: nginx
      securityContext:
        capabilities:
          drop:
            - ALL
            - NET_RAW
`,
			want: &SecurityContext{
				DropCaps: []string{"ALL", "NET_RAW"},
			},
		},
		{
			name: "privileged true",
			yaml: `apiVersion: v1
kind: Pod
metadata:
  name: priv-pod
spec:
  containers:
    - name: app
      image: nginx
      securityContext:
        privileged: true
`,
			want: &SecurityContext{
				Privileged: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse([]byte(tt.yaml), nil)
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}
			if len(result.Pods) != 1 {
				t.Fatalf("expected 1 pod, got %d", len(result.Pods))
			}
			if len(result.Pods[0].Containers) != 1 {
				t.Fatalf("expected 1 container, got %d", len(result.Pods[0].Containers))
			}
			got := result.Pods[0].Containers[0].SecurityContext
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil SecurityContext, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected SecurityContext %+v, got nil", tt.want)
			}
			if !reflect.DeepEqual(*got, *tt.want) {
				t.Errorf("SecurityContext mismatch\ngot:  %+v\nwant: %+v", *got, *tt.want)
			}
		})
	}
}
