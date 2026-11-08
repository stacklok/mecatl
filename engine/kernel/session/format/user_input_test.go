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

// These pre-rename v1 bytes pin the stored numeric identities independently of
// Go field names: ID remains key i, and the enclosing run remains key r.
func TestUserInputIdentityWire(t *testing.T) {
	wire := []byte("\xa3\x61i\x19\x01\x00\x61p\x80\x61r\x18\x18")
	want := format.UserInput{RunID: 24, ID: 256}
	encoded, err := codec.Encode(want, limits())
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("identity encoding = %x; want %x: %v", encoded, wire, err)
	}
	decoded, err := codec.Decode[format.UserInput](wire, limits())
	if err != nil || decoded.RunID != 24 || decoded.ID != 256 || len(decoded.Parts) != 0 {
		t.Fatalf("literal identity decoding = %+v: %v", decoded, err)
	}
}

func TestUserInput(t *testing.T) {
	v := format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{
		{Text: new(" /skill e\u0301  \n")},
		{Media: &format.Media{Kind: "image", MIMEType: "image/png", Source: "inline", InlineData: []byte{0xff, 0, 'x'}}},
		{Text: new("尾\n")},
	}}
	cloned := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.UserInput](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	clear(v.Parts[1].Media.InlineData)
	*v.Parts[0].Text = "changed"
	clear(wire)
	if !got.Equal(cloned) || !bytes.Equal(got.Parts[1].Media.InlineData, []byte{0xff, 0, 'x'}) {
		t.Fatal("clone or decoder retained storage")
	}
	if !(format.UserInput{RunID: 1, ID: 1}).Equal(format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{}}) {
		t.Fatal("nil and empty parts differ")
	}
	invalid := format.UserInput{Parts: cloned.Parts}
	if invalid.Validate() == nil {
		t.Fatal("zero ordinal validated")
	}
	if wire, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
		t.Fatalf("invalid encode: %v", err)
	}
	invalidWire := invalid.AppendCBOR(nil)
	got, err = codec.Decode[format.UserInput](invalidWire, limits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.UserInput{}) {
		t.Fatalf("invalid decode: %v", err)
	}
	receiver := cloned.Clone()
	before := receiver.Clone()
	storage := receiver.Parts
	reader, err := codec.NewReader(invalidWire, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) || &receiver.Parts[0] != &storage[0] {
		t.Fatalf("failed read mutated receiver: %v", err)
	}
}
