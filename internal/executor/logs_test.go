package executor

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildLogsArgs(t *testing.T) {
	got := buildLogsArgs("my-pod")
	want := []string{"pod", "logs", "--follow", "my-pod"}
	if !slices.Equal(got, want) {
		t.Errorf("buildLogsArgs = %v, want %v", got, want)
	}
}

func TestStreamFromReader_Lines(t *testing.T) {
	input := "line1\nline2\nline3\n"
	r := strings.NewReader(input)
	ctx := context.Background()

	ch := streamFromReader(ctx, r)

	var got []string
	for line := range ch {
		got = append(got, line)
	}

	want := []string{"line1", "line2", "line3"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStreamFromReader_EmptyInput(t *testing.T) {
	r := strings.NewReader("")
	ctx := context.Background()

	ch := streamFromReader(ctx, r)

	var got []string
	for line := range ch {
		got = append(got, line)
	}
	if len(got) != 0 {
		t.Errorf("expected no lines, got %v", got)
	}
}

func TestStreamFromReader_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Use an io.Pipe: writer side controls when data arrives.
	pr, pw := io.Pipe()

	ch := streamFromReader(ctx, pr)

	// Send one line.
	_, _ = pw.Write([]byte("first\n"))

	select {
	case line := <-ch:
		if line != "first" {
			t.Errorf("first line = %q, want %q", line, "first")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first line")
	}

	// Cancel context; the goroutine should stop trying to send.
	cancel()
	// Close the writer so the scanner unblocks from Read.
	pw.Close()

	// Channel should close.
	select {
	case _, ok := <-ch:
		if ok {
			// Possible race: one more value drained. Channel must still close.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Error("channel should be closed after context cancellation")
				}
			case <-time.After(time.Second):
				t.Error("timed out waiting for channel to close")
			}
		}
		// ok==false means channel closed, which is what we want.
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close after cancel")
	}
}
