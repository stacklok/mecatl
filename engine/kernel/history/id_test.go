package history_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestHistoryID(t *testing.T) {
	v := history.NewHistoryID()
	parsed, err := history.ParseHistoryID(v.String())
	if err != nil || parsed != v {
		t.Fatalf("round trip: got %v, %v; want %v", parsed, err, v)
	}
	if got := map[history.HistoryID]bool{v: true}; !got[parsed] {
		t.Fatal("equal history IDs must be interchangeable map keys")
	}
}

func TestParseHistoryID(t *testing.T) {
	text := "HST" + strings.Repeat("z", 22)
	v, err := history.ParseHistoryID(text)
	if err != nil || v.String() != text {
		t.Fatalf("ParseHistoryID(%q) = %v, %v", text, v, err)
	}
	for _, text := range []string{"", "RUN" + strings.Repeat("1", 22), "HST" + strings.Repeat("0", 22)} {
		v, err := history.ParseHistoryID(text)
		if err == nil || v != (history.HistoryID{}) {
			t.Errorf("ParseHistoryID(%q) = %v, %v; want zero ID and error", text, v, err)
		}
	}
	var zero history.HistoryID
	if zero.String() != "" {
		t.Fatalf("zero HistoryID = %q", zero.String())
	}
}
