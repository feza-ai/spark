package executor

import (
	"context"
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

// EnsureImage checks if a container image is available locally.
// If not found, it pulls the image.
func EnsureImage(ctx context.Context, imageRef string) error {
	args := buildImageExistsArgs(imageRef)
	slog.Info("checking image", "cmd", "podman", "args", args)
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err == nil {
		slog.Info("image already present", "image", imageRef)
		return nil
	}

	// Image not present locally, pull it.
	pullArgs := buildPullArgs(imageRef)
	slog.Info("pulling image", "cmd", "podman", "args", pullArgs)
	out, err = exec.CommandContext(ctx, "podman", pullArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman pull %s: %w: %s", imageRef, err, strings.TrimSpace(string(out)))
	}
	slog.Info("image pulled", "image", imageRef)
	return nil
}
