package format_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func FuzzEffectiveInputDecode(f *testing.F) {
	for _, v := range []format.EffectiveInput{
		{RunID: 1},
		{RunID: 1, Inputs: []format.InputItem{{UserInputID: new(uint64(1))}, {Inline: &format.AttributedInput{Actor: "skill", Purpose: "context", Parts: []format.ContentPart{{Media: &format.Media{Kind: "image", MIMEType: "image/x", Source: "inline", InlineData: []byte{0xff}}}}, Source: format.ContentSource{Description: "expanded", WorkspaceFile: &format.WorkspaceFileSource{Path: "skill.md"}}}}}},
	} {
		f.Add(v.AppendCBOR(nil))
	}
	f.Add([]byte{0xa0})
	f.Add([]byte{0xa2, 0x61, 'i', 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, wire []byte) {
		v, err := codec.Decode[format.EffectiveInput](wire, limits())
		if err != nil {
			if !reflect.DeepEqual(v, format.EffectiveInput{}) {
				t.Fatal("failed decode returned partial output")
			}
			return
		}
		out, err := codec.Encode(v, limits())
		if err != nil || !bytes.Equal(wire, out) {
			t.Fatalf("noncanonical success: %v", err)
		}
		clone := v.Clone()
		clear(wire)
		if !v.Equal(clone) {
			t.Fatal("decode borrowed input storage")
		}
		if !bytes.Equal(v.AppendCBOR(nil), out) {
			t.Fatal("mutating wire changed decoded value")
		}
	})
}

func FuzzToolCatalogDecode(f *testing.F) {
	for _, v := range []format.ToolCatalog{
		{},
		{Tools: []format.ToolDefinition{{Name: "a", ArgumentSchema: []byte(" {\"type\": \"object\"} ")}, {Name: "b", ArgumentSchema: []byte("false")}}},
		{Tools: []format.ToolDefinition{{Name: "a", ArgumentSchema: []byte("true")}, {Name: "a", ArgumentSchema: []byte("false")}}},
	} {
		f.Add(v.AppendCBOR(nil))
	}
	f.Add([]byte{0xa1, 0x61, 't', 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, wire []byte) {
		v, err := codec.Decode[format.ToolCatalog](wire, limits())
		if err != nil {
			if !reflect.DeepEqual(v, format.ToolCatalog{}) {
				t.Fatal("failed decode returned partial output")
			}
			return
		}
		out, err := codec.Encode(v, limits())
		if err != nil || !bytes.Equal(wire, out) {
			t.Fatalf("noncanonical success: %v", err)
		}
		clone := v.Clone()
		clear(wire)
		if !v.Equal(clone) || !bytes.Equal(v.AppendCBOR(nil), out) {
			t.Fatal("decode borrowed input storage")
		}
	})
}
