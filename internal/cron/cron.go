package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule represents a parsed cron schedule that can compute the next trigger time.
type Schedule struct {
	minutes    []bool // 0-59
	hours      []bool // 0-23
	daysOfMonth []bool // 1-31 (index 0 unused)
	months     []bool // 1-12 (index 0 unused)
	daysOfWeek []bool // 0-6, 0=Sunday
}

// Parse parses a standard 5-field cron expression (minute hour day-of-month month day-of-week).
func Parse(expr string) (Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}

	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron: invalid minute field %q: %w", fields[0], err)
	}

	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron: invalid hour field %q: %w", fields[1], err)
	}

	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron: invalid day-of-month field %q: %w", fields[2], err)
	}

	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron: invalid month field %q: %w", fields[3], err)
	}

	dow, err := parseField(fields[4], 0, 6)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron: invalid day-of-week field %q: %w", fields[4], err)
	}

	s := Schedule{
		minutes:    make([]bool, 60),
		hours:      make([]bool, 24),
		daysOfMonth: make([]bool, 32),
		months:     make([]bool, 13),
		daysOfWeek: make([]bool, 7),
	}

	for _, v := range minutes {
		s.minutes[v] = true
	}
	for _, v := range hours {
		s.hours[v] = true
	}
	for _, v := range dom {
		s.daysOfMonth[v] = true
	}
	for _, v := range months {
		s.months[v] = true
	}
	for _, v := range dow {
		s.daysOfWeek[v] = true
	}

	return s, nil
}

// Next returns the next trigger time strictly after from.
func (s Schedule) Next(from time.Time) time.Time {
	// Start from the next minute, zeroing out seconds and nanoseconds.
	t := from.Truncate(time.Minute).Add(time.Minute)

	// Search up to 4 years to handle leap year edge cases like Feb 29.
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		// Check month.
		if !s.months[t.Month()] {
			// Advance to the first day of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}

		// Check day-of-month and day-of-week.
		if !s.daysOfMonth[t.Day()] || !s.daysOfWeek[int(t.Weekday())] {
			// Advance to the next day.
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}

		// Check hour.
		if !s.hours[t.Hour()] {
			// Advance to the next hour.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}

		// Check minute.
		if !s.minutes[t.Minute()] {
			// Advance by one minute.
			t = t.Add(time.Minute)
			continue
		}

		return t
	}

	// Should not happen for valid schedules within a 4-year window.
	return time.Time{}
}

// parseField parses a single cron field and returns the set of matching values.
func parseField(field string, min, max int) ([]int, error) {
	var result []int
	seen := make(map[int]bool)

	parts := strings.Split(field, ",")
	for _, part := range parts {
		vals, err := parsePart(part, min, max)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				result = append(result, v)
			}
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("empty field")
	}

	return result, nil
}

// parsePart parses a single comma-separated element: *, N, N-M, */S, N-M/S.
func parsePart(part string, min, max int) ([]int, error) {
	var rangeStart, rangeEnd int
	step := 1

	// Split on '/' for step values.
	slashParts := strings.SplitN(part, "/", 2)
	rangePart := slashParts[0]

	if len(slashParts) == 2 {
		s, err := strconv.Atoi(slashParts[1])
		if err != nil || s <= 0 {
			return nil, fmt.Errorf("invalid step %q", slashParts[1])
		}
		step = s
	}

	if rangePart == "*" {
		rangeStart = min
		rangeEnd = max
	} else if idx := strings.Index(rangePart, "-"); idx >= 0 {
		lo, err := strconv.Atoi(rangePart[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid range start %q", rangePart[:idx])
		}
		hi, err := strconv.Atoi(rangePart[idx+1:])
		if err != nil {
			return nil, fmt.Errorf("invalid range end %q", rangePart[idx+1:])
		}
		if lo < min || lo > max {
			return nil, fmt.Errorf("value %d out of range [%d, %d]", lo, min, max)
		}
		if hi < min || hi > max {
			return nil, fmt.Errorf("value %d out of range [%d, %d]", hi, min, max)
		}
		if lo > hi {
			return nil, fmt.Errorf("invalid range: %d > %d", lo, hi)
		}
		rangeStart = lo
		rangeEnd = hi
	} else {
		// Single value.
		v, err := strconv.Atoi(rangePart)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q", rangePart)
		}
		if v < min || v > max {
			return nil, fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
		}
		if len(slashParts) == 2 {
			// e.g., "5/10" means starting at 5, every 10
			rangeStart = v
			rangeEnd = max
		} else {
			return []int{v}, nil
		}
	}

	var vals []int
	for i := rangeStart; i <= rangeEnd; i += step {
		vals = append(vals, i)
	}
	return vals, nil
}
