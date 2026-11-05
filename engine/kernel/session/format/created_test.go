package format_test

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestCreated(t *testing.T) {
	v := format.Created{}
	wire, err := codec.Encode(v, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa0}) {
		t.Fatalf("empty creation map: %x, %v", wire, err)
	}
	got, err := codec.Decode[format.Created](wire, limits())
	if err != nil || !got.Equal(v.Clone()) {
		t.Fatalf("locally valid zero creation: %v", err)
	}
}
