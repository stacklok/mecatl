package session

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

func FuzzModelInteractionConstructors(f *testing.F) {
	for kind := range uint8(4) {
		f.Add(kind, uint64(1), int64(1), int64(0), "unknown", []byte{0xff, 0})
		f.Add(kind, uint64(math.MaxUint64), int64(math.MaxInt64), int64(math.MaxInt64), "", []byte{})
		f.Add(kind, uint64(0), int64(0), int64(-1), string([]byte{0xff}), []byte{0})
	}
	f.Fuzz(func(t *testing.T, kind uint8, ordinal uint64, version, count int64, name string, data []byte) {
		fixture, limits := internalFixture(t)
		id := ModelRequestID{runID: RunID{historyID: fixture.Snapshot().HistoryID(), ordinal: ordinal}, requestOrdinal: ordinal}
		usage := &InvocationUsage{Input: &count, CacheRead: new(int64(0)), Raw: &OpaqueProviderData{Producer: name, Format: "usage", Version: version, Data: data}}
		evidence := &ObservedEvidence{Detail: &name, Correlation: new("")}
		var r proposedRecord
		var err error
		switch kind % 4 {
		case 0:
			r, err = makeRequestRecord(identifiedRequest{ID: id, Source: SourceHead{Sequence: version, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: name, Version: version, Parameters: data, Check: data}, RetryOf: &id}, limits)
		case 1:
			r, err = newModelResponseRecord(ModelResponse{RequestID: id, Terminal: ResponseTerminalOther, ProviderStopReason: &name, Items: []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: &name}}}}, {Call: &RequestedCall{Name: name, Arguments: data}}}, Evidence: evidence, Usage: usage, Private: &OpaqueProviderData{Producer: name, Format: "unknown", Version: version, Data: data}}, limits)
		case 2:
			r, err = newInvocationFailureRecord(InvocationFailure{RequestID: id, Class: InvocationFailureOther, Explanation: &name, Evidence: evidence, Usage: usage}, limits)
		case 3:
			r, err = newRunEndedRecord(RunEnded{RunID: id.runID, Reason: RunEndedCaptureFailed, Explanation: &name, Capture: &CaptureFailureEvidence{RequestID: id, Evidence: evidence, Usage: usage}}, limits)
		}
		if err != nil {
			if !reflect.DeepEqual(r, proposedRecord{}) {
				t.Fatal("failed constructor returned partial record")
			}
			return
		}
		captured := r.format()
		clear(data)
		name = "mutated"
		count = -1
		if !r.format().Equal(captured) {
			t.Fatal("constructor borrowed input storage")
		}
		imported, err := decodeRecord(id.runID.historyID, captured, limits)
		interactionCheck(t, err)
		if !imported.format().Equal(captured) {
			t.Fatal("constructor/decode canonical mismatch")
		}
		clear(captured.Payload)
		if !bytes.Equal(imported.format().Payload, r.format().Payload) {
			t.Fatal("decode borrowed DTO storage")
		}
	})
}
