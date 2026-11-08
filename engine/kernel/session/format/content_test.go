package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContentVocabulary(t *testing.T) {
	for _, pair := range [][2]string{
		{string(format.MediaKindImage), "image"},
		{string(format.MediaKindAudio), "audio"},
		{string(format.MediaKindDocument), "document"},
		{string(format.MediaSourceInline), "inline"},
		{string(format.MediaSourceRemote), "remote"},
		{string(format.MediaSourceProvider), "provider"},
		{format.PromptInstructions, "instructions"},
		{format.PromptContext, "context"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("persisted vocabulary = %q, want %q", pair[0], pair[1])
		}
	}
}

func TestContentRoundTrip(t *testing.T) {
	v := format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{
		{Text: new("  e\u0301\n\x00尾 ")},
		{Media: &format.Media{Kind: "image", MIMEType: "image/custom", FormatHint: "raw", Source: "inline", InlineData: []byte{0xff, 0}}},
		{Text: new("")},
		{Media: &format.Media{Kind: "audio", Source: "remote", RemoteURI: "custom:clip%2F1?x=2&x=1#尾"}},
		{Media: &format.Media{Kind: "document", Filename: " e\u0301.pdf ", Title: "Title\n", Source: "provider", ProviderName: "p ", ProviderScope: " tenant ", ProviderReferenceID: "01"}},
		{Media: &format.Media{Kind: "image", MIMEType: "image/empty", Source: "inline"}},
	}}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.UserInput](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	*v.Parts[0].Text = "changed"
	*v.Parts[2].Text = "no longer empty"
	v.Parts[1].Media.InlineData[0] = 1
	v.Parts[3].Media.RemoteURI = "changed"
	v.Parts[4].Media.ProviderScope = "changed"
	v.Parts[4].Media.Title = "changed"
	clear(wire)
	if !got.Equal(before) || !before.Equal(before.Clone()) {
		t.Fatal("decode or clone borrowed storage")
	}
	if !(format.UserInput{}).Equal(format.UserInput{Parts: []format.ContentPart{}}) {
		t.Fatal("nil/empty parts differ")
	}
	if (format.Media{}).Equal(format.Media{Source: "inline"}) {
		t.Fatal("inline presence lost")
	}
	if (format.ContentPart{}).Equal(format.ContentPart{Text: new("")}) || (format.ContentPart{Text: new("")}).Equal(format.ContentPart{Text: new(""), Media: &format.Media{}}) {
		t.Fatal("pointer presence lost")
	}
	if !(format.Media{Source: "inline"}).Equal(format.Media{Source: "inline", InlineData: []byte{}}) {
		t.Fatal("nil/empty bytes differ")
	}
	if !bytes.Equal(got.Parts[1].Media.InlineData, []byte{0xff, 0}) {
		t.Fatal("bytes changed")
	}
}

func TestContentMalformed(t *testing.T) {
	for name, v := range map[string]format.ContentPart{
		"neither":            {},
		"both empty text":    {Text: new(""), Media: &format.Media{Kind: "image", Source: "remote", RemoteURI: "urn:a"}},
		"both nonempty text": {Text: new("x"), Media: &format.Media{Kind: "audio", Source: "remote", RemoteURI: "urn:a"}},
		"invalid UTF-8":      {Text: new("\xff")},
		"invalid child":      {Media: &format.Media{}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := v.Validate(); err == nil {
				t.Fatal("malformed part passed recursive validation")
			}
			if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := codec.Decode[format.ContentPart](v.AppendCBOR(nil), limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ContentPart{}) {
				t.Fatalf("decode: %#v, %v", got, err)
			}
		})
	}
}

func TestContentTextWire(t *testing.T) {
	v := format.ContentPart{Text: new("")}
	wire, err := codec.Encode(v, limits())
	if err != nil || !bytes.Equal(wire, []byte{0xa1, 0x61, 't', 0x60}) {
		t.Fatalf("explicit empty text: %x, %v", wire, err)
	}
	got, err := codec.Decode[format.ContentPart](wire, limits())
	if err != nil || got.Text == nil || *got.Text != "" || got.Media != nil {
		t.Fatalf("explicit empty text lost: %#v, %v", got, err)
	}
	for _, wire := range [][]byte{{0xa0}, {0xa1, 0x61, 't', 0xf6}, {0xa2, 0x61, 't', 0x60, 0x61, 't', 0x60}, {0xa2, 0x61, 'k', 0x64, 't', 'e', 'x', 't', 0x61, 't', 0x60}} {
		if got, err := codec.Decode[format.ContentPart](wire, limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ContentPart{}) {
			t.Fatalf("malformed text: %#v, %v", got, err)
		}
	}
}

func TestRemoteMediaSchemes(t *testing.T) {
	for _, uri := range []string{"urn:example:a", "file:///workspace/a", "data:image/png;base64,", "custom:opaque", "https://example/a%2fb?q=1&q=2"} {
		v := format.Media{Kind: "image", Source: "remote", RemoteURI: uri}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatalf("%q: %v", uri, err)
		}
		got, err := codec.Decode[format.Media](wire, limits())
		if err != nil || got.RemoteURI != uri {
			t.Fatalf("URI normalized: %q, %v", got.RemoteURI, err)
		}
	}
}
