package watcher

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventType describes the kind of filesystem change detected.
type EventType int

const (
	Added EventType = iota
	Modified
	Removed
)

// String returns the string representation of an EventType.
func (e EventType) String() string {
	switch e {
	case Added:
		return "Added"
	case Modified:
		return "Modified"
	case Removed:
		return "Removed"
	default:
		return "Unknown"
	}
}

// WatchEvent describes a change detected in the watched directory.
type WatchEvent struct {
	Type    EventType
	Path    string
	Content []byte // populated for Added and Modified events
}

// pollInterval is the time between directory scans.
const pollInterval = 5 * time.Second

// isYAML returns true if the file path has a YAML extension.
func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// checksum returns the SHA-256 hex digest of data.
func checksum(data []byte) [sha256.Size]byte {
	return sha256.Sum256(data)
}

// snapshot scans dir for YAML files and returns a map of path to sha256 checksum.
func snapshot(dir string) (map[string][sha256.Size]byte, map[string][]byte, error) {
	checksums := make(map[string][sha256.Size]byte)
	contents := make(map[string][]byte)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checksums[path] = checksum(data)
		contents[path] = data
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return checksums, contents, nil
}

// Watch polls dir every 5 seconds and calls onChange when YAML files are
// added, modified, or removed. It blocks until ctx is cancelled.
func Watch(ctx context.Context, dir string, onChange func(event WatchEvent)) {
	prev := make(map[string][sha256.Size]byte)

	// Take initial snapshot without emitting events.
	if initial, _, err := snapshot(dir); err == nil {
		prev = initial
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			curr, contents, err := snapshot(dir)
			if err != nil {
				continue
			}

			// Detect added and modified files.
			for path, hash := range curr {
				oldHash, exists := prev[path]
				if !exists {
					onChange(WatchEvent{Type: Added, Path: path, Content: contents[path]})
				} else if hash != oldHash {
					onChange(WatchEvent{Type: Modified, Path: path, Content: contents[path]})
				}
			}

			// Detect removed files.
			for path := range prev {
				if _, exists := curr[path]; !exists {
					onChange(WatchEvent{Type: Removed, Path: path})
				}
			}

			prev = curr
		}
	}
}
