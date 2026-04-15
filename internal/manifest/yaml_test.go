package manifest

import "testing"

func TestFindMapSeparator(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"simple map", "key: value", 3},
		{"trailing colon", "key:", 3},
		{"no space after colon", "key:value", -1},
		{"url scalar", "nats://10.88.0.1:4222", -1},
		{"http url", "http://x/y", -1},
		{"quoted url double", `"nats://10.88.0.1:4222"`, -1},
		{"quoted url single", `'nats://10.88.0.1:4222'`, -1},
		{"quoted path single", `'path:/a/b'`, -1},
		{"tab after colon", "key:\tvalue", 3},
		{"empty", "", -1},
		{"colon inside quotes, real map after", `name: "nats://x:4222"`, 4},
		{"double quote inside single", `'he said "hi: there"'`, -1},
		{"no colon at all", "plain value", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMapSeparator(tt.input)
			if got != tt.want {
				t.Errorf("findMapSeparator(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

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
