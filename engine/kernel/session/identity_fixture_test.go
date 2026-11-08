package session_test

import (
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// allocatedRun builds a real provisional prefix rather than constructing an ID.
func allocatedRun(t testing.TB, scope historyid.HistoryID, count uint64) session.RunID {
	t.Helper()
	s, err := session.New(newSessionHistory(t), scope, sessionPolicy())
	mustBatch(t, err)
	b := beginBatch(t, s)
	defer b.Discard() //nolint:errcheck // Test-only provisional prefix.
	mustBatch(t, b.Create())
	var run session.RunID
	for range count {
		if run != (session.RunID{}) {
			mustBatch(t, b.EndRun(session.RunEndedInterrupted))
		}
		run = batchRun(t, b)
	}
	return run
}

// allocationBatch recreates a bounded fixture's run prefix in the same scope.
func allocationBatch(t testing.TB, run session.RunID) (*session.Session, *session.SessionCommit) {
	t.Helper()
	mustBatch(t, run.Validate())
	s, err := session.New(newSessionHistory(t), run.HistoryID(), sessionPolicy())
	mustBatch(t, err)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	for {
		if batchRun(t, b) == run {
			return s, b
		}
		mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	}
}

func allocatedInput(t testing.TB, run session.RunID, count uint64) session.UserInputID {
	t.Helper()
	_, b := allocationBatch(t, run)
	defer b.Discard() //nolint:errcheck // Test-only provisional prefix.
	var id session.UserInputID
	for range count {
		var err error
		id, err = b.AddUserInput(session.UserInput{RunID: run})
		mustBatch(t, err)
	}
	return id
}

func allocatedRequest(t testing.TB, run session.RunID, count uint64) session.ModelRequestID {
	t.Helper()
	s, b := allocationBatch(t, run)
	mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: run.HistoryID(), Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	submitBatch(t, s, b)
	var id session.ModelRequestID
	for range count {
		b = beginBatch(t, s)
		head := s.Snapshot().Head()
		var err error
		id, err = b.AddModelRequest(session.ModelRequest{RunID: run, Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "fixture", Version: 1, Check: []byte{1}}})
		mustBatch(t, err)
		submitBatch(t, s, b)
		b = beginBatch(t, s)
		mustBatch(t, b.AddModelResponse(session.ModelResponse{RequestID: id, Terminal: session.ResponseTerminalNormal}))
		submitBatch(t, s, b)
	}
	return id
}
