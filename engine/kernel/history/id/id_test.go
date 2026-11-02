package id_test

import (
	"strings"
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestHistoryID(t *testing.T) {
	v := historyid.NewHistoryID()
	parsed, err := historyid.ParseHistoryID(v.String())
	if err != nil || parsed != v {
		t.Fatalf("round trip: got %v, %v; want %v", parsed, err, v)
	}
	if got := map[historyid.HistoryID]bool{v: true}; !got[parsed] {
		t.Fatal("equal history IDs must be interchangeable map keys")
	}
}

func TestParseHistoryID(t *testing.T) {
	text := "HST" + strings.Repeat("z", 22)
	v, err := historyid.ParseHistoryID(text)
	if err != nil || v.String() != text {
		t.Fatalf("ParseHistoryID(%q) = %v, %v", text, v, err)
	}
	for _, text := range []string{
		"",
		"RUN" + strings.Repeat("1", 22),
		"HST" + strings.Repeat("0", 22),
		"HST" + strings.Repeat("1", 21),
		"HST" + strings.Repeat("1", 23),
		" " + text,
		text + "\n",
		strings.ToLower(text),
	} {
		v, err := historyid.ParseHistoryID(text)
		if err == nil || v != (historyid.HistoryID{}) {
			t.Errorf("ParseHistoryID(%q) = %v, %v; want zero ID and error", text, v, err)
		}
	}
	var zero historyid.HistoryID
	if zero.String() != "" {
		t.Fatalf("zero HistoryID = %q", zero.String())
	}
}
