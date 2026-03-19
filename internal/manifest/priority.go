package manifest

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultPriorityClasses returns the built-in priority class mapping.
func DefaultPriorityClasses() map[string]int {
	return map[string]int{
		"emergency": 0,
		"critical":  100,
		"high":      500,
		"normal":    1000,
		"low":       2000,
		"idle":      10000,
	}
}

// LoadPriorityClasses reads priority classes from a config file at the given path.
// If path is empty or the file does not exist, the default classes are returned.
// The config file uses a simple YAML-like format:
//
//	priorityClasses:
//	  emergency: 0
//	  critical: 100
func LoadPriorityClasses(path string) (map[string]int, error) {
	if path == "" {
		return DefaultPriorityClasses(), nil
	}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return DefaultPriorityClasses(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open priority config: %w", err)
	}
	defer f.Close()

	classes := make(map[string]int)
	scanner := bufio.NewScanner(f)
	inBlock := false

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and comments.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect the priorityClasses header.
		if trimmed == "priorityClasses:" {
			inBlock = true
			continue
		}

		// Inside the block, lines must be indented key: value pairs.
		if inBlock {
			// A non-indented line ends the block.
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}

			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.TrimSpace(parts[0])
			valStr := strings.TrimSpace(parts[1])
			if key == "" || valStr == "" {
				continue
			}

			val, err := strconv.Atoi(valStr)
			if err != nil {
				return nil, fmt.Errorf("invalid priority value for %q: %w", key, err)
			}
			classes[key] = val
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading priority config: %w", err)
	}

	if len(classes) == 0 {
		return DefaultPriorityClasses(), nil
	}

	return classes, nil
}

// ResolvePriority looks up the given class name in the provided map.
// If the name is not found, it returns the default normal priority (1000).
func ResolvePriority(classes map[string]int, className string) int {
	if v, ok := classes[className]; ok {
		return v
	}
	return 1000
}
