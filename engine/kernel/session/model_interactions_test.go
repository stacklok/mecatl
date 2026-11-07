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
	run := allocatedRun(t, historyID, 1)
	requestID := allocatedRequest(t, run, 2)
	request := session.ModelRequest{RunID: run, Source: session.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("head"))}, Recipe: session.ConstructionRecipe{Algorithm: "example/v1", Version: 1, Parameters: []byte{}, Check: []byte("check")}}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	requestCopy := request.Clone()
	request.Recipe.Check[0] = 'X'
	if requestCopy.Recipe.Check[0] != 'c' {
		t.Fatal("request clone shares check bytes")
	}
	foreign := requestCopy
	foreign.RetryOf = new(allocatedRequest(t, allocatedRun(t, historyID, 2), 1))
	if !errors.Is(foreign.Validate(), codec.ErrInvalidData) {
		t.Fatal("foreign retry scope validated")
	}

	zero := int64(0)
	empty := ""
	response := session.ModelResponse{RequestID: requestID, Terminal: session.ResponseTerminalNormal, ProviderStopReason: &empty, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Text: &empty}}}}, {Call: &session.RequestedCall{Name: "", Arguments: []byte("not json")}}}, Usage: &session.InvocationUsage{Input: &zero}, Private: &session.OpaqueProviderData{Producer: "provider", Format: "raw", Version: 1, Data: []byte{1}}}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := response.Clone()
	response.Items[1].Call.Arguments[0] = 'N'
	response.Private.Data[0] = 2
	if !bytes.Equal(clone.Items[1].Call.Arguments, []byte("not json")) || clone.Private.Data[0] != 1 || clone.Usage.Input == response.Usage.Input {
		t.Fatal("response clone aliases mutable storage")
	}
	if clone.Usage.Input == nil || *clone.Usage.Input != 0 || clone.ProviderStopReason == nil || *clone.ProviderStopReason != "" {
		t.Fatal("zero or explicit empty was lost")
	}
}

func TestModelRequestAuthoringScopeAndOwnership(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	retry := allocatedRequest(t, run, 1)
	v := session.ModelRequest{RunID: run, Source: session.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: session.ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte{1}, Check: []byte{2}}, RetryOf: &retry}
	clone := v.Clone()
	if v.Validate() != nil || !v.Equal(clone) || !clone.Equal(v) || clone.RetryOf == v.RetryOf {
		t.Fatal("authoring clone changed values or borrowed retry storage")
	}
	v.Recipe.Parameters[0], v.Recipe.Check[0] = 3, 4
	*v.RetryOf = session.ModelRequestID{}
	if clone.Recipe.Parameters[0] != 1 || clone.Recipe.Check[0] != 2 || clone.RetryOf.Validate() != nil || v.Equal(clone) {
		t.Fatal("authoring clone borrowed nested storage")
	}
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for name, edit := range map[string]func(*session.ModelRequest){
		"run":           func(v *session.ModelRequest) { v.RunID = allocatedRun(t, run.HistoryID(), 2) },
		"history":       func(v *session.ModelRequest) { v.RunID = allocatedRun(t, foreign, 1) },
		"source":        func(v *session.ModelRequest) { v.Source.CID = contentid.Sum([]byte("different")) },
		"recipe":        func(v *session.ModelRequest) { v.Recipe.Check[0]++ },
		"retry absence": func(v *session.ModelRequest) { v.RetryOf = nil },
		"retry run": func(v *session.ModelRequest) {
			v.RetryOf = new(allocatedRequest(t, allocatedRun(t, run.HistoryID(), 2), 1))
		},
		"retry history": func(v *session.ModelRequest) { v.RetryOf = new(allocatedRequest(t, allocatedRun(t, foreign, 1), 1)) },
	} {
		t.Run(name, func(t *testing.T) {
			other := clone.Clone()
			edit(&other)
			if clone.Equal(other) || other.Equal(clone) {
				t.Fatal("equality lost an authoring field or full retry scope")
			}
			if (name == "run" || name == "history" || name == "retry run" || name == "retry history") && other.Validate() == nil {
				t.Fatal("conflicting full retry scope validated")
			}
		})
	}
	for _, invalid := range []session.ModelRequest{{}, {RunID: run}, {RunID: run, Source: clone.Source}, {RunID: run, Source: clone.Source, Recipe: clone.Recipe, RetryOf: new(session.ModelRequestID)}} {
		if invalid.Validate() == nil {
			t.Fatal("invalid authoring value validated")
		}
	}
}

func TestRunEndedEqualPreservesCaptureRequestScope(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	ending := session.RunEnded{RunID: run, Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: allocatedRequest(t, run, 1)}}
	for name, requestRun := range map[string]session.RunID{
		"run":        allocatedRun(t, run.HistoryID(), 2),
		"history ID": allocatedRun(t, historyid.NewHistoryID(), 1),
	} {
		t.Run(name, func(t *testing.T) {
			other := ending.Clone()
			other.Capture.RequestID = allocatedRequest(t, requestRun, 1)
			if ending.Equal(other) || other.Equal(ending) {
				t.Fatal("ending equality erased capture request scope")
			}
		})
	}
}

func TestInteractionFailuresAndEndingScope(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	request := allocatedRequest(t, run, 1)
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
	ending.Capture.RequestID = allocatedRequest(t, allocatedRun(t, run.HistoryID(), 2), 1)
	if !errors.Is(ending.Validate(), codec.ErrInvalidData) {
		t.Fatal("foreign capture request scope validated")
	}
}
