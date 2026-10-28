package eventsource_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestDurableOccurrenceSurvivesReloadAndPrefixPruning(t *testing.T) {
	es := []session.Event{
		decode(t, `{"Type":"user.prompt","Text":"unrelated"}`).WithLegacySource("durable", "offset:0"),
		decode(t, oldCall).WithLegacySource("durable", "offset:100"),
		decode(t, oldResult).WithLegacySource("durable", "offset:200"),
		decode(t, oldCall).WithLegacySource("durable", "offset:300"),
		decode(t, oldResult).WithLegacySource("durable", "offset:400"),
	}
	want := project(t, es)
	for _, source := range [][]session.Event{es, want.Events} {
		var reloaded []session.Event
		for _, e := range source {
			raw, err := eventcodec.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			reloaded = append(reloaded, decode(t, string(raw)))
		}
		for _, retained := range [][]session.Event{source[1:], reloaded[1:], source[3:], reloaded[3:]} {
			got := project(t, retained)
			start := len(es) - len(retained)
			for i := 0; i < len(retained); i += 2 {
				if got.Events[i].ToolCall.ID != want.Events[start+i].ToolCall.ID || got.Events[i+1].ToolResult.CallID != got.Events[i].ToolCall.ID {
					t.Fatal("durable occurrence changed after save/reload/prune")
				}
				if got.Events[i].LegacyIdentity().Ordinal != -1 {
					t.Fatal("retained index became a frozen evidence ordinal")
				}
			}
		}
	}
	if want.Events[1].ToolCall.ID == want.Events[3].ToolCall.ID {
		t.Fatal("completed provider-key reuse collapsed")
	}
	// Previously persisted bindings must not be renamed by a new derivation.
	call := session.NewToolCall("persisted-canonical-binding", "Read", nil)
	es[1].ToolCall = &call
	bound := project(t, es[1:3])
	if bound.Events[0].ToolCall.ID != call.ID || bound.Events[1].ToolResult.CallID != call.ID {
		t.Fatal("persisted binding was re-derived")
	}
}

func TestLegacyCallsPairIndependentlyAcrossSources(t *testing.T) {
	events := []session.Event{
		decode(t, oldCall).WithLegacySource("source-a", "call"),
		decode(t, oldCall).WithLegacySource("source-b", "call"),
		decode(t, oldResult).WithLegacySource("source-b", "result"),
		decode(t, oldResult).WithLegacySource("source-a", "result"),
	}
	p := project(t, events)
	if p.Events[0].ToolCall.ID == p.Events[1].ToolCall.ID || p.Events[2].ToolResult.CallID != p.Events[1].ToolCall.ID || p.Events[3].ToolResult.CallID != p.Events[0].ToolCall.ID {
		t.Fatal("provider-key equality crossed source boundaries")
	}
	if _, err := fold(p.Events); err != nil {
		t.Fatal(err)
	}
}

func TestMissingOccurrenceAnchorRejectedAcrossRestart(t *testing.T) {
	es := []session.Event{
		decode(t, `{"Type":"tool.call","ToolCall":{"ID":"old","Name":"Read"}}`),
		decode(t, `{"Type":"tool.result","ToolResult":{"CallID":"old","Content":"readable"}}`),
	}
	for range 2 {
		requireFoldError(t, es, eventsource.ErrReconstruct)
		if _, err := eventsource.Project(es, session.LegacyIncarnationID("test", 0, nil)); !errors.Is(err, eventsource.ErrReconstruct) {
			t.Fatal("unanchored source projected", err)
		}
		for i, e := range es {
			raw, err := eventcodec.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			es[i] = decode(t, string(raw))
		}
	}
}

func TestDurableCoordinateDoesNotRebaseFrozenEvidenceOrdinal(t *testing.T) {
	call := decode(t, oldCall).WithLegacySource("durable", "offset:100")
	identity := call.LegacyIdentity()
	identity.Ordinal = 47
	call = call.WithLegacyIdentity(*identity)
	p := project(t, []session.Event{call, decode(t, oldResult).WithLegacySource("durable", "offset:200")})
	if p.Events[0].LegacyIdentity().Ordinal != 47 {
		t.Fatal("original evidence ordinal was rebased")
	}
	identity.Ordinal = 99
	other := project(t, []session.Event{call.WithLegacyIdentity(*identity), decode(t, oldResult).WithLegacySource("durable", "offset:200")})
	if p.Events[0].ToolCall.ID != other.Events[0].ToolCall.ID {
		t.Fatal("evidence ordinal overrode the durable invocation coordinate")
	}
}

func TestLegacyInvocationDerivationUsesOnlyStableSourceFacts(t *testing.T) {
	inc := session.LegacyIncarnationID("test", 0, nil)
	ordinal := decode(t, `{"Type":"tool.call","ToolCall":{"ID":"old","Name":"Read"}}`)
	identity := ordinal.LegacyIdentity()
	identity.Ordinal = 47
	ordinal = ordinal.WithLegacyIdentity(*identity)
	persisted := decode(t, `{"Type":"tool.call","ToolCall":{"ID":"old","Name":"Read"}}`)
	binding := session.NewToolCall("persisted-without-coordinate", "Read", nil)
	persisted.ToolCall = &binding
	for name, tc := range map[string]struct {
		call session.Event
		ref  string
		want session.ToolCallID
	}{
		"original ordinal":  {ordinal, "old", session.HistoricalInvocationID(inc, eventcodec.LegacyNamespace, 47)},
		"run sequence":      {decode(t, oldCall), "provider-secret", session.ToolCallID("call_" + session.HistoricalRecordID(inc, eventcodec.LegacyNamespace, "run:fixture-run/seq:90"))},
		"persisted binding": {persisted, "old", binding.ID},
	} {
		t.Run(name, func(t *testing.T) {
			result := decode(t, `{"Type":"tool.result","ToolResult":{"CallID":"`+tc.ref+`","Content":"done"}}`)
			events := []session.Event{tc.call, result}
			p := project(t, events)
			if p.Events[0].ToolCall.ID != tc.want || p.Events[1].ToolResult.CallID != tc.want {
				t.Fatal("stable derivation changed")
			}
			s, err := fold(events)
			if err != nil || s.Conversation.Messages[0].ToolCalls[0].ID != tc.want {
				t.Fatal("Fold and Project disagree", err)
			}
			// Reencoding cannot turn an unknown ordinal into a retained-page index.
			for i, e := range p.Events {
				raw, err := eventcodec.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				events[i] = decode(t, string(raw))
			}
			if again := project(t, events); again.Events[0].ToolCall.ID != tc.want {
				t.Fatal("persisted derivation changed")
			}
		})
	}
}

func TestLegacyPersistedReferenceConflictsRejected(t *testing.T) {
	call := decode(t, oldCall)
	result := decode(t, oldResult)
	wrongResult := session.NewToolResult("conflicting-binding", "done")
	result.ToolResult = &wrongResult
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret"}}`)
	ask.Ask = &session.PendingAsk{AskID: "ask", Tool: "Read", Call: "conflicting-binding"}
	for _, last := range []session.Event{result, ask} {
		events := []session.Event{call, last}
		requireFoldError(t, events, eventsource.ErrReconstruct)
		if _, err := eventsource.Project(events, session.LegacyIncarnationID("test", 0, nil)); !errors.Is(err, eventsource.ErrReconstruct) {
			t.Fatal("persisted reference silently rebound", err)
		}
	}
}

func TestLegacyDisplayDoesNotInventCoordinates(t *testing.T) {
	e := decode(t, `{"Type":"hook","Hook":{"Phase":"PreToolUse","Tool":"Read"}}`)
	p := project(t, []session.Event{e})
	if p.Events[0].Historical.RecordID != "" || p.Events[0].LegacyIdentity().Ordinal != -1 || p.Events[0].LegacyIdentity().Coordinate != "" {
		t.Fatal("retained-page index became durable evidence")
	}
}
