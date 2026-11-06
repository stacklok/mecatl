package format_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestEffectiveInput(t *testing.T) {
	first, last := uint64(1), uint64(math.MaxUint64)
	v := format.EffectiveInput{RunID: 1, Inputs: []format.InputItem{
		{UserInput: &first},
		{Inline: &format.AttributedInput{Actor: "skill/example", Purpose: "instructions", Parts: []format.ContentPart{{Text: new("expanded\n")}}, Source: format.ContentSource{Description: "skill expansion", WorkspaceFile: &format.WorkspaceFileSource{Path: "skills/example.md"}}}},
		{Inline: &format.AttributedInput{Actor: "extension/尾", Purpose: "context", Parts: []format.ContentPart{{Media: &format.Media{Kind: "document", MIMEType: "application/pdf", Source: "inline", InlineData: []byte{1, 2}}}}, Source: format.ContentSource{Description: "supplement"}}},
		{UserInput: &last}, // Aggregate admission owns membership and ordering.
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.EffectiveInput](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	first = 3
	last = 0
	v.Inputs[1].Inline.Source.WorkspaceFile.Path = "changed"
	*v.Inputs[1].Inline.Parts[0].Text = "changed"
	v.Inputs[2].Inline.Parts[0].Media.InlineData[0] = 9
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("effective input storage shared")
	}
	if (format.InputItem{}).Equal(format.InputItem{UserInput: new(uint64(1))}) {
		t.Fatal("reference presence lost")
	}
	if !(format.EffectiveInput{RunID: 1}).Equal(format.EffectiveInput{RunID: 1, Inputs: []format.InputItem{}}) {
		t.Fatal("nil/empty inputs differ")
	}
	if _, err := codec.Encode(format.EffectiveInput{RunID: 1}, limits()); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Encode(format.EffectiveInput{}, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("ordinal: %v", err)
	}
}

func TestInputItemMalformed(t *testing.T) {
	for _, v := range []format.InputItem{
		{}, {UserInput: new(uint64)},
		{UserInput: new(uint64(1)), Inline: &format.AttributedInput{Actor: "a", Purpose: "context", Source: format.ContentSource{Description: "d"}}},
		{Inline: &format.AttributedInput{Actor: "a", Purpose: "user", Source: format.ContentSource{Description: "d"}}},
		{Inline: &format.AttributedInput{Actor: "a", Purpose: "system", Source: format.ContentSource{Description: "d"}}},
		{Inline: &format.AttributedInput{Purpose: "context", Source: format.ContentSource{Description: "d"}}},
		{Inline: &format.AttributedInput{Actor: "\xff", Purpose: "context", Source: format.ContentSource{Description: "d"}}},
		{Inline: &format.AttributedInput{Actor: "a", Purpose: "context"}},
		{Inline: &format.AttributedInput{Actor: "a", Purpose: "instructions", Source: format.ContentSource{Description: "d", WorkspaceFile: &format.WorkspaceFileSource{Path: "../a"}}}},
	} {
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("encode: %v", err)
		}
		wire := v.AppendCBOR(nil)
		got, err := codec.Decode[format.InputItem](wire, limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.InputItem{}) {
			t.Fatalf("decode: %v", err)
		}
		receiver := format.InputItem{Inline: &format.AttributedInput{Actor: "a", Purpose: "context", Source: format.ContentSource{Description: "retain"}}}
		before := receiver.Clone()
		storage := receiver.Inline
		r, err := codec.NewReader(wire, limits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) || receiver.Inline != storage {
			t.Fatalf("failed read mutated receiver: %v", err)
		}
	}
}
