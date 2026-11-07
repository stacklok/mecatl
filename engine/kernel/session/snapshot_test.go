package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestSnapshotZeroKindsAndIndexBounds(t *testing.T) {
	var zero session.Snapshot
	if zero.HistoryID() != (historyid.HistoryID{}) || zero.Head() != (history.CommitRef{}) || zero.Created() || zero.ActiveRun() != (session.RunID{}) || zero.RunCount() != 0 || zero.RecordCount() != 0 {
		t.Fatal("zero snapshot")
	}
	var run session.RunView
	var input session.UserInputView
	var record session.RecordView
	if run.ID() != (session.RunID{}) || run.RecordCount() != 0 || input.ID() != (session.UserInputID{}) || input.PartCount() != 0 || record.Kind() != "" || record.KindVersion() != 0 {
		t.Fatal("zero child handle")
	}
	h := newSessionHistory(t)
	s := boundSession(t, h, sessionPolicy())
	s = loadRecords(t, h, s, createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1), startRecord(t, 2))
	v := s.Snapshot()
	for i := 0; i < v.RecordCount(); i++ {
		r := v.RecordAt(i)
		created, c := r.Created()
		started, st := r.RunStarted()
		user, u := r.UserInput()
		interrupted, in := r.RunEnded()
		if c != (i == 0) || st != (i == 1 || i == 4) || u != (i == 2) || in != (i == 3) {
			t.Fatal("wrong-kind access indistinguishable")
		}
		if c && created.HistoryID != v.HistoryID() {
			t.Fatal("created scope")
		}
		if st {
			position := 0
			if i == 4 {
				position = 1
			}
			if started.ID != v.RunAt(position).ID() {
				t.Fatal("start scope")
			}
		}
		if u && user.ID() != lookupInput(t, v, v.RunAt(0).ID(), 1).ID() {
			t.Fatal("typed input identity")
		}
		if in && interrupted.RunID() != v.RunAt(0).ID() {
			t.Fatal("interruption scope")
		}
		version := int64(1)
		if r.KindVersion() != version {
			t.Fatal("version")
		}
		want := r.Format()
		dto := r.Format()
		clear(dto.Payload)
		dto.Kind = "changed"
		dto.KindVersion = 9
		if !v.RecordAt(i).Format().Equal(want) || v.RecordAt(i).KindVersion() != version {
			t.Fatal("raw export aliases")
		}
	}
	if _, ok := record.Created(); ok {
		t.Fatal("zero created")
	}
	if _, ok := record.UserInput(); ok {
		t.Fatal("zero input")
	}
	if _, ok := record.RunStarted(); ok {
		t.Fatal("zero start")
	}
	if _, ok := record.RunEnded(); ok {
		t.Fatal("zero interruption")
	}
	if !userInputsMatch(v, v.RunAt(1).ID(), 0) || v.RunAt(1).RecordCount() != 1 {
		t.Fatal("zero-input active run")
	}
	emptyRun := v.RunAt(1)
	b := beginBatch(t, s)
	added := batchInput(t, b, emptyRun.ID(), []byte{0xff, 0, 'x'})
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	successor := batchRun(t, b)
	submitBatch(t, s, b)
	latest := s.Snapshot()
	if added != allocatedInput(t, emptyRun.ID(), 1) || latest.RecordCount() != v.RecordCount()+3 || latest.ActiveRun() != successor || !userInputsMatch(latest, emptyRun.ID(), 1) || latest.RunAt(latest.RunCount()-1).ID() != successor {
		t.Fatal("same-owner append did not publish input/closeout/successor")
	}
	if !userInputsMatch(v, emptyRun.ID(), 0) || emptyRun.RecordCount() != 1 || !userInputsMatch(v, v.RunAt(1).ID(), 0) || v.ActiveRun() != emptyRun.ID() {
		t.Fatal("old zero-input active handle changed after later input/closeout/successor")
	}
	for name, access := range map[string]func(){
		"zero run": func() { zero.RunAt(0) }, "zero record": func() { zero.RecordAt(0) },
		"negative run": func() { v.RunAt(-1) }, "run end": func() { v.RunAt(v.RunCount()) },
		"negative record": func() { v.RecordAt(-1) }, "record end": func() { v.RecordAt(v.RecordCount()) },
		"negative run record": func() { v.RunAt(0).RecordAt(-1) }, "run record end": func() { v.RunAt(0).RecordAt(v.RunAt(0).RecordCount()) },
		"zero run record": func() { run.RecordAt(0) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("out-of-bounds access did not panic")
				}
			}()
			access()
		})
	}
}
