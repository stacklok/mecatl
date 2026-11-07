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
	created, e := NewCreatedRecord(Created{HistoryID: id}, limits)
	if e != nil {
		t.Fatal(e)
	}
	start := func(n uint64) Record {
		r, e := NewRunStartedRecord(RunStarted{ID: RunID{historyID: id, ordinal: n}}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	input := func(run, n uint64) Record {
		r, e := NewUserInputRecord(UserInput{ID: UserInputID{runID: RunID{historyID: id, ordinal: run}, ordinal: n}, Parts: internalByteParts([]byte{0xff, 0, 'x'})}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	closeout := func(n uint64) Record {
		r, e := NewRunEndedRecord(RunEnded{RunID: RunID{historyID: id, ordinal: n}, Reason: RunEndedInterrupted}, limits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	idle := []Record{created}
	active := []Record{created, start(1), input(1, 1), input(1, 2)}
	closed := append(append([]Record{}, active...), closeout(1))
	for _, tc := range []struct {
		name          string
		prefix, batch []Record
		want          error
	}{
		{"complete", nil, append(append([]Record{}, closed...), start(2), input(2, 1)), nil},
		{"input before creation", nil, []Record{input(1, 1)}, ErrTransition},
		{"start before creation", nil, []Record{start(1)}, ErrTransition},
		{"interrupt before creation", nil, []Record{closeout(1)}, ErrTransition},
		{"duplicate creation", idle, []Record{created}, ErrTransition},
		{"input before start", idle, []Record{input(1, 1)}, ErrTransition},
		{"run gap", idle, []Record{start(2)}, ErrTransition},
		{"input gap", active, []Record{input(1, 4)}, ErrTransition},
		{"input duplicate", active, []Record{input(1, 2)}, ErrTransition},
		{"later input", active, []Record{input(1, 3)}, nil},
		{"active start", active, []Record{start(2)}, ErrTransition},
		{"wrong interruption", active, []Record{closeout(2)}, ErrTransition},
		{"idle interruption", closed, []Record{closeout(1)}, ErrTransition},
		{"closed owner", closed, []Record{input(1, 3)}, ErrTransition},
		{"zero input successor", closed, []Record{start(2)}, nil},
		{"valid successor", closed, []Record{start(2), input(2, 1)}, nil},
		{"invalid final owner", closed, []Record{start(2), input(1, 3)}, ErrTransition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, imported := range []bool{false, true} {
				s, _ := internalFixture(t)
				if len(tc.prefix) > 0 {
					internalApply(t, s, tc.prefix...)
				}
				batch := append([]Record{}, tc.batch...)
				if imported {
					for i, r := range batch {
						var err error
						batch[i], err = DecodeRecord(id, r.Format(), limits)
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
					dto.Records = append(dto.Records, r.Format())
				}
				wire, err := codec.Encode(dto, limits)
				if err != nil {
					t.Fatal(err)
				}
				ref := history.CommitRef{Sequence: dto.Sequence, CID: contentid.Sum(wire)}
				if err := s.Apply(history.Commit{Ref: ref, Commit: dto}); !errors.Is(err, tc.want) {
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
	start, err := NewRunStartedRecord(RunStarted{ID: RunID{historyID: id, ordinal: 1}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewUserInputRecord(UserInput{ID: UserInputID{runID: RunID{historyID: id, ordinal: 1}, ordinal: 1}, Parts: internalByteParts([]byte("too long"))}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// A later payload limit must not mask the earlier lifecycle violation.
	if _, err := s.prepare([]Record{start, input}); !errors.Is(err, ErrTransition) {
		t.Fatalf("metadata: %v", err)
	}
	dto := format.Commit{FormatVersion: 1, HistoryID: new(id), Sequence: 1, Records: []format.Record{start.Format(), input.Format()}}
	wire, err := codec.Encode(dto, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(history.Commit{Ref: history.CommitRef{Sequence: 1, CID: contentid.Sum(wire)}, Commit: dto}); !errors.Is(err, ErrTransition) {
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
	if _, err := b.AddUserInput(AddUserInputRequest{RunID: run, Parts: internalByteParts([]byte{0xff, 0, 'x'})}); err != nil {
		t.Fatal(err)
	}
	batch := append([]Record{}, b.records...)
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
			applyErr := s.Apply(history.Commit{Ref: p.ref, Commit: p.commit})
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
	created, err := NewCreatedRecord(Created{HistoryID: id}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, records := range [][]Record{nil, {{}}} {
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
		p, err := s.prepare([]Record{created})
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
