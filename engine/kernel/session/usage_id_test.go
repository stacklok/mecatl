package session

import (
	"reflect"
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestUsageID(t *testing.T) {
	h := historyid.NewHistoryID()
	id, err := NewUsageID(h, 1)
	if err != nil || id.Validate() != nil || id.HistoryID() != h || id.HistoryRelative() != 1 || !id.Equal(id) {
		t.Fatalf("identity: %v", err)
	}
	copyID, err := NewUsageID(id.HistoryID(), id.HistoryRelative())
	if err != nil || copyID != id {
		t.Fatalf("reconstruction: %v", err)
	}
	other, err := NewUsageID(historyid.NewHistoryID(), 1)
	if err != nil || id.Equal(other) {
		t.Fatal("history scope lost")
	}
	other, err = NewUsageID(h, 2)
	if err != nil || id.Equal(other) {
		t.Fatal("occurrence component lost")
	}
	for _, invalid := range []UsageID{{}, {historyID: h}, {usageOrdinal: 1}} {
		if invalid.Validate() == nil {
			t.Fatal("malformed identity validated")
		}
		if got, err := NewUsageID(invalid.HistoryID(), invalid.HistoryRelative()); err == nil || got != (UsageID{}) {
			t.Fatal("malformed reconstruction did not return zero/error")
		}
	}
	if (UsageID{}).HistoryID() != (historyid.HistoryID{}) || (UsageID{}).HistoryRelative() != 0 {
		t.Fatal("zero accessors")
	}
	usageType := reflect.TypeOf(UsageID{})
	if !usageType.Comparable() {
		t.Fatal("usage identity is not comparable")
	}
	for _, other := range []reflect.Type{reflect.TypeOf(RunID{}), reflect.TypeOf(UserInputID{}), reflect.TypeOf(ModelRequestID{}), reflect.TypeOf(RequestedCallID{})} {
		if usageType.ConvertibleTo(other) || other.ConvertibleTo(usageType) {
			t.Fatalf("accidental identity conversion to/from %s", other)
		}
	}
}
