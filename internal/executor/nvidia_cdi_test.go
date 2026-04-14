package executor

import (
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

func TestPodSpecRequestsGPU(t *testing.T) {
	tests := []struct {
		name string
		spec manifest.PodSpec
		want bool
	}{
		{
			name: "no gpu requested",
			spec: manifest.PodSpec{
				Containers: []manifest.ContainerSpec{
					{Name: "a", Resources: manifest.ResourceRequirements{Limits: manifest.ResourceList{CPUMillis: 1000}}},
				},
			},
			want: false,
		},
		{
			name: "gpu count in main container",
			spec: manifest.PodSpec{
				Containers: []manifest.ContainerSpec{
					{Name: "a", Resources: manifest.ResourceRequirements{Limits: manifest.ResourceList{GPUCount: 1}}},
				},
			},
			want: true,
		},
		{
			name: "gpu memory in main container",
			spec: manifest.PodSpec{
				Containers: []manifest.ContainerSpec{
					{Name: "a", Resources: manifest.ResourceRequirements{Limits: manifest.ResourceList{GPUMemoryMB: 8192}}},
				},
			},
			want: true,
		},
		{
			name: "gpu requested by init container",
			spec: manifest.PodSpec{
				InitContainers: []manifest.ContainerSpec{
					{Name: "setup", Resources: manifest.ResourceRequirements{Limits: manifest.ResourceList{GPUCount: 1}}},
				},
				Containers: []manifest.ContainerSpec{
					{Name: "a"},
				},
			},
			want: true,
		},
		{
			name: "empty pod",
			spec: manifest.PodSpec{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := podSpecRequestsGPU(tt.spec); got != tt.want {
				t.Errorf("podSpecRequestsGPU() = %v, want %v", got, tt.want)
			}
		})
	}
}
