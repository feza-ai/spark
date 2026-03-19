package executor

import (
	"slices"
	"testing"
)

func TestBuildImageExistsArgs(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     []string
	}{
		{"simple image", "alpine:latest", []string{"image", "exists", "alpine:latest"}},
		{"full registry path", "docker.io/library/nginx:1.25", []string{"image", "exists", "docker.io/library/nginx:1.25"}},
		{"image with digest", "alpine@sha256:abc123", []string{"image", "exists", "alpine@sha256:abc123"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildImageExistsArgs(tt.imageRef)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildImageExistsArgs(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}

func TestBuildPullArgs(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     []string
	}{
		{"simple image", "alpine:latest", []string{"pull", "alpine:latest"}},
		{"full registry path", "docker.io/library/nginx:1.25", []string{"pull", "docker.io/library/nginx:1.25"}},
		{"image with digest", "alpine@sha256:abc123", []string{"pull", "alpine@sha256:abc123"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPullArgs(tt.imageRef)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildPullArgs(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}
