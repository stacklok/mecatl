package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunInterrupted(t *testing.T) {
	v := session.RunInterrupted{ID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := session.NewRunInterruptedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[sessionformat.RunInterrupted](r.Format().Payload, testLimits())
	if err != nil || got.ID != v.ID.Ordinal || !v.Equal(v.Clone()) {
		t.Fatalf("interruption roundtrip: %v", err)
	}
	if (session.RunInterrupted{}).Validate() == nil {
		t.Fatal("zero interruption validated")
	}
	r, err = session.NewRunInterruptedRecord(session.RunInterrupted{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("zero interruption record: %v", err)
	}
}
