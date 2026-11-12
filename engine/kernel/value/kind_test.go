package value_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/value"
)

func TestKindString(t *testing.T) {
	for _, tc := range []struct {
		kind value.Kind
		want string
	}{
		{value.InvalidKind, "invalid"},
		{value.NullKind, "null"},
		{value.BoolKind, "bool"},
		{value.StringKind, "string"},
		{value.IntegerKind, "integer"},
		{value.ArrayKind, "array"},
		{value.MapKind, "map"},
		{value.Kind(99), "Kind(99)"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
