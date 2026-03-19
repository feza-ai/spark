package cron

import (
	"testing"
	"time"
)

func TestParseAndNext(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name string
		expr string
		from time.Time
		want time.Time
	}{
		{
			name: "daily at 2am",
			expr: "0 2 * * *",
			from: time.Date(2025, 6, 15, 10, 0, 0, 0, loc),
			want: time.Date(2025, 6, 16, 2, 0, 0, 0, loc),
		},
		{
			name: "daily at 2am from before 2am",
			expr: "0 2 * * *",
			from: time.Date(2025, 6, 15, 1, 0, 0, 0, loc),
			want: time.Date(2025, 6, 15, 2, 0, 0, 0, loc),
		},
		{
			name: "every 5 minutes",
			expr: "*/5 * * * *",
			from: time.Date(2025, 6, 15, 10, 3, 0, 0, loc),
			want: time.Date(2025, 6, 15, 10, 5, 0, 0, loc),
		},
		{
			name: "every 5 minutes from exact match",
			expr: "*/5 * * * *",
			from: time.Date(2025, 6, 15, 10, 5, 0, 0, loc),
			want: time.Date(2025, 6, 15, 10, 10, 0, 0, loc),
		},
		{
			name: "weekly Sunday midnight",
			expr: "0 0 * * 0",
			from: time.Date(2025, 6, 15, 0, 0, 0, 0, loc), // Sunday
			want: time.Date(2025, 6, 22, 0, 0, 0, 0, loc),  // next Sunday
		},
		{
			name: "weekly Sunday midnight from Wednesday",
			expr: "0 0 * * 0",
			from: time.Date(2025, 6, 18, 12, 0, 0, 0, loc), // Wednesday
			want: time.Date(2025, 6, 22, 0, 0, 0, 0, loc),  // next Sunday
		},
		{
			name: "1st of month at 2:30pm",
			expr: "30 14 1 * *",
			from: time.Date(2025, 6, 1, 14, 30, 0, 0, loc),
			want: time.Date(2025, 7, 1, 14, 30, 0, 0, loc),
		},
		{
			name: "1st of month at 2:30pm from mid-month",
			expr: "30 14 1 * *",
			from: time.Date(2025, 6, 15, 10, 0, 0, 0, loc),
			want: time.Date(2025, 7, 1, 14, 30, 0, 0, loc),
		},
		{
			name: "Feb 29 edge case from non-leap year",
			expr: "0 0 29 2 *",
			from: time.Date(2025, 1, 1, 0, 0, 0, 0, loc),
			want: time.Date(2028, 2, 29, 0, 0, 0, 0, loc),
		},
		{
			name: "Feb 29 edge case from leap year before date",
			expr: "0 0 29 2 *",
			from: time.Date(2028, 2, 1, 0, 0, 0, 0, loc),
			want: time.Date(2028, 2, 29, 0, 0, 0, 0, loc),
		},
		{
			name: "range expression 1-5 for day of week (Mon-Fri)",
			expr: "0 9 * * 1-5",
			from: time.Date(2025, 6, 14, 12, 0, 0, 0, loc), // Saturday
			want: time.Date(2025, 6, 16, 9, 0, 0, 0, loc),  // Monday
		},
		{
			name: "list expression",
			expr: "0 9,17 * * *",
			from: time.Date(2025, 6, 15, 10, 0, 0, 0, loc),
			want: time.Date(2025, 6, 15, 17, 0, 0, 0, loc),
		},
		{
			name: "step in range 1-10/2",
			expr: "1-10/2 * * * *",
			from: time.Date(2025, 6, 15, 10, 0, 0, 0, loc),
			want: time.Date(2025, 6, 15, 10, 1, 0, 0, loc),
		},
		{
			name: "step in range produces correct values",
			expr: "1-10/3 * * * *",
			from: time.Date(2025, 6, 15, 10, 2, 0, 0, loc),
			want: time.Date(2025, 6, 15, 10, 4, 0, 0, loc),
		},
		{
			name: "month boundary Dec to Jan",
			expr: "0 0 1 1 *",
			from: time.Date(2025, 12, 31, 23, 0, 0, 0, loc),
			want: time.Date(2026, 1, 1, 0, 0, 0, 0, loc),
		},
		{
			name: "year rollover",
			expr: "0 0 * * 0",
			from: time.Date(2025, 12, 29, 0, 0, 0, 0, loc), // Monday
			want: time.Date(2026, 1, 4, 0, 0, 0, 0, loc),   // next Sunday
		},
		{
			name: "midnight every day",
			expr: "0 0 * * *",
			from: time.Date(2025, 6, 15, 23, 59, 0, 0, loc),
			want: time.Date(2025, 6, 16, 0, 0, 0, 0, loc),
		},
		{
			name: "every minute",
			expr: "* * * * *",
			from: time.Date(2025, 6, 15, 10, 30, 45, 0, loc),
			want: time.Date(2025, 6, 15, 10, 31, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.expr, err)
			}
			got := sched.Next(tt.from)
			if !got.Equal(tt.want) {
				t.Errorf("Next(%v) = %v, want %v", tt.from, got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{name: "too few fields", expr: "0 2 * *"},
		{name: "too many fields", expr: "0 2 * * * *"},
		{name: "empty string", expr: ""},
		{name: "minute out of range high", expr: "60 * * * *"},
		{name: "minute out of range negative", expr: "-1 * * * *"},
		{name: "hour out of range", expr: "0 24 * * *"},
		{name: "day of month out of range zero", expr: "0 0 0 * *"},
		{name: "day of month out of range high", expr: "0 0 32 * *"},
		{name: "month out of range zero", expr: "0 0 * 0 *"},
		{name: "month out of range high", expr: "0 0 * 13 *"},
		{name: "day of week out of range", expr: "0 0 * * 7"},
		{name: "invalid step zero", expr: "*/0 * * * *"},
		{name: "invalid step negative", expr: "*/-1 * * * *"},
		{name: "invalid range reversed", expr: "10-5 * * * *"},
		{name: "non-numeric value", expr: "abc * * * *"},
		{name: "non-numeric step", expr: "*/abc * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.expr)
			if err == nil {
				t.Errorf("Parse(%q) expected error, got nil", tt.expr)
			}
		})
	}
}

func TestNextConsecutiveCalls(t *testing.T) {
	sched, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	expected := []time.Time{
		time.Date(2025, 6, 15, 10, 15, 0, 0, time.UTC),
		time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
		time.Date(2025, 6, 15, 10, 45, 0, 0, time.UTC),
		time.Date(2025, 6, 15, 11, 0, 0, 0, time.UTC),
	}

	for i, want := range expected {
		got := sched.Next(from)
		if !got.Equal(want) {
			t.Errorf("iteration %d: Next(%v) = %v, want %v", i, from, got, want)
		}
		from = got
	}
}
