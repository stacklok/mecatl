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
	index := uint64(0)
	last := uint64(math.MaxUint64)
	v := format.EffectiveInput{RunID: 1, InputID: 2, Prompts: []format.PreparedPrompt{
		{AuthoredMessage: &index},
		{Harness: &format.HarnessPrompt{Purpose: "instructions", Content: format.Content{{Kind: "text", Text: "expanded\n"}}, Source: format.ContentSource{Description: "skill expansion", WorkspaceFile: &format.WorkspaceFileSource{Path: "skills/example.md"}}}},
		{Harness: &format.HarnessPrompt{Purpose: "context", Content: format.Content{{Kind: "document", Media: &format.MediaContent{MediaType: "application/pdf", Source: format.MediaSource{Inline: &format.InlineMedia{Data: []byte{1, 2}}}}}}, Source: format.ContentSource{Description: "supplement"}}},
		{AuthoredMessage: &last}, // Bounds are aggregate-owned, not format validation.
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
	index = 3
	last = 0
	v.Prompts[1].Harness.Source.WorkspaceFile.Path = "changed"
	v.Prompts[1].Harness.Content[0].Text = "changed"
	v.Prompts[2].Harness.Content[0].Media.Source.Inline.Data[0] = 9
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("effective input storage shared")
	}
	if (format.PreparedPrompt{}).Equal(format.PreparedPrompt{AuthoredMessage: new(uint64)}) {
		t.Fatal("index zero lost presence")
	}
	if !(format.EffectiveInput{RunID: 1, InputID: 2}).Equal(format.EffectiveInput{RunID: 1, InputID: 2, Prompts: []format.PreparedPrompt{}}) {
		t.Fatal("nil/empty prompts differ")
	}
	if _, err := codec.Encode(format.EffectiveInput{RunID: 1, InputID: 2}, limits()); err != nil {
		t.Fatal(err)
	}
	for _, v := range []format.EffectiveInput{{}, {RunID: 1}, {InputID: 1}} {
		if _, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("ordinals: %v", err)
		}
		got, err := codec.Decode[format.EffectiveInput](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.EffectiveInput{}) {
			t.Fatalf("decode ordinals: %v", err)
		}
	}
}

func TestPreparedPromptMalformed(t *testing.T) {
	for _, v := range []format.PreparedPrompt{
		{},
		{AuthoredMessage: new(uint64), Harness: &format.HarnessPrompt{Purpose: "context", Source: format.ContentSource{Description: "d"}}},
		{Harness: &format.HarnessPrompt{Purpose: "user", Source: format.ContentSource{Description: "d"}}},
		{Harness: &format.HarnessPrompt{Purpose: "system", Source: format.ContentSource{Description: "d"}}},
		{Harness: &format.HarnessPrompt{Purpose: "context"}},
		{Harness: &format.HarnessPrompt{Purpose: "instructions", Source: format.ContentSource{Description: "d", WorkspaceFile: &format.WorkspaceFileSource{Path: "../a"}}}},
	} {
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("encode: %v", err)
		}
		wire := v.AppendCBOR(nil)
		got, err := codec.Decode[format.PreparedPrompt](wire, limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.PreparedPrompt{}) {
			t.Fatalf("decode: %v", err)
		}
		receiver := format.PreparedPrompt{Harness: &format.HarnessPrompt{Purpose: "context", Source: format.ContentSource{Description: "retain"}}}
		before := receiver.Clone()
		storage := receiver.Harness
		r, err := codec.NewReader(wire, limits())
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) || receiver.Harness != storage {
			t.Fatalf("failed read mutated receiver: %v", err)
		}
	}
}
