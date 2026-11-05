package session_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestSnapshotZeroKindsAndIndexBounds(t *testing.T) {
	var zero session.Snapshot
	if zero.HistoryID() != (historyid.HistoryID{}) || zero.Head() != (history.CommitRef{}) || zero.Created() || zero.ActiveRun() != (session.RunID{}) || zero.RunCount() != 0 || zero.RecordCount() != 0 {
		t.Fatal("zero snapshot")
	}
	var run session.RunView
	var input session.InputView
	var record session.RecordView
	if run.ID() != (session.RunID{}) || run.RecordCount() != 0 || run.InputCount() != 0 || input.ID() != (session.InputID{}) || input.Content() != nil || !reflect.DeepEqual(input.Format(), sessionformat.UserInput{}) || record.Kind() != "" || record.KindVersion() != 0 {
		t.Fatal("zero child handle")
	}
	s := newSession(t, sessionPolicy())
	applyRecords(t, s, createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1), startRecord(t, 2))
	v := s.Snapshot()
	for i := 0; i < v.RecordCount(); i++ {
		r := v.RecordAt(i)
		created, c := r.Created()
		started, st := r.RunStarted()
		user, u := r.UserInput()
		interrupted, in := r.RunInterrupted()
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
		if u && user.ID() != v.RunAt(0).InputAt(0).ID() {
			t.Fatal("typed input identity")
		}
		if in && interrupted.ID != v.RunAt(0).ID() {
			t.Fatal("interruption scope")
		}
		if r.KindVersion() != 1 {
			t.Fatal("version")
		}
		want := r.Format()
		dto := r.Format()
		clear(dto.Payload)
		dto.Kind = "changed"
		dto.KindVersion = 9
		if !v.RecordAt(i).Format().Equal(want) || v.RecordAt(i).KindVersion() != 1 {
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
	if _, ok := record.RunInterrupted(); ok {
		t.Fatal("zero interruption")
	}
	if v.RunAt(1).InputCount() != 0 || v.RunAt(1).RecordCount() != 1 {
		t.Fatal("zero-input active run")
	}
	emptyRun := v.RunAt(1)
	applyRecords(t, s, runInputRecord(t, 2, 1), closeoutRecord(t, 2), startRecord(t, 3))
	if emptyRun.InputCount() != 0 || emptyRun.RecordCount() != 1 || v.RunAt(1).InputCount() != 0 || v.ActiveRun() != emptyRun.ID() {
		t.Fatal("old zero-input active handle changed after later input/closeout/successor")
	}
	for name, access := range map[string]func(){
		"zero run": func() { zero.RunAt(0) }, "zero record": func() { zero.RecordAt(0) },
		"negative run": func() { v.RunAt(-1) }, "run end": func() { v.RunAt(v.RunCount()) },
		"negative record": func() { v.RecordAt(-1) }, "record end": func() { v.RecordAt(v.RecordCount()) },
		"negative run record": func() { v.RunAt(0).RecordAt(-1) }, "run record end": func() { v.RunAt(0).RecordAt(v.RunAt(0).RecordCount()) },
		"negative input": func() { v.RunAt(0).InputAt(-1) }, "input end": func() { v.RunAt(0).InputAt(v.RunAt(0).InputCount()) },
		"zero-input run": func() { v.RunAt(1).InputAt(0) }, "zero run record": func() { run.RecordAt(0) }, "zero run input": func() { run.InputAt(0) },
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
