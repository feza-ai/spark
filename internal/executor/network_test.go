package executor

import "testing"

func TestIsAlreadyExistsError(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "network already exists",
			stderr: "Error: network spark-net already exists",
			want:   true,
		},
		{
			name:   "other error",
			stderr: "Error: something went wrong",
			want:   false,
		},
		{
			name:   "empty stderr",
			stderr: "",
			want:   false,
		},
		{
			name:   "already exists substring",
			stderr: "network already exists in store",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAlreadyExistsError(tt.stderr)
			if got != tt.want {
				t.Errorf("isAlreadyExistsError(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}
