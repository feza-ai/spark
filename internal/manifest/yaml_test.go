package manifest

import "testing"

func TestParseMemoryKiAndK(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		// Ki suffix (divide by 1024)
		{"1024Ki", 1},
		{"512Ki", 0},
		{"2048Ki", 2},
		// K suffix (divide by 1000)
		{"1000K", 1},
		{"500K", 0},
		{"2000K", 2},
		// Existing suffixes still work
		{"1Gi", 1024},
		{"2Mi", 2},
		{"1G", 1000},
		{"512M", 512},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseMemory(tt.input)
			if got != tt.want {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
