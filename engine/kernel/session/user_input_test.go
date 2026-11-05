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
		v := session.UserInput{ID: id, Messages: byteMessages(bytes.Clone(content))}
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		cloned := v.Clone()
		if !reflect.DeepEqual(cloned, v) {
			t.Fatal("clone changed storage shape or bytes")
		}
		r, err := session.NewUserInputRecord(v, testLimits())
		if err != nil {
			t.Fatal(err)
		}
		wire := r.Format().Payload
		decoded, err := codec.Decode[sessionformat.UserInput](wire, testLimits())
		if err != nil || decoded.ID != id.Ordinal || !bytes.Equal(dtoInputBytes(decoded), content) {
			t.Fatalf("round trip: %v", err)
		}
		if len(v.Messages) > 0 {
			clear(v.Messages[0].Content[0].Media.Source.Inline.Data)
		}
		clear(wire)
		got, err := codec.Decode[sessionformat.UserInput](r.Format().Payload, testLimits())
		if err != nil || !got.Equal(sessionformat.UserInput{RunID: 1, ID: 1, Messages: formatByteMessages(content)}) || !cloned.Equal(session.UserInput{ID: id, Messages: byteMessages(content)}) {
			t.Fatalf("borrowed storage retained: %v", err)
		}
	}
	if !(session.UserInput{ID: id}).Equal(session.UserInput{ID: id, Messages: []session.AuthoredMessage{}}) {
		t.Fatal("nil and empty differ")
	}
	if (session.UserInput{ID: id}).Equal(session.UserInput{ID: id, Messages: []session.AuthoredMessage{{}}}) {
		t.Fatal("empty message boundary lost")
	}
	if (session.UserInput{}).Validate() == nil {
		t.Fatal("zero input validated")
	}
	if r, err := session.NewUserInputRecord(session.UserInput{}, testLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("zero input: %v", err)
	}
	invalid := session.UserInput{ID: id, Messages: []session.AuthoredMessage{{Content: session.Content{{Kind: session.ContentText, Text: string([]byte{0xff})}}}}}
	if invalid.Validate() == nil {
		t.Fatal("non-UTF-8 text validated")
	}
	if _, err := session.NewUserInputRecord(invalid, testLimits()); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("invalid text: %v", err)
	}
}

func TestUserInputLimitsAndTransactionalRead(t *testing.T) {
	v := session.UserInput{ID: session.InputID{RunID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}, Ordinal: 1}, Messages: byteMessages([]byte{1, 2})}
	r, err := session.NewUserInputRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wire := r.Format().Payload
	limited := testLimits()
	limited.MaxByteStringBytes = 1
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if r, err := session.NewUserInputRecord(v, limited); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(r, session.Record{}) {
		t.Fatalf("limit: %v", err)
	}
	for _, test := range []struct {
		wire     []byte
		limits   codec.Limits
		category error
	}{{wire, limited, codec.ErrLimitExceeded}, {wire[:len(wire)-1], testLimits(), codec.ErrInvalidData}} {
		before := sessionformat.UserInput{RunID: 1, ID: 1, Messages: formatByteMessages([]byte{1, 2})}
		receiver := before.Clone()
		reader, err := codec.NewReader(test.wire, test.limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.ReadCBOR(&reader, 0, math.MaxInt); !errors.Is(err, test.category) || !reflect.DeepEqual(receiver, before) {
			t.Fatalf("transactional read: %v", err)
		}
		got, err := codec.Decode[sessionformat.UserInput](test.wire, test.limits)
		if !errors.Is(err, test.category) || !reflect.DeepEqual(got, sessionformat.UserInput{}) {
			t.Fatalf("partial decode: %v", err)
		}
	}
}
