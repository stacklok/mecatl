package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func newLifecycleHistory(t testing.TB, backend history.Backend) *history.History {
	t.Helper()
	h, err := history.New(backend, history.Policy{CommitLimits: sessionPolicy().CommitLimits, RetryWindow: 16, MaxPageBytes: sessionPolicy().CommitLimits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// This wrapper fails even if a reconstruction accidentally ignores a write error.
type readOnlyHistoryBackend struct {
	history.Backend
	t testing.TB
}

func (b readOnlyHistoryBackend) Append(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error) {
	b.t.Error("reconstruction attempted write")
	return history.Observation{}, history.ErrInvalid
}

var errReplayPrefix = errors.New("replay ended before pinned endpoint")

func reconstructHistorySession(t testing.TB, read func(context.Context, history.HistoryID, history.ReadRequest) (history.Page, error), endpoint history.CommitRef) (*session.Session, error) {
	t.Helper()
	// Materialize only the intended accepted prefix in isolated test storage;
	// public Load always pins that storage's actual head.
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	request := history.ReadRequest{Through: endpoint, MaxCommits: 1}
	for {
		page, err := read(context.Background(), testHistoryID(t), request)
		if err != nil {
			return nil, err
		}
		if len(page.Commits) == 0 {
			break
		}
		for _, c := range page.Commits {
			result, err := h.Append(context.Background(), testHistoryID(t), c.Commit)
			if err != nil {
				return nil, err
			}
			if result.Status != history.StatusAccepted {
				t.Fatal("prefix fixture append rejected")
			}
		}
		request.After = page.Next
	}
	if request.After != endpoint {
		return nil, errReplayPrefix
	}
	return session.Load(context.Background(), newLifecycleHistory(t, readOnlyHistoryBackend{Backend: backend, t: t}), testHistoryID(t), sessionPolicy())
}
func assertHistoryHead(t testing.TB, h *history.History, want history.CommitRef) {
	t.Helper()
	got, err := h.Head(context.Background(), testHistoryID(t))
	if err != nil || got != want {
		t.Fatalf("Head = %v, %v; want %v", got, err, want)
	}
}

func TestSessionRealHistoryLifecycleAndReplay(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	readOnly := newLifecycleHistory(t, readOnlyHistoryBackend{Backend: backend, t: t})
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	batchInput(t, b, run, []byte("first"))
	pinned := submitBatch(t, s, b)
	active := s.Snapshot()
	recovered, err := reconstructHistorySession(t, readOnly.Read, pinned)
	mustBatch(t, err)
	if !reflect.DeepEqual(recovered.Snapshot(), active) || recovered.Snapshot().ActiveRun() != run {
		t.Fatal("active reconstruction")
	}
	assertHistoryHead(t, h, pinned)
	// Use the real public bound path to close out and start a successor.
	recovered, err = session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
	mustBatch(t, err)
	b = beginBatch(t, recovered)
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	submitBatch(t, recovered, b)
	if got := recovered.Snapshot(); got.ActiveRun() != (session.RunID{}) || got.RunCount() != 1 {
		t.Fatal("explicit closeout")
	}
	b = beginBatch(t, recovered)
	next := batchRun(t, b)
	batchInput(t, b, next, []byte("second"))
	submitBatch(t, recovered, b)
	final := recovered.Snapshot()
	replay, err := reconstructHistorySession(t, readOnly.Read, final.Head())
	mustBatch(t, err)
	if !reflect.DeepEqual(replay.Snapshot(), final) {
		t.Fatal("successor reconstruction")
	}
	old, err := reconstructHistorySession(t, readOnly.Read, pinned)
	mustBatch(t, err)
	if !reflect.DeepEqual(old.Snapshot(), active) {
		t.Fatal("old endpoint drift")
	}
	assertHistoryHead(t, h, final.Head())
	prefixRead := func(ctx context.Context, id history.HistoryID, r history.ReadRequest) (history.Page, error) {
		r.Through = pinned
		return readOnly.Read(ctx, id, r)
	}
	if got, err := reconstructHistorySession(t, prefixRead, final.Head()); got != nil || !errors.Is(err, errReplayPrefix) {
		t.Fatalf("premature prefix = %v, %v", got, err)
	}
}

func TestSessionRealHistoryCompetingStarts(t *testing.T) {
	h := newLifecycleHistory(t, mem.New())
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	submitBatch(t, s, b)
	other, err := session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
	mustBatch(t, err)
	first := beginBatch(t, s)
	run := batchRun(t, first)
	batchInput(t, first, run, []byte("first"))
	second := beginBatch(t, other)
	otherRun := batchRun(t, second)
	batchInput(t, second, otherRun, []byte("second"))
	before := other.Snapshot()
	accepted := submitBatch(t, s, first)
	if ref, err := other.SubmitCommit(context.Background(), second); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrConflict) {
		t.Fatalf("competing start: %v, %v", ref, err)
	}
	if !reflect.DeepEqual(before, other.Snapshot()) {
		t.Fatal("loser published")
	}
	assertHistoryHead(t, h, accepted)
	if got := s.Snapshot(); got.ActiveRun() != run || got.RunCount() != 1 || string(inputBytes(lookupInput(t, got, run, 1))) != "first" {
		t.Fatal("winner facts")
	}
}

// Commit boundaries are persistence metadata, not semantic traversal boundaries.
func TestSessionNavigationIndependentOfCommitGrouping(t *testing.T) {
	facts := []fixtureRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), inputRecord(t, 2), closeoutRecord(t, 1), startRecord(t, 2), closeoutRecord(t, 2), startRecord(t, 3), runInputRecord(t, 3, 1)}
	var snapshots []session.Snapshot
	for _, groups := range [][]int{{9}, {1, 1, 1, 1, 1, 1, 1, 1, 1}, {2, 2, 3, 2}} {
		h := newSessionHistory(t)
		s := boundSession(t, h, sessionPolicy())
		offset := 0
		for _, n := range groups {
			c := proposedCommit(t, s, facts[offset:offset+n]...)
			result, err := h.Append(context.Background(), testHistoryID(t), c.Commit)
			mustBatch(t, err)
			if result.Status != history.StatusAccepted || result.Proposal != c.Ref {
				t.Fatal("append")
			}
			s, err = session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
			mustBatch(t, err)
			offset += n
		}
		v := s.Snapshot()
		loaded, err := session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
		mustBatch(t, err)
		if !reflect.DeepEqual(v, loaded.Snapshot()) {
			t.Fatal("same grouping replay differs")
		}
		snapshots = append(snapshots, v)
	}
	for _, v := range snapshots {
		base := snapshots[0]
		if v.Created() != base.Created() || v.ActiveRun() != base.ActiveRun() || v.RecordCount() != 9 || v.RunCount() != 3 {
			t.Fatal("semantic scalars depend on commits")
		}
		for i := 0; i < v.RecordCount(); i++ {
			if !v.RecordAt(i).Format().Equal(base.RecordAt(i).Format()) {
				t.Fatal("chronology depends on commits")
			}
		}
		for i := 0; i < v.RunCount(); i++ {
			run, want := v.RunAt(i), base.RunAt(i)
			if run.ID() != want.ID() || run.RecordCount() != want.RecordCount() || !userInputsMatch(v, run.ID(), []uint64{2, 0, 1}[i]) || !userInputsMatch(base, want.ID(), []uint64{2, 0, 1}[i]) {
				t.Fatal("run bounds depend on commits")
			}
			for j := 0; j < run.RecordCount(); j++ {
				if !run.RecordAt(j).Format().Equal(want.RecordAt(j).Format()) {
					t.Fatal("run chronology")
				}
				if input, ok := run.RecordAt(j).UserInput(); ok {
					got, _ := v.UserInputByID(input.ID())
					wantInput, _ := base.UserInputByID(input.ID())
					if got.ID() != wantInput.ID() || got.Parts().Len() != wantInput.Parts().Len() || !reflect.DeepEqual(inputBytes(got), inputBytes(wantInput)) {
						t.Fatal("input navigation")
					}
				}
			}
		}
		if !userInputsMatch(v, v.RunAt(1).ID(), 0) || v.RunAt(1).RecordCount() != 2 {
			t.Fatal("input-free run lost")
		}
	}
}
