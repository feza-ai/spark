package manifest

import "testing"

func TestTotalRequests_Empty(t *testing.T) {
	p := &PodSpec{}
	got := p.TotalRequests()
	if got.CPUMillis != 0 || got.MemoryMB != 0 || got.GPUMemoryMB != 0 {
		t.Errorf("empty pod: want zero resources, got %+v", got)
	}
}

func TestTotalRequests_SingleContainer(t *testing.T) {
	p := &PodSpec{
		Containers: []ContainerSpec{
			{
				Name:  "app",
				Image: "nginx",
				Resources: ResourceRequirements{
					Requests: ResourceList{CPUMillis: 500, MemoryMB: 256, GPUMemoryMB: 1024},
				},
			},
		},
	}
	got := p.TotalRequests()
	if got.CPUMillis != 500 || got.MemoryMB != 256 || got.GPUMemoryMB != 1024 {
		t.Errorf("single container: got %+v", got)
	}
}

func TestTotalRequests_MultipleContainers(t *testing.T) {
	p := &PodSpec{
		Containers: []ContainerSpec{
			{
				Name: "app",
				Resources: ResourceRequirements{
					Requests: ResourceList{CPUMillis: 500, MemoryMB: 256, GPUMemoryMB: 2048},
				},
			},
			{
				Name: "sidecar",
				Resources: ResourceRequirements{
					Requests: ResourceList{CPUMillis: 100, MemoryMB: 64, GPUMemoryMB: 0},
				},
			},
			{
				Name: "gpu-worker",
				Resources: ResourceRequirements{
					Requests: ResourceList{CPUMillis: 1000, MemoryMB: 512, GPUMemoryMB: 4096},
				},
			},
		},
	}
	got := p.TotalRequests()
	if got.CPUMillis != 1600 {
		t.Errorf("CPUMillis: want 1600, got %d", got.CPUMillis)
	}
	if got.MemoryMB != 832 {
		t.Errorf("MemoryMB: want 832, got %d", got.MemoryMB)
	}
	if got.GPUMemoryMB != 6144 {
		t.Errorf("GPUMemoryMB: want 6144, got %d", got.GPUMemoryMB)
	}
}
