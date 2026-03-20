package executor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"time"
)

// ExecProbe runs a command inside a container via podman exec and returns the
// exit code. An exit code of 0 indicates a healthy probe. The command is
// bounded by the supplied timeout.
func (p *PodmanExecutor) ExecProbe(ctx context.Context, podName string, containerName string, command []string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := buildExecArgs(podName, containerName, command)
	slog.Info("exec probe", "cmd", "podman", "args", args)

	cmd := exec.CommandContext(ctx, "podman", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return -1, fmt.Errorf("exec probe timed out after %s", timeout)
		}
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), nil
		}
		return -1, fmt.Errorf("exec probe: %w: %s", err, out)
	}
	return 0, nil
}

// HTTPProbe makes an HTTP GET request to http://localhost:<port><path> and
// returns nil when the response status is in the 200–399 range. The request
// is bounded by the supplied timeout.
func (p *PodmanExecutor) HTTPProbe(ctx context.Context, port int, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("http://localhost:%d%s", port, path)
	slog.Info("http probe", "url", url)

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("http probe request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("http probe: unexpected status %d", resp.StatusCode)
	}
	return nil
}
