package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestCreated(t *testing.T) {
	v := session.Created{HistoryID: testHistoryID(t)}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := session.NewCreatedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = codec.Decode[sessionformat.Created](r.Format().Payload, testLimits())
	if err != nil || !v.Clone().Equal(v) {
		t.Fatalf("creation roundtrip: %v", err)
	}
	if (session.Created{}).Validate() == nil {
		t.Fatal("zero creation validated")
	}
	r, err = session.NewCreatedRecord(session.Created{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("zero creation record: %v", err)
	}
}
