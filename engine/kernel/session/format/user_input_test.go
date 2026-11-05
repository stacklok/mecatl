package format_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserInput(t *testing.T) {
	v := format.UserInput{RunID: 1, ID: 1, Content: []byte{0xff, 0, 'x'}}
	cloned := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.UserInput](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	clear(v.Content)
	clear(wire)
	if !got.Equal(cloned) || !bytes.Equal(got.Content, []byte{0xff, 0, 'x'}) {
		t.Fatal("clone or decoder retained content storage")
	}
	if !(format.UserInput{RunID: 1, ID: 1}).Equal(format.UserInput{RunID: 1, ID: 1, Content: []byte{}}) {
		t.Fatal("nil and empty content differ")
	}
	invalid := format.UserInput{Content: []byte("opaque")}
	if invalid.Validate() == nil {
		t.Fatal("zero ordinal validated")
	}
	if wire, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
		t.Fatalf("invalid encode: %v", err)
	}
	// Deliberately bypass prechecking so the decoder first reads populated
	// mutable content, then discards its temporary value on local validation.
	invalidWire := format.UserInput{Content: []byte("opaque")}.AppendCBOR(nil)
	got, err = codec.Decode[format.UserInput](invalidWire, limits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.UserInput{}) {
		t.Fatalf("invalid decode returned nonzero value: %v", err)
	}
	receiver := format.UserInput{RunID: 1, ID: 7, Content: []byte("retain")}
	before := receiver.Clone()
	storage := receiver.Content
	reader, err := codec.NewReader(invalidWire, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) ||
		!receiver.Equal(before) || &receiver.Content[0] != &storage[0] || !bytes.Equal(storage, before.Content) {
		t.Fatalf("invalid local decode changed receiver or storage: %v", err)
	}
}
