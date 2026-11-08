package format_test

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Every new root gets both valid and deliberately unchecked malformed seeds.
// ReadCBOR must reject without replacing an existing receiver, independently of
// the allocating Decode entry point.
func FuzzModelInteractionDecode(f *testing.F) {
	request := format.ModelRequest{RunID: 1, ID: 2, Source: format.SourceHead{Sequence: 24, CID: contentid.Sum([]byte("head"))}, Recipe: format.ConstructionRecipe{Algorithm: "unknown", Version: 19, Parameters: []byte{0xff}, Check: []byte{0}}, RetryOf: new(uint64(1))}
	usage := &format.InvocationUsage{Input: new(int64(0)), Output: new(int64(math.MaxInt64)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4)), Raw: &format.OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}
	response := format.ModelResponse{RunID: 1, RequestID: 2, Terminal: format.ResponseTerminalOther, ProviderStopReason: new(""), Items: []format.ResponseItem{{Content: &format.ResponseContent{Kind: format.ResponseContentReasoning, Parts: []format.ContentPart{{Text: new("")}}}}, {Call: &format.RequestedCall{Name: "", Arguments: []byte{0xff}}}, {Content: &format.ResponseContent{Kind: format.ResponseContentRefusal}}}, Evidence: &format.ObservedEvidence{Correlation: new("")}, Usage: usage, Private: &format.OpaqueProviderData{Producer: "p", Format: "unknown", Version: 7, Data: []byte{0, 0xff}}}
	failure := format.InvocationFailure{RunID: 1, RequestID: 2, Class: format.InvocationFailureTransport, Explanation: new(""), Usage: usage}
	ending := format.RunEnded{RunID: 1, Reason: format.RunEndedCaptureFailed, Capture: &format.CaptureFailureEvidence{RequestID: 2, Usage: usage, Evidence: &format.ObservedEvidence{Detail: new("")}}}
	for kind, root := range []codec.Marshaler{request, response, failure, ending} {
		f.Add(uint8(kind), root.AppendCBOR(nil))
	}
	for _, root := range []format.ModelResponse{
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalNormal},
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalError, Private: response.Private},
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalNormal, Items: []format.ResponseItem{{}}},
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalNormal, Items: []format.ResponseItem{{Content: &format.ResponseContent{Kind: format.ResponseContentOutput}, Call: &format.RequestedCall{}}}},
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalNormal, Usage: &format.InvocationUsage{Input: new(int64(-1))}},
		{RunID: 1, RequestID: 1, Terminal: format.ResponseTerminalNormal, Private: &format.OpaqueProviderData{Producer: "p", Format: "f", Version: 0}},
	} {
		f.Add(uint8(1), root.AppendCBOR(nil))
	}
	request.Recipe.Check = nil
	f.Add(uint8(0), request.AppendCBOR(nil))
	failure.Explanation = new(string([]byte{0xff}))
	f.Add(uint8(2), failure.AppendCBOR(nil))
	ending.Reason = format.RunEndedCompleted
	f.Add(uint8(3), ending.AppendCBOR(nil))
	for kind := range uint8(4) {
		f.Add(kind, []byte{0xa0})
		f.Add(kind, []byte{0xa1, 0x61, 'i', 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	}
	f.Fuzz(func(t *testing.T, kind uint8, wire []byte) {
		switch kind % 4 {
		case 0:
			interactionDecode[format.ModelRequest](t, wire)
		case 1:
			interactionDecode[format.ModelResponse](t, wire)
		case 2:
			interactionDecode[format.InvocationFailure](t, wire)
		case 3:
			interactionDecode[format.RunEnded](t, wire)
		}
	})
}

func interactionDecode[T codec.Marshaler, P interface {
	*T
	codec.Unmarshaler
}](t *testing.T, wire []byte) {
	t.Helper()
	value, err := codec.Decode[T, P](wire, limits())
	if err != nil {
		var zero T
		if !reflect.DeepEqual(value, zero) {
			t.Fatal("failed decode returned partial output")
		}
		// ReadCBOR must also preserve its caller-owned receiver on failure.
		var receiver T
		reflect.ValueOf(&receiver).Elem().FieldByName("RunID").SetUint(99)
		reader, e := codec.NewReader(wire, limits())
		if e == nil {
			before := receiver.AppendCBOR(nil)
			e = P(&receiver).ReadCBOR(&reader, 0, math.MaxInt)
			if e != nil && !bytes.Equal(before, receiver.AppendCBOR(nil)) {
				t.Fatal("failed ReadCBOR changed receiver")
			}
		}
		return
	}
	out, err := codec.Encode(value, limits())
	if err != nil || !bytes.Equal(out, wire) {
		t.Fatalf("noncanonical successful decode: %v", err)
	}
	clear(wire)
	if !bytes.Equal(value.AppendCBOR(nil), out) {
		t.Fatal("decode borrowed input bytes")
	}
}
