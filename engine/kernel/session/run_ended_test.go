package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunEnded(t *testing.T) {
	v := session.RunEnded{RunID: allocatedRun(t, testHistoryID(t), 1), Reason: session.RunEndedInterrupted}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := session.NewRunEndedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[sessionformat.RunEnded](r.Format().Payload, testLimits())
	if err != nil || got.RunID != 1 || !v.Equal(v.Clone()) {
		t.Fatalf("interruption roundtrip: %v", err)
	}
	if (session.RunEnded{}).Validate() == nil {
		t.Fatal("zero interruption validated")
	}
	r, err = session.NewRunEndedRecord(session.RunEnded{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("zero interruption record: %v", err)
	}
}
