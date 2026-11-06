package format_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunOwnedSchema(t *testing.T) {
	limits := codec.Limits{MaxBytes: 8192, MaxStringBytes: 8192, MaxByteStringBytes: 8192,
		MaxArrayElements: 16, MaxMapPairs: 8, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	input := format.UserInput{RunID: 24, ID: 1, Parts: []format.ContentPart{{Text: new("exact text")}}}
	wire, err := codec.Encode(input, limits)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode[format.UserInput](wire, limits)
	if err != nil || !decoded.Equal(input) {
		t.Fatalf("round trip: %v", err)
	}
	clear(wire)
	if !decoded.Equal(input) {
		t.Fatal("decode borrows encoded data")
	}
	clone := decoded.Clone()
	*clone.Parts[0].Text = "changed"
	if !decoded.Equal(input) {
		t.Fatal("clone aliases content")
	}
	for _, v := range []format.UserInput{{}, {ID: 1}, {RunID: 1}} {
		if _, err := codec.Encode(v, limits); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal(err)
		}
	}
	for _, malformed := range [][]byte{
		{0xa2, 0x61, 'i', 1, 0x61, 'p', 0x80},               // Missing owning run.
		{0xa3, 0x61, 'i', 1, 0x61, 'p', 0x80, 0x61, 'r', 0}, // Zero owning run.
	} {
		if _, err := codec.Decode[format.UserInput](malformed, limits); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatal(err)
		}
	}
	start, err := codec.Encode(format.RunStarted{ID: 1}, limits)
	if err != nil || !bytes.Equal(start, []byte{0xa1, 0x61, 'r', 1}) {
		t.Fatalf("start schema: %x, %v", start, err)
	}
	if _, err := codec.Decode[format.RunStarted]([]byte{0xa2, 0x61, 'i', 0x81, 1, 0x61, 'r', 1}, limits); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
}
