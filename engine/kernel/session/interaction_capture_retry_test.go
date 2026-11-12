package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestModelInteractionUncertainMinimalEndingRetriesExactReason(t *testing.T) {
	for _, reason := range []session.RunEndedReason{session.RunEndedCompleted, session.RunEndedError, session.RunEndedLimit, session.RunEndedInterrupted, session.RunEndedCaptureFailed} {
		for _, accepted := range []bool{false, true} {
			t.Run(string(reason)+map[bool]string{false: "/before acceptance", true: "/lost acknowledgement"}[accepted], func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
				submitBatch(t, s, b)
				if reason != session.RunEndedCompleted {
					addInteractionRequest(t, s, run, nil, nil)
				}
				before := s.Snapshot()
				b = beginBatch(t, s)
				mustBatch(t, b.EndRun(reason))
				backend.accept, backend.fail = accepted, codec.ErrLimitExceeded
				if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrAcceptanceUnknown) || !reflect.DeepEqual(before, s.Snapshot()) {
					t.Fatalf("unknown minimal ending acceptance: %v", err)
				}
				attempt := backend.attempts[len(backend.attempts)-1]
				if err := b.EndRun(session.RunEndedError); !errors.Is(err, session.ErrBatch) {
					t.Fatal("uncertain ending reason replaced")
				}
				if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
					t.Fatal("uncertain ending discarded")
				}
				if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
					t.Fatal("alternate proposal allowed")
				}
				if ref := submitBatch(t, s, b); ref != attempt.commit.Ref || !reflect.DeepEqual(attempt, backend.attempts[len(backend.attempts)-1]) {
					t.Fatal("minimal ending retry changed bytes or identity")
				}
				ending, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
				if !ok || ending.Reason() != reason {
					t.Fatal("retry changed ending reason")
				}
				loaded, err := session.Load(context.Background(), h, run.HistoryID(), sessionPolicy())
				mustBatch(t, err)
				if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
					t.Fatal("minimal ending retry replay")
				}
			})
		}
	}
}

func TestModelInteractionUncertainDetailedCaptureCannotBecomeMinimal(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acceptance", true: "lost acknowledgement"}[accepted], func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			submitBatch(t, s, b)
			id := addInteractionRequest(t, s, run, nil, nil)
			b = beginBatch(t, s)
			mustBatch(t, b.AddRunEnded(run, session.RunEnded{Reason: session.RunEndedCaptureFailed, Explanation: new("local capture rejected"), Capture: &session.CaptureFailureEvidence{RequestID: id.RunRelative(), Usage: &session.InvocationUsage{Output: new(int64(0))}}}))
			before := s.Snapshot()
			backend.accept = accepted
			backend.fail = codec.ErrLimitExceeded
			_, err := s.SubmitCommit(context.Background(), b)
			if !errors.Is(err, session.ErrAcceptanceUnknown) || !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatalf("unknown ending acceptance: %v", err)
			}
			attempt := backend.attempts[len(backend.attempts)-1]
			if err = b.EndRun(session.RunEndedCaptureFailed); !errors.Is(err, session.ErrBatch) {
				t.Fatal("uncertain detail replaced by minimal ending")
			}
			if err = b.Discard(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("uncertain proposal discarded")
			}
			if _, err = s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("fresh alternate proposal allowed")
			}
			if ref := submitBatch(t, s, b); ref != attempt.commit.Ref || !reflect.DeepEqual(attempt, backend.attempts[len(backend.attempts)-1]) {
				t.Fatal("exact ending retry changed")
			}
			ending, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
			if !ok {
				t.Fatal("ending missing")
			}
			if capture, ok := ending.Capture(); !ok || capture.RequestID() != id || func() bool {
				usage, present := capture.Usage()
				output, outputPresent := usage.Output()
				return !present || !outputPresent || output != 0
			}() {
				t.Fatal("retry dropped detailed evidence")
			}
			loaded, err := session.Load(context.Background(), h, run.HistoryID(), sessionPolicy())
			mustBatch(t, err)
			if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
				t.Fatal("ending retry replay")
			}
		})
	}
}

func TestToolDispositionUncertainAcceptanceRetainsFrozenFacts(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acceptance", true: "lost acknowledgement"}[accepted], func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			submitBatch(t, s, b)
			request := addInteractionRequest(t, s, run, nil, nil)
			b = beginBatch(t, s)
			mustBatch(t, b.AddModelResponse(request, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Call: &session.RequestedCall{Name: "requested", Arguments: []byte(" { }")}}}}))
			submitBatch(t, s, b)
			response, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).ModelResponse()
			if !ok {
				t.Fatal("missing response")
			}
			var call session.RequestedCallID
			for candidate := range response.Calls() {
				call = candidate.ID()
			}
			if call == (session.RequestedCallID{}) {
				t.Fatal("missing requested call")
			}
			b = beginBatch(t, s)
			mustBatch(t, b.AddToolInvocation(call, session.ToolInvocation{Name: "effective"}))
			submitBatch(t, s, b)

			diagnostic := "retained"
			b = beginBatch(t, s)
			mustBatch(t, b.AddToolDisposition(call, session.ToolDisposition{Diagnostic: &diagnostic, Returned: &session.ToolReturned{Status: session.ToolReturnError, Output: session.ToolOutput{Capture: session.ToolCaptureComplete}}}))
			before, attempts := s.Snapshot(), len(backend.attempts)
			backend.accept, backend.fail = accepted, codec.ErrLimitExceeded
			if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrAcceptanceUnknown) || !reflect.DeepEqual(before, s.Snapshot()) || len(backend.attempts) != attempts+1 {
				t.Fatalf("unknown tool disposition acceptance: %v", err)
			}
			attempt := backend.attempts[len(backend.attempts)-1]
			diagnostic = "mutated"
			if err := b.AddToolDisposition(call, session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedOther}}); !errors.Is(err, session.ErrBatch) {
				t.Fatalf("frozen tool disposition replaced: %v", err)
			}
			if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
				t.Fatalf("alternate tool proposal allowed: %v", err)
			}
			if ref := submitBatch(t, s, b); ref != attempt.commit.Ref || len(backend.attempts) != attempts+2 || !reflect.DeepEqual(attempt, backend.attempts[len(backend.attempts)-1]) {
				t.Fatal("tool disposition retry changed bytes, identity or write count")
			}
			loaded, err := session.Load(context.Background(), h, run.HistoryID(), sessionPolicy())
			mustBatch(t, err)
			for _, inspected := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
				disposition, present := inspected.RunAt(0).ToolCalls().At(0).Disposition()
				got, diagnosticPresent := disposition.Diagnostic()
				if !present || !diagnosticPresent || got != "retained" {
					t.Fatal("frozen retry leaked caller mutation or lost disposition")
				}
			}
		})
	}
}
