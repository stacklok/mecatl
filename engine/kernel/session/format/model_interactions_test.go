package format_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestModelResponseCodecPreservesEmptyAndUsageZero(t *testing.T) {
	empty := ""
	zero := int64(0)
	v := format.ModelResponse{RunID: 1, RequestOrdinal: 2, Terminal: format.ResponseTerminalNormal, ProviderReason: &empty, Items: []format.ResponseItem{{Content: &format.ResponseContent{Kind: format.ResponseContentOutput, Parts: []format.ContentPart{{Text: &empty}}}}, {Call: &format.RequestedCall{Name: "", Arguments: []byte("invalid JSON")}}}, Usage: &format.InvocationUsage{Input: &zero}, Private: &format.OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: []byte{1, 2}}}
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ModelResponse](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.Items[1].Call.Arguments[0] = 'X'
	v.Private.Data[0] = 9
	if string(got.Items[1].Call.Arguments) != "invalid JSON" || got.Private.Data[0] != 1 || got.Usage.Input == nil || *got.Usage.Input != 0 || got.ProviderReason == nil || *got.ProviderReason != "" {
		t.Fatal("codec aliased or lost explicit representation")
	}
}

func TestModelResponseCodecRejectsInvalidUsageAndOpaqueVersions(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	valid := func() format.ModelResponse {
		return format.ModelResponse{
			RunID:          1,
			RequestOrdinal: 1,
			Terminal:       format.ResponseTerminalNormal,
			Usage: &format.InvocationUsage{
				Input:      &zero,
				Output:     &zero,
				CacheRead:  &zero,
				CacheWrite: &zero,
				Reasoning:  &zero,
				Raw:        &format.OpaqueProviderData{Producer: "provider", Format: "usage", Version: 1},
			},
			Private: &format.OpaqueProviderData{Producer: "provider", Format: "response", Version: 1},
		}
	}
	if wire, err := codec.Encode(valid(), limits()); err != nil || wire == nil {
		t.Fatalf("valid zero usage and positive opaque versions: %v", err)
	} else if got, err := codec.Decode[format.ModelResponse](wire, limits()); err != nil || !got.Equal(valid()) {
		t.Fatalf("valid control roundtrip: %v", err)
	}

	for _, tc := range []struct {
		name string
		edit func(*format.ModelResponse)
	}{
		{"negative input", func(v *format.ModelResponse) { v.Usage.Input = &negative }},
		{"negative output", func(v *format.ModelResponse) { v.Usage.Output = &negative }},
		{"negative cache read", func(v *format.ModelResponse) { v.Usage.CacheRead = &negative }},
		{"negative cache write", func(v *format.ModelResponse) { v.Usage.CacheWrite = &negative }},
		{"negative reasoning", func(v *format.ModelResponse) { v.Usage.Reasoning = &negative }},
		{"zero private version", func(v *format.ModelResponse) { v.Private.Version = 0 }},
		{"negative private version", func(v *format.ModelResponse) { v.Private.Version = -1 }},
		{"zero raw version", func(v *format.ModelResponse) { v.Usage.Raw.Version = 0 }},
		{"negative raw version", func(v *format.ModelResponse) { v.Usage.Raw.Version = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid()
			tc.edit(&v)
			if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
				t.Fatalf("encode: %v", err)
			}

			// AppendCBOR deliberately bypasses Validate so decode must reject the
			// locally malformed but structurally encoded value itself.
			wire := v.AppendCBOR(nil)
			got, err := codec.Decode[format.ModelResponse](wire, limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ModelResponse{}) {
				t.Fatalf("decode: %v", err)
			}

			receiver := valid()
			before := receiver.Clone()
			reader, err := codec.NewReader(wire, limits())
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
				t.Fatalf("ReadCBOR changed receiver: %v", err)
			}
		})
	}
}

func TestInteractionFormatMalformedUnionsAndEvidence(t *testing.T) {
	valid := format.ResponseItem{Call: &format.RequestedCall{Name: "x"}}
	for name, v := range map[string]format.ResponseItem{
		"neither": {},
		"both":    {Content: &format.ResponseContent{Kind: format.ResponseContentOutput}, Call: valid.Call},
	} {
		if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
			t.Fatalf("%s encode: %v", name, err)
		}
		got, err := codec.Decode[format.ResponseItem](v.AppendCBOR(nil), limits())
		if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.ResponseItem{}) {
			t.Fatalf("%s decode: %v", name, err)
		}
	}
	badEvidence := format.ObservedEvidence{Detail: ptr(string([]byte{0xff}))}
	if _, err := codec.Encode(badEvidence, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("bad evidence: %v", err)
	}
	ending := format.RunEnded{RunID: 1, Reason: format.RunEndedError, Capture: &format.CaptureFailureEvidence{RequestOrdinal: 1}}
	if _, err := codec.Encode(ending, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("wrong ending evidence: %v", err)
	}
}

func TestModelRequestFormatAndLimits(t *testing.T) {
	v := format.ModelRequest{RunID: 1, Ordinal: 1, Source: format.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("head"))}, Recipe: format.ConstructionRecipe{Algorithm: "algorithm", Version: 1, Parameters: []byte{}, Check: []byte("check")}}
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.ModelRequest](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.Recipe.Check = nil
	if _, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("missing check: %v", err)
	}
	v.Recipe.Check = []byte("check")
	limited := limits()
	limited.MaxByteStringBytes = len(v.Recipe.Check) - 1
	if out, err := codec.Encode(v, limited); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
		t.Fatalf("bounded request encode: %v", err)
	}
	if got, err := codec.Decode[format.ModelRequest](v.AppendCBOR(nil), limited); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.ModelRequest{}) {
		t.Fatalf("bounded request decode: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
