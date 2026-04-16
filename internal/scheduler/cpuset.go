// Package scheduler — cpuset.go adds parsing of CPU core ID lists.
package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ParseCoreRange parses a string like "0-1", "0,1", or "0,2-3" into a sorted,
// deduplicated []int. Empty input returns nil, nil. Each ID must be in
// [0, maxCore); otherwise an error is returned. Ranges must be ascending.
func ParseCoreRange(s string, maxCore int) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := make(map[int]struct{})
	for _, seg := range strings.Split(s, ",") {
		ids, err := parseCoreSegment(seg, maxCore)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
}

// parseCoreSegment parses a single segment: either a bare core ID ("3") or an
// ascending range ("2-5"). Each emitted ID must be in [0, maxCore).
func parseCoreSegment(seg string, maxCore int) ([]int, error) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return nil, fmt.Errorf("empty core segment")
	}
	if i := strings.Index(seg, "-"); i >= 0 {
		lo, err := strconv.Atoi(strings.TrimSpace(seg[:i]))
		if err != nil {
			return nil, fmt.Errorf("invalid core range %q: %w", seg, err)
		}
		hi, err := strconv.Atoi(strings.TrimSpace(seg[i+1:]))
		if err != nil {
			return nil, fmt.Errorf("invalid core range %q: %w", seg, err)
		}
		if lo > hi {
			return nil, fmt.Errorf("invalid core range %q: descending", seg)
		}
		if lo < 0 || hi >= maxCore {
			return nil, fmt.Errorf("core range %q out of bounds [0,%d)", seg, maxCore)
		}
		ids := make([]int, 0, hi-lo+1)
		for id := lo; id <= hi; id++ {
			ids = append(ids, id)
		}
		return ids, nil
	}
	id, err := strconv.Atoi(seg)
	if err != nil {
		return nil, fmt.Errorf("invalid core ID %q: %w", seg, err)
	}
	if id < 0 || id >= maxCore {
		return nil, fmt.Errorf("core ID %d out of bounds [0,%d)", id, maxCore)
	}
	return []int{id}, nil
}
