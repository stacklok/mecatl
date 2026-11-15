package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserContextPrefixCodec(t *testing.T) {
	v := format.UserContextPrefix{Blocks: []format.InstructionBlock{
		{Parts: []format.ContentPart{{Text: new("prepared prefix")}}, Source: format.ContentSource{Description: "source", WorkspaceFile: &format.WorkspaceFileSource{Path: "context.md"}}},
		{Source: format.ContentSource{Description: "empty block"}},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil || v.Validate() != nil {
		t.Fatalf("encode/validate: %v", err)
	}
	got, err := codec.Decode[format.UserContextPrefix](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("decode: %v", err)
	}
	*v.Blocks[0].Parts[0].Text = "changed"
	v.Blocks[0].Source.WorkspaceFile.Path = "changed"
	clear(wire)
	if !got.Equal(before) || v.Equal(before) {
		t.Fatal("prefix ownership or equality")
	}
	nilWire, err := codec.Encode(format.UserContextPrefix{}, limits())
	if err != nil {
		t.Fatal(err)
	}
	empty := format.UserContextPrefix{Blocks: []format.InstructionBlock{}}
	emptyWire, err := codec.Encode(empty, limits())
	if err != nil || !bytes.Equal(nilWire, emptyWire) || !(format.UserContextPrefix{}).Equal(empty) || empty.Equal(before) {
		t.Fatal("empty replacement canonical semantics")
	}
	for _, invalid := range []format.UserContextPrefix{
		{Blocks: []format.InstructionBlock{{}}},
		{Blocks: []format.InstructionBlock{{Parts: []format.ContentPart{{}}, Source: format.ContentSource{Description: "source"}}}},
	} {
		if invalid.Validate() == nil {
			t.Fatal("invalid prefix validated")
		}
		if wire, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("invalid encode: %v", err)
		}
		if got, err := codec.Decode[format.UserContextPrefix](invalid.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.UserContextPrefix{}) {
			t.Fatalf("invalid decode: %v", err)
		}
	}
}
