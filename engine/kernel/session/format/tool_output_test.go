package format_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func richToolOutput() format.ToolOutput {
	return format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{
		{Content: &format.ContentPart{Text: ptr("é")}},
		{Content: &format.ContentPart{Media: &format.Media{Kind: format.MediaKindImage, Source: format.MediaSourceInline, MIMEType: "image/x", InlineData: []byte{1, 2, 3}}}},
		{Content: &format.ContentPart{Media: &format.Media{Kind: format.MediaKindAudio, Source: format.MediaSourceRemote, RemoteURI: "https://example.invalid/audio"}}},
		{Content: &format.ContentPart{Media: &format.Media{Kind: format.MediaKindDocument, Source: format.MediaSourceProvider, ProviderName: "p", ProviderScope: "s", ProviderReferenceID: "r"}}},
		{JSON: ptr([]byte(" { \"z\":1e+02, \"z\":null } \n"))},
		{ResourceLink: &format.ToolResource{URI: "urn:link", Name: ptr("link"), Title: ptr("Resource title"), MIMEType: ptr("application/octet-stream")}},
		{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:text"}, Text: ptr("body")}},
		{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:blob"}, Blob: ptr([]byte{0xff, 0})}},
		{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:empty-text"}, Text: ptr("")}},
		{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:empty-blob"}, Blob: ptr([]byte(nil))}},
	}}
}

func TestToolOutputRoundtrip(t *testing.T) {
	v := richToolOutput()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	before := v.Clone()
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ToolOutput](wire, toolLimits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	*v.Parts[0].Content.Text = "changed"
	v.Parts[1].Content.Media.InlineData[0] = 9
	(*v.Parts[4].JSON)[0] = 'x'
	v.Parts[5].ResourceLink.URI = "urn:changed"
	*v.Parts[6].Resource.Text = "changed"
	(*v.Parts[7].Resource.Blob)[0] = 0
	clear(wire)
	if !got.Equal(before) {
		t.Fatal("output alias or changed exact bytes")
	}
	if !bytes.Equal(*got.Parts[4].JSON, *before.Parts[4].JSON) {
		t.Fatal("JSON normalized")
	}
	if got.Parts[8].Resource.Text == nil || got.Parts[9].Resource.Blob == nil || len(*got.Parts[9].Resource.Blob) != 0 {
		t.Fatal("empty body presence lost")
	}
	for _, capture := range []format.ToolCaptureState{format.ToolCaptureComplete, format.ToolCaptureTruncated, format.ToolCaptureOmitted} {
		v := format.ToolOutput{Capture: capture}
		out, err := codec.Encode(v, toolLimits())
		if err != nil || v.Validate() != nil {
			t.Fatalf("empty %s: %v", capture, err)
		}
		if got, err := codec.Decode[format.ToolOutput](out, toolLimits()); err != nil || !got.Equal(v) {
			t.Fatalf("empty roundtrip: %v", err)
		}
	}
}

// rejectToolOutput checks both trust boundaries and atomic receiver failure.
func rejectToolOutput(t *testing.T, v format.ToolOutput, kind error) {
	t.Helper()
	if v.Validate() == nil {
		t.Fatal("direct validation accepted invalid output")
	}
	if out, err := codec.Encode(v, toolLimits()); !errors.Is(err, kind) || out != nil {
		t.Fatalf("encode: %v", err)
	}
	wire := v.AppendCBOR(nil)
	if got, err := codec.Decode[format.ToolOutput](wire, toolLimits()); !errors.Is(err, kind) || !reflect.DeepEqual(got, format.ToolOutput{}) {
		t.Fatalf("decode: %v", err)
	}
	receiver := richToolOutput()
	before := receiver.Clone()
	r, err := codec.NewReader(wire, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, kind) || !receiver.Equal(before) {
		t.Fatalf("receiver changed: %v", err)
	}
}

func TestToolOutputMalformed(t *testing.T) {
	for name, v := range map[string]format.ToolOutput{
		"zero": {}, "unknown": {Capture: "future"},
		"omitted parts": {Capture: format.ToolCaptureOmitted, Parts: []format.ToolOutputPart{{JSON: ptr([]byte("true"))}}},
	} {
		t.Run(name, func(t *testing.T) { rejectToolOutput(t, v, codec.ErrInvalidData) })
	}
	for name, part := range map[string]format.ToolOutputPart{
		"no branch":             {},
		"content and json":      {Content: &format.ContentPart{Text: ptr("")}, JSON: ptr([]byte("true"))},
		"two resources":         {ResourceLink: &format.ToolResource{URI: "urn:x"}, Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}, Text: ptr("")}},
		"empty content":         {Content: &format.ContentPart{}},
		"bad media":             {Content: &format.ContentPart{Media: &format.Media{}}},
		"bad resource":          {ResourceLink: &format.ToolResource{}},
		"no embedded body":      {Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}}},
		"two embedded bodies":   {Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}, Text: ptr(""), Blob: ptr([]byte{})}},
		"bad embedded text":     {Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}, Text: ptr("\xff")}},
		"bad embedded metadata": {Resource: &format.ToolEmbeddedResource{Text: ptr("")}},
	} {
		t.Run(name, func(t *testing.T) {
			rejectToolOutput(t, format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{part}}, codec.ErrInvalidData)
		})
	}
	for _, j := range [][]byte{nil, {}, []byte(" "), []byte("{"), []byte("true false"), {'"', 0xff, '"'}} {
		rejectToolOutput(t, format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{{JSON: &j}}}, codec.ErrInvalidData)
	}
	for _, j := range []string{"null", "true", "123", "\"\"", "[]", "{}", " {\"b\":2,\"a\":1,\"a\":3} "} {
		v := format.ToolOutputPart{JSON: ptr([]byte(j))}
		if err := v.Validate(); err != nil {
			t.Fatalf("valid JSON %q: %v", j, err)
		}
	}
	// Capture is not a prefix assertion: retain arbitrary canonical occurrences.
	v := richToolOutput()
	v.Capture = format.ToolCaptureTruncated
	v.Parts = []format.ToolOutputPart{v.Parts[4], v.Parts[7]}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestToolOutputSchemaCaps(t *testing.T) {
	for _, field := range []string{"json", "text", "blob", "parts"} {
		t.Run(field, func(t *testing.T) {
			for _, over := range []int{0, 1} {
				v := format.ToolOutput{Capture: format.ToolCaptureComplete}
				switch field {
				case "json":
					v.Parts = []format.ToolOutputPart{{JSON: ptr([]byte("\"" + strings.Repeat("x", 4194304-2+over) + "\""))}}
				case "text":
					v.Parts = []format.ToolOutputPart{{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}, Text: ptr(strings.Repeat("x", 4194304+over))}}}
				case "blob":
					v.Parts = []format.ToolOutputPart{{Resource: &format.ToolEmbeddedResource{Resource: format.ToolResource{URI: "urn:x"}, Blob: ptr(make([]byte, 4194304+over))}}}
				case "parts":
					v.Parts = make([]format.ToolOutputPart, 4096+over)
					for i := range v.Parts {
						v.Parts[i].Content = &format.ContentPart{Text: ptr("")}
					}
				}
				if over != 0 {
					rejectToolOutput(t, v, codec.ErrLimitExceeded)
					continue
				}
				if err := v.Validate(); err != nil {
					t.Fatal(err)
				}
				out, err := codec.Encode(v, toolLimits())
				if err != nil {
					t.Fatalf("exact cap: %v", err)
				}
				if got, err := codec.Decode[format.ToolOutput](out, toolLimits()); err != nil || !got.Equal(v) {
					t.Fatalf("roundtrip cap: %v", err)
				}
			}
		})
	}
	// Oversized malformed JSON must fail the field cap before syntax validation.
	oversizedValid := format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{{JSON: ptr([]byte("\"" + strings.Repeat("x", 4194303) + "\""))}}}
	oversizedValidErr := oversizedValid.Validate()
	if oversizedValidErr == nil {
		t.Fatal("oversized valid JSON directly validated")
	}
	oversizedMalformed := format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{{JSON: ptr(bytes.Repeat([]byte{'x'}, 4194305))}}}
	if err := oversizedMalformed.Validate(); err == nil || err.Error() != oversizedValidErr.Error() {
		t.Fatalf("oversized malformed JSON direct validation: %v", err)
	}
	underCapMalformed := format.ToolOutput{Capture: format.ToolCaptureComplete, Parts: []format.ToolOutputPart{{JSON: ptr([]byte("{"))}}}
	if err := underCapMalformed.Validate(); err == nil || err.Error() == oversizedValidErr.Error() {
		t.Fatalf("under-cap malformed JSON direct validation: %v", err)
	}
	rejectToolOutput(t, oversizedMalformed, codec.ErrLimitExceeded)
}

func TestToolOutputSharedCodecPolicy(t *testing.T) {
	v := richToolOutput()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := codec.Encode(v, toolLimits())
	if err != nil {
		t.Fatal(err)
	}
	strict := toolLimits()
	strict.MaxByteStringBytes = 2
	if out, err := codec.Encode(v, strict); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
		t.Fatalf("strict encode: %v", err)
	}
	if got, err := codec.Decode[format.ToolOutput](wire, strict); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ToolOutput{}) {
		t.Fatalf("strict decode: %v", err)
	}
	receiver := richToolOutput()
	before := receiver.Clone()
	r, err := codec.NewReader(wire, strict)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrLimitExceeded) || !receiver.Equal(before) {
		t.Fatalf("strict read changed receiver: %v", err)
	}
}
