package session

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestCreated(t *testing.T) {
	v := Created{}
	r, err := newCreatedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[sessionformat.Created](r.format().Payload, testLimits())
	if err != nil || !got.Equal(v.Clone()) {
		t.Fatalf("empty canonical creation roundtrip: %v", err)
	}
}
