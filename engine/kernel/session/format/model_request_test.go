package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestModelRequestOptionalSource(t *testing.T) {
	v := format.ModelRequest{RunID: 1, ID: 1, Recipe: format.ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}}
	for _, source := range []*format.SourceHead{nil, {Sequence: 1, CID: contentid.Sum([]byte("head"))}} {
		v.Source = source
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.ModelRequest](wire, limits())
		if err != nil || !v.Equal(got) || (got.Source == nil) != (source == nil) {
			t.Fatalf("optional anchor roundtrip: %v", err)
		}
		clone := got.Clone()
		if clone.Source != nil {
			if clone.Source == got.Source {
				t.Fatal("clone borrowed anchor")
			}
			clone.Source.Sequence++
			if clone.Equal(got) {
				t.Fatal("anchor identity omitted from equality")
			}
		}
		if source == nil {
			// Absence is omission, not an encoded CID link or CBOR null.
			if bytes.Contains(wire, []byte{0xd8, 0x2a}) {
				t.Fatal("absent anchor encoded CID")
			}
		}
	}
	for _, source := range []*format.SourceHead{{}, {Sequence: 1}, {CID: contentid.Sum([]byte("head"))}, {Sequence: -1, CID: contentid.Sum([]byte("head"))}} {
		v.Source = source
		if err := v.Validate(); err == nil {
			t.Fatal("present malformed anchor validated")
		}
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("malformed anchor encode: %v", err)
		}
		// Deliberately bypass checks to exercise generated child decoding,
		// including the CID owner's strict rejection of its zero representation.
		got, err := codec.Decode[format.ModelRequest](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ModelRequest{}) {
			t.Fatalf("malformed anchor decode: %v", err)
		}
	}
}
