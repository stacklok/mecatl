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

func TestUserInput(t *testing.T) {
	v := format.UserInput{RunID: 1, ID: 1, Messages: []format.AuthoredMessage{{Content: format.Content{
		{Kind: "text", Text: " /skill e\u0301  \n"},
		{Kind: "image", Media: &format.MediaContent{MediaType: "image/png", Source: format.MediaSource{Inline: &format.InlineMedia{Data: []byte{0xff, 0, 'x'}}}}},
		{Kind: "text", Text: "尾\n"},
	}}, {}}}
	cloned := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.UserInput](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	clear(v.Messages[0].Content[1].Media.Source.Inline.Data)
	v.Messages[0].Content[0].Text = "changed"
	v.Messages[1].Content = format.Content{{Kind: "text", Text: "changed"}}
	clear(wire)
	if !got.Equal(cloned) || !bytes.Equal(got.Messages[0].Content[1].Media.Source.Inline.Data, []byte{0xff, 0, 'x'}) {
		t.Fatal("clone or decoder retained content storage")
	}
	if !(format.UserInput{RunID: 1, ID: 1}).Equal(format.UserInput{RunID: 1, ID: 1, Messages: []format.AuthoredMessage{}}) {
		t.Fatal("nil and empty messages differ")
	}
	if (format.UserInput{RunID: 1, ID: 1}).Equal(format.UserInput{RunID: 1, ID: 1, Messages: []format.AuthoredMessage{{}}}) {
		t.Fatal("one empty message collapsed into no messages")
	}
	invalid := format.UserInput{Messages: cloned.Messages}
	if invalid.Validate() == nil {
		t.Fatal("zero ordinal validated")
	}
	if wire, err := codec.Encode(invalid, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
		t.Fatalf("invalid encode: %v", err)
	}
	// Bypass prechecking to exercise transactional local validation after children.
	invalidWire := invalid.AppendCBOR(nil)
	got, err = codec.Decode[format.UserInput](invalidWire, limits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.UserInput{}) {
		t.Fatalf("invalid decode returned nonzero value: %v", err)
	}
	receiver := cloned.Clone()
	before := receiver.Clone()
	storage := receiver.Messages
	reader, err := codec.NewReader(invalidWire, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) ||
		!receiver.Equal(before) || &receiver.Messages[0] != &storage[0] {
		t.Fatalf("invalid local decode changed receiver or storage: %v", err)
	}
}
