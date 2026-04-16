package gpu

import (
	"runtime"
	"testing"
)

func TestDetectSystem(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, info SystemInfo)
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

func TestDetectSystem_CoreIDs(t *testing.T) {
	info, err := DetectSystem()
	if err != nil {
		t.Fatalf("DetectSystem() error: %v", err)
	}

	tests := []struct {
		name  string
		check func(t *testing.T, info SystemInfo)
	}{
		{
			name: "length matches runtime.NumCPU()",
			check: func(t *testing.T, info SystemInfo) {
				want := runtime.NumCPU()
				if len(info.CoreIDs) != want {
					t.Errorf("len(CoreIDs) = %d, want %d", len(info.CoreIDs), want)
				}
			},
		},
		{
			name: "first ID is 0",
			check: func(t *testing.T, info SystemInfo) {
				if len(info.CoreIDs) == 0 {
					t.Fatal("CoreIDs is empty")
				}
				if info.CoreIDs[0] != 0 {
					t.Errorf("CoreIDs[0] = %d, want 0", info.CoreIDs[0])
				}
			},
		},
		{
			name: "IDs are contiguous",
			check: func(t *testing.T, info SystemInfo) {
				for i, id := range info.CoreIDs {
					if id != i {
						t.Errorf("CoreIDs[%d] = %d, want %d", i, id, i)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, info)
		})
	}
}
