package executor

import (
	"slices"
	"testing"
)

func TestBuildListImagesArgs(t *testing.T) {
	got := buildListImagesArgs()
	want := []string{"images", "--format", "json"}
	if !slices.Equal(got, want) {
		t.Errorf("buildListImagesArgs() = %v, want %v", got, want)
	}
}

func TestListImages_ParseJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []ImageInfo
		wantErr bool
	}{
		{
			name:  "multiple images",
			input: `[{"Id":"abc123","Names":["docker.io/library/alpine:latest"],"Size":7654321,"Created":1710000000},{"Id":"def456","Names":["docker.io/library/nginx:1.25","nginx:latest"],"Size":1234567890,"Created":1710100000}]`,
			want: []ImageInfo{
				{ID: "abc123", Names: []string{"docker.io/library/alpine:latest"}, Size: "7.3 MB", Created: "1710000000"},
				{ID: "def456", Names: []string{"docker.io/library/nginx:1.25", "nginx:latest"}, Size: "1.1 GB", Created: "1710100000"},
			},
		},
		{
			name:  "empty list",
			input: `[]`,
			want:  []ImageInfo{},
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:  "small image size in bytes",
			input: `[{"Id":"tiny","Names":["scratch:latest"],"Size":1024,"Created":1710000000}]`,
			want: []ImageInfo{
				{ID: "tiny", Names: []string{"scratch:latest"}, Size: "1024 B", Created: "1710000000"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseImagesJSON([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseImagesJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d images, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].ID != tt.want[i].ID {
					t.Errorf("image[%d].ID = %q, want %q", i, got[i].ID, tt.want[i].ID)
				}
				if !slices.Equal(got[i].Names, tt.want[i].Names) {
					t.Errorf("image[%d].Names = %v, want %v", i, got[i].Names, tt.want[i].Names)
				}
				if got[i].Size != tt.want[i].Size {
					t.Errorf("image[%d].Size = %q, want %q", i, got[i].Size, tt.want[i].Size)
				}
				if got[i].Created != tt.want[i].Created {
					t.Errorf("image[%d].Created = %q, want %q", i, got[i].Created, tt.want[i].Created)
				}
			}
		})
	}
}

func TestPullImage_Args(t *testing.T) {
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

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1024, "1024 B"},
		{1048576, "1.0 MB"},
		{7654321, "7.3 MB"},
		{1073741824, "1.0 GB"},
		{1234567890, "1.1 GB"},
	}
	for _, tt := range tests {
		got := formatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

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
