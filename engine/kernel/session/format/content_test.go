package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContentRoundTrip(t *testing.T) {
	v := format.Content{
		{Kind: "text", Text: "  e\u0301\n\x00尾 "},
		{Kind: "image", Media: &format.MediaContent{MediaType: "image/custom", Format: "raw", Source: format.MediaSource{Inline: &format.InlineMedia{Data: []byte{0xff, 0}}}}},
		{Kind: "text"},
		{Kind: "audio", Media: &format.MediaContent{Source: format.MediaSource{Remote: &format.RemoteMedia{URI: "custom:clip%2F1?x=2&x=1#尾"}}}},
		{Kind: "document", Media: &format.MediaContent{Filename: " e\u0301.pdf ", Title: "Title\n", Source: format.MediaSource{Provider: &format.ProviderMedia{Provider: "p ", Scope: " tenant ", ID: "01"}}}},
		{Kind: "image", Media: &format.MediaContent{MediaType: "image/empty", Source: format.MediaSource{Inline: &format.InlineMedia{}}}},
	}
	before := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.Content](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v[1].Media.Source.Inline.Data[0] = 1
	v[3].Media.Source.Remote.URI = "changed"
	v[4].Media.Source.Provider.Scope = "changed"
	v[4].Media.Title = "changed"
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("decode or clone borrowed mutable storage")
	}
	if !(format.Content(nil)).Equal(format.Content{}) {
		t.Fatal("nil and empty differ")
	}
	if (format.MediaSource{}).Equal(format.MediaSource{Inline: &format.InlineMedia{}}) {
		t.Fatal("empty inline presence lost")
	}
	if (format.ContentPart{Kind: "text"}).Equal(format.ContentPart{Kind: "text", Media: &format.MediaContent{}}) {
		t.Fatal("media pointer presence lost")
	}
	if !(format.InlineMedia{}).Equal(format.InlineMedia{Data: []byte{}}) {
		t.Fatal("nil and empty bytes differ")
	}
	if !bytes.Equal(got[1].Media.Source.Inline.Data, []byte{0xff, 0}) {
		t.Fatal("inline bytes changed")
	}
}

func TestContentMalformed(t *testing.T) {
	inline := format.MediaSource{Inline: &format.InlineMedia{}}
	remote := format.MediaSource{Remote: &format.RemoteMedia{URI: "urn:example:item"}}
	for name, v := range map[string]format.ContentPart{
		"zero":              {},
		"unknown":           {Kind: "video"},
		"text media":        {Kind: "text", Media: &format.MediaContent{Source: remote}},
		"invalid UTF-8":     {Kind: "text", Text: string([]byte{0xff})},
		"missing media":     {Kind: "image"},
		"media text":        {Kind: "audio", Text: "x", Media: &format.MediaContent{Source: remote}},
		"missing source":    {Kind: "image", Media: &format.MediaContent{}},
		"multiple sources":  {Kind: "image", Media: &format.MediaContent{MediaType: "image/x", Source: format.MediaSource{Inline: &format.InlineMedia{}, Remote: remote.Remote}}},
		"all sources":       {Kind: "document", Media: &format.MediaContent{MediaType: "x", Source: format.MediaSource{Inline: &format.InlineMedia{}, Remote: remote.Remote, Provider: &format.ProviderMedia{Provider: "p", Scope: "s", ID: "i"}}}},
		"inline media type": {Kind: "image", Media: &format.MediaContent{Source: inline}},
		"image filename":    {Kind: "image", Media: &format.MediaContent{Source: remote, Filename: "file"}},
		"audio title":       {Kind: "audio", Media: &format.MediaContent{Source: remote, Title: "title"}},
		"bad format":        {Kind: "document", Media: &format.MediaContent{Source: remote, Format: string([]byte{0xff})}},
		"relative URI":      {Kind: "audio", Media: &format.MediaContent{Source: format.MediaSource{Remote: &format.RemoteMedia{URI: "relative/path"}}}},
		"bad escape":        {Kind: "audio", Media: &format.MediaContent{Source: format.MediaSource{Remote: &format.RemoteMedia{URI: "https://example/%xx"}}}},
		"empty provider":    {Kind: "document", Media: &format.MediaContent{Source: format.MediaSource{Provider: &format.ProviderMedia{Scope: "s", ID: "i"}}}},
		"empty scope":       {Kind: "document", Media: &format.MediaContent{Source: format.MediaSource{Provider: &format.ProviderMedia{Provider: "p", ID: "i"}}}},
		"empty reference":   {Kind: "document", Media: &format.MediaContent{Source: format.MediaSource{Provider: &format.ProviderMedia{Provider: "p", Scope: "s"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
				t.Fatalf("encode: %v", err)
			}
			// Intentionally bypass CheckCBOR to exercise the independent reader.
			got, err := codec.Decode[format.ContentPart](v.AppendCBOR(nil), limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ContentPart{}) {
				t.Fatalf("decode: %#v, %v", got, err)
			}
		})
	}
}

func TestRemoteMediaSchemes(t *testing.T) {
	for _, uri := range []string{"urn:example:a", "file:///workspace/a", "data:image/png;base64,", "custom:opaque", "https://example/a%2fb?q=1&q=2"} {
		v := format.RemoteMedia{URI: uri}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatalf("%q: %v", uri, err)
		}
		got, err := codec.Decode[format.RemoteMedia](wire, limits())
		if err != nil || got.URI != uri {
			t.Fatalf("URI normalized: %q, %v", got.URI, err)
		}
	}
}
