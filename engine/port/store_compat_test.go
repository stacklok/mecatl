package port_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestInvariant_session_continuity_api_is_additive(t *testing.T) {
	typ := reflect.TypeOf(port.SessionMeta{})
	want := []string{"ID", "ModifiedAt", "State", "Turns", "ModelID", "CreatedAt", "Title", "Owner"}
	if typ.NumField() != len(want) {
		t.Fatalf("port.SessionMeta has %d fields, want the pre-existing %d-field shape: %v", typ.NumField(), len(want), want)
	}
	for i, name := range want {
		if typ.Field(i).Name != name {
			t.Fatalf("port.SessionMeta field %d = %q, want %q", i, typ.Field(i).Name, name)
		}
	}
	if reflect.TypeOf(port.SessionDiscoveryMeta{}).NumField() <= typ.NumField() {
		t.Fatal("SessionDiscoveryMeta must carry additive discovery fields independently of SessionMeta")
	}
}
