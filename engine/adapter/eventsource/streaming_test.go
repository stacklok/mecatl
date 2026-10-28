package eventsource_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestMalformedLegacyPrefixCannotBeRepairedByCurrentActivity(t *testing.T) {
	call := decode(t, oldCall)
	for name, prefix := range map[string][]session.Event{
		"duplicate":      {call, call},
		"ask":            {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"old","Tool":"Read","Call":"wrong"}}`)},
		"authorization":  {call, decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"old","Call":"provider-secret","Status":"pending"}}`)},
		"unanswered":     {call},
		"proven pending": {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"old","Tool":"Read","Call":"provider-secret"}}`)},
	} {
		for _, boundary := range []session.EventType{session.EvUserPrompt, session.EvTurnStart, session.EvModelRetry} {
			t.Run(name+"/"+string(boundary), func(t *testing.T) {
				start := session.Event{Type: boundary}
				if boundary == session.EvUserPrompt {
					start.UserPrompt = &session.UserPromptPayload{Text: "new work", Provenance: session.UserPromptProvenancePrincipal}
				}
				if boundary == session.EvModelRetry {
					start.ModelRetry = &session.ModelRetryPayload{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}
				}
				current := []session.Event{
					start,
					{Type: session.EvTurnStart},
					{Type: session.EvMessageDelta, Text: "valid current answer"},
					{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
				}
				if _, err := fold(current); err != nil {
					t.Fatal("invalid control fixture", err)
				}
				events := append(append([]session.Event{}, prefix...), current...)
				requireFoldError(t, events, eventsource.ErrReconstruct)
				if _, err := eventsource.Project(events, session.LegacyIncarnationID("test", 0, nil)); !errors.Is(err, eventsource.ErrReconstruct) {
					t.Fatal("current suffix repaired legacy source", err)
				}
			})
		}
	}
}

func TestMixedSourceRetainsCanonicalAuthorizationStrictness(t *testing.T) {
	prefix := []session.Event{decode(t, oldCall), decode(t, oldResult)}
	auth := authorizationCall("current-auth", "current-call")
	requireFoldError(t, append(prefix, auth[:3]...), eventsource.ErrPrivateStateRequired)
	requireFoldError(t, append(prefix, auth[4]), eventsource.ErrReconstruct)
	if _, err := fold(append(prefix, auth...)); err != nil {
		t.Fatal("complete current authorization rejected", err)
	}
	prior := authorizationCall("reused-auth", "prior-call")
	mixed := append(prior, prefix...)
	requireFoldError(t, append(mixed, authorizationCall("reused-auth", "later-call")...), eventsource.ErrReconstruct)
}

func TestMixedSourceCannotAnswerLegacyCallWithCurrentResult(t *testing.T) {
	call := decode(t, oldCall)
	bound := project(t, []session.Event{call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret"}}`)}).Events[0].ToolCall.ID
	for _, id := range []session.ToolCallID{"provider-secret", bound} {
		result := session.NewToolResult(id, "current result")
		events := []session.Event{call, {Type: session.EvToolResult, ToolResult: &result}}
		requireFoldError(t, events, eventsource.ErrReconstruct)
		if _, err := eventsource.Project(events, session.LegacyIncarnationID("test", 0, nil)); !errors.Is(err, eventsource.ErrReconstruct) {
			t.Fatal("canonical result answered legacy source", err)
		}
	}
}

func TestMixedSourceCannotReplaceLegacyApproval(t *testing.T) {
	prefix := []session.Event{decode(t, oldCall), decode(t, `{"Type":"permission.ask","Ask":{"AskID":"old","Tool":"Read","Call":"provider-secret","Args":{"path":"x"}}}`)}
	proven := project(t, prefix)
	if _, err := fold(prefix); err != nil {
		t.Fatal("valid legacy approval rejected", err)
	}
	current := session.Event{Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "current", Tool: "Read", Call: proven.Events[0].ToolCall.ID, Args: proven.Events[0].ToolCall.Args}}
	events := append(prefix, current)
	requireFoldError(t, events, eventsource.ErrReconstruct)
	if _, err := eventsource.Project(events, session.LegacyIncarnationID("test", 0, nil)); !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatal("current ask replaced legacy approval", err)
	}
}

func TestFoldStopsIteratorAtDecisiveReconstructionError(t *testing.T) {
	call := decode(t, oldCall)
	for name, events := range map[string][]session.Event{
		"legacy duplicate":                {call, call},
		"legacy orphan":                   {decode(t, oldResult)},
		"legacy ask":                      {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"old","Tool":"Read","Call":"wrong"}}`)},
		"legacy authorization":            {call, decode(t, `{"Type":"authorization.required"}`)},
		"current malformed authorization": {{Type: session.EvAuthorizationRequired}},
		"current orphan resolution":       {authorizationResolved("orphan", "call", session.AuthorizationGranted)},
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
