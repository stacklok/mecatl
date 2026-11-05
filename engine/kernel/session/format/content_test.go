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
	v := format.UserInput{RunID: 1, ID: 1, Parts: []format.ContentPart{
		{Kind: "text", Text: "  e\u0301\n\x00尾 "},
		{Kind: "image", Media: &format.Media{MediaType: "image/custom", Format: "raw", Source: "inline", Data: []byte{0xff, 0}}},
		{Kind: "text"},
		{Kind: "audio", Media: &format.Media{Source: "remote", URI: "custom:clip%2F1?x=2&x=1#尾"}},
		{Kind: "document", Media: &format.Media{Filename: " e\u0301.pdf ", Title: "Title\n", Source: "provider", Provider: "p ", Scope: " tenant ", ReferenceID: "01"}},
		{Kind: "image", Media: &format.Media{MediaType: "image/empty", Source: "inline"}},
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
	v.Parts[1].Media.Data[0] = 1
	v.Parts[3].Media.URI = "changed"
	v.Parts[4].Media.Scope = "changed"
	v.Parts[4].Media.Title = "changed"
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("decode or clone borrowed storage")
	}
	if !(format.UserInput{}).Equal(format.UserInput{Parts: []format.ContentPart{}}) {
		t.Fatal("nil/empty parts differ")
	}
	if (format.Media{}).Equal(format.Media{Source: "inline"}) {
		t.Fatal("inline presence lost")
	}
	if (format.ContentPart{Kind: "text"}).Equal(format.ContentPart{Kind: "text", Media: &format.Media{}}) {
		t.Fatal("pointer presence lost")
	}
	if !(format.Media{Source: "inline"}).Equal(format.Media{Source: "inline", Data: []byte{}}) {
		t.Fatal("nil/empty bytes differ")
	}
	if !bytes.Equal(got.Parts[1].Media.Data, []byte{0xff, 0}) {
		t.Fatal("bytes changed")
	}
}

func TestContentMalformed(t *testing.T) {
	for name, v := range map[string]format.ContentPart{
		"zero": {}, "unknown": {Kind: "video"},
		"text media":            {Kind: "text", Media: &format.Media{Source: "remote", URI: "urn:a"}},
		"invalid UTF-8":         {Kind: "text", Text: "\xff"},
		"missing media":         {Kind: "image"},
		"media text":            {Kind: "audio", Text: "x", Media: &format.Media{Source: "remote", URI: "urn:a"}},
		"missing source":        {Kind: "image", Media: &format.Media{}},
		"unknown source":        {Kind: "image", Media: &format.Media{Source: "other"}},
		"inline URI":            {Kind: "image", Media: &format.Media{MediaType: "x", Source: "inline", URI: "urn:a"}},
		"inline binding":        {Kind: "image", Media: &format.Media{MediaType: "x", Source: "inline", Provider: "p"}},
		"inline media type":     {Kind: "image", Media: &format.Media{Source: "inline"}},
		"image filename":        {Kind: "image", Media: &format.Media{Source: "remote", URI: "urn:a", Filename: "f"}},
		"audio title":           {Kind: "audio", Media: &format.Media{Source: "remote", URI: "urn:a", Title: "t"}},
		"bad format":            {Kind: "document", Media: &format.Media{Source: "remote", URI: "urn:a", Format: "\xff"}},
		"relative URI":          {Kind: "audio", Media: &format.Media{Source: "remote", URI: "relative/path"}},
		"bad escape":            {Kind: "audio", Media: &format.Media{Source: "remote", URI: "https://example/%xx"}},
		"remote bytes":          {Kind: "audio", Media: &format.Media{Source: "remote", URI: "urn:a", Data: []byte{1}}},
		"remote binding":        {Kind: "audio", Media: &format.Media{Source: "remote", URI: "urn:a", Scope: "s"}},
		"empty provider":        {Kind: "document", Media: &format.Media{Source: "provider", Scope: "s", ReferenceID: "i"}},
		"empty scope":           {Kind: "document", Media: &format.Media{Source: "provider", Provider: "p", ReferenceID: "i"}},
		"empty reference":       {Kind: "document", Media: &format.Media{Source: "provider", Provider: "p", Scope: "s"}},
		"provider bytes":        {Kind: "document", Media: &format.Media{Source: "provider", Provider: "p", Scope: "s", ReferenceID: "i", Data: []byte{1}}},
		"provider URI":          {Kind: "document", Media: &format.Media{Source: "provider", Provider: "p", Scope: "s", ReferenceID: "i", URI: "urn:a"}},
		"invalid binding UTF-8": {Kind: "document", Media: &format.Media{Source: "provider", Provider: "p", Scope: "\xff", ReferenceID: "i"}},
	} {
		t.Run(name, func(t *testing.T) {
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

func TestRemoteMediaSchemes(t *testing.T) {
	for _, uri := range []string{"urn:example:a", "file:///workspace/a", "data:image/png;base64,", "custom:opaque", "https://example/a%2fb?q=1&q=2"} {
		v := format.Media{Source: "remote", URI: uri}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatalf("%q: %v", uri, err)
		}
		got, err := codec.Decode[format.Media](wire, limits())
		if err != nil || got.URI != uri {
			t.Fatalf("URI normalized: %q, %v", got.URI, err)
		}
	}
}
