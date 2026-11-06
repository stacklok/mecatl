package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestRecordOriginatingScope(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, fixture := range recordFixtures(t) {
		t.Run(fixture.kind, func(t *testing.T) {
			local, err := fixture.make(testLimits())
			mustBatch(t, err)
			if r, err := session.DecodeRecord(historyid.HistoryID{}, local.Format(), testLimits()); !errors.Is(err, session.ErrIdentity) || !reflect.DeepEqual(r, session.Record{}) {
				t.Fatalf("zero decode scope: %v", err)
			}
			rebound, err := session.DecodeRecord(foreign, local.Format(), testLimits())
			mustBatch(t, err)
			if !rebound.Format().Equal(local.Format()) {
				t.Fatal("scope leaked into compact DTO")
			}
		})
	}
	for _, id := range []historyid.HistoryID{testHistoryID(t), foreign} {
		s, err := session.New(newSessionHistory(t), id, sessionPolicy())
		mustBatch(t, err)
		for _, r := range []session.Record{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1)} {
			decoded, err := session.DecodeRecord(id, r.Format(), testLimits())
			mustBatch(t, err)
			applyRecords(t, s, decoded)
		}
		v := s.Snapshot()
		if v.RunAt(0).ID().HistoryID != id || lookupInput(t, v, session.RunID{HistoryID: id, Ordinal: 1}, 1).ID().RunID.HistoryID != id {
			t.Fatal("decoded scope lost")
		}
	}
}

func TestSessionLocalOrdinalsBindToVerifiedChain(t *testing.T) {
	local := newSession(t, sessionPolicy())
	c := proposedCommit(t, local, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	dto := c.Commit.Clone()
	dto.HistoryID = new(foreign)
	accepted := identify(t, dto)
	other, err := session.New(newSessionHistory(t), foreign, sessionPolicy())
	mustBatch(t, err)
	mustBatch(t, other.Apply(accepted))
	got := other.Snapshot()
	run := session.RunID{HistoryID: foreign, Ordinal: 1}
	if got.HistoryID() != foreign || got.ActiveRun() != run || got.RunAt(0).ID() != run || lookupInput(t, got, run, 1).ID() != (session.UserInputID{RunID: run, Ordinal: 1}) {
		t.Fatal("local ordinals not scoped to verified chain")
	}
	assertRejected(t, local, accepted, session.ErrIdentity)
	// A successor binds through the exact parent, without a repeated history field.
	applyRecords(t, other, closeoutRecord(t, 1))
	if other.Snapshot().ActiveRun() != (session.RunID{}) {
		t.Fatal("scoped closeout")
	}
	if !c.Commit.Records[0].Equal(format.Record{Kind: "session.created", KindVersion: 1, Payload: []byte{0xa0}}) {
		t.Fatal("creation repeats scope")
	}
}
