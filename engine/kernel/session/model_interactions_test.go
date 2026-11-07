package session_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestModelInteractionValuesCloneAndScope(t *testing.T) {
	historyID := testHistoryID(t)
	run := session.RunID{HistoryID: historyID, Ordinal: 1}
	request := session.ModelRequest{ID: session.ModelRequestID{RunID: run, Ordinal: 2}, Source: session.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("head"))}, Recipe: session.ConstructionRecipe{Algorithm: "example/v1", Version: 1, Parameters: []byte{}, Check: []byte("check")}}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	requestCopy := request.Clone()
	request.Recipe.Check[0] = 'X'
	if requestCopy.Recipe.Check[0] != 'c' {
		t.Fatal("request clone shares check bytes")
	}
	foreign := requestCopy
	foreign.RetryOf = &session.ModelRequestID{RunID: session.RunID{HistoryID: historyID, Ordinal: 2}, Ordinal: 1}
	if !errors.Is(foreign.Validate(), codec.ErrInvalidData) {
		t.Fatal("foreign retry scope validated")
	}

	zero := int64(0)
	empty := ""
	response := session.ModelResponse{RequestID: requestCopy.ID, Terminal: session.ResponseTerminalNormal, ProviderReason: &empty, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Text: &empty}}}}, {Call: &session.RequestedCall{Name: "", Arguments: []byte("not json")}}}, Usage: &session.InvocationUsage{Input: &zero}, Private: &session.OpaqueProviderData{Producer: "provider", Format: "raw", Version: 1, Data: []byte{1}}}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := response.Clone()
	response.Items[1].Call.Arguments[0] = 'N'
	response.Private.Data[0] = 2
	if !bytes.Equal(clone.Items[1].Call.Arguments, []byte("not json")) || clone.Private.Data[0] != 1 || clone.Usage.Input == response.Usage.Input {
		t.Fatal("response clone aliases mutable storage")
	}
	if clone.Usage.Input == nil || *clone.Usage.Input != 0 || clone.ProviderReason == nil || *clone.ProviderReason != "" {
		t.Fatal("zero or explicit empty was lost")
	}
}

func TestRunEndedEqualPreservesCaptureRequestScope(t *testing.T) {
	run := session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}
	ending := session.RunEnded{RunID: run, Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: session.ModelRequestID{RunID: run, Ordinal: 1}}}
	for name, requestRun := range map[string]session.RunID{
		"ordinal":    {HistoryID: run.HistoryID, Ordinal: 2},
		"history ID": {HistoryID: historyid.NewHistoryID(), Ordinal: run.Ordinal},
	} {
		t.Run(name, func(t *testing.T) {
			other := ending.Clone()
			other.Capture.RequestID.RunID = requestRun
			if ending.Equal(other) || other.Equal(ending) {
				t.Fatal("ending equality erased capture request scope")
			}
		})
	}
}

func TestInteractionFailuresAndEndingScope(t *testing.T) {
	run := session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}
	request := session.ModelRequestID{RunID: run, Ordinal: 1}
	failure := session.InvocationFailure{RequestID: request, Class: session.InvocationFailureTransport}
	if err := failure.Validate(); err != nil {
		t.Fatal(err)
	}
	ending := session.RunEnded{RunID: run, Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: request}}
	if err := ending.Validate(); err != nil {
		t.Fatal(err)
	}
	ending.Reason = session.RunEndedError
	if err := ending.Validate(); err == nil {
		t.Fatal("capture evidence accepted for non-capture ending")
	}
	ending.Reason = session.RunEndedCaptureFailed
	ending.Capture.RequestID.RunID.Ordinal = 2
	if !errors.Is(ending.Validate(), codec.ErrInvalidData) {
		t.Fatal("foreign capture request scope validated")
	}
}
