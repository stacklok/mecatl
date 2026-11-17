package format_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestInstructionsWire(t *testing.T) {
	v := format.RootInstructions{Blocks: []format.InstructionBlock{{
		Key: "harness/core",
		Parts: []format.ContentPart{
			{Text: new("a")},
			{Text: new("b")},
		},
		Source: format.ContentSource{Description: "d"},
	}}}
	// Canonical maps retain ordered parts and require the exact block key.
	want := []byte("\xa1\x61b\x81\xa3\x61c\x82\xa1\x61t\x61a\xa1\x61t\x61b\x61k\x6charness/core\x61s\xa1\x61d\x61d")
	wire, err := codec.Encode(v, limits())
	if err != nil || format.Version != 1 || !bytes.Equal(wire, want) {
		t.Fatalf("instruction schema: %x, version %d, %v", wire, format.Version, err)
	}
	got, err := codec.Decode[format.RootInstructions](want, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("instruction fixture: %#v, %v", got, err)
	}
}

func TestInstructions(t *testing.T) {
	v := format.RootInstructions{Blocks: []format.InstructionBlock{
		{Key: "harness/core", Parts: []format.ContentPart{{Text: new(" root\n")}}, Source: format.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &format.WorkspaceFileSource{Path: "AGENTS.md"}}},
		{Key: "harness/empty", Source: format.ContentSource{Description: "explicitly empty contribution"}},
		{Key: "harness/media", Parts: []format.ContentPart{{Media: &format.Media{Kind: "image", MIMEType: "image/x", Source: "inline", InlineData: []byte{3, 2, 1}}}}, Source: format.ContentSource{Description: "prepared guidance"}},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.RootInstructions](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	*v.Blocks[0].Parts[0].Text = "changed"
	v.Blocks[0].Source.WorkspaceFile.Path = "changed"
	v.Blocks[2].Parts[0].Media.InlineData[0] = 9
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("instructions storage shared")
	}
	if !(format.RootInstructions{}).Equal(format.RootInstructions{Blocks: []format.InstructionBlock{}}) {
		t.Fatal("nil/empty blocks differ")
	}
	if (format.RootInstructions{}).Equal(format.RootInstructions{Blocks: []format.InstructionBlock{{Key: "harness/core", Source: format.ContentSource{Description: "empty contribution"}}}}) {
		t.Fatal("empty block collapsed")
	}
	if _, err := codec.Encode(format.RootInstructions{}, limits()); err != nil {
		t.Fatal(err)
	}
	invalid := format.RootInstructions{Blocks: []format.InstructionBlock{{Key: "harness/core"}}}
	if _, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
	if _, err := codec.Decode[format.RootInstructions](invalid.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
}
