package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunStarted(t *testing.T) {
	v := session.RunStarted{ID: allocatedRun(t, testHistoryID(t), 1)}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := v.Clone()
	r, err := session.NewRunStartedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wire := r.Format().Payload
	got, err := codec.Decode[sessionformat.RunStarted](wire, testLimits())
	if err != nil || !got.Equal(sessionformat.RunStarted{ID: 1}) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.ID = session.RunID{}
	clear(wire)
	if cloned.ID.Validate() != nil || got.ID != 1 {
		t.Fatal("value or export aliases")
	}
	got, err = codec.Decode[sessionformat.RunStarted](r.Format().Payload, testLimits())
	if err != nil || got.ID != 1 {
		t.Fatalf("record export aliases: %v", err)
	}
}

func TestRunStartedInvalid(t *testing.T) {
	for _, id := range []session.RunID{{}} {
		v := session.RunStarted{ID: id}
		if v.Validate() == nil {
			t.Fatal("invalid run validated")
		}
		if r, err := session.NewRunStartedRecord(v, testLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
			t.Fatalf("invalid record: %v", err)
		}
	}
	limited := testLimits()
	limited.MaxBytes = 1
	if r, err := session.NewRunStartedRecord(session.RunStarted{ID: allocatedRun(t, testHistoryID(t), 1)}, limited); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("payload limit: %v", err)
	}
}
