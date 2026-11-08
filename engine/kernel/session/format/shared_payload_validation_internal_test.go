package format

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestContentPartValidationLayers(t *testing.T) {
	v := ContentPart{Media: &Media{}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate union check must not validate media; public validation must")
	}
	v.Text = new("")
	if err := v.Validate(); err == nil || err.Error() != "content part requires exactly one text or media branch" {
		t.Fatalf("public union error must precede malformed child: %v", err)
	}
}

func TestInvocationUsageValidationLayers(t *testing.T) {
	v := InvocationUsage{Raw: &OpaqueProviderData{}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate counter check must not validate raw evidence; public validation must")
	}
	v.Input = new(int64(-1))
	if err := v.Validate(); err == nil || err.Error() != "usage count must be nonnegative" {
		t.Fatalf("public counter error must precede malformed raw metadata: %v", err)
	}
}

func TestResponseContentValidationLayers(t *testing.T) {
	v := ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Media: &Media{}}}}
	if v.validateLocal() != nil || v.Validate() == nil {
		t.Fatal("immediate kind check must not validate parts; public validation must")
	}
	v.Kind = "unknown"
	if err := v.Validate(); err == nil || err.Error() != "unknown response content kind" {
		t.Fatalf("public kind error must precede malformed part: %v", err)
	}
}

func TestResponseItemValidationLayers(t *testing.T) {
	for _, v := range []ResponseItem{
		{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{}}}},
		{Call: &RequestedCall{Name: "\xff"}},
	} {
		if v.validateLocal() != nil || v.Validate() == nil {
			t.Fatal("immediate union check must not validate children; public validation must")
		}
	}
	v := ResponseItem{Content: &ResponseContent{}, Call: &RequestedCall{Name: "\xff"}}
	if err := v.Validate(); err == nil || err.Error() != "response item requires exactly one branch" {
		t.Fatalf("public union error must precede malformed child: %v", err)
	}
}

func TestSharedPayloadCodecBoundsPrecedeParentValidation(t *testing.T) {
	l := codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 128, MaxByteStringBytes: 4, MaxArrayElements: 32, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 1024, MaxCollectionElements: 1024}
	// Each parent is malformed, but an earlier child byte limit must win on both
	// codec paths. Recursive public validation deliberately reports the parent first.
	media := &Media{Kind: MediaKindImage, Source: MediaSourceInline, MIMEType: "x", InlineData: make([]byte, 5)}
	part := ContentPart{Text: new(""), Media: media}
	content := ResponseContent{Kind: "unknown", Parts: []ContentPart{{Media: media}}}
	item := ResponseItem{Call: &RequestedCall{Arguments: make([]byte, 5)}, Content: &ResponseContent{Kind: ResponseContentOutput}}
	usage := InvocationUsage{Input: new(int64(-1)), Raw: &OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: make([]byte, 5)}}
	for _, tc := range []struct {
		name   string
		value  codec.Marshaler
		decode func([]byte) error
	}{
		{"part", part, func(b []byte) error { _, err := codec.Decode[ContentPart](b, l); return err }},
		{"content", content, func(b []byte) error { _, err := codec.Decode[ResponseContent](b, l); return err }},
		{"item", item, func(b []byte) error { _, err := codec.Decode[ResponseItem](b, l); return err }},
		{"usage", usage, func(b []byte) error { _, err := codec.Decode[InvocationUsage](b, l); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := codec.Encode(tc.value, l); out != nil || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("bounded encode: %x, %v", out, err)
			}
			if err := tc.decode(tc.value.AppendCBOR(nil)); !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("bounded decode: %v", err)
			}
			wide := l
			wide.MaxByteStringBytes = 5
			if out, err := codec.Encode(tc.value, wide); out != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("raised limit must expose invalid parent: %x, %v", out, err)
			}
		})
	}
}
