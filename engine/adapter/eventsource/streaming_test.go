package eventsource_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFoldStopsIteratorAtDecisiveReconstructionError(t *testing.T) {
	for name, events := range map[string][]session.Event{
		"current malformed authorization":  {{Type: session.EvAuthorizationRequired}},
		"current orphan resolution":        {authorizationResolved("orphan", "call", session.AuthorizationGranted)},
		"current result before resolution": {authorizationRequired("auth", "call"), authorizationResolved("auth", "call", session.AuthorizationGranted)},
	} {
		t.Run(name, func(t *testing.T) {
			sentinel := errors.New("later iterator failure")
			read := 0
			s, err := eventsource.Fold(meta(), func(yield func(session.Event, error) bool) {
				for _, e := range events {
					read++
					if !yield(e, nil) {
						return
					}
				}
				read++
				yield(session.Event{}, sentinel)
			})
			if s != nil || !errors.Is(err, eventsource.ErrReconstruct) || errors.Is(err, sentinel) || read != len(events) {
				t.Fatalf("session=%v error=%v read=%d, want decisive rejection after %d records", s, err, read, len(events))
			}
		})
	}
}

func TestFoldCurrentIteratorContinuesUntilCompletionOrStreamFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		sentinel := errors.New("actual iterator failure")
		read := 0
		s, err := eventsource.Fold(meta(), func(yield func(session.Event, error) bool) {
			for _, e := range []session.Event{{Type: session.EvTurnStart}, {Type: session.EvMessageDelta, Text: "answer"}, {Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}}} {
				read++
				if !yield(e, nil) {
					return
				}
			}
			if fail {
				read++
				yield(session.Event{}, sentinel)
			}
		})
		if fail {
			if s != nil || !errors.Is(err, eventsource.ErrStream) || !errors.Is(err, sentinel) || read != 4 {
				t.Fatalf("stream failure: session=%v error=%v read=%d", s, err, read)
			}
		} else if err != nil || s == nil || s.State != session.StateCompleted || read != 3 || s.Conversation.Messages[0].Text != "answer" {
			t.Fatalf("completion: session=%v error=%v read=%d", s, err, read)
		}
	}
}

func TestFoldDoesNotSilentlyIgnoreCodecSkipErrors(t *testing.T) {
	read := 0
	s, err := eventsource.Fold(meta(), func(yield func(session.Event, error) bool) {
		read++
		if !yield(session.Event{}, eventcodec.ErrSkipLegacyRecord) {
			return
		}
		read++
		yield(session.Event{Type: session.EvMessageDelta, Text: "later"}, nil)
	})
	if s != nil || !errors.Is(err, eventsource.ErrStream) || !errors.Is(err, eventcodec.ErrSkipLegacyRecord) || read != 1 {
		t.Fatalf("session=%v error=%v read=%d", s, err, read)
	}
}
