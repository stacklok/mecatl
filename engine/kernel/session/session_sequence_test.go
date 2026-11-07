package session

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// Synthetic counters exercise overflow without allocating impossible histories.
func TestSessionCommitOrdinalExhaustion(t *testing.T) {
	for _, input := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "input"}[input], func(t *testing.T) {
			s, _ := internalFixture(t)
			run := RunID{HistoryID: s.Snapshot().HistoryID(), Ordinal: 1}
			s.state.view.lifecycle = lifecycleState{created: true, runs: math.MaxUint64}
			if input {
				s.state.view.lifecycle = lifecycleState{created: true, runs: 1, activeRun: run, inputs: math.MaxUint64}
			}
			b, err := s.BeginCommit()
			if err != nil {
				t.Fatal(err)
			}
			before := b.admission.candidate
			if input {
				if id, err := b.AddUserInput(AddUserInputRequest{RunID: run}); id != (UserInputID{}) || !errors.Is(err, codec.ErrLimitExceeded) {
					t.Fatalf("input overflow: %v, %v", id, err)
				}
			} else {
				if id, err := b.StartRun(); id != (RunID{}) || !errors.Is(err, codec.ErrLimitExceeded) {
					t.Fatalf("run overflow: %v, %v", id, err)
				}
			}
			if b.admission.candidate != before || len(b.records) != 0 {
				t.Fatal("overflow changed batch")
			}
			if err := b.Discard(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A synthetic head avoids constructing MaxInt64 real predecessors.
func TestSessionSequenceExhaustion(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "start cannot strand", true: "last closeout fits"}[active], func(t *testing.T) {
			s, limits := internalFixture(t)
			id := s.Snapshot().HistoryID()
			run := RunID{HistoryID: id, Ordinal: 1}
			created, err := NewCreatedRecord(Created{HistoryID: id}, limits)
			if err != nil {
				t.Fatal(err)
			}
			start, err := NewRunStartedRecord(RunStarted{ID: run}, limits)
			if err != nil {
				t.Fatal(err)
			}
			records := []Record{created}
			if active {
				records = append(records, start)
			}
			internalApply(t, s, records...)
			s.state.view.head.Sequence = math.MaxInt64 - 1
			before := s.Snapshot()
			if active {
				p, err := s.prepareEnding(RunEndedInterrupted)
				if err != nil || p.ref.Sequence != math.MaxInt64 {
					t.Fatalf("last interruption: %v", err)
				}
				if err := s.Apply(history.Commit{Ref: p.ref, Commit: p.commit}); err != nil {
					t.Fatal(err)
				}
			} else {
				if p, err := s.prepare([]Record{start}); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(p, prepared{}) {
					t.Fatalf("stranding preparation: %v", err)
				}
				dto := format.Commit{FormatVersion: 1, Sequence: math.MaxInt64, Parent: new(before.Head().CID), Records: []format.Record{start.Format()}}
				wire, err := codec.Encode(dto, limits)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Apply(history.Commit{Ref: history.CommitRef{Sequence: dto.Sequence, CID: contentid.Sum(wire)}, Commit: dto}); err != nil {
					t.Fatalf("hard-valid Apply must not reserve future sequence: %v", err)
				}
				if s.Snapshot().ActiveRun() != run || s.Snapshot().Head().Sequence != math.MaxInt64 {
					t.Fatal("replay lost final active prefix")
				}
				s.state.view.head.Sequence = math.MaxInt64
			}
			before = s.Snapshot()
			if p, err := s.prepare([]Record{start}); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(p, prepared{}) {
				t.Fatalf("exhausted preparation: %v", err)
			}
			if err := s.Apply(history.Commit{Commit: format.Commit{Records: []format.Record{start.Format()}}}); !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("exhausted Apply: %v", err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("exhaustion mutation")
			}
		})
	}
}
