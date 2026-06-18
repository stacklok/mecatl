package forker

import (
	"reflect"
	"testing"
)

// TestSplitNUL pins the NUL-splitter's contract (git -z output): drop EVERY empty
// element, not just the trailing one. Kept identical to the sibling impl in
// cmd/mecatequi/run.go — the drift this guards against was the "fix one, miss the
// other" bug the duplication reviewer flagged.
func TestSplitNUL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"trailing NUL (normal git -z)", "a\x00b\x00", []string{"a", "b"}},
		{"empty output", "", nil},
		{"single entry without trailing NUL", "only", []string{"only"}},
		{"single entry with trailing NUL", "only\x00", []string{"only"}},
		{"interior empties dropped", "a\x00\x00b\x00", []string{"a", "b"}},
		{"only a NUL", "\x00", nil},
		{"paths with spaces", "a b\x00c/d e\x00", []string{"a b", "c/d e"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitNUL([]byte(tc.in))
			if len(got) == 0 && len(tc.want) == 0 {
				return // both empty (nil vs len-0 slice) — equivalent
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitNUL(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
