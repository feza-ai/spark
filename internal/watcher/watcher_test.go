package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// collectEvents runs Watch for a brief period and returns collected events.
func collectEvents(t *testing.T, dir string, setup func(), mutate func()) []WatchEvent {
	t.Helper()

	// Run setup before watcher starts (initial snapshot).
	if setup != nil {
		setup()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []WatchEvent

	go Watch(ctx, dir, func(e WatchEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	// Let the watcher take its initial snapshot.
	time.Sleep(100 * time.Millisecond)

	// Perform mutation after watcher has started.
	if mutate != nil {
		mutate()
	}

	// Wait for at least one poll cycle.
	time.Sleep(pollInterval + 2*time.Second)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	return events
}

func TestWatch(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(dir string)
		mutate    func(dir string)
		wantType  EventType
		wantCount int
		checkPath func(t *testing.T, events []WatchEvent, dir string)
	}{
		{
			name: "detect new YAML file",
			mutate: func(dir string) {
				os.WriteFile(filepath.Join(dir, "pod.yaml"), []byte("apiVersion: v1"), 0644)
			},
			wantType:  Added,
			wantCount: 1,
			checkPath: func(t *testing.T, events []WatchEvent, dir string) {
				if events[0].Path != filepath.Join(dir, "pod.yaml") {
					t.Errorf("path = %s, want %s", events[0].Path, filepath.Join(dir, "pod.yaml"))
				}
				if string(events[0].Content) != "apiVersion: v1" {
					t.Errorf("content = %q, want %q", events[0].Content, "apiVersion: v1")
				}
			},
		},
		{
			name: "detect new YML file",
			mutate: func(dir string) {
				os.WriteFile(filepath.Join(dir, "pod.yml"), []byte("kind: Pod"), 0644)
			},
			wantType:  Added,
			wantCount: 1,
		},
		{
			name: "detect removed file",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "remove-me.yaml"), []byte("data"), 0644)
			},
			mutate: func(dir string) {
				os.Remove(filepath.Join(dir, "remove-me.yaml"))
			},
			wantType:  Removed,
			wantCount: 1,
			checkPath: func(t *testing.T, events []WatchEvent, dir string) {
				if events[0].Content != nil {
					t.Errorf("removed event should have nil content, got %q", events[0].Content)
				}
			},
		},
		{
			name: "detect modified file",
			setup: func(dir string) {
				os.WriteFile(filepath.Join(dir, "mod.yaml"), []byte("version: 1"), 0644)
			},
			mutate: func(dir string) {
				os.WriteFile(filepath.Join(dir, "mod.yaml"), []byte("version: 2"), 0644)
			},
			wantType:  Modified,
			wantCount: 1,
			checkPath: func(t *testing.T, events []WatchEvent, dir string) {
				if string(events[0].Content) != "version: 2" {
					t.Errorf("content = %q, want %q", events[0].Content, "version: 2")
				}
			},
		},
		{
			name: "ignore non-YAML files",
			mutate: func(dir string) {
				os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644)
				os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0644)
				os.WriteFile(filepath.Join(dir, "script.sh"), []byte("#!/bin/bash"), 0644)
			},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			var setupFn func()
			if tc.setup != nil {
				setupFn = func() { tc.setup(dir) }
			}

			var mutateFn func()
			if tc.mutate != nil {
				mutateFn = func() { tc.mutate(dir) }
			}

			events := collectEvents(t, dir, setupFn, mutateFn)

			if len(events) != tc.wantCount {
				t.Fatalf("got %d events, want %d: %+v", len(events), tc.wantCount, events)
			}

			if tc.wantCount > 0 && events[0].Type != tc.wantType {
				t.Errorf("event type = %v, want %v", events[0].Type, tc.wantType)
			}

			if tc.checkPath != nil && len(events) > 0 {
				tc.checkPath(t, events, dir)
			}
		})
	}
}

func TestEventTypeString(t *testing.T) {
	tests := []struct {
		et   EventType
		want string
	}{
		{Added, "Added"},
		{Modified, "Modified"},
		{Removed, "Removed"},
		{EventType(99), "Unknown"},
	}
	for _, tc := range tests {
		if got := tc.et.String(); got != tc.want {
			t.Errorf("EventType(%d).String() = %q, want %q", tc.et, got, tc.want)
		}
	}
}

func TestWatchCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Watch(ctx, dir, func(e WatchEvent) {})
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Watch returned as expected.
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after context cancellation")
	}
}
