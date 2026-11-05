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

func TestContentBoundedTraversal(t *testing.T) {
	v := format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{
		{Kind: "text", Text: "exact UTF-8 text"},
		{Kind: "image", Media: &format.Media{MediaType: "image/custom", Source: "inline", Data: bytes.Repeat([]byte{1}, 20)}},
	}}
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range []string{"bytes", "string", "byte string", "array", "map", "depth", "nodes", "collections"} {
		t.Run(dimension, func(t *testing.T) {
			l := limits()
			switch dimension {
			case "bytes":
				l.MaxBytes = len(wire) - 1
			case "string":
				l.MaxStringBytes = 10
			case "byte string":
				l.MaxByteStringBytes = 19
			case "array":
				l.MaxArrayElements = 1
			case "map":
				l.MaxMapPairs = 2
			case "depth":
				l.MaxDepth = 3
			case "nodes":
				l.MaxNodes = 10
			case "collections":
				l.MaxCollectionElements = 5
			}
			if out, err := codec.Encode(v, l); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := codec.Decode[format.UserInput](wire, l)
			if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.UserInput{}) {
				t.Fatalf("decode: %v", err)
			}
		})
	}
	for _, count := range [][]byte{
		{0x9a, 0x80, 0, 0, 0}, // More than MaxInt on 386.
		{0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		// Declared counts must fail before any count-based allocation or element read.
		if got, err := codec.Decode[format.UserInput](append(append([]byte{0xa3, 0x61, 'i', 1, 0x61, 'p'}, count...), 0x61, 'r', 1), limits()); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.UserInput{}) {
			t.Fatalf("content count: %v", err)
		}
		catalog := append([]byte{0xa1, 0x61, 't'}, count...)
		if got, err := codec.Decode[format.ToolCatalog](catalog, limits()); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolCatalog{}) {
			t.Fatalf("catalog count: %v", err)
		}
	}
}

func TestJSONBoundedBeforeValidation(t *testing.T) {
	// Oversized invalid JSON must hit the byte budget, not JSON validation. No
	// schema-specific cap is involved: raising the budget exposes the syntax error.
	badJSON := bytes.Repeat([]byte{'x'}, 64)
	l := limits()
	l.MaxByteStringBytes = len(badJSON) - 1
	binding := format.ModelBinding{Route: "r", API: "a", Model: "m", Settings: badJSON}
	if out, err := codec.Encode(binding, l); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
		t.Fatalf("binding encode: %v", err)
	}
	gotBinding, err := codec.Decode[format.ModelBinding](binding.AppendCBOR(nil), l)
	if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(gotBinding, format.ModelBinding{}) {
		t.Fatalf("binding decode: %v", err)
	}
	if _, err := codec.Encode(binding, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("binding syntax: %v", err)
	}

	catalog := format.ToolCatalog{Tools: []format.ToolDefinition{{Name: "a", ArgumentSchema: badJSON}, {Name: "a", ArgumentSchema: []byte("true")}}}
	if out, err := codec.Encode(catalog, l); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
		t.Fatalf("catalog encode before syntax/uniqueness: %v", err)
	}
	gotCatalog, err := codec.Decode[format.ToolCatalog](catalog.AppendCBOR(nil), l)
	if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(gotCatalog, format.ToolCatalog{}) {
		t.Fatalf("catalog decode: %v", err)
	}

	// Array bounds also precede duplicate-name set allocation.
	catalog.Tools[0].ArgumentSchema = []byte("true")
	l = limits()
	l.MaxArrayElements = 1
	if _, err := codec.Encode(catalog, l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("catalog count before uniqueness: %v", err)
	}
	if _, err := codec.Decode[format.ToolCatalog](catalog.AppendCBOR(nil), l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("catalog read count: %v", err)
	}
}

func TestInputItemWire(t *testing.T) {
	v := format.InputItem{UserInput: new(uint64(1))}
	wire, err := codec.Encode(v, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa1, 0x61, 'u', 1}) {
		t.Fatalf("reference schema: %x, %v", wire, err)
	}
	for _, bad := range [][]byte{
		{0xa0}, {0xa1, 0x61, 'u', 0}, {0xa1, 0x61, 'u', 0xf6}, {0xa1, 0x61, 'r', 1},
		{0xa2, 0x61, 'u', 1, 0x61, 'u', 1}, append(bytes.Clone(wire), 0),
	} {
		if got, err := codec.Decode[format.InputItem](bad, limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.InputItem{}) {
			t.Fatalf("malformed item: %v", err)
		}
	}
	wire, err = codec.Encode(format.UserInput{RunID: 1, ID: 1}, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa3, 0x61, 'i', 1, 0x61, 'p', 0x80, 0x61, 'r', 1}) {
		t.Fatalf("input schema: %x, %v", wire, err)
	}
}

func TestFormatValidatorsAreLocal(t *testing.T) {
	// Generated traversal, not local validation, owns child validity and bounds.
	for _, err := range []error{
		(format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{{}}}).Validate(),
		(format.ContentPart{Kind: "image", Media: &format.Media{}}).Validate(),
		(format.ContentSource{Description: "d", WorkspaceFile: &format.WorkspaceFileSource{}}).Validate(),
		(format.InputItem{Inline: &format.AttributedInput{}}).Validate(),
		(format.AttributedInput{Actor: "a", Purpose: "context"}).Validate(),
		(format.EffectiveInput{RunID: 1, Inputs: []format.InputItem{{UserInput: new(uint64(math.MaxUint64))}, {}}}).Validate(),
		(format.ToolCatalog{Tools: []format.ToolDefinition{{Name: "a"}}}).Validate(),
	} {
		if err != nil {
			t.Fatalf("recursive validator: %v", err)
		}
	}
}
