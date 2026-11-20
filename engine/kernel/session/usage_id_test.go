package session

import (
	"reflect"
	"testing"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func TestUsageID(t *testing.T) {
	h := sessionid.NewSessionID()
	id, err := NewUsageID(h, 1)
	if err != nil || id.Validate() != nil || id.SessionID() != h || id.SessionRelative() != 1 || !id.Equal(id) {
		t.Fatalf("identity: %v", err)
	}
	copyID, err := NewUsageID(id.SessionID(), id.SessionRelative())
	if err != nil || copyID != id {
		t.Fatalf("reconstruction: %v", err)
	}
	other, err := NewUsageID(sessionid.NewSessionID(), 1)
	if err != nil || id.Equal(other) {
		t.Fatal("history scope lost")
	}
	other, err = NewUsageID(h, 2)
	if err != nil || id.Equal(other) {
		t.Fatal("occurrence component lost")
	}
	for _, invalid := range []UsageID{{}, {sessionID: h}, {usageOrdinal: 1}} {
		if invalid.Validate() == nil {
			t.Fatal("malformed identity validated")
		}
		if got, err := NewUsageID(invalid.SessionID(), invalid.SessionRelative()); err == nil || got != (UsageID{}) {
			t.Fatal("malformed reconstruction did not return zero/error")
		}
	}
	if (UsageID{}).SessionID() != (sessionid.SessionID{}) || (UsageID{}).SessionRelative() != 0 {
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
