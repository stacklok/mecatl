package session

import (
	"bytes"
	"math"
	"sync"
	"testing"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func TestSnapshotUsagesLatestAndPinned(t *testing.T) {
	s := toolCallViewSession(t)
	run := s.Snapshot().ActiveRun()
	b := beginBatch(t, s)
	request, err := b.AddModelRequest(run, captureCommit(t, b), ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}})
	mustBatch(t, err)
	original := usageReportFixture()
	wantOriginal := original.Clone()
	first, err := b.AddUsage(nil, &request, UsagePurposeMain, original)
	mustBatch(t, err)
	second, err := b.AddUsage(nil, nil, " Session Work ", UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	third, err := b.AddUsage(&run, nil, "aux", UsageReport{State: UsageReportActive, Buckets: []UsageBucket{{}}})
	mustBatch(t, err)
	// A captured proposal and the accepted first join both borrow owned payloads.
	proposed := captureCommit(t, b)
	*original.Buckets[0].Counters.Output = 99
	*original.Buckets[0].SelectedModel = "changed"
	clear(original.Raw.Data)
	submitBatch(t, s, b)
	old := s.Snapshot()
	oldJoin := old.Usages()
	oldEntry := oldJoin.At(0)
	proposedUsage, _ := proposed.RecordAt(proposed.RecordCount() - 3).Usage()
	if !proposedUsage.Report().MutableClone().Equal(wantOriginal) || !oldEntry.Report().MutableClone().Equal(wantOriginal) {
		t.Fatal("caller report mutation reached captured or accepted backing")
	}

	ids := []UsageID{first, second, third}
	check := func(want UsageReport) {
		t.Helper()
		loaded, err := Load(t.Context(), s.history, run.SessionID(), testConfigFor(s.policy))
		mustBatch(t, err)
		for seam, snapshot := range map[string]Snapshot{"Submit": s.Snapshot(), "Load": loaded.Snapshot()} {
			t.Run(seam, func(t *testing.T) {
				joined := snapshot.Usages()
				if joined.Len() != len(ids) {
					t.Fatalf("identities = %d, want %d", joined.Len(), len(ids))
				}
				for pass := range 2 {
					count := 0
					for i, entry := range joined.All() {
						count++
						looked, ok := joined.ByID(ids[i])
						if !ok || entry != looked || joined.At(i) != entry || entry.ID() != ids[i] {
							t.Fatalf("pass %d index %d: order/lookup mismatch", pass, i)
						}
						d := entry.Definition()
						r, rp := d.RunID()
						q, qp := d.RequestID()
						switch i {
						case 0:
							if !rp || r != run || !qp || q != request || d.Purpose() != UsagePurposeMain || !entry.Report().MutableClone().Equal(want) {
								t.Fatal("latest report or fixed scoped definition changed")
							}
						case 1:
							if rp || qp || d.Purpose() != " Session Work " || entry.Report().Buckets().Len() != 0 {
								t.Fatal("session-level unknown measurement changed")
							}
						case 2:
							if !rp || r != run || qp || d.Purpose() != "aux" || entry.Report().Buckets().Len() != 1 {
								t.Fatal("run-level unknown counters changed")
							}
						case 3:
							if !rp || r != run || !qp || q != request || d.Purpose() != "late" {
								t.Fatal("late fact was filtered by or associated with B's span")
							}
						}
					}
					if count != len(ids) {
						t.Fatal("enumeration not repeatable")
					}
				}
				for range joined.All() {
					break
				}
				count := 0
				for range joined.All() {
					count++
				}
				if count != len(ids) {
					t.Fatal("early stop consumed iteration")
				}
				assertUsageJoinBorrows(t, snapshot, joined)
				assertUsageEntryIsolated(t, joined.At(0))
			})
		}
	}
	check(wantOriginal)
	// All later reports for A arrive after B starts, outside A's record span.
	b = beginBatch(t, s)
	mustBatch(t, b.EndRun(RunEndedInterrupted))
	next := batchRun(t, b)
	late, err := b.AddUsage(nil, &request, "late", UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	submitBatch(t, s, b)
	ids = append(ids, late)
	if s.Snapshot().ActiveRun() != next {
		t.Fatal("late accounting changed active run")
	}
	decreased := UsageReport{State: UsageReportActive, Buckets: []UsageBucket{{Counters: UsageCounters{Output: new(int64(1))}}}}
	for _, report := range []UsageReport{decreased, decreased, {State: UsageReportWithdrawn}, {State: UsageReportActive}, wantOriginal} {
		interactionSubmit(t, s, func(b *SessionCommit) {
			mustBatch(t, b.ReplaceUsage(first, UsageReport{State: UsageReportWithdrawn}))
			mustBatch(t, b.ReplaceUsage(first, report)) // Last chronological record wins within one commit too.
		})
		check(report)
		if !oldEntry.Report().MutableClone().Equal(wantOriginal) || old.Usages().Len() != 3 || oldJoin.Len() != 3 || old.Usages().At(0).Report().value != oldEntry.Report().value {
			t.Fatal("old snapshot, join or entry changed after append")
		}
		if _, ok := oldJoin.ByID(late); ok {
			t.Fatal("old join acquired later identity")
		}
	}
}

// Pointer checks establish that the join borrows first/latest record payloads,
// rather than duplicating them while still returning equivalent DTOs.
func assertUsageJoinBorrows(t testing.TB, snapshot Snapshot, joined UsageEntriesView) {
	t.Helper()
	for _, entry := range joined.All() {
		var first, latest UsageView
		for record := range snapshot.Records() {
			usage, ok := record.Usage()
			if !ok || usage.ID() != entry.ID() {
				continue
			}
			if _, ok := usage.Definition(); ok {
				first = usage
			}
			latest = usage
		}
		d, _ := first.Definition()
		if entry.Definition().value != d.value || entry.Report().value != latest.Report().value {
			t.Fatal("join did not borrow first/latest payloads")
		}
	}
}

func assertUsageEntryIsolated(t testing.TB, entry UsageEntryView) {
	t.Helper()
	definition := entry.Definition().MutableClone()
	wantDefinition := definition.Clone()
	report := entry.Report().MutableClone()
	wantReport := report.Clone()
	definition.Purpose = "changed"
	if definition.RunID != nil {
		*definition.RunID = 99
	}
	if definition.RequestID != nil {
		*definition.RequestID = 99
	}
	for i := range report.Buckets {
		bucket := &report.Buckets[i]
		for _, label := range [...]*string{bucket.SelectedRoute, bucket.SelectedModel} {
			if label != nil {
				*label = "changed"
			}
		}
		c := bucket.Counters
		for _, counter := range [...]*int64{c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
			if counter != nil {
				*counter = 99
			}
		}
	}
	if raw, ok := entry.Report().Raw(); ok {
		clear(raw.Data().Copy())
		clear(report.Raw.Data)
	}
	if !entry.Definition().MutableClone().Equal(wantDefinition) || !entry.Report().MutableClone().Equal(wantReport) {
		t.Fatal("MutableClone or selected leaf Copy mutated joined backing")
	}
}

func TestUsageEntriesZeroLookupAndBounds(t *testing.T) {
	s := toolCallViewSession(t)
	var id UsageID
	interactionSubmit(t, s, func(b *SessionCommit) {
		var err error
		id, err = b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
		mustBatch(t, err)
	})
	var zero UsageEntriesView
	if zero.Len() != 0 || (Snapshot{}).Usages().Len() != 0 {
		t.Fatal("zero collection not empty")
	}
	for range zero.All() {
		t.Fatal("zero collection yielded entry")
	}
	entry := UsageEntryView{}
	if entry.ID() != (UsageID{}) || entry.Definition() != (UsageDefinitionView{}) || entry.Report() != (UsageReportView{}) {
		t.Fatal("unsafe zero entry")
	}
	joined := s.Snapshot().Usages()
	for _, absent := range []UsageID{{}, {usageOrdinal: 1}, {sessionID: id.sessionID}, {sessionID: sessionid.NewSessionID(), usageOrdinal: 1}, {sessionID: id.sessionID, usageOrdinal: 2}, {sessionID: id.sessionID, usageOrdinal: math.MaxUint64}} {
		for _, collection := range []UsageEntriesView{zero, joined} {
			if got, ok := collection.ByID(absent); ok || got != (UsageEntryView{}) {
				t.Fatal("invalid/foreign/absent identity present")
			}
		}
	}
	for _, collection := range []UsageEntriesView{zero, joined} {
		for _, i := range []int{-1, collection.Len()} {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("out-of-range At did not panic")
					}
				}()
				_ = collection.At(i)
			}()
		}
	}
}

func usageEntriesFixture(t testing.TB, identities, replacements, sibling int) *Session {
	t.Helper()
	s := toolCallViewSession(t)
	report := usageReportFixture()
	report.Buckets[1].SelectedModel = new(string(bytes.Repeat([]byte{'m'}, sibling)))
	report.Raw.Data = bytes.Repeat([]byte{0xff}, sibling)
	interactionSubmit(t, s, func(b *SessionCommit) {
		for range identities {
			id, err := b.AddUsage(nil, nil, UsagePurposeMain, report)
			interactionCheck(t, err)
			for range replacements {
				interactionCheck(t, b.ReplaceUsage(id, report))
			}
		}
	})
	return s
}

func TestUsageEntriesPinnedConcurrentReaders(t *testing.T) {
	s := usageEntriesFixture(t, 2, 1, 4096)
	pinned := s.Snapshot()
	joined := pinned.Usages()
	want := joined.At(0).Report().MutableClone()
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 100 {
				for _, collection := range []UsageEntriesView{joined, pinned.Usages()} {
					for _, entry := range collection.All() {
						if !entry.Report().MutableClone().Equal(want) {
							t.Error("pinned report changed during owner append")
						}
						assertUsageEntryIsolated(t, entry)
					}
				}
			}
		})
	}
	for range 30 {
		interactionSubmit(t, s, func(b *SessionCommit) {
			mustBatch(t, b.ReplaceUsage(joined.At(0).ID(), UsageReport{State: UsageReportWithdrawn}))
			_, err := b.AddUsage(nil, nil, "later", UsageReport{State: UsageReportActive})
			mustBatch(t, err)
		})
	}
	readers.Wait()
	if joined.Len() != 2 || s.Snapshot().Usages().Len() != 32 {
		t.Fatal("pinned collection bounds changed")
	}
}
