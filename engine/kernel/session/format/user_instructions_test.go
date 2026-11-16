package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserInstructionsCodec(t *testing.T) {
	v := format.UserInstructions{Blocks: []format.InstructionBlock{
		{Key: "operator/persona", Parts: []format.ContentPart{{Text: new("prepared prefix")}}, Source: format.ContentSource{Description: "source", WorkspaceFile: &format.WorkspaceFileSource{Path: "context.md"}}},
		{Key: "operator/empty", Source: format.ContentSource{Description: "empty block"}},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil || v.Validate() != nil {
		t.Fatalf("encode/validate: %v", err)
	}
	got, err := codec.Decode[format.UserInstructions](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("decode: %v", err)
	}
	*v.Blocks[0].Parts[0].Text = "changed"
	v.Blocks[0].Source.WorkspaceFile.Path = "changed"
	clear(wire)
	if !got.Equal(before) || v.Equal(before) {
		t.Fatal("prefix ownership or equality")
	}
	nilWire, err := codec.Encode(format.UserInstructions{}, limits())
	if err != nil {
		t.Fatal(err)
	}
	empty := format.UserInstructions{Blocks: []format.InstructionBlock{}}
	emptyWire, err := codec.Encode(empty, limits())
	if err != nil || !bytes.Equal(nilWire, emptyWire) || !(format.UserInstructions{}).Equal(empty) || empty.Equal(before) {
		t.Fatal("empty replacement canonical semantics")
	}
	for _, invalid := range []format.UserInstructions{
		{Blocks: []format.InstructionBlock{{Key: "operator/persona"}}},
		{Blocks: []format.InstructionBlock{{Key: "operator/persona", Parts: []format.ContentPart{{}}, Source: format.ContentSource{Description: "source"}}}},
	} {
		if invalid.Validate() == nil {
			t.Fatal("invalid prefix validated")
		}
		if wire, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("invalid encode: %v", err)
		}
		if got, err := codec.Decode[format.UserInstructions](invalid.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.UserInstructions{}) {
			t.Fatalf("invalid decode: %v", err)
		}
	}
}
