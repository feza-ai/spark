package scheduler

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCoreRange(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		maxCore int
		want    []int
		wantErr string
	}{
		{name: "empty", input: "", maxCore: 8, want: nil},
		{name: "whitespace only", input: "  ", maxCore: 8, want: nil},
		{name: "single range", input: "0-1", maxCore: 8, want: []int{0, 1}},
		{name: "comma list", input: "0,1", maxCore: 8, want: []int{0, 1}},
		{name: "mixed range and id", input: "0,2-3", maxCore: 8, want: []int{0, 2, 3}},
		{name: "sorts unsorted", input: "3,1,2", maxCore: 8, want: []int{1, 2, 3}},
		{name: "dedup exact", input: "0,0", maxCore: 8, want: []int{0}},
		{name: "dedup overlapping", input: "0,0-1", maxCore: 8, want: []int{0, 1}},
		{name: "single core", input: "5", maxCore: 8, want: []int{5}},
		{name: "range with spaces", input: " 0 - 1 , 3 ", maxCore: 8, want: []int{0, 1, 3}},

		{name: "descending range", input: "3-1", maxCore: 8, wantErr: "descending"},
		{name: "core out of range high", input: "8", maxCore: 8, wantErr: "out of bounds"},
		{name: "range out of range high", input: "6-8", maxCore: 8, wantErr: "out of bounds"},
		{name: "negative", input: "-1", maxCore: 8, wantErr: "invalid core range"},
		{name: "trailing dash", input: "0-", maxCore: 8, wantErr: "invalid core range"},
		{name: "non-numeric", input: "abc", maxCore: 8, wantErr: "invalid core ID"},
		{name: "non-numeric range", input: "a-1", maxCore: 8, wantErr: "invalid core range"},
		{name: "empty segment", input: "0,,1", maxCore: 8, wantErr: "empty core segment"},
		{name: "zero maxCore", input: "0", maxCore: 0, wantErr: "out of bounds"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCoreRange(tc.input, tc.maxCore)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseCoreRange(%q) = %v, want error containing %q", tc.input, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseCoreRange(%q) error = %q, want substring %q", tc.input, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCoreRange(%q) unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseCoreRange(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
