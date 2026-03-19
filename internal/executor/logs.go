package executor

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
)

// buildLogsArgs constructs the arguments for a podman pod logs command.
func buildLogsArgs(podName string) []string {
	return []string{"pod", "logs", "--follow", podName}
}

// streamFromReader reads lines from r and sends them to the returned channel.
// The channel is closed when the reader is exhausted or the context is cancelled.
func streamFromReader(ctx context.Context, r io.Reader) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			case ch <- scanner.Text():
			}
		}
	}()
	return ch
}

// StreamLogs starts streaming stdout/stderr from a running pod.
// Returns a channel of log lines and an error if the command fails to start.
// The channel is closed when the context is cancelled or the pod exits.
func StreamLogs(ctx context.Context, podName string) (<-chan string, error) {
	args := buildLogsArgs(podName)
	slog.Info("started log streaming", "pod", podName)

	cmd := exec.CommandContext(ctx, "podman", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout pipe

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ch := streamFromReader(ctx, stdout)

	// Wait for the command to finish in a separate goroutine so we don't leak.
	go func() {
		_ = cmd.Wait()
		slog.Info("log streaming ended", "pod", podName)
	}()

	return ch, nil
}
