package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultPriorityClasses(t *testing.T) {
	got := DefaultPriorityClasses()
	want := map[string]int{
		"emergency": 0,
		"critical":  100,
		"high":      500,
		"normal":    1000,
		"low":       2000,
		"idle":      10000,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultPriorityClasses() = %v, want %v", got, want)
	}
}

func TestLoadPriorityClasses(t *testing.T) {
	defaults := map[string]int{
		"emergency": 0,
		"critical":  100,
		"high":      500,
		"normal":    1000,
		"low":       2000,
		"idle":      10000,
	}

	tests := []struct {
		name     string
		content  string // file content; empty means no file created
		noFile   bool   // use a path that doesn't exist
		emptyArg bool   // pass empty string as path
		want     map[string]int
		wantErr  bool
	}{
		{
			name:     "empty path returns defaults",
			emptyArg: true,
			want:     defaults,
		},
		{
			name:   "missing file returns defaults",
			noFile: true,
			want:   defaults,
		},
		{
			name: "valid config",
			content: `priorityClasses:
  fast: 50
  slow: 5000
`,
			want: map[string]int{
				"fast": 50,
				"slow": 5000,
			},
		},
		{
			name: "config with comments and blank lines",
			content: `# Priority configuration
priorityClasses:
  # Highest priority
  urgent: 10

  background: 9000
`,
			want: map[string]int{
				"urgent":     10,
				"background": 9000,
			},
		},
		{
			name: "empty block returns defaults",
			content: `priorityClasses:
`,
			want: defaults,
		},
		{
			name: "invalid value returns error",
			content: `priorityClasses:
  bad: notanumber
`,
			wantErr: true,
		},
		{
			name: "block ends at next top-level key",
			content: `priorityClasses:
  fast: 50
otherSection:
  ignored: 999
`,
			want: map[string]int{
				"fast": 50,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string

			if tt.emptyArg {
				path = ""
			} else if tt.noFile {
				path = filepath.Join(t.TempDir(), "nonexistent.yaml")
			} else {
				dir := t.TempDir()
				path = filepath.Join(dir, "priority.yaml")
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := LoadPriorityClasses(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadPriorityClasses() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadPriorityClasses() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolvePriority(t *testing.T) {
	classes := map[string]int{
		"emergency": 0,
		"critical":  100,
		"normal":    1000,
	}

	tests := []struct {
		name      string
		className string
		want      int
	}{
		{"known class emergency", "emergency", 0},
		{"known class critical", "critical", 100},
		{"known class normal", "normal", 1000},
		{"unknown class defaults to 1000", "unknown", 1000},
		{"empty string defaults to 1000", "", 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePriority(classes, tt.className)
			if got != tt.want {
				t.Errorf("ResolvePriority(%q) = %d, want %d", tt.className, got, tt.want)
			}
		})
	}
}
