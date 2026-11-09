package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestMediaWire(t *testing.T) {
	v := format.Media{Kind: format.MediaKindDocument, Source: format.MediaSourceInline,
		InlineData: []byte{0xff, 0}, MIMEType: "x", FormatHint: "raw", Filename: "f", Title: "t"}
	// Existing source and metadata keys retain their meanings; "k" now belongs to media.
	want := []byte("\xab\x61d\x42\xff\x00\x61f\x63raw\x61k\x68document\x61m\x61x\x61n\x61f\x61o\x60\x61p\x60\x61r\x60\x61s\x66inline\x61t\x61t\x61u\x60")
	wire, err := codec.Encode(v, limits())
	if err != nil || !bytes.Equal(wire, want) {
		t.Fatalf("media schema: %x, %v", wire, err)
	}
	got, err := codec.Decode[format.Media](want, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("media fixture: %#v, %v", got, err)
	}
}

func TestStandaloneMediaMatrix(t *testing.T) {
	for _, kind := range []string{"image", "audio", "document"} {
		for _, source := range []string{"inline", "remote", "provider"} {
			t.Run(kind+"/"+source, func(t *testing.T) {
				v := format.Media{Kind: format.MediaKind(kind), Source: format.MediaSourceKind(source), FormatHint: " exact\n"}
				switch source {
				case "inline":
					v.MIMEType = "application/custom"
				case "remote":
					v.RemoteURI = "custom:exact%2f尾"
				case "provider":
					v.ProviderName, v.ProviderScope, v.ProviderReferenceID = " p ", " scope/尾 ", "01"
				}
				check := func(v format.Media, valid bool) {
					t.Helper()
					if err := v.Validate(); (err == nil) != valid {
						t.Fatalf("Validate(%#v): %v; valid=%t", v, err, valid)
					}
					wire, err := codec.Encode(v, limits())
					got, decodeErr := codec.Decode[format.Media](v.AppendCBOR(nil), limits())
					if valid {
						if err != nil || decodeErr != nil || !got.Equal(v) {
							t.Fatalf("roundtrip: %v, %v", err, decodeErr)
						}
					} else if wire != nil || !errors.Is(err, codec.ErrInvalidData) || !errors.Is(decodeErr, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.Media{}) {
						t.Fatalf("invalid media: encode %v, decode %#v, %v", err, got, decodeErr)
					}
				}
				check(v, true)
				for _, field := range []string{"Filename", "Title"} {
					withMetadata := v
					reflect.ValueOf(&withMetadata).Elem().FieldByName(field).SetString(" exact e\u0301\n")
					check(withMetadata, kind == "document")
				}
				withMIME := v
				withMIME.MIMEType = " optional/custom "
				check(withMIME, true)
				for _, field := range []string{"Kind", "Source", "MIMEType", "FormatHint", "Filename", "Title", "RemoteURI", "ProviderName", "ProviderScope", "ProviderReferenceID"} {
					bad := v
					reflect.ValueOf(&bad).Elem().FieldByName(field).SetString("\xff")
					check(bad, false)
				}
				for _, field := range []struct {
					name, source string
				}{
					{"RemoteURI", "remote"}, {"ProviderName", "provider"}, {"ProviderScope", "provider"}, {"ProviderReferenceID", "provider"},
				} {
					bad := v
					f := reflect.ValueOf(&bad).Elem().FieldByName(field.name)
					if source == field.source {
						f.SetString("") // Each required source field is independently required.
					} else {
						f.SetString("urn:foreign") // Every foreign source field is independently forbidden.
					}
					check(bad, false)
				}
				withBytes := v
				withBytes.InlineData = []byte{0xff, 0, 'x'}
				check(withBytes, source == "inline")
				for _, empty := range [][]byte{nil, {}} {
					withBytes.InlineData = empty
					check(withBytes, true)
				}
				if source == "inline" {
					v.MIMEType = ""
					check(v, false)
				}
			})
		}
	}
	for _, v := range []format.Media{
		{},
		{Kind: "text", Source: "remote", RemoteURI: "urn:a"},
		{Kind: "video", Source: "remote", RemoteURI: "urn:a"},
		{Kind: "image", Source: "other"},
		{Kind: "audio", Source: "remote", RemoteURI: "relative/path"},
		{Kind: "audio", Source: "remote", RemoteURI: "https://example/%xx"},
	} {
		if err := v.Validate(); err == nil {
			t.Fatalf("invalid media validated: %#v", v)
		}
	}
}
