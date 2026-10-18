package eventcodec_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
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

func TestMissingOccurrenceAnchorStaysUnattachedAcrossRestart(t *testing.T) {
	es := []session.Event{
		decode(t, `{"Type":"tool.call","ToolCall":{"ID":"old","Name":"Read"}}`),
		decode(t, `{"Type":"tool.result","ToolResult":{"CallID":"old","Content":"readable"}}`),
	}
	for range 2 {
		p := project(t, es)
		for i, e := range p.Events {
			if e.Historical == nil || !e.Historical.AttachmentUnavailable || e.ToolCall != nil || e.ToolResult != nil || e.LegacyIdentity().Ordinal != -1 || e.LegacyIdentity().Coordinate != "" {
				t.Fatal("retained index became a durable invocation anchor")
			}
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

func TestLegacyTeamDetailIsStandalone(t *testing.T) {
	e := decode(t, `{"Type":"team.member","Team":{"Member":"reviewer","InnerKind":"tool.result","ToolName":"Read","Text":"preview","Detail":"unique bounded team result","ParentCallID":"private-parent","ChildToolCallID":"private-child"}}`)
	p := project(t, []session.Event{e})
	h := p.Events[0].Historical
	if h == nil || !strings.Contains(h.Text, "unique bounded team result") || !h.AttachmentUnavailable || p.Events[0].Team != nil || p.Events[0].ToolResult != nil {
		t.Fatalf("lost detail or guessed link: %+v", p.Events[0])
	}
	if strings.Contains(h.Text, "private-") {
		t.Fatal("private delegation references leaked")
	}
}
