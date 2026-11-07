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
		var r Record
		var err error
		switch kind % 4 {
		case 0:
			r, err = NewModelRequestRecord(ModelRequest{ID: id, Source: SourceHead{Sequence: version, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: name, Version: version, Parameters: data, Check: data}, RetryOf: &id}, limits)
		case 1:
			r, err = NewModelResponseRecord(ModelResponse{RequestID: id, Terminal: ResponseTerminalOther, ProviderStopReason: &name, Items: []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: &name}}}}, {Call: &RequestedCall{Name: name, Arguments: data}}}, Evidence: evidence, Usage: usage, Private: &OpaqueProviderData{Producer: name, Format: "unknown", Version: version, Data: data}}, limits)
		case 2:
			r, err = NewInvocationFailureRecord(InvocationFailure{RequestID: id, Class: InvocationFailureOther, Explanation: &name, Evidence: evidence, Usage: usage}, limits)
		case 3:
			r, err = NewRunEndedRecord(RunEnded{RunID: id.runID, Reason: RunEndedCaptureFailed, Explanation: &name, Capture: &CaptureFailureEvidence{RequestID: id, Evidence: evidence, Usage: usage}}, limits)
		}
		if err != nil {
			if !reflect.DeepEqual(r, Record{}) {
				t.Fatal("failed constructor returned partial record")
			}
			return
		}
		captured := r.Format()
		clear(data)
		name = "mutated"
		count = -1
		if !r.Format().Equal(captured) {
			t.Fatal("constructor borrowed input storage")
		}
		imported, err := DecodeRecord(id.runID.historyID, captured, limits)
		interactionCheck(t, err)
		if !imported.Format().Equal(captured) {
			t.Fatal("constructor/decode canonical mismatch")
		}
		clear(captured.Payload)
		if !bytes.Equal(imported.Format().Payload, r.Format().Payload) {
			t.Fatal("decode borrowed DTO storage")
		}
	})
}
