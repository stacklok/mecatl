package format_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestInstructionsWire(t *testing.T) {
	v := format.Instructions{Blocks: []format.InstructionBlock{{
		Parts: []format.ContentPart{
			{Kind: format.ContentText, Text: "a"},
			{Kind: format.ContentText, Text: "b"},
		},
		Source: format.ContentSource{Description: "d"},
	}}}
	// The public field rename retains version 1's "c" key and ordered parts.
	want := []byte("\xa1\x61b\x81\xa2\x61c\x82\xa2\x61k\x64text\x61t\x61a\xa2\x61k\x64text\x61t\x61b\x61s\xa1\x61d\x61d")
	wire, err := codec.Encode(v, limits())
	if err != nil || format.Version != 1 || !bytes.Equal(wire, want) {
		t.Fatalf("instruction schema: %x, version %d, %v", wire, format.Version, err)
	}
	got, err := codec.Decode[format.Instructions](want, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("instruction fixture: %#v, %v", got, err)
	}
}

func TestInstructions(t *testing.T) {
	v := format.Instructions{Blocks: []format.InstructionBlock{
		{Parts: []format.ContentPart{{Kind: "text", Text: " root\n"}}, Source: format.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &format.WorkspaceFileSource{Path: "AGENTS.md"}}},
		{Source: format.ContentSource{Description: "explicitly empty contribution"}},
		{Parts: []format.ContentPart{{Kind: "image", Media: &format.Media{MediaType: "image/x", Source: "inline", Data: []byte{3, 2, 1}}}}, Source: format.ContentSource{Description: "prepared guidance"}},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.Instructions](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.Blocks[0].Parts[0].Text = "changed"
	v.Blocks[0].Source.WorkspaceFile.Path = "changed"
	v.Blocks[2].Parts[0].Media.Data[0] = 9
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("instructions storage shared")
	}
	if !(format.Instructions{}).Equal(format.Instructions{Blocks: []format.InstructionBlock{}}) {
		t.Fatal("nil/empty blocks differ")
	}
	if (format.Instructions{}).Equal(format.Instructions{Blocks: []format.InstructionBlock{{}}}) {
		t.Fatal("empty block collapsed")
	}
	if _, err := codec.Encode(format.Instructions{}, limits()); err != nil {
		t.Fatal(err)
	}
	invalid := format.Instructions{Blocks: []format.InstructionBlock{{}}}
	if _, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	if _, err := codec.Decode[format.Instructions](invalid.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
}
