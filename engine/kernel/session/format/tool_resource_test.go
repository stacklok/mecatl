package format_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestToolResource(t *testing.T) {
	v := format.ToolResource{URI: "custom:opaque#fragment", Name: ptr(""), Title: ptr("title"), MIMEType: ptr("custom/type")}
	before := v.Clone()
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolResource](wire, toolLimits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	*v.Name = "changed"
	*v.Title = "changed"
	*v.MIMEType = "changed"
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("resource aliases")
	}
	if (format.ToolResource{URI: "urn:x"}).Equal(format.ToolResource{URI: "urn:x", Name: ptr("")}) {
		t.Fatal("lost label presence")
	}
	for _, uri := range []string{"urn:x", "file:///not/fetched", "https://example.invalid/a?q=b", "custom:opaque"} {
		if err := (format.ToolResource{URI: uri}).Validate(); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
	}
	for _, uri := range []string{"", "relative/path", "//example.invalid/path", ":bad", "https://example.invalid/%zz", "urn:\xff", "https://example.invalid/\n"} {
		v := format.ToolResource{URI: uri}
		if v.Validate() == nil {
			t.Fatalf("URI accepted: %q", uri)
		}
		if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
			t.Fatalf("encode: %v", err)
		}
		if got, err := codec.Decode[format.ToolResource](v.AppendCBOR(nil), toolLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ToolResource{}) {
			t.Fatalf("decode: %v", err)
		}
	}
}

func TestToolResourceMetadataCaps(t *testing.T) {
	for _, field := range []string{"uri", "name", "title", "mime"} {
		t.Run(field, func(t *testing.T) {
			for _, over := range []int{0, 1} {
				v := format.ToolResource{URI: "urn:x"}
				switch field {
				case "uri":
					v.URI = "urn:" + strings.Repeat("x", 16384-4+over)
				case "name":
					v.Name = ptr(strings.Repeat("x", 1024+over))
				case "title":
					v.Title = ptr(strings.Repeat("x", 1024+over))
				case "mime":
					v.MIMEType = ptr(strings.Repeat("x", 1024+over))
				}
				out, err := codec.Encode(v, toolLimits())
				if over == 0 {
					if err != nil || v.Validate() != nil {
						t.Fatalf("exact cap: %v", err)
					}
					if got, err := codec.Decode[format.ToolResource](out, toolLimits()); err != nil || !got.Equal(v) {
						t.Fatalf("roundtrip: %v", err)
					}
				} else {
					if v.Validate() == nil || !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
						t.Fatalf("one over: %v", err)
					}
					if got, err := codec.Decode[format.ToolResource](v.AppendCBOR(nil), toolLimits()); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolResource{}) {
						t.Fatalf("decode cap: %v", err)
					}
				}
			}
		})
	}
	for _, v := range []format.ToolResource{{URI: "urn:x", Name: ptr("\xff")}, {URI: "urn:x", Title: ptr("\xff")}, {URI: "urn:x", MIMEType: ptr("\xff")}} {
		if v.Validate() == nil {
			t.Fatal("accepted invalid UTF-8 label")
		}
	}
}
