package format_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestProviderStopReasonOpenVocabularyAndWireKey(t *testing.T) {
	for _, reason := range []*string{nil, new(""), new("provider/future-finish-reason")} {
		v := format.ModelResponse{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalOther, ProviderStopReason: reason}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		// This pre-rename shape pins key p, exact text, and absent/empty distinction.
		want := []byte("\xa4\x61i\x80\x61q\x01\x61r\x01\x61t\x65other")
		if reason != nil {
			want = []byte("\xa5\x61i\x80\x61p")
			if *reason == "" {
				want = append(want, 0x60)
			} else {
				want = append(want, 0x78, byte(len(*reason)))
				want = append(want, *reason...)
			}
			want = append(want, []byte("\x61q\x01\x61r\x01\x61t\x65other")...)
		}
		if !bytes.Equal(wire, want) {
			t.Fatalf("provider stop reason wire: %x; want %x", wire, want)
		}
		got, err := codec.Decode[format.ModelResponse](wire, limits())
		if err != nil || !got.Equal(v) {
			t.Fatalf("open provider stop reason: %v", err)
		}
	}
	v := format.ModelResponse{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalOther, ProviderStopReason: new(string([]byte{0xff}))}
	if _, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("non-UTF-8 stop reason: %v", err)
	}
}

func TestInteractionClosedVocabularies(t *testing.T) {
	for _, kind := range []format.ResponseContentKind{format.ResponseContentOutput, format.ResponseContentReasoning, format.ResponseContentRefusal} {
		if err := (format.ResponseContent{Kind: kind}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, terminal := range []format.ResponseTerminalClass{format.ResponseTerminalNormal, format.ResponseTerminalLimit, format.ResponseTerminalRefusal, format.ResponseTerminalError, format.ResponseTerminalCancelled, format.ResponseTerminalOther} {
		if err := (format.ModelResponse{RunID: 1, RequestID: 1, Terminal: terminal}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, class := range []string{format.InvocationFailureTransport, format.InvocationFailureTimeout, format.InvocationFailureLocal, format.InvocationFailureOther} {
		if err := (format.InvocationFailure{RunID: 1, RequestID: 1, Class: class}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, unknown := range []string{"", "unknown"} {
		for _, v := range []codec.Marshaler{format.ResponseContent{Kind: format.ResponseContentKind(unknown)}, format.ModelResponse{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalClass(unknown)}, format.InvocationFailure{RunID: 1, RequestID: 1, Class: unknown}} {
			if wire, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || wire != nil {
				t.Fatalf("unknown category encoded: %v", err)
			}
		}
	}
}

func TestModelResponseCodecPreservesEmptyAndUsageZero(t *testing.T) {
	empty := ""
	zero := int64(0)
	v := format.ModelResponse{RunID: 1, RequestID: 2, Terminal: format.ResponseTerminalNormal, ProviderStopReason: &empty, Items: []format.ResponseItem{{Content: &format.ResponseContent{Kind: format.ResponseContentOutput, Parts: []format.ContentPart{{Text: &empty}}}}, {Call: &format.RequestedCall{Name: "", Arguments: []byte("invalid JSON")}}}, Usage: &format.InvocationUsage{Input: &zero}, Private: &format.OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: []byte{1, 2}}}
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
	if string(got.Items[1].Call.Arguments) != "invalid JSON" || got.Private.Data[0] != 1 || got.Usage.Input == nil || *got.Usage.Input != 0 || got.ProviderStopReason == nil || *got.ProviderStopReason != "" {
		t.Fatal("codec aliased or lost explicit representation")
	}
}

func TestModelResponseCodecRejectsInvalidUsageAndOpaqueVersions(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	valid := func() format.ModelResponse {
		return format.ModelResponse{
			RunID:     1,
			RequestID: 1,
			Terminal:  format.ResponseTerminalNormal,
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
	ending := format.RunEnded{RunID: 1, Reason: format.RunEndedError, Capture: &format.CaptureFailureEvidence{RequestID: 1}}
	if _, err := codec.Encode(ending, limits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("wrong ending evidence: %v", err)
	}
}

func TestModelRequestFormatAndLimits(t *testing.T) {
	v := format.ModelRequest{RunID: 1, ID: 1, Source: format.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("head"))}, Recipe: format.ConstructionRecipe{Algorithm: "algorithm", Version: 1, Parameters: []byte{}, Check: []byte("check")}}
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

// The ending's reason encoding deliberately changes to text; all numeric identity
// components retain their keys and canonical bytes.
func TestModelInteractionIdentityWireComponents(t *testing.T) {
	cid := contentid.Sum([]byte("head"))
	binaryCID := cid.Binary()
	requestWire := []byte("\xa5\x61c\xa4\x61a\x61a\x61c\x41\x01\x61p\x40\x61v\x01\x61o\x19\x01\x00\x61r\x18\x18\x61s\xa2\x61c\xd8\x2a\x58\x25\x00")
	requestWire = append(requestWire, binaryCID[:]...)
	requestWire = append(requestWire, []byte("\x61s\x01\x61x\x01")...)
	capture := format.CaptureFailureEvidence{RequestID: 256}
	for _, tc := range []struct {
		name  string
		value codec.Marshaler
		want  []byte
	}{
		{"request", format.ModelRequest{RunID: 24, ID: 256, Source: format.SourceHead{Sequence: 1, CID: cid}, Recipe: format.ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}, RetryOf: new(uint64(1))}, requestWire},
		{"response", format.ModelResponse{RunID: 24, RequestID: 256, Terminal: format.ResponseTerminalNormal}, []byte("\xa4\x61i\x80\x61q\x19\x01\x00\x61r\x18\x18\x61t\x66normal")},
		{"failure", format.InvocationFailure{RunID: 24, RequestID: 256, Class: format.InvocationFailureOther}, []byte("\xa3\x61c\x65other\x61q\x19\x01\x00\x61r\x18\x18")},
		{"capture", capture, []byte("\xa1\x61q\x19\x01\x00")},
		{"ending", format.RunEnded{RunID: 24, Reason: format.RunEndedCaptureFailed, Capture: &capture}, []byte("\xa3\x61c\xa1\x61q\x19\x01\x00\x61e\x6ecapture-failed\x61r\x18\x18")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codec.Encode(tc.value, limits())
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("identity wire changed: %x; want %x: %v", got, tc.want, err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
