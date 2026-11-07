package session_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyformat "github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// These helpers are independent of the pre-run-ownership suite during migration.
func ownedCoreFixture(t *testing.T, maxRecords int) (*session.Session, *history.History, session.Policy, historyid.HistoryID) {
	t.Helper()
	limits := codec.Limits{MaxBytes: 8192, MaxStringBytes: 8192, MaxByteStringBytes: 8192,
		MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	p := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16,
		SoftMaxRetainedRecords: maxRecords, SoftMaxPayloadBytes: 1 << 20, SoftMaxDecodedWork: 1 << 30,
		MaxRetainedRecords: maxRecords, MaxPayloadBytes: 1 << 20, MaxDecodedWork: 1 << 30}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(h, id, p)
	if err != nil {
		t.Fatal(err)
	}
	return s, h, p, id
}

func ownedCoreBatch(t *testing.T, s *session.Session) *session.SessionCommit {
	t.Helper()
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ownedCoreSubmit(t *testing.T, s *session.Session, b *session.SessionCommit) {
	t.Helper()
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedCoreLifecycleSnapshotsAndReplay(t *testing.T) {
	s, h, p, id := ownedCoreFixture(t, 2048)
	empty := s.Snapshot()
	if empty.HistoryID() != id || empty.Created() || empty.RecordCount() != 0 {
		t.Fatal("new scope")
	}
	b := ownedCoreBatch(t, s)
	if _, err := b.AddUserInput(session.AddUserInputRequest{}); !errors.Is(err, session.ErrTransition) {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	run, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	content := []byte{0xff, 0, 'x'}
	input, err := b.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: byteParts(content)})
	if err != nil {
		t.Fatal(err)
	}
	content[0] = 0
	if input.RunID != run || input.Ordinal != 1 {
		t.Fatal("input identity")
	}
	if s.Snapshot().RecordCount() != 0 {
		t.Fatal("provisional publication")
	}
	ownedCoreSubmit(t, s, b)
	old := s.Snapshot()
	oldRun, oldInput := old.RunAt(0), lookupInput(t, old, run, input.Ordinal)
	oldRecord := oldRun.RecordAt(1)
	if _, ok := old.RecordAt(0).Created(); !ok {
		t.Fatal("creation kind")
	}
	if _, ok := old.RecordAt(1).Created(); ok {
		t.Fatal("wrong kind")
	}
	if _, ok := (session.RecordView{}).Created(); ok {
		t.Fatal("zero kind")
	}
	if got := inputBytes(oldInput); !bytes.Equal(got, []byte{0xff, 0, 'x'}) {
		t.Fatal("content not owned")
	}
	got := inputBytes(oldInput)
	got[0] = 0
	raw := old.RecordAt(2).Format()
	raw.Payload[0] = 0

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 4000 {
				if old.RecordCount() != 3 || oldRun.RecordCount() != 2 || !userInputsMatch(old, run, 1) || inputBytes(oldInput)[0] != 0xff {
					t.Error("old handle changed")
					return
				}
				if got, ok := old.UserInputByID(input); !ok || got.ID() != input {
					t.Error("navigation changed")
					return
				}
				fact, ok := oldRecord.UserInput()
				if !ok || fact.ID() != input || !bytes.Equal(inputBytes(fact), []byte{0xff, 0, 'x'}) {
					t.Error("independent record handle changed")
					return
				}
				for i := 0; i < oldRun.RecordCount(); i++ {
					if !oldRun.RecordAt(i).Format().Equal(old.RecordAt(i + 1).Format()) {
						t.Error("independent traversal changed")
						return
					}
				}
			}
		})
	}
	for n := 0; n < 128; n++ {
		b := ownedCoreBatch(t, s)
		if _, err := b.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: byteParts([]byte{byte(n)})}); err != nil {
			t.Fatal(err)
		}
		ownedCoreSubmit(t, s, b)
	}
	b = ownedCoreBatch(t, s)
	if err := b.EndRun(session.RunEndedInterrupted); err != nil {
		t.Fatal(err)
	}
	next, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddUserInput(session.AddUserInputRequest{RunID: run}); !errors.Is(err, session.ErrTransition) {
		t.Fatal(err)
	}
	nextInput, err := b.AddUserInput(session.AddUserInputRequest{RunID: next})
	if err != nil || nextInput.Ordinal != 1 {
		t.Fatalf("new run numbering: %v", err)
	}
	ownedCoreSubmit(t, s, b)
	wg.Wait()
	v := s.Snapshot()
	if v.RunAt(0).RecordCount() != 131 || !userInputsMatch(v, run, 129) || v.RunAt(1).RecordCount() != 2 {
		t.Fatal("run endpoints")
	}
	if oldRun.RecordCount() != 2 || old.RecordCount() != 3 {
		t.Fatal("old last-run endpoint")
	}
	loaded, err := session.Load(context.Background(), h, id, p)
	if err != nil {
		t.Fatal(err)
	}
	replay := loaded.Snapshot()
	if replay.Head() != v.Head() || replay.ActiveRun() != next || replay.RecordCount() != v.RecordCount() {
		t.Fatal("replay scalar mismatch")
	}
	for i := 0; i < v.RecordCount(); i++ {
		if !v.RecordAt(i).Format().Equal(replay.RecordAt(i).Format()) {
			t.Fatal("replay fact mismatch")
		}
	}
	if allocations := testing.AllocsPerRun(100, func() { _ = s.Snapshot() }); allocations != 0 {
		t.Fatalf("snapshot allocations: %g", allocations)
	}
}

func TestOwnedCoreApplyAtomicityAndImportedStorage(t *testing.T) {
	s, _, p, id := ownedCoreFixture(t, 64)
	makeRecord := func(kind string, payload []byte) historyformat.Record {
		return historyformat.Record{Kind: kind, KindVersion: sessionformat.Version, Payload: payload}
	}
	encodeInput := func(ordinal uint64) []byte {
		payload, err := codec.Encode(sessionformat.UserInput{RunID: 1, ID: ordinal, Parts: formatByteParts([]byte("exact"))}, p.PayloadLimits)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	created, err := codec.Encode(sessionformat.Created{}, p.PayloadLimits)
	if err != nil {
		t.Fatal(err)
	}
	started, err := codec.Encode(sessionformat.RunStarted{ID: 1}, p.PayloadLimits)
	if err != nil {
		t.Fatal(err)
	}
	c := historyformat.Commit{FormatVersion: 1, HistoryID: &id, Sequence: 1, Records: []historyformat.Record{
		makeRecord(sessionformat.KindCreated, created), makeRecord(sessionformat.KindRunStarted, started), makeRecord(sessionformat.KindUserInput, encodeInput(1)), makeRecord(sessionformat.KindUserInput, encodeInput(3)),
	}}
	identify := func(c historyformat.Commit) history.Commit {
		wire, err := codec.Encode(c, p.CommitLimits)
		if err != nil {
			t.Fatal(err)
		}
		return history.Commit{Ref: history.CommitRef{Sequence: c.Sequence, CID: contentid.Sum(wire)}, Commit: c}
	}
	if err := s.Apply(identify(c)); !errors.Is(err, session.ErrTransition) {
		t.Fatal(err)
	}
	if v := s.Snapshot(); v.Created() || v.RecordCount() != 0 || v.RunCount() != 0 {
		t.Fatal("partial apply")
	}
	c.Records = c.Records[:3]
	if err := s.Apply(identify(c)); err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Records {
		clear(r.Payload)
	}
	if string(inputBytes(lookupInput(t, s.Snapshot(), session.RunID{HistoryID: id, Ordinal: 1}, 1))) != "exact" {
		t.Fatal("imported payload alias")
	}
}

func TestOwnedCoreReserveRejectionAndScopedContracts(t *testing.T) {
	s, _, p, id := ownedCoreFixture(t, 2)
	b := ownedCoreBatch(t, s)
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	first, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if s.Snapshot().RecordCount() != 0 {
		t.Fatal("reserve rejection published")
	}
	b = ownedCoreBatch(t, s)
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	ownedCoreSubmit(t, s, b)
	if first.Ordinal != 1 {
		t.Fatal("provisional run")
	}
	inputID := session.UserInputID{RunID: session.RunID{HistoryID: id, Ordinal: 2}, Ordinal: math.MaxUint64}
	if _, err := inputID.Next(); !errors.Is(err, session.ErrOrdinalExhausted) {
		t.Fatal(err)
	}
	inputID.Ordinal = 1
	r, err := session.NewUserInputRecord(session.UserInput{ID: inputID, Parts: byteParts([]byte{0xff})}, p.PayloadLimits)
	if err != nil {
		t.Fatal(err)
	}
	dto, err := codec.Decode[sessionformat.UserInput](r.Format().Payload, p.PayloadLimits)
	if err != nil || dto.RunID != 2 || dto.ID != 1 || dtoInputBytes(dto)[0] != 0xff {
		t.Fatalf("scoped DTO: %v", err)
	}
	if _, err := codec.Encode(sessionformat.UserInput{ID: 1}, p.PayloadLimits); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatal(err)
	}
}
