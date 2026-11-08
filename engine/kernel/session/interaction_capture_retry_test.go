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
				mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: run.HistoryID(), Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
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
			mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: run.HistoryID(), Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			submitBatch(t, s, b)
			id := addInteractionRequest(t, s, run, nil, nil)
			b = beginBatch(t, s)
			mustBatch(t, b.AddRunEnded(session.RunEnded{RunID: run, Reason: session.RunEndedCaptureFailed, Explanation: new("local capture rejected"), Capture: &session.CaptureFailureEvidence{RequestID: id, Usage: &session.InvocationUsage{Output: new(int64(0))}}}))
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
