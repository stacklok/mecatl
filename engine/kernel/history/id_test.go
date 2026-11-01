package history_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestHistoryIDAlias(t *testing.T) {
	direct := historyid.NewHistoryID()
	throughHistory := history.NewHistoryID()
	// Bidirectional assignment proves both packages expose the same Go type.
	direct, throughHistory = throughHistory, direct
	parsed, err := historyid.ParseHistoryID(direct.String())
	if err != nil || parsed != direct {
		t.Fatalf("parse wrapper-generated ID through leaf: got %v, %v; want %v", parsed, err, direct)
	}

	direct, err = history.ParseHistoryID(throughHistory.String())
	if err != nil || direct != throughHistory {
		t.Fatalf("parse leaf-generated ID through wrapper: got %v, %v; want %v", direct, err, throughHistory)
	}
	if got := map[history.HistoryID]bool{throughHistory: true}; !got[direct] {
		t.Fatal("aliased IDs must be interchangeable map keys")
	}
}

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
