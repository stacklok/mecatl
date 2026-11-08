package session

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunStarted(t *testing.T) {
	v := RunStarted{ID: allocatedRun(t, testHistoryID(t), 1)}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := v.Clone()
	r, err := newRunStartedRecord(testHistoryID(t), v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wire := r.format().Payload
	got, err := codec.Decode[sessionformat.RunStarted](wire, testLimits())
	if err != nil || !got.Equal(sessionformat.RunStarted{ID: 1}) {
		t.Fatalf("roundtrip: %v", err)
	}
	v.ID = RunID{}
	clear(wire)
	if cloned.ID.Validate() != nil || got.ID != 1 {
		t.Fatal("value or export aliases")
	}
	got, err = codec.Decode[sessionformat.RunStarted](r.format().Payload, testLimits())
	if err != nil || got.ID != 1 {
		t.Fatalf("record export aliases: %v", err)
	}
}

func TestRunStartedInvalid(t *testing.T) {
	for _, id := range []RunID{{}} {
		v := RunStarted{ID: id}
		if v.Validate() == nil {
			t.Fatal("invalid run validated")
		}
		if r, err := newRunStartedRecord(testHistoryID(t), v, testLimits()); !errors.Is(err, codec.ErrInvalidData) || r != nil {
			t.Fatalf("invalid record: %v", err)
		}
	}
	limited := testLimits()
	limited.MaxBytes = 1
	if r, err := newRunStartedRecord(testHistoryID(t), RunStarted{ID: allocatedRun(t, testHistoryID(t), 1)}, limited); !errors.Is(err, codec.ErrLimitExceeded) || r != nil {
		t.Fatalf("payload limit: %v", err)
	}
}
