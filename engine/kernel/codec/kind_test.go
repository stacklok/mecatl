package codec_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestKindString(t *testing.T) {
	for _, tc := range []struct {
		kind codec.Kind
		want string
	}{
		{codec.InvalidKind, "invalid"},
		{codec.NullKind, "null"},
		{codec.BoolKind, "bool"},
		{codec.IntKind, "int"},
		{codec.UintKind, "uint"},
		{codec.StringKind, "string"},
		{codec.ArrayKind, "array"},
		{codec.MapKind, "map"},
		{codec.Kind(99), "Kind(99)"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
