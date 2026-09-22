package metrics

import (
	"strings"
	"testing"
)

// TestParseMemAvailable covers the reading the memory admission guard
// depends on. The failure cases matter as much as the success one: a
// meminfo this parser cannot read must produce an error, because a caller
// that silently treats "unreadable" as "0MB free" refuses every pod
// forever, and one that treats it as "plenty free" reopens issue #121.
func TestParseMemAvailable(t *testing.T) {
	t.Parallel()

	const realMeminfo = `MemTotal:       121305088 kB
MemFree:         2189312 kB
MemAvailable:   12259328 kB
Buffers:          123456 kB
Cached:         10485760 kB
`

	tests := []struct {
		name    string
		content string
		wantMB  int
		wantErr string
	}{
		{
			name:    "a real meminfo reads MemAvailable, not MemFree",
			content: realMeminfo,
			wantMB:  11972,
		},
		{
			name:    "zero is a legitimate reading",
			content: "MemAvailable:          0 kB\n",
			wantMB:  0,
		},
		{
			name:    "a field with no trailing newline still parses",
			content: "MemTotal: 100 kB\nMemAvailable: 2097152 kB",
			wantMB:  2048,
		},
		{
			name:    "a kernel without MemAvailable is an error, not zero",
			content: "MemTotal:       121305088 kB\nMemFree:         2189312 kB\n",
			wantErr: "MemAvailable not found",
		},
		{
			name:    "a malformed value is an error",
			content: "MemAvailable:   not-a-number kB\n",
			wantErr: "parse MemAvailable",
		},
		{
			name:    "a value-less MemAvailable line is an error",
			content: "MemAvailable:\n",
			wantErr: "unexpected MemAvailable format",
		},
		{
			name:    "empty content is an error",
			content: "",
			wantErr: "MemAvailable not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMemAvailable(tt.content)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got nil (value %d)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantMB {
				t.Fatalf("ParseMemAvailable() = %d MB, want %d MB", got, tt.wantMB)
			}
		})
	}
}
