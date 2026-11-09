package session_test

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestModelInteractionValuesCloneAndRelativeReferences(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	request := session.ModelRequest{RunID: 1, ID: 2, Source: session.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("head"))}, Recipe: session.ConstructionRecipe{Algorithm: "example/v1", Version: 1, Parameters: []byte{}, Check: []byte("check")}}
	mustBatch(t, request.Validate())
	requestCopy := request.Clone()
	request.Recipe.Check[0] = 'X'
	if requestCopy.Recipe.Check[0] != 'c' {
		t.Fatal("request clone shares check bytes")
	}
	requestCopy.RetryOf = new(allocatedRequest(t, allocatedRun(t, historyid.NewHistoryID(), 2), 1).RunRelative())
	mustBatch(t, requestCopy.Validate()) // Numeric references carry no origin proof.
	if *requestCopy.RetryOf != allocatedRequest(t, run, 1).RunRelative() {
		t.Fatal("relative reference changed")
	}

	zero, empty := int64(0), ""
	response := session.ModelResponse{RunID: 1, RequestID: 2, Terminal: session.ResponseTerminalNormal, ProviderStopReason: &empty, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Text: &empty}}}}, {Call: &session.RequestedCall{Name: "", Arguments: []byte("not json")}}}, Usage: &session.InvocationUsage{Input: &zero}, Private: &session.OpaqueProviderData{Producer: "provider", Format: "raw", Version: 1, Data: []byte{1}}}
	mustBatch(t, response.Validate())
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

func TestModelRequestCanonicalValueAndOwnership(t *testing.T) {
	v := session.ModelRequest{RunID: 1, ID: 2, Source: session.SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: session.ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte{1}, Check: []byte{2}}, RetryOf: new(uint64(1))}
	clone := v.Clone()
	if v.Validate() != nil || !v.Equal(clone) || !clone.Equal(v) || clone.RetryOf == v.RetryOf {
		t.Fatal("clone changed values or borrowed retry storage")
	}
	v.Recipe.Parameters[0], v.Recipe.Check[0], *v.RetryOf = 3, 4, 0
	if clone.Recipe.Parameters[0] != 1 || clone.Recipe.Check[0] != 2 || *clone.RetryOf != 1 || v.Equal(clone) {
		t.Fatal("clone borrowed nested storage")
	}
	for name, edit := range map[string]func(*session.ModelRequest){
		"run component":   func(v *session.ModelRequest) { v.RunID++ },
		"ID component":    func(v *session.ModelRequest) { v.ID++ },
		"source":          func(v *session.ModelRequest) { v.Source.CID = contentid.Sum([]byte("different")) },
		"recipe":          func(v *session.ModelRequest) { v.Recipe.Check[0]++ },
		"retry absence":   func(v *session.ModelRequest) { v.RetryOf = nil },
		"retry component": func(v *session.ModelRequest) { *v.RetryOf++ },
	} {
		t.Run(name, func(t *testing.T) {
			other := clone.Clone()
			edit(&other)
			if clone.Equal(other) || other.Equal(clone) {
				t.Fatal("equality lost a canonical field")
			}
		})
	}
	for _, invalid := range []session.ModelRequest{{}, {RunID: 1}, {RunID: 1, ID: 2, Source: clone.Source}, {RunID: 1, ID: 2, Source: clone.Source, Recipe: clone.Recipe, RetryOf: new(uint64)}} {
		if invalid.Validate() == nil {
			t.Fatal("invalid persisted value validated")
		}
	}
}

func TestRunEndedEqualUsesRelativeCaptureRequest(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	ending := session.RunEnded{RunID: 1, Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: allocatedRequest(t, run, 1).RunRelative()}}
	other := ending.Clone()
	other.Capture.RequestID = allocatedRequest(t, allocatedRun(t, historyid.NewHistoryID(), 2), 1).RunRelative()
	if !ending.Equal(other) || !other.Equal(ending) {
		t.Fatal("relative equality depends on discarded origin")
	}
	other.Capture.RequestID++
	if ending.Equal(other) || other.Equal(ending) {
		t.Fatal("equality lost relative capture component")
	}
}

func TestInteractionFailuresAndEndingValidation(t *testing.T) {
	failure := session.InvocationFailure{RunID: 1, RequestID: 1, Class: session.InvocationFailureTransport}
	mustBatch(t, failure.Validate())
	ending := session.RunEnded{RunID: 1, Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: 1}}
	mustBatch(t, ending.Validate())
	ending.Reason = session.RunEndedError
	if err := ending.Validate(); err == nil {
		t.Fatal("capture evidence accepted for non-capture ending")
	}
	ending.Reason = session.RunEndedCaptureFailed
	ending.Capture.RequestID = 0
	if ending.Validate() == nil {
		t.Fatal("zero capture request validated")
	}
}
