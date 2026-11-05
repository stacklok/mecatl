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
	v := format.UserInput{RunID: 1, ID: 1, Messages: []format.AuthoredMessage{{Content: format.Content{
		{Kind: "text", Text: "exact UTF-8 text"},
		{Kind: "image", Media: &format.MediaContent{MediaType: "image/custom", Source: format.MediaSource{Inline: &format.InlineMedia{Data: bytes.Repeat([]byte{1}, 20)}}}}},
	}}}
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
				l.MaxDepth = 7
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
		if got, err := codec.Decode[format.Content](count, limits()); !errors.Is(err, codec.ErrLimitExceeded) || got != nil {
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

func TestPreparedPromptWire(t *testing.T) {
	v := format.PreparedPrompt{AuthoredMessage: new(uint64)}
	wire, err := codec.Encode(v, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa1, 0x61, 'a', 0}) {
		t.Fatalf("authored index zero schema: %x, %v", wire, err)
	}
	for _, bad := range [][]byte{
		{0xa0},                             // Absent origin.
		{0xa1, 0x61, 'a', 0xf6},            // Null is not pointer presence.
		{0xa1, 0x61, 'r', 0},               // No provider role field.
		{0xa2, 0x61, 'a', 0, 0x61, 'a', 1}, // Duplicate field.
		append(bytes.Clone(wire), 0),       // Trailing bytes.
	} {
		if got, err := codec.Decode[format.PreparedPrompt](bad, limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.PreparedPrompt{}) {
			t.Fatalf("malformed prompt: %v", err)
		}
	}
	input := format.UserInput{RunID: 1, ID: 1}
	wire, err = codec.Encode(input, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa3, 0x61, 'i', 1, 0x61, 'm', 0x80, 0x61, 'r', 1}) {
		t.Fatalf("authored v2 schema: %x, %v", wire, err)
	}
	if format.Version != 1 || format.UserInputVersion != 2 || format.EffectiveInputVersion != 1 || format.InstructionsVersion != 1 || format.ToolCatalogVersion != 1 || format.ModelBindingVersion != 1 {
		t.Fatal("kind version contract changed")
	}
}

func TestFormatValidatorsAreLocal(t *testing.T) {
	// Invalid descendants are the generated traversal's responsibility. Parent
	// validators must not recursively parse or traverse unbounded child values.
	for _, err := range []error{
		(format.UserInput{RunID: 1, ID: 1, Messages: []format.AuthoredMessage{{Content: format.Content{{}}}}}).Validate(),
		(format.ContentPart{Kind: "image", Media: &format.MediaContent{}}).Validate(),
		(format.MediaContent{Source: format.MediaSource{}}).Validate(),
		(format.MediaSource{Remote: &format.RemoteMedia{}}).Validate(),
		(format.ContentSource{Description: "d", WorkspaceFile: &format.WorkspaceFileSource{}}).Validate(),
		(format.PreparedPrompt{Harness: &format.HarnessPrompt{}}).Validate(),
		(format.HarnessPrompt{Purpose: "context"}).Validate(),
		(format.EffectiveInput{RunID: 1, InputID: math.MaxUint64, Prompts: []format.PreparedPrompt{{}}}).Validate(),
		(format.ToolCatalog{Tools: []format.ToolDefinition{{Name: "a"}}}).Validate(),
	} {
		if err != nil {
			t.Fatalf("validator recursively checked a child: %v", err)
		}
	}
}
