package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestUsageUncertainAcceptanceFreezesExactReplacement(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acceptance", true: "lost acknowledgement"}[accepted], func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			id, err := b.AddUsage(nil, nil, session.UsagePurposeMain, session.UsageReport{State: session.UsageReportActive})
			mustBatch(t, err)
			submitBatch(t, s, b)
			before := s.Snapshot()
			b = beginBatch(t, s)
			report := session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(0)), Output: new(int64(5))}}}, Raw: &session.OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}
			want := report.Clone()
			mustBatch(t, b.ReplaceUsage(id, report))
			next, err := b.AddUsage(nil, nil, "aux", session.UsageReport{State: session.UsageReportActive})
			mustBatch(t, err)
			captured := captureCommit(t, b)
			backend.accept, backend.fail = accepted, codec.ErrLimitExceeded
			if ref, err := s.SubmitCommit(t.Context(), b); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrAcceptanceUnknown) || !reflect.DeepEqual(s.Snapshot(), before) {
				t.Fatalf("uncertain usage publication: %v", err)
			}
			attempt := backend.attempts[len(backend.attempts)-1]
			*report.Buckets[0].Counters.Output = 99
			clear(report.Raw.Data)
			if err := b.ReplaceUsage(id, session.UsageReport{State: session.UsageReportWithdrawn}); !errors.Is(err, session.ErrBatch) {
				t.Fatalf("uncertain report changed: %v", err)
			}
			if _, err := b.AddUsage(nil, nil, "other", want); !errors.Is(err, session.ErrBatch) {
				t.Fatalf("uncertain allocation changed: %v", err)
			}
			if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("discarded uncertain accounting")
			}
			if ref := submitBatch(t, s, b); ref != attempt.commit.Ref || !reflect.DeepEqual(attempt, backend.attempts[len(backend.attempts)-1]) {
				t.Fatal("retry changed exact accounting bytes")
			}
			usage, ok := captured.RecordAt(before.RecordCount()).Usage()
			if !ok || !usage.Report().MutableClone().Equal(want) {
				t.Fatal("frozen capture changed")
			}
			loaded, err := session.Load(t.Context(), h, before.HistoryID(), sessionPolicy(), testConfig())
			mustBatch(t, err)
			if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
				t.Fatal("retry replay changed accounting")
			}
			b = beginBatch(t, loaded)
			last, err := b.AddUsage(nil, nil, "after retry", session.UsageReport{State: session.UsageReportActive})
			mustBatch(t, err)
			if next.HistoryRelative() != 2 || last.HistoryRelative() != 3 {
				t.Fatal("retry double-consumed identity")
			}
			submitBatch(t, loaded, b)
		})
	}
}

func TestUsageConflictDoesNotPublishOrMerge(t *testing.T) {
	h := newLifecycleHistory(t, mem.New())
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	id, err := b.AddUsage(nil, nil, session.UsagePurposeMain, session.UsageReport{State: session.UsageReportActive})
	mustBatch(t, err)
	submitBatch(t, s, b)
	other, err := session.Load(t.Context(), h, s.Snapshot().HistoryID(), sessionPolicy(), testConfig())
	mustBatch(t, err)
	before := other.Snapshot()
	winner := beginBatch(t, s)
	mustBatch(t, winner.ReplaceUsage(id, session.UsageReport{State: session.UsageReportWithdrawn}))
	loser := beginBatch(t, other)
	mustBatch(t, loser.ReplaceUsage(id, session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(10))}}}}))
	_, err = loser.AddUsage(nil, nil, "loser", session.UsageReport{State: session.UsageReportActive})
	mustBatch(t, err)
	head := submitBatch(t, s, winner)
	if ref, err := other.SubmitCommit(t.Context(), loser); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrConflict) || errors.Is(err, session.ErrAcceptanceUnknown) {
		t.Fatalf("conflicting accounting: %v", err)
	}
	if !reflect.DeepEqual(before, other.Snapshot()) {
		t.Fatal("conflict published accounting prefix")
	}
	if _, err := other.BeginCommit(); !errors.Is(err, session.ErrReloadRequired) {
		t.Fatal("conflicting owner reused")
	}
	loaded, err := session.Load(t.Context(), h, before.HistoryID(), sessionPolicy(), testConfig())
	mustBatch(t, err)
	if loaded.Snapshot().Head() != head || !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
		t.Fatal("fresh load merged losing usage")
	}
	b = beginBatch(t, loaded)
	next, err := b.AddUsage(nil, nil, "winner", session.UsageReport{State: session.UsageReportActive})
	mustBatch(t, err)
	if next.HistoryRelative() != 2 {
		t.Fatal("losing allocation consumed identity")
	}
	submitBatch(t, loaded, b)
}
