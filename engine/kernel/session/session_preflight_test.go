package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestSessionMetadataAndDecodedLegalityAgree(t *testing.T) {
	fixture, limits := internalFixture(t)
	id := fixture.Snapshot().HistoryID()
	created, e := newCreatedRecord(Created{HistoryID: id}, limits)
	if e != nil {
		t.Fatal(e)
	}
	start := func(n uint64) proposedRecord {
		r, e := newRunStartedRecord(RunStarted{ID: RunID{historyID: id, ordinal: n}}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	input := func(run, n uint64) proposedRecord {
		r, e := makeInputRecord(identifiedInput{ID: UserInputID{runID: RunID{historyID: id, ordinal: run}, ordinal: n}, Parts: internalByteParts([]byte{0xff, 0, 'x'})}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	closeout := func(n uint64) proposedRecord {
		r, e := newRunEndedRecord(RunEnded{RunID: RunID{historyID: id, ordinal: n}, Reason: RunEndedInterrupted}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	idle := []proposedRecord{created}
	active := []proposedRecord{created, start(1), input(1, 1), input(1, 2)}
	closed := append(append([]proposedRecord{}, active...), closeout(1))
	for _, tc := range []struct {
		name          string
		prefix, batch []proposedRecord
		want          error
	}{
		{"complete", nil, append(append([]proposedRecord{}, closed...), start(2), input(2, 1)), nil},
		{"input before creation", nil, []proposedRecord{input(1, 1)}, ErrTransition},
		{"start before creation", nil, []proposedRecord{start(1)}, ErrTransition},
		{"interrupt before creation", nil, []proposedRecord{closeout(1)}, ErrTransition},
		{"duplicate creation", idle, []proposedRecord{created}, ErrTransition},
		{"input before start", idle, []proposedRecord{input(1, 1)}, ErrTransition},
		{"run gap", idle, []proposedRecord{start(2)}, ErrTransition},
		{"input gap", active, []proposedRecord{input(1, 4)}, ErrTransition},
		{"input duplicate", active, []proposedRecord{input(1, 2)}, ErrTransition},
		{"later input", active, []proposedRecord{input(1, 3)}, nil},
		{"active start", active, []proposedRecord{start(2)}, ErrTransition},
		{"wrong interruption", active, []proposedRecord{closeout(2)}, ErrTransition},
		{"idle interruption", closed, []proposedRecord{closeout(1)}, ErrTransition},
		{"closed owner", closed, []proposedRecord{input(1, 3)}, ErrTransition},
		{"zero input successor", closed, []proposedRecord{start(2)}, nil},
		{"valid successor", closed, []proposedRecord{start(2), input(2, 1)}, nil},
		{"invalid final owner", closed, []proposedRecord{start(2), input(1, 3)}, ErrTransition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, imported := range []bool{false, true} {
				s, _ := internalFixture(t)
				if len(tc.prefix) > 0 {
					internalApply(t, s, tc.prefix...)
				}
				batch := append([]proposedRecord{}, tc.batch...)
				if imported {
					for i, r := range batch {
						var err error
						batch[i], err = decodeRecord(id, r.format(), limits)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				before := s.Snapshot()
				p, err := s.prepare(batch)
				if !errors.Is(err, tc.want) || (err != nil && !reflect.DeepEqual(p, prepared{})) {
					t.Fatalf("metadata (imported=%t): %v; want %v", imported, err, tc.want)
				}
				if !reflect.DeepEqual(before, s.Snapshot()) {
					t.Fatal("preflight mutation")
				}
				dto := format.Commit{FormatVersion: 1, Sequence: before.Head().Sequence + 1}
				if before.Head() == (history.CommitRef{}) {
					dto.HistoryID = new(id)
				} else {
					dto.Parent = new(before.Head().CID)
				}
				for _, r := range batch {
					dto.Records = append(dto.Records, r.format())
				}
				wire, err := codec.Encode(dto, limits)
				if err != nil {
					t.Fatal(err)
				}
				ref := history.CommitRef{Sequence: dto.Sequence, CID: contentid.Sum(wire)}
				if err := s.apply(history.Commit{Ref: ref, Commit: dto}); !errors.Is(err, tc.want) {
					t.Fatalf("decoded: %v; want %v", err, tc.want)
				}
				if tc.want != nil {
					if !reflect.DeepEqual(before, s.Snapshot()) {
						t.Fatal("decoded rejection mutation")
					}
				} else if p.ref != ref || !p.commit.Equal(dto) {
					t.Fatal("metadata changed bytes")
				}
			}
		})
	}
}

func TestSessionMetadataPreservesOrderedFailure(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	s.policy.PayloadLimits.MaxByteStringBytes = 1
	start, err := newRunStartedRecord(RunStarted{ID: RunID{historyID: id, ordinal: 1}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	input, err := makeInputRecord(identifiedInput{ID: UserInputID{runID: RunID{historyID: id, ordinal: 1}, ordinal: 1}, Parts: internalByteParts([]byte("too long"))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// A later payload limit must not mask the earlier lifecycle violation.
	if _, err := s.prepare([]proposedRecord{start, input}); !errors.Is(err, ErrTransition) {
		t.Fatalf("metadata: %v", err)
	}
	dto := format.Commit{FormatVersion: 1, HistoryID: new(id), Sequence: 1, Records: []format.Record{start.format(), input.format()}}
	wire, err := codec.Encode(dto, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.apply(history.Commit{Ref: history.CommitRef{Sequence: 1, CID: contentid.Sum(wire)}, Commit: dto}); !errors.Is(err, ErrTransition) {
		t.Fatalf("decoded: %v", err)
	}
}

func TestSessionMetadataAndDecodedPayloadLimitsAgree(t *testing.T) {
	wide, limits := internalFixture(t)
	b, err := wide.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	run, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddUserInput(UserInput{RunID: run, Parts: internalByteParts([]byte{0xff, 0, 'x'})}); err != nil {
		t.Fatal(err)
	}
	batch := append([]proposedRecord{}, b.records...)
	if err := b.Discard(); err != nil {
		t.Fatal(err)
	}
	p, err := wide.prepare(batch)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < reflect.TypeOf(codec.Limits{}).NumField(); i++ {
		t.Run(reflect.TypeOf(limits).Field(i).Name, func(t *testing.T) {
			s, _ := internalFixture(t)
			reflect.ValueOf(&s.policy.PayloadLimits).Elem().Field(i).SetInt(1)
			before := s.Snapshot()
			_, prepareErr := s.prepare(batch)
			applyErr := s.apply(history.Commit{Ref: p.ref, Commit: p.commit})
			var want error
			if applyErr != nil {
				want = codec.ErrLimitExceeded
			}
			if !errors.Is(prepareErr, want) || !errors.Is(applyErr, want) {
				t.Fatalf("metadata %v differs from decoded %v", prepareErr, applyErr)
			}
			if applyErr != nil && !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("limit rejection mutation")
			}
		})
	}
}

func TestPrivatePreparationAdmissionAndBarriers(t *testing.T) {
	s, limits := internalFixture(t)
	id := s.Snapshot().HistoryID()
	created, err := newCreatedRecord(Created{HistoryID: id}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, records := range [][]proposedRecord{nil, {{}}} {
		if p, err := s.prepare(records); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(p, prepared{}) {
			t.Fatalf("invalid preparation: %v", err)
		}
	}
	if p, err := s.prepareEnding(RunEndedInterrupted); err != nil || !reflect.DeepEqual(p, prepared{}) {
		t.Fatalf("uncreated interruption: %v", err)
	}
	internalApply(t, s, created)
	if p, err := s.prepareEnding(RunEndedInterrupted); err != nil || !reflect.DeepEqual(p, prepared{}) {
		t.Fatalf("idle interruption: %v", err)
	}
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepareEnding(RunEndedInterrupted); !errors.Is(err, ErrBatch) {
		t.Fatal("private closeout bypassed slot")
	}
	if err := b.Discard(); err != nil {
		t.Fatal(err)
	}
	// Admission must precede even envelope allocation.
	s.policy.MaxDecodedWork = 1
	s.policy.SoftMaxDecodedWork = 1
	before := s.Snapshot()
	if n := testing.AllocsPerRun(10, func() {
		p, err := s.prepare([]proposedRecord{created})
		if !errors.Is(err, codec.ErrLimitExceeded) || p.ref != (history.CommitRef{}) || p.commit.Records != nil {
			panic("admission")
		}
	}); n != 0 {
		t.Fatalf("preparation admission allocated: %g", n)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("admission mutation")
	}
}
