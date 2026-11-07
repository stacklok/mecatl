package session_test

import (
	"reflect"
	"sync"
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestSnapshotUserInputByID(t *testing.T) {
	s := newSession(t, sessionPolicy())
	beforeCreation := s.Snapshot()
	applyRecords(t, s, createdRecord(t), startRecord(t, 1))
	beforeInput := s.Snapshot()
	first := allocatedInput(t, beforeInput.ActiveRun(), 1)
	empty, err := session.NewUserInputRecord(session.UserInput{ID: first}, testLimits())
	mustBatch(t, err)
	applyRecords(t, s, empty)
	pinned := s.Snapshot()
	second := allocatedInput(t, first.RunID(), 2)
	applyRecords(t, s, inputRecord(t, 2), closeoutRecord(t, 1), startRecord(t, 2))
	beforeNewInput := s.Snapshot()
	newInput := allocatedInput(t, beforeNewInput.ActiveRun(), 1)
	applyRecords(t, s, runInputRecord(t, 2, 1))
	latest := s.Snapshot()
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for name, id := range map[string]session.UserInputID{
		"zero":        {},
		"foreign":     allocatedInput(t, allocatedRun(t, foreign, 1), 1),
		"absent run":  allocatedInput(t, allocatedRun(t, first.RunID().HistoryID(), 3), 1),
		"old run end": allocatedInput(t, first.RunID(), 3),
		"new run end": allocatedInput(t, newInput.RunID(), 2),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := latest.UserInputByID(id); ok || !reflect.DeepEqual(got, session.UserInputView{}) {
				t.Fatalf("lookup = %#v, %t", got, ok)
			}
		})
	}
	for _, tc := range []struct {
		view session.Snapshot
		id   session.UserInputID
	}{
		{session.Snapshot{}, first}, {beforeCreation, first}, {beforeInput, first},
		{pinned, second}, {pinned, newInput}, {beforeNewInput, newInput},
	} {
		if got, ok := tc.view.UserInputByID(tc.id); ok || !reflect.DeepEqual(got, session.UserInputView{}) {
			t.Fatalf("unpinned lookup %v = %#v, %t", tc.id, got, ok)
		}
	}
	for _, id := range []session.UserInputID{first, second, newInput} {
		got, ok := latest.UserInputByID(id)
		if !ok || got.ID() != id {
			t.Fatalf("accepted lookup %v = %#v, %t", id, got, ok)
		}
	}
	if got, ok := pinned.UserInputByID(first); !ok || got.ID() != first || got.PartCount() != 0 {
		t.Fatal("empty submission is not present")
	}
}

func TestSnapshotUserInputByIDConcurrentPinned(t *testing.T) {
	p := sessionPolicy()
	p.MaxRetainedRecords, p.SoftMaxRetainedRecords = 1024, 1024
	s := newSession(t, p)
	applyRecords(t, s, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	pinned := s.Snapshot()
	first := allocatedInput(t, pinned.ActiveRun(), 1)
	absent := []session.UserInputID{allocatedInput(t, first.RunID(), 2), allocatedInput(t, allocatedRun(t, first.RunID().HistoryID(), 2), 1)}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 128 {
				got, ok := pinned.UserInputByID(first)
				if !ok || got.ID() != first || !reflect.DeepEqual(inputBytes(got), []byte{0xff, 0, 'x'}) {
					t.Error("pinned input changed")
				}
				for _, id := range absent {
					if got, ok := pinned.UserInputByID(id); ok || !reflect.DeepEqual(got, session.UserInputView{}) {
						t.Error("pinned lookup exposed later input")
					}
				}
			}
		})
	}
	for ordinal := uint64(2); ordinal <= 64; ordinal++ {
		applyRecords(t, s, inputRecord(t, ordinal))
	}
	applyRecords(t, s, closeoutRecord(t, 1), startRecord(t, 2), runInputRecord(t, 2, 1))
	readers.Wait()
}
