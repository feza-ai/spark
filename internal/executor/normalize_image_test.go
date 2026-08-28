package executor

import (
	"testing"

	"github.com/feza-ai/spark/internal/manifest"
)

// Issue #45: podman rejects Docker Hub short names when no unqualified-search
// registries are configured. Refs must be qualified the way Kubernetes and
// containerd do it.
func TestNormalizeImage(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// The issue's repro
		{"pgvector/pgvector:pg16", "docker.io/pgvector/pgvector:pg16"},
		// Bare names get the library/ namespace
		{"alpine:latest", "docker.io/library/alpine:latest"},
		{"alpine", "docker.io/library/alpine"},
		{"bash:5", "docker.io/library/bash:5"},
		// Digest refs
		{"alpine@sha256:abc123", "docker.io/library/alpine@sha256:abc123"},
		{"pgvector/pgvector@sha256:abc123", "docker.io/pgvector/pgvector@sha256:abc123"},
		// Already qualified — untouched
		{"docker.io/library/alpine:latest", "docker.io/library/alpine:latest"},
		{"docker.io/pgvector/pgvector:pg16", "docker.io/pgvector/pgvector:pg16"},
		{"ghcr.io/feza-ai/wolf:latest", "ghcr.io/feza-ai/wolf:latest"},
		{"nvcr.io/nvidia/pytorch:26.02-py3", "nvcr.io/nvidia/pytorch:26.02-py3"},
		{"quay.io/podman/hello", "quay.io/podman/hello"},
		// Registry with port
		{"registry:5000/myimage:v1", "registry:5000/myimage:v1"},
		{"localhost/myimage", "localhost/myimage"},
		{"localhost:5000/mymodel:latest", "localhost:5000/mymodel:latest"},
		// Deep paths under a non-registry first component still qualify
		{"myorg/myteam/app:v2", "docker.io/myorg/myteam/app:v2"},
		// Empty passes through
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeImage(tt.in); got != tt.want {
				t.Errorf("normalizeImage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildRunArgs_NormalizesImage(t *testing.T) {
	spec := manifest.ContainerSpec{
		Name:  "db",
		Image: "pgvector/pgvector:pg16",
	}
	args := buildRunArgs("mypod", spec, nil, "spark-net", true, nil, nil)
	found := false
	for _, a := range args {
		if a == "docker.io/pgvector/pgvector:pg16" {
			found = true
		}
		if a == "pgvector/pgvector:pg16" {
			t.Errorf("unnormalized image ref in run args: %v", args)
		}
	}
	if !found {
		t.Errorf("normalized image ref missing from run args: %v", args)
	}
}
