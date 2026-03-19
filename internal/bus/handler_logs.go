package bus

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// LogBatch is the JSON payload published for each batch of log lines.
type LogBatch struct {
	Lines     []string `json:"lines"`
	Timestamp string   `json:"timestamp"`
}

// LogStreamer manages log streaming for running pods.
type LogStreamer struct {
	bus     Bus
	mu      sync.Mutex
	streams map[string]context.CancelFunc // podName -> cancel
}

// NewLogStreamer creates a new LogStreamer.
func NewLogStreamer(b Bus) *LogStreamer {
	return &LogStreamer{
		bus:     b,
		streams: make(map[string]context.CancelFunc),
	}
}

// StartStream begins streaming logs for a pod to log.spark.{podName}.
// logSource is a function that returns a log line channel (matches StreamLogs signature).
func (ls *LogStreamer) StartStream(podName string, logSource func(ctx context.Context, name string) (<-chan string, error)) {
	ls.mu.Lock()
	if cancel, ok := ls.streams[podName]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ls.streams[podName] = cancel
	ls.mu.Unlock()

	ch, err := logSource(ctx, podName)
	if err != nil {
		slog.Error("failed to start log stream", "pod", podName, "err", err)
		cancel()
		ls.mu.Lock()
		delete(ls.streams, podName)
		ls.mu.Unlock()
		return
	}

	subject := "log.spark." + podName

	go func() {
		defer func() {
			ls.mu.Lock()
			delete(ls.streams, podName)
			ls.mu.Unlock()
			slog.Info("log stream goroutine exited", "pod", podName)
		}()

		const (
			maxBatch   = 10
			flushDelay = 100 * time.Millisecond
		)

		var batch []string
		timer := time.NewTimer(flushDelay)
		timer.Stop()

		flush := func() {
			if len(batch) == 0 {
				return
			}
			payload, err := json.Marshal(LogBatch{
				Lines:     batch,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				slog.Error("failed to marshal log batch", "pod", podName, "err", err)
			} else if err := ls.bus.Publish(subject, payload); err != nil {
				slog.Error("failed to publish log batch", "pod", podName, "err", err)
			}
			batch = nil
		}

		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case line, ok := <-ch:
				if !ok {
					flush()
					return
				}
				batch = append(batch, line)
				if len(batch) >= maxBatch {
					timer.Stop()
					flush()
				} else if len(batch) == 1 {
					timer.Reset(flushDelay)
				}
			case <-timer.C:
				flush()
			}
		}
	}()
}

// StopStream stops log streaming for a pod.
func (ls *LogStreamer) StopStream(podName string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if cancel, ok := ls.streams[podName]; ok {
		cancel()
		delete(ls.streams, podName)
	}
}

// StopAll stops all active log streams.
func (ls *LogStreamer) StopAll() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for name, cancel := range ls.streams {
		cancel()
		delete(ls.streams, name)
	}
}
