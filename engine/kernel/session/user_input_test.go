package session_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserInput(t *testing.T) {
	id := allocatedInput(t, allocatedRun(t, testHistoryID(t), 1), 1)
	for _, content := range [][]byte{nil, {}, {0xff, 0, 0x9f}, []byte(`{ "not": json }`), []byte(" e\u0301\r\n")} {
		v := session.UserInput{RunID: id.RunID(), Parts: byteParts(bytes.Clone(content))}
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		cloned := v.Clone()
		if !reflect.DeepEqual(cloned, v) {
			t.Fatal("clone changed storage shape or bytes")
		}
		s, b := allocationBatch(t, id.RunID())
		gotID, err := b.AddUserInput(v)
		if err != nil || gotID != id {
			t.Fatalf("author input: %v, %v", gotID, err)
		}
		submitBatch(t, s, b)
		r := s.Snapshot().RecordAt(2)
		wire := r.Format().Payload
		decoded, err := codec.Decode[sessionformat.UserInput](wire, testLimits())
		if err != nil || decoded.ID != 1 || !bytes.Equal(dtoInputBytes(decoded), content) {
			t.Fatalf("round trip: %v", err)
		}
		if len(v.Parts) > 0 {
			clear(v.Parts[0].Media.InlineData)
		}
		clear(wire)
		got, err := codec.Decode[sessionformat.UserInput](r.Format().Payload, testLimits())
		if err != nil || !got.Equal(sessionformat.UserInput{RunID: 1, ID: 1, Parts: formatByteParts(content)}) || !cloned.Equal(session.UserInput{RunID: id.RunID(), Parts: byteParts(content)}) {
			t.Fatalf("borrowed storage retained: %v", err)
		}
	}
	if !(session.UserInput{RunID: id.RunID()}).Equal(session.UserInput{RunID: id.RunID(), Parts: []session.ContentPart{}}) {
		t.Fatal("nil and empty differ")
	}
	if (session.UserInput{RunID: id.RunID()}).Equal(session.UserInput{RunID: id.RunID(), Parts: []session.ContentPart{{Text: new("")}}}) {
		t.Fatal("empty text part lost")
	}
	if (session.UserInput{}).Validate() == nil {
		t.Fatal("zero input validated")
	}
	for _, run := range []session.RunID{allocatedRun(t, id.RunID().HistoryID(), 2), allocatedRun(t, historyid.NewHistoryID(), 1), {}} {
		v := session.UserInput{RunID: run, Parts: []session.ContentPart{{Text: new("exact")}}}
		clone := v.Clone()
		if !v.Equal(clone) || (session.UserInput{RunID: id.RunID(), Parts: clone.Parts}).Equal(v) {
			t.Fatal("cloning/equality discarded authoring run scope")
		}
		*v.Parts[0].Text = "changed"
		if *clone.Parts[0].Text != "exact" {
			t.Fatal("clone borrowed text with unaccepted or zero run scope")
		}
	}
	_, b := allocationBatch(t, id.RunID())
	defer b.Discard()
	if got, err := b.AddUserInput(session.UserInput{}); !errors.Is(err, session.ErrTransition) || got != (session.UserInputID{}) {
		t.Fatalf("zero input: %v", err)
	}
	invalid := session.UserInput{RunID: id.RunID(), Parts: []session.ContentPart{{Text: new(string([]byte{0xff}))}}}
	if invalid.Validate() == nil {
		t.Fatal("non-UTF-8 text validated")
	}
	if _, err := b.AddUserInput(invalid); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("invalid text: %v", err)
	}
}

func TestUserInputLimitsAndTransactionalRead(t *testing.T) {
	v := session.UserInput{RunID: allocatedRun(t, testHistoryID(t), 1), Parts: byteParts([]byte{1, 2})}
	wire, err := codec.Encode(sessionformat.UserInput{RunID: 1, ID: 1, Parts: formatByteParts([]byte{1, 2})}, testLimits())
	mustBatch(t, err)
	p := sessionPolicy()
	p.PayloadLimits.MaxByteStringBytes = 1
	s := newSession(t, p)
	b := beginBatch(t, s)
	defer b.Discard()
	mustBatch(t, b.Create())
	if run := batchRun(t, b); run != v.RunID {
		t.Fatal("fixture run")
	}
	limited := testLimits()
	limited.MaxByteStringBytes = 1
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if id, err := b.AddUserInput(v); !errors.Is(err, codec.ErrLimitExceeded) || id != (session.UserInputID{}) {
		t.Fatalf("limit: %v", err)
	}
	for _, test := range []struct {
		wire     []byte
		limits   codec.Limits
		category error
	}{{wire, limited, codec.ErrLimitExceeded}, {wire[:len(wire)-1], testLimits(), codec.ErrInvalidData}} {
		before := sessionformat.UserInput{RunID: 1, ID: 1, Parts: formatByteParts([]byte{1, 2})}
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
