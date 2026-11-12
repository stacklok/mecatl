package session_test

import (
	"reflect"
	"sync"
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestSnapshotUserInputByID(t *testing.T) {
	h := newSessionHistory(t)
	s := boundSession(t, h, sessionPolicy())
	beforeCreation := s.Snapshot()
	s = loadRecords(t, h, s, createdRecord(t), startRecord(t, 1))
	beforeInput := s.Snapshot()
	first := allocatedInput(t, beforeInput.ActiveRun(), 1)
	b := beginBatch(t, s)
	if got := batchInput(t, b, beforeInput.ActiveRun(), nil); got != first {
		t.Fatalf("first input = %v; want %v", got, first)
	}
	submitBatch(t, s, b)
	pinned := s.Snapshot()
	second := allocatedInput(t, first.RunID(), 2)
	b = beginBatch(t, s)
	if got := batchInput(t, b, first.RunID(), []byte{0xff, 0, 'x'}); got != second {
		t.Fatalf("second input = %v; want %v", got, second)
	}
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	secondRun := batchRun(t, b)
	submitBatch(t, s, b)
	beforeNewInput := s.Snapshot()
	newInput := allocatedInput(t, beforeNewInput.ActiveRun(), 1)
	b = beginBatch(t, s)
	if got := batchInput(t, b, beforeNewInput.ActiveRun(), []byte{0xff, 0, 'x'}); got != newInput {
		t.Fatalf("new-run input = %v; want %v", got, newInput)
	}
	submitBatch(t, s, b)
	latest := s.Snapshot()
	if latest.RecordCount() != pinned.RecordCount()+4 || latest.ActiveRun() != secondRun || !userInputsMatch(latest, first.RunID(), 2) || !userInputsMatch(latest, secondRun, 1) {
		t.Fatal("same-owner appends did not publish expected inputs and successor")
	}
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
	if got, ok := pinned.UserInputByID(first); !ok || got.ID() != first || got.Parts().Len() != 0 {
		t.Fatal("empty submission is not present")
	}
}

func TestSnapshotUserInputByIDConcurrentPinned(t *testing.T) {
	p := sessionPolicy()
	p.MaxRetainedRecords, p.SoftMaxRetainedRecords = 1024, 1024
	h := newSessionHistory(t)
	s := boundSession(t, h, p)
	s = loadRecords(t, h, s, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	pinned := s.Snapshot()
	first := allocatedInput(t, pinned.ActiveRun(), 1)
	absent := []session.UserInputID{allocatedInput(t, first.RunID(), 2), allocatedInput(t, allocatedRun(t, first.RunID().HistoryID(), 2), 1)}
	checkPinned := func() {
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
	var beforeAppend, afterFirstAppend sync.WaitGroup
	beforeAppend.Add(4)
	afterFirstAppend.Add(4)
	start := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			checkPinned()
			beforeAppend.Done()
			<-start
			checkPinned()
			afterFirstAppend.Done()
			for range 127 {
				checkPinned()
			}
		})
	}
	appendInput := func(ordinal uint64) {
		b := beginBatch(t, s)
		if got := batchInput(t, b, first.RunID(), []byte{0xff, 0, 'x'}); got != allocatedInput(t, first.RunID(), ordinal) {
			t.Fatalf("input %d = %v", ordinal, got)
		}
		submitBatch(t, s, b)
	}
	beforeAppend.Wait()
	appendInput(2)
	close(start)
	afterFirstAppend.Wait()
	for ordinal := uint64(3); ordinal <= 64; ordinal++ {
		appendInput(ordinal)
	}
	b := beginBatch(t, s)
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	secondRun := batchRun(t, b)
	secondInput := batchInput(t, b, secondRun, []byte{0xff, 0, 'x'})
	submitBatch(t, s, b)
	readers.Wait()
	latest := s.Snapshot()
	if latest.RecordCount() != pinned.RecordCount()+66 || latest.ActiveRun() != secondRun || !userInputsMatch(latest, first.RunID(), 64) || !userInputsMatch(latest, secondRun, 1) || secondInput != allocatedInput(t, secondRun, 1) {
		t.Fatal("same-owner appends did not publish concurrent fixture")
	}
}
