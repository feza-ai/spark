package gpu

import (
	"errors"
	"testing"
)

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    GPUInfo
		wantErr error
	}{
		{
			name:  "single GPU",
			input: "NVIDIA GH200 120GB, 102400, 512, 35\n",
			want: GPUInfo{
				Model:              "NVIDIA GH200 120GB",
				MemoryTotalMB:      102400,
				MemoryUsedMB:       512,
				UtilizationPercent: 35,
				GPUCount:           1,
			},
		},
		{
			name:  "multiple GPUs",
			input: "NVIDIA A100, 81920, 1024, 50\nNVIDIA A100, 81920, 2048, 70\n",
			want: GPUInfo{
				Model:              "NVIDIA A100",
				MemoryTotalMB:      163840,
				MemoryUsedMB:       3072,
				UtilizationPercent: 60,
				GPUCount:           2,
			},
		},
		{
			name:    "empty output",
			input:   "",
			wantErr: ErrNoGPU,
		},
		{
			name:    "whitespace only",
			input:   "  \n  \n",
			wantErr: ErrNoGPU,
		},
		{
			name:    "malformed lines only",
			input:   "bad data\n",
			wantErr: ErrNoGPU,
		},
		{
			name:  "mixed valid and malformed",
			input: "bad data\nNVIDIA A100, 81920, 1024, 50\n",
			want: GPUInfo{
				Model:              "NVIDIA A100",
				MemoryTotalMB:      81920,
				MemoryUsedMB:       1024,
				UtilizationPercent: 50,
				GPUCount:           1,
			},
		},
		{
			name:  "trailing whitespace in fields",
			input: "  NVIDIA T4 , 16384 , 256 , 10  \n",
			want: GPUInfo{
				Model:              "NVIDIA T4",
				MemoryTotalMB:      16384,
				MemoryUsedMB:       256,
				UtilizationPercent: 10,
				GPUCount:           1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCSV(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("parseCSV() error = %v, wantErr %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCSV() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseCSV() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetect_NoGPU(t *testing.T) {
	// On machines without nvidia-smi, Detect should return ErrNoGPU.
	// This test is useful in CI where no GPU is available.
	_, err := Detect()
	if err == nil {
		// nvidia-smi is available; skip the error check.
		t.Skip("nvidia-smi is available; skipping ErrNoGPU test")
	}
	if !errors.Is(err, ErrNoGPU) {
		t.Fatalf("Detect() error = %v, want ErrNoGPU", err)
	}
}
