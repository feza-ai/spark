package gpu

import (
	"errors"
	"reflect"
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
				DeviceIDs:          []int{0},
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
				DeviceIDs:          []int{0, 1},
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
				DeviceIDs:          []int{0},
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
				DeviceIDs:          []int{0},
			},
		},
		{
			name:  "unified memory GPU with all N/A memory values",
			input: "NVIDIA GB10, [N/A], [N/A], 0\n",
			want: GPUInfo{
				Model:              "NVIDIA GB10",
				MemoryTotalMB:      0,
				MemoryUsedMB:       0,
				UtilizationPercent: 0,
				GPUCount:           1,
				DeviceIDs:          []int{0},
			},
		},
		{
			name:  "N/A without brackets",
			input: "NVIDIA GB10, N/A, N/A, 5\n",
			want: GPUInfo{
				Model:              "NVIDIA GB10",
				MemoryTotalMB:      0,
				MemoryUsedMB:       0,
				UtilizationPercent: 5,
				GPUCount:           1,
				DeviceIDs:          []int{0},
			},
		},
		{
			name:  "partial N/A with valid utilization",
			input: "NVIDIA GB10, [N/A], [N/A], 42\n",
			want: GPUInfo{
				Model:              "NVIDIA GB10",
				MemoryTotalMB:      0,
				MemoryUsedMB:       0,
				UtilizationPercent: 42,
				GPUCount:           1,
				DeviceIDs:          []int{0},
			},
		},
		{
			name:  "mixed normal and unified memory GPUs",
			input: "NVIDIA A100, 81920, 1024, 50\nNVIDIA GB10, [N/A], [N/A], 0\n",
			want: GPUInfo{
				Model:              "NVIDIA A100",
				MemoryTotalMB:      81920,
				MemoryUsedMB:       1024,
				UtilizationPercent: 25,
				GPUCount:           2,
				DeviceIDs:          []int{0, 1},
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
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCSV() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDeviceIDs_SingleGPU(t *testing.T) {
	info, err := parseCSV("NVIDIA A100, 81920, 1024, 50\n")
	if err != nil {
		t.Fatalf("parseCSV() unexpected error: %v", err)
	}
	want := []int{0}
	if !reflect.DeepEqual(info.DeviceIDs, want) {
		t.Errorf("DeviceIDs = %v, want %v", info.DeviceIDs, want)
	}
}

func TestDeviceIDs_MultiGPU(t *testing.T) {
	input := "NVIDIA A100, 81920, 1024, 50\n" +
		"NVIDIA A100, 81920, 2048, 60\n" +
		"NVIDIA A100, 81920, 512, 40\n" +
		"NVIDIA A100, 81920, 1024, 70\n"
	info, err := parseCSV(input)
	if err != nil {
		t.Fatalf("parseCSV() unexpected error: %v", err)
	}
	want := []int{0, 1, 2, 3}
	if !reflect.DeepEqual(info.DeviceIDs, want) {
		t.Errorf("DeviceIDs = %v, want %v", info.DeviceIDs, want)
	}
}

func TestIsNA(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"[N/A]", true},
		{"N/A", true},
		{" [N/A] ", true},
		{" N/A ", true},
		{"not available", true},
		{"Not Available", true},
		{"0", false},
		{"1024", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isNA(tt.input); got != tt.want {
			t.Errorf("isNA(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseIntOrNA(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"1024", 1024, false},
		{"0", 0, false},
		{"[N/A]", 0, false},
		{"N/A", 0, false},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseIntOrNA(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseIntOrNA(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseIntOrNA(%q) = %d, want %d", tt.input, got, tt.want)
		}
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
