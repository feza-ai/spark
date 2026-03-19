package bus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeLogSource returns a logSource function that sends the given lines then closes.
func fakeLogSource(lines []string) func(ctx context.Context, name string) (<-chan string, error) {
	return func(ctx context.Context, name string) (<-chan string, error) {
		ch := make(chan string)
		go func() {
			defer close(ch)
			for _, line := range lines {
				select {
				case <-ctx.Done():
					return
				case ch <- line:
				}
			}
		}()
		return ch, nil
	}
}

func TestLogStreamerPublishesToCorrectSubject(t *testing.T) {
	sb := NewStubBus()
	ls := NewLogStreamer(sb)

	lines := []string{"hello", "world"}
	ls.StartStream("mypod", fakeLogSource(lines))

	// Wait for the batch to be flushed (channel closes quickly, then flush happens).
	time.Sleep(300 * time.Millisecond)

	msgs := sb.Published()
	if len(msgs) == 0 {
		t.Fatal("expected at least one published message, got none")
	}

	for _, msg := range msgs {
		if !strings.HasPrefix(msg.Subject, "log.spark.mypod") {
			t.Errorf("subject = %q, want prefix %q", msg.Subject, "log.spark.mypod")
		}

		var batch LogBatch
		if err := json.Unmarshal(msg.Data, &batch); err != nil {
			t.Fatalf("failed to unmarshal log batch: %v", err)
		}
		if len(batch.Lines) == 0 {
			t.Error("expected non-empty lines in batch")
		}
		if batch.Timestamp == "" {
			t.Error("expected non-empty timestamp")
		}
	}

	// Verify all lines were published across batches.
	var allLines []string
	for _, msg := range msgs {
		var batch LogBatch
		json.Unmarshal(msg.Data, &batch)
		allLines = append(allLines, batch.Lines...)
	}
	if len(allLines) != 2 {
		t.Errorf("expected 2 total lines, got %d: %v", len(allLines), allLines)
	}
}

func TestLogStreamerBatching(t *testing.T) {
	sb := NewStubBus()
	ls := NewLogStreamer(sb)

	// Send 15 lines — should produce at least 2 batches (10 + 5).
	lines := make([]string, 15)
	for i := range lines {
		lines[i] = "line"
	}
	ls.StartStream("batchpod", fakeLogSource(lines))

	time.Sleep(300 * time.Millisecond)

	msgs := sb.Published()
	if len(msgs) < 2 {
		t.Errorf("expected at least 2 batches for 15 lines, got %d", len(msgs))
	}

	// First batch should have exactly 10 lines.
	var first LogBatch
	json.Unmarshal(msgs[0].Data, &first)
	if len(first.Lines) != 10 {
		t.Errorf("first batch lines = %d, want 10", len(first.Lines))
	}
}

func TestLogStreamerStopStream(t *testing.T) {
	sb := NewStubBus()
	ls := NewLogStreamer(sb)

	// Use a slow source that sends a line, waits, then sends more.
	slowSource := func(ctx context.Context, name string) (<-chan string, error) {
		ch := make(chan string)
		go func() {
			defer close(ch)
			select {
			case ch <- "before-stop":
			case <-ctx.Done():
				return
			}
			// Wait so we can stop the stream before more lines.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
			select {
			case ch <- "after-stop":
			case <-ctx.Done():
				return
			}
		}()
		return ch, nil
	}

	ls.StartStream("stoppod", slowSource)

	// Let the first line arrive and flush.
	time.Sleep(200 * time.Millisecond)
	ls.StopStream("stoppod")

	countBefore := len(sb.Published())

	// Wait to ensure no more messages arrive.
	time.Sleep(300 * time.Millisecond)
	countAfter := len(sb.Published())

	if countAfter != countBefore {
		t.Errorf("messages published after stop: before=%d after=%d", countBefore, countAfter)
	}

	// Verify "after-stop" was never published.
	for _, msg := range sb.Published() {
		var batch LogBatch
		json.Unmarshal(msg.Data, &batch)
		for _, line := range batch.Lines {
			if line == "after-stop" {
				t.Error("received line that should not have been published after stop")
			}
		}
	}
}

func TestLogStreamerStopAll(t *testing.T) {
	sb := NewStubBus()
	ls := NewLogStreamer(sb)

	blockingSource := func(ctx context.Context, name string) (<-chan string, error) {
		ch := make(chan string)
		go func() {
			defer close(ch)
			<-ctx.Done()
		}()
		return ch, nil
	}

	ls.StartStream("pod1", blockingSource)
	ls.StartStream("pod2", blockingSource)

	time.Sleep(50 * time.Millisecond)
	ls.StopAll()

	ls.mu.Lock()
	remaining := len(ls.streams)
	ls.mu.Unlock()

	if remaining != 0 {
		t.Errorf("expected 0 active streams after StopAll, got %d", remaining)
	}
}
