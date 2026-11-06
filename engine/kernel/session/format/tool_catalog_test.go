package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestToolCatalog(t *testing.T) {
	v := format.ToolCatalog{Tools: []format.ToolDefinition{
		{Name: " z ", Description: " exact\n", ArgumentSchema: []byte(" {\n \"z\":1e+02, \"a\": [true, null], \"z\": 2 } ")},
		{Name: "a", ArgumentSchema: []byte("false")},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolCatalog](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.Tools[0].Name = "changed"
	clear(v.Tools[0].ArgumentSchema)
	clear(wire)
	if !got.Equal(before) || !bytes.Equal(got.Tools[0].ArgumentSchema, before.Tools[0].ArgumentSchema) {
		t.Fatal("catalog borrowed or normalized JSON")
	}
	if !(format.ToolCatalog{}).Equal(format.ToolCatalog{Tools: []format.ToolDefinition{}}) {
		t.Fatal("nil/empty tools differ")
	}
	if _, err := codec.Encode(format.ToolCatalog{}, limits()); err != nil {
		t.Fatal(err)
	}
}

func TestToolCatalogMalformed(t *testing.T) {
	for _, tools := range [][]format.ToolDefinition{
		{{Name: "a", ArgumentSchema: []byte("true")}, {Name: "a", ArgumentSchema: []byte("false")}},
		{{ArgumentSchema: []byte("true")}},
		{{Name: "a"}},
		{{Name: "a", ArgumentSchema: []byte("{} trailing")}},
		{{Name: "a", ArgumentSchema: []byte("{")}},
		{{Name: "a", Description: string([]byte{0xff}), ArgumentSchema: []byte("true")}},
	} {
		v := format.ToolCatalog{Tools: tools}
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := codec.Decode[format.ToolCatalog](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolCatalog{}) {
			t.Fatalf("decode: %v", err)
		}
	}
}
