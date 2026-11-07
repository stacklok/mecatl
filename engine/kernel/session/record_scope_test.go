package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestRecordOriginatingScope(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, fixture := range recordFixtures(t) {
		t.Run(fixture.kind, func(t *testing.T) {
			local, err := fixture.make(testLimits())
			mustBatch(t, err)
			if r, err := decodeRecord(historyid.HistoryID{}, local.format(), testLimits()); !errors.Is(err, ErrIdentity) || !reflect.DeepEqual(r, proposedRecord{}) {
				t.Fatalf("zero decode scope: %v", err)
			}
			rebound, err := decodeRecord(foreign, local.format(), testLimits())
			mustBatch(t, err)
			if !rebound.format().Equal(local.format()) {
				t.Fatal("scope leaked into compact DTO")
			}
		})
	}
	for _, id := range []historyid.HistoryID{testHistoryID(t), foreign} {
		s, err := New(newSessionHistory(t), id, sessionPolicy())
		mustBatch(t, err)
		for _, r := range []proposedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1)} {
			decoded, err := decodeRecord(id, r.format(), testLimits())
			mustBatch(t, err)
			applyRecords(t, s, decoded)
		}
		v := s.Snapshot()
		if v.RunAt(0).ID().HistoryID() != id || lookupInput(t, v, v.RunAt(0).ID(), 1).ID().RunID().HistoryID() != id {
			t.Fatal("decoded scope lost")
		}
	}
}

func TestAllocatedAuthoringScopeBeforePersistenceConversion(t *testing.T) {
	s, limits := internalFixture(t)
	run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, owner := range []RunID{{}, {historyID: run.historyID, ordinal: 2}, {historyID: foreign, ordinal: 1}} {
		inputID := UserInputID{runID: owner, ordinal: 1}
		if r, err := newUserInputRecord(UserInput{RunID: run}, inputID, limits); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, proposedRecord{}) {
			t.Fatalf("input identity lost scope before conversion: %v", err)
		}
		requestID := ModelRequestID{runID: owner, requestOrdinal: 1}
		v := ModelRequest{RunID: run, Source: SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}}
		if r, err := newModelRequestRecord(v, requestID, limits); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, proposedRecord{}) {
			t.Fatalf("request identity lost scope before conversion: %v", err)
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
	other, err := New(newSessionHistory(t), foreign, sessionPolicy())
	mustBatch(t, err)
	mustBatch(t, other.apply(accepted))
	got := other.Snapshot()
	run := allocatedRun(t, foreign, 1)
	if got.HistoryID() != foreign || got.ActiveRun() != run || got.RunAt(0).ID() != run || lookupInput(t, got, run, 1).ID() != allocatedInput(t, run, 1) {
		t.Fatal("local ordinals not scoped to verified chain")
	}
	assertRejected(t, local, accepted, ErrIdentity)
	// A successor binds through the exact parent, without a repeated history field.
	applyRecords(t, other, closeoutRecord(t, 1))
	if other.Snapshot().ActiveRun() != (RunID{}) {
		t.Fatal("scoped closeout")
	}
	if !c.Commit.Records[0].Equal(format.Record{Kind: "session.created", KindVersion: 1, Payload: []byte{0xa0}}) {
		t.Fatal("creation repeats scope")
	}
}
