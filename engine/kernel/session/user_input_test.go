package session_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserInput(t *testing.T) {
	id := session.InputID{RunID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}, Ordinal: 1}
	for _, content := range [][]byte{nil, {}, {0xff, 0, 0x9f}, []byte(`{ "not": json }`), []byte(" e\u0301\r\n")} {
		v := session.UserInput{ID: id, Content: bytes.Clone(content)}
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		cloned := v.Clone()
		if !reflect.DeepEqual(cloned, v) {
			t.Fatal("clone did not preserve nil/empty and exact bytes")
		}
		r, err := session.NewUserInputRecord(v, testLimits())
		if err != nil {
			t.Fatal(err)
		}
		wire := r.Format().Payload
		decoded, err := codec.Decode[sessionformat.UserInput](wire, testLimits())
		if err != nil || decoded.ID != id.Ordinal || !bytes.Equal(decoded.Content, content) {
			t.Fatalf("input roundtrip: %v", err)
		}
		clear(v.Content)
		clear(wire)
		if !bytes.Equal(cloned.Content, content) || !bytes.Equal(decoded.Content, content) {
			t.Fatal("clone or decoder retained borrowed storage")
		}
		got, err := codec.Decode[sessionformat.UserInput](r.Format().Payload, testLimits())
		if err != nil || !got.Equal(sessionformat.UserInput{RunID: 1, ID: cloned.ID.Ordinal, Content: cloned.Content}) {
			t.Fatalf("constructor retained input storage: %v", err)
		}
	}
	if !(session.UserInput{ID: id}).Equal(session.UserInput{ID: id, Content: []byte{}}) {
		t.Fatal("nil and empty input must be equivalent")
	}
	if (session.UserInput{}).Validate() == nil {
		t.Fatal("zero input validated")
	}
	r, err := session.NewUserInputRecord(session.UserInput{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("zero input record: %v", err)
	}
}

func TestUserInputLimitsAndTransactionalRead(t *testing.T) {
	v := session.UserInput{ID: session.InputID{RunID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}, Ordinal: 1}, Content: []byte{1, 2}}
	r, err := session.NewUserInputRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wire := r.Format().Payload
	limited := testLimits()
	limited.MaxByteStringBytes = 1
	if err := v.Validate(); err != nil {
		t.Fatal("local validation must not apply codec limits")
	}
	if r, err := session.NewUserInputRecord(v, limited); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("content limit: %v", err)
	}
	for _, test := range []struct {
		wire     []byte
		limits   codec.Limits
		category error
	}{
		{wire, limited, codec.ErrLimitExceeded},
		{wire[:len(wire)-1], testLimits(), codec.ErrInvalidData},
	} {
		before := sessionformat.UserInput{RunID: 1, ID: v.ID.Ordinal, Content: bytes.Clone(v.Content)}
		receiver := before.Clone()
		reader, err := codec.NewReader(test.wire, test.limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, test.category) || !reflect.DeepEqual(receiver, before) {
			t.Fatalf("failed read mutated receiver: %v", err)
		}
		got, err := codec.Decode[sessionformat.UserInput](test.wire, test.limits)
		if !errors.Is(err, test.category) || !reflect.DeepEqual(got, sessionformat.UserInput{}) {
			t.Fatalf("decode failure returned partial input: %v", err)
		}
	}
}
