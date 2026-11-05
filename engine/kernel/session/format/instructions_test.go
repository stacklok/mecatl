package format_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestInstructions(t *testing.T) {
	v := format.Instructions{Blocks: []format.InstructionBlock{
		{Content: format.Content{{Kind: "text", Text: " root\n"}}, Source: format.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &format.WorkspaceFileSource{Path: "AGENTS.md"}}},
		{Source: format.ContentSource{Description: "explicitly empty contribution"}},
		{Content: format.Content{{Kind: "image", Media: &format.MediaContent{MediaType: "image/x", Source: format.MediaSource{Inline: &format.InlineMedia{Data: []byte{3, 2, 1}}}}}}, Source: format.ContentSource{Description: "prepared guidance"}},
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
	v.Blocks[0].Content[0].Text = "changed"
	v.Blocks[0].Source.WorkspaceFile.Path = "changed"
	v.Blocks[2].Content[0].Media.Source.Inline.Data[0] = 9
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
