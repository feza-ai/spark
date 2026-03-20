package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// buildImageExistsArgs constructs the arguments for checking if an image exists locally.
func buildImageExistsArgs(imageRef string) []string {
	return []string{"image", "exists", imageRef}
}

// buildPullArgs constructs the arguments for pulling a container image.
func buildPullArgs(imageRef string) []string {
	return []string{"pull", imageRef}
}

// buildListImagesArgs constructs the arguments for listing images.
func buildListImagesArgs() []string {
	return []string{"images", "--format", "json"}
}

// ListImages returns all container images stored locally.
func (p *PodmanExecutor) ListImages(ctx context.Context) ([]ImageInfo, error) {
	args := buildListImagesArgs()
	slog.Info("listing images", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman images: %w: %s", err, out)
	}
	return parseImagesJSON(out)
}

// parseImagesJSON parses the JSON output of podman images.
func parseImagesJSON(data []byte) ([]ImageInfo, error) {
	var raw []struct {
		ID      string   `json:"Id"`
		Names   []string `json:"Names"`
		Size    int64    `json:"Size"`
		Created int64    `json:"Created"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse image list: %w", err)
	}
	result := make([]ImageInfo, len(raw))
	for i, r := range raw {
		result[i] = ImageInfo{
			ID:      r.ID,
			Names:   r.Names,
			Size:    formatSize(r.Size),
			Created: fmt.Sprintf("%d", r.Created),
		}
	}
	return result, nil
}

// formatSize converts bytes to a human-readable size string.
func formatSize(bytes int64) string {
	const (
		mb = 1024 * 1024
		gb = 1024 * mb
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// PullImage pulls a container image by name.
func (p *PodmanExecutor) PullImage(ctx context.Context, name string) error {
	args := buildPullArgs(name)
	slog.Info("pulling image", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pull %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureImage checks if a container image is available locally.
// If not found, it pulls the image.
func EnsureImage(ctx context.Context, imageRef string) error {
	args := buildImageExistsArgs(imageRef)
	slog.Info("checking image", "cmd", "podman", "args", args)
	_, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err == nil {
		slog.Info("image already present", "image", imageRef)
		return nil
	}

	// Image not present locally, pull it.
	pullArgs := buildPullArgs(imageRef)
	slog.Info("pulling image", "cmd", "podman", "args", pullArgs)
	out, err := exec.CommandContext(ctx, "podman", pullArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pull %s: %w: %s", imageRef, err, strings.TrimSpace(string(out)))
	}
	slog.Info("image pulled", "image", imageRef)
	return nil
}
