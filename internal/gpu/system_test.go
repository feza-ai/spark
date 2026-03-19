package gpu

import (
	"runtime"
	"testing"
)

func TestDetectSystem(t *testing.T) {
	tests := []struct {
		name    string
		check   func(t *testing.T, info SystemInfo)
	}{
		{
			name: "CPU millicores are positive",
			check: func(t *testing.T, info SystemInfo) {
				want := runtime.NumCPU() * 1000
				if info.CPUMillis != want {
					t.Errorf("CPUMillis = %d, want %d", info.CPUMillis, want)
				}
			},
		},
		{
			name: "memory is positive",
			check: func(t *testing.T, info SystemInfo) {
				if info.MemoryTotalMB <= 0 {
					t.Errorf("MemoryTotalMB = %d, want > 0", info.MemoryTotalMB)
				}
			},
		},
	}

	info, err := DetectSystem()
	if err != nil {
		t.Fatalf("DetectSystem() error: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, info)
		})
	}
}
