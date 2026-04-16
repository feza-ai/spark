package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

func TestHandleNode(t *testing.T) {
	hostname, _ := os.Hostname()

	tests := []struct {
		name    string
		gpuInfo *gpu.GPUInfo
		sysInfo *gpu.SystemInfo
		check   func(t *testing.T, resp NodeResponse)
	}{
		{
			name:    "nil gpu and sys info returns minimal response",
			gpuInfo: nil,
			sysInfo: nil,
			check: func(t *testing.T, resp NodeResponse) {
				t.Helper()
				if resp.Hostname != hostname {
					t.Errorf("hostname: got %q, want %q", resp.Hostname, hostname)
				}
				if resp.OS != runtime.GOOS {
					t.Errorf("os: got %q, want %q", resp.OS, runtime.GOOS)
				}
				if resp.Arch != runtime.GOARCH {
					t.Errorf("arch: got %q, want %q", resp.Arch, runtime.GOARCH)
				}
				if resp.CPUCores != runtime.NumCPU() {
					t.Errorf("cpu_cores: got %d, want %d", resp.CPUCores, runtime.NumCPU())
				}
				if resp.MemoryTotalMB != 0 {
					t.Errorf("memory_total_mb: got %d, want 0", resp.MemoryTotalMB)
				}
				if resp.GPUCount != 0 {
					t.Errorf("gpu_count: got %d, want 0", resp.GPUCount)
				}
				if len(resp.GPUDeviceIDs) != 0 {
					t.Errorf("gpu_device_ids: got %v, want empty", resp.GPUDeviceIDs)
				}
			},
		},
		{
			name: "with gpu and sys info",
			gpuInfo: &gpu.GPUInfo{
				Model:         "NVIDIA GH200",
				MemoryTotalMB: 131072,
				GPUCount:      1,
				DeviceIDs:     []int{0},
			},
			sysInfo: &gpu.SystemInfo{
				CPUMillis:     72000,
				MemoryTotalMB: 131072,
			},
			check: func(t *testing.T, resp NodeResponse) {
				t.Helper()
				if resp.GPUModel != "NVIDIA GH200" {
					t.Errorf("gpu_model: got %q, want %q", resp.GPUModel, "NVIDIA GH200")
				}
				if resp.GPUCount != 1 {
					t.Errorf("gpu_count: got %d, want 1", resp.GPUCount)
				}
				if resp.GPUMemoryMB != 131072 {
					t.Errorf("gpu_memory_mb: got %d, want 131072", resp.GPUMemoryMB)
				}
				if len(resp.GPUDeviceIDs) != 1 || resp.GPUDeviceIDs[0] != 0 {
					t.Errorf("gpu_device_ids: got %v, want [0]", resp.GPUDeviceIDs)
				}
				if resp.MemoryTotalMB != 131072 {
					t.Errorf("memory_total_mb: got %d, want 131072", resp.MemoryTotalMB)
				}
			},
		},
		{
			name: "multiple GPUs",
			gpuInfo: &gpu.GPUInfo{
				Model:         "NVIDIA A100",
				MemoryTotalMB: 81920,
				GPUCount:      2,
				DeviceIDs:     []int{0, 1},
			},
			sysInfo: &gpu.SystemInfo{
				CPUMillis:     64000,
				MemoryTotalMB: 262144,
			},
			check: func(t *testing.T, resp NodeResponse) {
				t.Helper()
				if resp.GPUCount != 2 {
					t.Errorf("gpu_count: got %d, want 2", resp.GPUCount)
				}
				if len(resp.GPUDeviceIDs) != 2 {
					t.Errorf("gpu_device_ids length: got %d, want 2", len(resp.GPUDeviceIDs))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := state.NewPodStore()
			tracker := scheduler.NewResourceTracker(
				scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 32768},
				scheduler.Resources{},
				nil, 0,
			)
			srv := NewServer(store, tracker, nil, nil, nil, nil, nil, "", nil, tt.gpuInfo, tt.sysInfo, "test")

			req := httptest.NewRequest(http.MethodGet, "/api/v1/node", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200", rec.Code)
			}

			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
			}

			var resp NodeResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			tt.check(t, resp)
		})
	}
}

func TestHandleNode_IncludesCoreFields(t *testing.T) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, Cores: []int{0, 1, 2, 3, 4, 5, 6, 7}},
		scheduler.Resources{Cores: []int{0, 1}, CPUMillis: 2000},
		nil, 0,
	)
	srv := NewServer(store, tracker, nil, nil, nil, nil, nil, "", nil, nil, nil, "test")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/node", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var resp NodeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	wantReserved := []int{0, 1}
	if len(resp.CPUReservedCores) != len(wantReserved) {
		t.Fatalf("cpu_reserved_cores: got %v, want %v", resp.CPUReservedCores, wantReserved)
	}
	for i, c := range wantReserved {
		if resp.CPUReservedCores[i] != c {
			t.Errorf("cpu_reserved_cores[%d]: got %d, want %d", i, resp.CPUReservedCores[i], c)
		}
	}

	wantAlloc := []int{2, 3, 4, 5, 6, 7}
	if len(resp.CPUAllocatableCores) != len(wantAlloc) {
		t.Fatalf("cpu_allocatable_cores: got %v, want %v", resp.CPUAllocatableCores, wantAlloc)
	}
	for i, c := range wantAlloc {
		if resp.CPUAllocatableCores[i] != c {
			t.Errorf("cpu_allocatable_cores[%d]: got %d, want %d", i, resp.CPUAllocatableCores[i], c)
		}
	}
}

func TestHandleNodeMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/node", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("POST /api/v1/node should not return 200")
	}
}
