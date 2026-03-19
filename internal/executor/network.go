package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

const DefaultNetwork = "spark-net"

// EnsureNetwork creates the podman network if it doesn't already exist.
// This is idempotent - ignores "already exists" errors.
func EnsureNetwork(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "podman", "network", "create", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		stderr := string(output)
		if isAlreadyExistsError(stderr) {
			slog.Info("spark network already exists", "name", name)
			return nil
		}
		return fmt.Errorf("creating podman network %q: %w: %s", name, err, stderr)
	}
	slog.Info("spark network created", "name", name)
	return nil
}

// isAlreadyExistsError checks whether the command output indicates that the
// network already exists.
func isAlreadyExistsError(stderr string) bool {
	return strings.Contains(stderr, "already exists")
}
