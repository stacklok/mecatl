package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestCreated(t *testing.T) {
	v := Created{HistoryID: testHistoryID(t)}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := newCreatedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = codec.Decode[sessionformat.Created](r.format().Payload, testLimits())
	if err != nil || !v.Clone().Equal(v) {
		t.Fatalf("creation roundtrip: %v", err)
	}
	if (Created{}).Validate() == nil {
		t.Fatal("zero creation validated")
	}
	r, err = newCreatedRecord(Created{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, proposedRecord{}) {
		t.Fatalf("zero creation record: %v", err)
	}
}
