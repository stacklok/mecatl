package eventsource_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func legacyContinuationEvents(t *testing.T, kind string) []session.Event {
	t.Helper()
	raws := []string{
		`{"Type":"tool.call","ToolCall":{"ID":"completed","Name":"Read","Args":{}}}`,
		`{"Type":"tool.result","ToolResult":{"CallID":"completed","Content":"completed before upgrade","Parts":[{"Kind":"image","Data":"AQI="}]}}`,
		`{"Type":"result","Result":{"Stop":"end_turn","Usage":{"InputTokens":3,"OutputTokens":1}}}`,
		`{"Type":"turn.start"}`,
		`{"Type":"tool.call","ToolCall":{"ID":"proven","Name":"Read","Args":{}}}`,
	}
	if kind == "auth" {
		raws = append(raws, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"obsolete-auth","Call":"proven","Status":"pending"}}`)
	} else {
		raws = append(raws,
			`{"Type":"tool.call","ToolCall":{"ID":"duplicate","Name":"Read","Args":{}}}`,
			`{"Type":"tool.call","ToolCall":{"ID":"duplicate","Name":"Read","Args":{}}}`,
			`{"Type":"permission.ask","Ask":{"AskID":"obsolete-ask","Call":"duplicate","Tool":"Read"}}`)
	}
	events := make([]session.Event, len(raws))
	for i, raw := range raws {
		event, err := eventcodec.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		events[i] = event.WithLegacySource("durable", fmt.Sprintf("offset:%d", i))
	}
	return events
}

func currentRunEvents(t *testing.T, events []session.Event) []session.Event {
	t.Helper()
	out := make([]session.Event, len(events))
	for i, event := range events {
		event.RunID, event.Seq = "newly-admitted", int64(i)
		data, err := eventcodec.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		out[i], err = eventcodec.UnmarshalFormat(data, eventcodec.Format)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestFoldAbandonmentEndsBeforeNewCanonicalRun(t *testing.T) {
	call := toolCall("current-call", "Read", `{"path":"current"}`)
	failure := session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopError, Error: "current failure", Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible, Usage: session.Usage{InputTokens: 7, OutputTokens: 2}}}
	retry := session.Event{Type: session.EvModelRetry, ModelRetry: &session.ModelRetryPayload{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}}
	for _, kind := range []string{"auth", "ambiguous-ask"} {
		for _, outcome := range []string{"awaiting", "completed", "stopped", "failed", "retry-pending", "retry-running", "retry-completed"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				old := legacyContinuationEvents(t, kind)
				initial, err := eventsource.Fold(meta(), seq(old))
				if err != nil || initial.State != session.StateIdle {
					t.Fatal("initial migration", err)
				}
				current := []session.Event{
					{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "separately admitted work", Provenance: session.UserPromptProvenancePrincipal}},
					{Type: session.EvTurnStart},
					{Type: session.EvMessageDelta, Text: "current answer"},
				}
				switch outcome {
				case "awaiting":
					current = append(current, session.Event{Type: session.EvToolCall, ToolCall: &call}, session.Event{Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "current-ask", Tool: call.Name, Call: call.ID, Args: call.Args}})
				case "completed":
					current = append(current, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
				case "stopped":
					current = append(current, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopMaxTurns}})
				default:
					current = append(current, failure)
					if outcome != "failed" {
						current = append(current, retry)
					}
					if outcome == "retry-running" || outcome == "retry-completed" {
						current = append(current, session.Event{Type: session.EvTurnStart}, session.Event{Type: session.EvMessageDelta, Text: "retry answer"})
					}
					if outcome == "retry-completed" {
						current = append(current, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
					}
				}
				current = currentRunEvents(t, current)
				expected, err := eventsource.Fold(meta(), seq(current))
				if err != nil {
					t.Fatal(err)
				}
				usage := initial.UsageFor(session.UsageKindMain).Add(expected.UsageFor(session.UsageKindMain))
				expected.RestoreTokenUsage(map[session.UsageKind]session.TokenUsage{
					session.UsageKindMain: {Total: usage, Models: map[string]session.Usage{"unknown": usage}},
				})
				prefix, err := sessnap.Of(initial)
				if err != nil {
					t.Fatal(err)
				}
				want, err := sessnap.Of(expected)
				if err != nil {
					t.Fatal(err)
				}
				want.Messages = append(prefix.Messages, want.Messages...)
				expected, err = want.Restore()
				if err != nil {
					t.Fatal(err)
				}
				events := append(old, current...)
				projection, err := eventcodec.Project(events, initial.Incarnation())
				if err != nil {
					t.Fatal(err)
				}
				if len(projection.Events) != len(events) {
					t.Fatal("synthetic durable records")
				}
				for i, event := range projection.Events {
					if event.RunID != events[i].RunID || event.Seq != events[i].Seq {
						t.Fatal("source order/coordinates changed")
					}
					if p := events[i].LegacyIdentity(); p != nil {
						q := event.LegacyIdentity()
						if p.Coordinate != q.Coordinate || p.Ordinal != q.Ordinal || p.Namespace != q.Namespace || !bytes.Equal(p.Payload, q.Payload) {
							t.Fatal("legacy source provenance changed")
						}
					}
				}
				for range 2 {
					folded, err := eventsource.Fold(meta(), seq(events))
					if err != nil {
						t.Fatal(err)
					}
					assertRetryParity(t, expected, folded)
					a, aok := expected.PendingAsk()
					b, bok := folded.PendingAsk()
					if aok != bok || !reflect.DeepEqual(a, b) {
						t.Fatal("current approval changed")
					}
					notices := 0
					for i, message := range folded.Conversation.Messages {
						if strings.Contains(message.Text, "abandoned during upgrade") {
							notices++
							if i >= len(prefix.Messages) {
								t.Fatal("normalization moved into current run")
							}
						}
					}
					if notices != 1 {
						t.Fatalf("notices = %d", notices)
					}
					for i, event := range projection.Events {
						data, err := eventcodec.Marshal(event)
						if err != nil {
							t.Fatal(err)
						}
						events[i], err = eventcodec.Unmarshal(data)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestFoldAbandonmentBeforeCanonicalTurn(t *testing.T) {
	old := legacyContinuationEvents(t, "auth")
	current := currentRunEvents(t, []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvMessageDelta, Text: "new answer without retained prompt event"},
		{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	})
	folded, err := eventsource.Fold(meta(), seq(append(old, current...)))
	if err != nil {
		t.Fatal(err)
	}
	stop, _ := folded.RecordedStopReason()
	if folded.State != session.StateCompleted || stop != session.StopEndTurn || folded.Counters.Turns != 1 {
		t.Fatal("new canonical turn inherited legacy lifecycle")
	}
	messages := folded.Conversation.Messages
	if messages[len(messages)-1].Text != "new answer without retained prompt event" || !strings.Contains(messages[len(messages)-2].Text, "abandoned during upgrade") {
		t.Fatal("migration did not precede current turn")
	}
}

func TestFoldMigrationBoundaryKeepsCanonicalAuthorizationValidation(t *testing.T) {
	old := legacyContinuationEvents(t, "auth")
	prompt := session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "new work", Provenance: session.UserPromptProvenancePrincipal}}
	auth := authorizationCall("current-auth", "current-call")
	current := append([]session.Event{prompt}, auth[:3]...)
	requireFoldError(t, append(old, currentRunEvents(t, current)...), eventsource.ErrPrivateStateRequired)
	current = append(current, auth[4]) // Resolved without its result must still fail.
	requireFoldError(t, append(old, currentRunEvents(t, current)...), eventsource.ErrReconstruct)

	// A migration boundary cannot forget uniqueness evidence from completed /2 auth.
	prior := currentRunEvents(t, authorizationCall("reused-auth", "prior-current-call"))
	mixed := append(prior, old...)
	reused := append([]session.Event{prompt}, authorizationCall("reused-auth", "later-current-call")...)
	requireFoldError(t, append(mixed, currentRunEvents(t, reused)...), eventsource.ErrReconstruct)
}

func TestFoldMigrationBoundaryDoesNotPairAcrossSources(t *testing.T) {
	old := legacyContinuationEvents(t, "auth")
	prompt := session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "new work", Provenance: session.UserPromptProvenancePrincipal}}
	// Equality to the old provider key is not proof of a canonical occurrence.
	result := session.NewToolResult("proven", "current result")
	current := currentRunEvents(t, []session.Event{prompt, {Type: session.EvToolResult, ToolResult: &result}})
	requireFoldError(t, append(old, current...), eventsource.ErrReconstruct)

	// Nor can a late /1 result answer an occurrence abandoned before the new run.
	late, err := eventcodec.Unmarshal([]byte(`{"Type":"tool.result","ToolResult":{"CallID":"proven","Content":"late standalone result","Parts":[{"Kind":"image","Data":"AwQ="}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	late = late.WithLegacySource("durable", "late-result")
	events := append(old, currentRunEvents(t, []session.Event{prompt})...)
	events = append(events, late)
	p, err := eventcodec.Project(events, session.LegacyIncarnationID(meta().ID, meta().CreatedAt.UnixNano(), nil))
	if err != nil {
		t.Fatal(err)
	}
	last := p.Events[len(p.Events)-1]
	if last.Type != session.EvHistorical || last.ToolResult != nil || last.Historical == nil || !last.Historical.AttachmentUnavailable || len(last.Historical.Parts) != 1 {
		t.Fatal("late result gained authority or lost media")
	}
	folded, err := eventsource.Fold(meta(), seq(events))
	if err != nil {
		t.Fatal(err)
	}
	results, notices := 0, 0
	for _, message := range folded.Conversation.Messages {
		if message.ToolResult != nil {
			results++
		}
		if strings.Contains(message.Text, "abandoned during upgrade") {
			notices++
		}
	}
	if results != 2 || notices != 1 {
		t.Fatalf("results/notices = %d/%d", results, notices)
	}
}
