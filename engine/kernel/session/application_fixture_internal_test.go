package session

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func allocatedRun(t testing.TB, scope historyid.HistoryID, ordinal uint64) RunID {
	t.Helper()
	return RunID{historyID: scope, ordinal: ordinal}
}
func allocatedInput(t testing.TB, run RunID, ordinal uint64) UserInputID {
	t.Helper()
	return UserInputID{runID: run, ordinal: ordinal}
}
func newLifecycleHistory(t testing.TB, backend history.Backend) *history.History {
	t.Helper()
	h, err := history.New(backend, history.Policy{CommitLimits: testLimits(), RetryWindow: 16, MaxPageBytes: testLimits().MaxBytes, MaxPageCommits: 2})
	mustBatch(t, err)
	return h
}
func boundSession(t testing.TB, h *history.History, p Policy) *Session {
	t.Helper()
	s, err := New(h, testHistoryID(t), p)
	mustBatch(t, err)
	return s
}
func beginBatch(t testing.TB, s *Session) *SessionCommit {
	t.Helper()
	b, err := s.BeginCommit()
	mustBatch(t, err)
	return b
}
func mustBatch(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func batchRun(t testing.TB, b *SessionCommit) RunID {
	t.Helper()
	id, err := b.StartRun()
	mustBatch(t, err)
	return id
}
func batchInput(t testing.TB, b *SessionCommit, run RunID, content []byte) UserInputID {
	t.Helper()
	id, err := b.AddUserInput(UserInput{RunID: run, Parts: internalByteParts(content)})
	mustBatch(t, err)
	return id
}
func submitBatch(t testing.TB, s *Session, b *SessionCommit) history.CommitRef {
	t.Helper()
	ref, err := s.SubmitCommit(context.Background(), b)
	mustBatch(t, err)
	return ref
}
func assertHistoryHead(t testing.TB, h *history.History, want history.CommitRef) {
	t.Helper()
	got, err := h.Head(context.Background(), testHistoryID(t))
	if err != nil || got != want {
		t.Fatalf("Head = %v, %v; want %v", got, err, want)
	}
}

// batchBackend counts real writes for private exact-boundary tests.
type batchBackend struct {
	history.Backend
	attempts []history.ByteCommit
}

func (b *batchBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	b.attempts = append(b.attempts, c)
	return b.Backend.Append(ctx, id, expected, c)
}
