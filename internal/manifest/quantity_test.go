package manifest

import (
	"strings"
	"testing"
)

// Issue #43: memory quantities with unrecognized suffixes silently parsed to
// 0 MB, so admission control saw zero-memory requests and overcommitted the
// node until the host froze. Unparseable quantities must be errors.

func TestParseMemory_InvalidQuantitiesAreErrors(t *testing.T) {
	inputs := []string{
		"102400m", // lowercase m = Kubernetes millibytes; the #43 incident value
		"100g",
		"abc",
		"1.5.3Gi",
		"-1Gi",
		"-100",
		"Mi",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			if got, err := parseMemory(in); err == nil {
				t.Errorf("parseMemory(%q) = %d, want error", in, got)
			}
		})
	}
}

func TestParseMemory_LowercaseMSuffixHint(t *testing.T) {
	_, err := parseMemory("102400m")
	if err == nil {
		t.Fatal("expected error for lowercase m suffix")
	}
	if !strings.Contains(err.Error(), "millibytes") {
		t.Errorf("error should explain the millibytes trap, got: %v", err)
	}
}

func TestParseMemory_FractionalQuantities(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"1.5Gi", 1536},
		{"0.5Gi", 512},
		{"2.5G", 2500},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseMemory(tt.input)
			if err != nil {
				t.Fatalf("parseMemory(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseCPU_InvalidQuantitiesAreErrors(t *testing.T) {
	inputs := []string{"abc", "12x", "1.5m", "-500m", "-1"}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			if got, err := parseCPU(in); err == nil {
				t.Errorf("parseCPU(%q) = %d, want error", in, got)
			}
		})
	}
}

func TestParseGPU_InvalidQuantitiesAreErrors(t *testing.T) {
	inputs := []string{"abc", "1.5", "-1", "2x"}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			if got, err := parseGPU(in); err == nil {
				t.Errorf("parseGPU(%q) = %d, want error", in, got)
			}
		})
	}
}

func TestParse_RejectsInvalidMemoryQuantity(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: render
spec:
  containers:
    - name: main
      image: example/render
      resources:
        requests:
          cpu: "12"
          memory: 102400m
`
	_, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected parse error for memory quantity 102400m, got nil")
	}
	for _, want := range []string{"main", "102400m", "resources.requests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}

func TestParse_RequestsDefaultFromLimits(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: gpu-job
spec:
  containers:
    - name: main
      image: example/cuda
      resources:
        limits:
          cpu: "4"
          memory: 32Gi
          nvidia.com/gpu: "1"
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	total := result.Pods[0].TotalRequests()
	if total.CPUMillis != 4000 {
		t.Errorf("CPUMillis = %d, want 4000", total.CPUMillis)
	}
	if total.MemoryMB != 32768 {
		t.Errorf("MemoryMB = %d, want 32768", total.MemoryMB)
	}
	if total.GPUCount != 1 {
		t.Errorf("GPUCount = %d, want 1", total.GPUCount)
	}
}

func TestParse_ExplicitRequestsNotOverriddenByLimits(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: burstable
spec:
  containers:
    - name: main
      image: example/app
      resources:
        requests:
          cpu: 500m
          memory: 1Gi
        limits:
          cpu: "2"
          memory: 4Gi
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	req := result.Pods[0].Containers[0].Resources.Requests
	if req.CPUMillis != 500 {
		t.Errorf("Requests.CPUMillis = %d, want 500", req.CPUMillis)
	}
	if req.MemoryMB != 1024 {
		t.Errorf("Requests.MemoryMB = %d, want 1024", req.MemoryMB)
	}
}

func TestParse_PartialRequestsFillFromLimits(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: partial
spec:
  containers:
    - name: main
      image: example/app
      resources:
        requests:
          cpu: 500m
        limits:
          memory: 4Gi
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	req := result.Pods[0].Containers[0].Resources.Requests
	if req.CPUMillis != 500 {
		t.Errorf("Requests.CPUMillis = %d, want 500", req.CPUMillis)
	}
	if req.MemoryMB != 4096 {
		t.Errorf("Requests.MemoryMB = %d, want 4096 (defaulted from limits)", req.MemoryMB)
	}
}
