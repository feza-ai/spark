package executor

import "testing"

// Issue #52: podman reports a pod as "Degraded" whenever any container has
// exited while another (including the always-running infra container) is
// still up, so the pod-level state cannot distinguish success from failure.
// The verdict must come from per-container states with infra excluded.

func TestParseContainerPS(t *testing.T) {
	out := []byte(`[
  {"Names": ["f4de59c8122f-infra"], "State": "running", "ExitCode": 0, "IsInfra": true},
  {"Names": ["issue52-probe-crashy"], "State": "exited", "ExitCode": 7, "IsInfra": false}
]`)
	statuses, err := parseContainerPS(out)
	if err != nil {
		t.Fatalf("parseContainerPS: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(statuses))
	}
	if !statuses[0].IsInfra || !statuses[0].Running {
		t.Errorf("infra container parsed wrong: %+v", statuses[0])
	}
	if statuses[1].IsInfra || statuses[1].Running || statuses[1].ExitCode != 7 {
		t.Errorf("workload container parsed wrong: %+v", statuses[1])
	}
}

func TestParseContainerPS_InfraByNameSuffix(t *testing.T) {
	// Older podman versions omit IsInfra from the ps JSON.
	out := []byte(`[{"Names": ["abc123-infra"], "State": "running", "ExitCode": 0}]`)
	statuses, err := parseContainerPS(out)
	if err != nil {
		t.Fatalf("parseContainerPS: %v", err)
	}
	if !statuses[0].IsInfra {
		t.Errorf("expected -infra name suffix to mark container as infra: %+v", statuses[0])
	}
}

func TestParseContainerPS_Malformed(t *testing.T) {
	if _, err := parseContainerPS([]byte("not json")); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestDerivePodStatus(t *testing.T) {
	infra := ContainerStatus{Name: "x-infra", Running: true, IsInfra: true}
	tests := []struct {
		name       string
		containers []ContainerStatus
		want       Status
	}{
		{
			// The live probe from issue #52: single workload container
			// exited 7, infra still up, pod state "Degraded". Was reported
			// as completed (exit 0).
			name: "single container failed",
			containers: []ContainerStatus{
				infra,
				{Name: "p-crashy", Running: false, ExitCode: 7},
			},
			want: Status{Running: false, ExitCode: 7},
		},
		{
			name: "single container succeeded",
			containers: []ContainerStatus{
				infra,
				{Name: "p-main", Running: false, ExitCode: 0},
			},
			want: Status{Running: false, ExitCode: 0},
		},
		{
			// The issue #46 shape: crashed sidecar, healthy main. The pod
			// is still running; tearing it down is the reconciler's call,
			// not a fait accompli from a zero exit code.
			name: "sidecar crashed, main still running",
			containers: []ContainerStatus{
				infra,
				{Name: "p-healthy", Running: true},
				{Name: "p-crashy", Running: false, ExitCode: 1},
			},
			want: Status{Running: true, ExitCode: 0},
		},
		{
			name: "all workload containers exited, one failed",
			containers: []ContainerStatus{
				infra,
				{Name: "p-a", Running: false, ExitCode: 0},
				{Name: "p-b", Running: false, ExitCode: 5},
			},
			want: Status{Running: false, ExitCode: 5},
		},
		{
			name: "all workload containers exited cleanly",
			containers: []ContainerStatus{
				infra,
				{Name: "p-a", Running: false, ExitCode: 0},
				{Name: "p-b", Running: false, ExitCode: 0},
			},
			want: Status{Running: false, ExitCode: 0},
		},
		{
			name:       "only infra remains",
			containers: []ContainerStatus{infra},
			want:       Status{Running: false, ExitCode: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := derivePodStatus(tt.containers); got != tt.want {
				t.Errorf("derivePodStatus() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
