package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunEnded(t *testing.T) {
	v := RunEnded{RunID: allocatedRun(t, testHistoryID(t), 1), Reason: RunEndedInterrupted}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := newRunEndedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[sessionformat.RunEnded](r.format().Payload, testLimits())
	if err != nil || got.RunID != 1 || !v.Equal(v.Clone()) {
		t.Fatalf("interruption roundtrip: %v", err)
	}
	if (RunEnded{}).Validate() == nil {
		t.Fatal("zero interruption validated")
	}
	r, err = newRunEndedRecord(RunEnded{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, proposedRecord{}) {
		t.Fatalf("zero interruption record: %v", err)
	}
}
