package eventcodec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyAbandonmentCompleteSource(t *testing.T) {
	for _, kind := range []string{"ask", "auth"} {
		for _, defect := range []string{"missing-key", "duplicate-key", "same-tool-siblings", "completed", "unanchored", "missing-control"} {
			t.Run(kind+"/"+defect, func(t *testing.T) {
				call := decode(t, oldCall).WithLegacySource("durable", "call-1")
				other := decode(t, `{"Type":"tool.call","ToolCall":{"ID":"other","Name":"Read","Args":{"path":"other"}}}`).WithLegacySource("durable", "call-2")
				events := []session.Event{call}
				wantCalls, wantCompleted := 1, false
				switch defect {
				case "missing-key":
					events[0] = decode(t, `{"Type":"tool.call","ToolCall":{"Name":"Read","Args":{}}}`).WithLegacySource("durable", "call-1")
					wantCalls = 0
				case "duplicate-key":
					events = append(events, call.WithLegacySource("durable", "call-2"))
					wantCalls = 0
				case "same-tool-siblings":
					events = append(events, other)
					wantCalls = 2
				case "completed":
					events = append(events,
						decode(t, `{"Type":"permission.ask","Ask":{"AskID":"completed-ask","Tool":"Read","Call":"provider-secret"}}`).WithLegacySource("durable", "completed-ask"),
						decode(t, oldResult).WithLegacySource("durable", "result-1"))
					wantCompleted = true
				case "unanchored":
					events[0] = decode(t, `{"Type":"tool.call","ToolCall":{"ID":"provider-secret","Name":"Read","Args":{}}}`)
					wantCalls = 0
				}
				control := `{"Type":"permission.ask","Ask":{"AskID":"obsolete-ask","Tool":"Read","Call":"wrong","Args":{},"Reason":"approve"}}`
				if kind == "auth" {
					control = `{"Type":"authorization.required","Authorization":{"AuthorizationID":"obsolete-auth","Call":"provider-secret","Status":"pending","DisplayName":"External service"}}`
				}
				if defect == "missing-control" {
					if kind == "ask" {
						control = `{"Type":"permission.ask","Text":"approval details unavailable"}`
					} else {
						control = `{"Type":"authorization.required","Text":"authorization details unavailable"}`
					}
				}
				events = append(events,
					decode(t, `{"Type":"tool.result","ToolResult":{"CallID":"unattached","Content":"standalone result","Parts":[{"Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}`).WithLegacySource("durable", "orphan-result"),
					decode(t, control).WithLegacySource("durable", "control"))
				p := project(t, events)
				if len(p.Events) != len(events) {
					t.Fatal("synthetic durable records")
				}
				for i, e := range p.Events {
					before, after := events[i].LegacyIdentity(), e.LegacyIdentity()
					if e.RunID != events[i].RunID || e.Seq != events[i].Seq || !bytes.Equal(before.Payload, after.Payload) || before.Coordinate != after.Coordinate || before.Ordinal != after.Ordinal || (before.Namespace != "" && before.Namespace != after.Namespace) {
						t.Fatalf("source provenance changed at %d", i)
					}
				}
				first, err := fold(events)
				if err != nil {
					t.Fatal(err)
				}
				second, err := fold(events)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(first.Conversation.Messages, second.Conversation.Messages) {
					t.Fatal("independent reads changed identities")
				}
				reloaded := make([]session.Event, len(p.Events))
				for i, e := range p.Events {
					raw, err := eventcodec.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}
					reloaded[i] = decode(t, string(raw))
				}
				second, err = fold(reloaded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(first.Conversation.Messages, second.Conversation.Messages) {
					t.Fatal("projected event save/reload changed normalization")
				}
				calls, results, notices := 0, 0, 0
				standalone := false
				for _, m := range first.Conversation.Messages {
					if h := m.Historical; h != nil && strings.Contains(h.Text, "standalone result") {
						standalone = h.AttachmentUnavailable && len(h.Parts) == 1 && bytes.Equal(h.Parts[0].Data, []byte{1, 2}) && m.ToolResult == nil
					}
					calls += len(m.ToolCalls)
					if m.ToolResult != nil {
						results++
						if wantCompleted {
							if m.ToolResult.Content != "hello" || m.ToolResult.IsError || len(m.ToolResult.Parts) != 1 {
								t.Fatal("completed result lost")
							}
						} else if !m.ToolResult.IsError || !strings.Contains(m.ToolResult.Content, "effects and completion may be unknown") || m.Legacy != nil {
							t.Fatal("not an unattached migration interruption")
						}
					}
					if strings.Contains(m.Text, "abandoned during upgrade") {
						notices++
						if m.Role != session.RoleAssistant || m.Legacy != nil || m.Historical != nil {
							t.Fatal("notice invented source authority")
						}
					}
				}
				if calls != wantCalls || results != wantCalls || notices != 1 {
					t.Fatalf("calls/results/notices = %d/%d/%d, want %d/%d/1", calls, results, notices, wantCalls, wantCalls)
				}
				if !standalone {
					t.Fatal("unattached text/media lost or attached")
				}
				want, err := sessnap.Marshal(first)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if first.State != session.StateIdle {
						t.Fatal("not ordinary idle history")
					}
					if _, ok := first.PendingAsk(); ok {
						t.Fatal("stale approval")
					}
					if _, err := first.ResumeWith(); err == nil {
						t.Fatal("obsolete approval resumed")
					}
					if _, err := first.ClaimAuthorization(); err == nil {
						t.Fatal("obsolete authorization claimed")
					}
					if _, err := port.ProjectMessages(first.Conversation.Messages); err != nil {
						t.Fatal(err)
					}
					first, err = sessnap.Unmarshal(want)
					if err != nil {
						t.Fatal(err)
					}
					got, err := sessnap.Marshal(first)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, want) || bytes.Contains(got, []byte("identity_hold")) {
						t.Fatal("save/reload changed normalization")
					}
					want = got
				}
				if err := first.BeginTurn(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestLegacySparseControlsNeverAcquireAuthority(t *testing.T) {
	call := decode(t, oldCall).WithLegacySource("durable", "call")
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"private-ask","Tool":"Read","Call":"provider-secret","Reason":"readable approval"}}`).WithLegacySource("durable", "ask")
	auth := decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"private-auth","Call":"provider-secret","DisplayName":"readable service","Status":"pending"}}`).WithLegacySource("durable", "auth")
	for _, events := range [][]session.Event{{call, ask}, {call, auth}} {
		p := project(t, events)
		for _, source := range [][]session.Event{events, p.Events} {
			for i, e := range source {
				data, err := eventcodec.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				e = decode(t, string(data))
				sparse, err := eventcodec.ProjectRecord(e, session.LegacyIncarnationID("test", 0, nil), -1)
				if err != nil {
					t.Fatal(err)
				}
				if sparse.Type != session.EvHistorical || sparse.Historical == nil || !sparse.Historical.AttachmentUnavailable || sparse.Ask != nil || sparse.Authorization != nil || sparse.ToolCall != nil {
					t.Fatal("sparse record acquired authority")
				}
				if sparse.LegacyIdentity().Coordinate != events[i].LegacyIdentity().Coordinate || sparse.LegacyIdentity().Ordinal != -1 {
					t.Fatal("sparse coordinate changed")
				}
				if i == 1 && !strings.Contains(sparse.Historical.Text, "readable") {
					t.Fatal("standalone control text lost")
				}
			}
		}
	}
}

func TestCanonicalControlsRemainStrictInMixedSource(t *testing.T) {
	legacy := []session.Event{decode(t, oldCall), decode(t, `{"Type":"permission.ask","Ask":{"AskID":"old","Tool":"Read","Call":"wrong"}}`)}
	canonical := func(raw string) session.Event { return decode(t, raw) }
	required := canonical(`{"identity_schema":2,"Type":"authorization.required","Authorization":{"AuthorizationID":"current","Call":"current-call","Status":"pending","DisplayName":"service","ExpiresAt":"2026-09-01T12:00:00Z"}}`)
	for _, prefix := range [][]session.Event{nil, legacy} {
		if _, err := fold(append(prefix, required)); !errors.Is(err, eventsource.ErrPrivateStateRequired) {
			t.Fatal("current open authorization accepted", err)
		}
		resolved := canonical(`{"identity_schema":2,"Type":"authorization.resolved","Authorization":{"AuthorizationID":"current","Call":"current-call","Status":"granted","DisplayName":"service","ExpiresAt":"2026-09-01T12:00:00Z"}}`)
		if _, err := fold(append(prefix, required, resolved)); !errors.Is(err, eventsource.ErrReconstruct) {
			t.Fatal("current completeness relaxed", err)
		}
	}
	call := canonical(`{"identity_schema":2,"Type":"tool.call","ToolCall":{"ID":"canonical-call","Name":"Read","Args":{"path":"x"}}}`)
	ask := canonical(`{"identity_schema":2,"Type":"permission.ask","Ask":{"AskID":"canonical-ask","Tool":"Read","Call":"canonical-call","Args":{"path":"x"}}}`)
	s, err := fold([]session.Event{call, ask})
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := s.PendingAsk()
	if !ok || s.State != session.StateAwaiting || pending.Call != call.ToolCall.ID {
		t.Fatal("valid current ask changed")
	}
	// The actual current-format bytes, not legacy provider strings, survive mapping.
	data, err := json.Marshal(pending)
	if err != nil || !bytes.Contains(data, []byte("canonical-ask")) {
		t.Fatal(err)
	}
}

func TestLegacyAbandonmentEvidenceDoesNotInventAuthority(t *testing.T) {
	inc := session.LegacyIncarnationID("test", 0, nil)
	call := decode(t, oldCall).WithLegacySource("durable", "call")
	identity := call.LegacyIdentity()
	identity.Ordinal = 0
	call = call.WithLegacyIdentity(*identity)
	old := learning.Input{Trajectory: learning.Trajectory{SessionID: "test"}, Events: []learning.EvidenceEventData{{Type: session.EvToolCall, Seq: 90, ToolCall: &learning.ToolProjection{ID: "provider-secret", Name: "Read"}}}}
	ref, err := learning.EventEvidenceRef(old, 0, "provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, ambiguous := range []bool{false, true} {
		events := []session.Event{call}
		if ambiguous {
			events = append(events, decode(t, oldCall).WithLegacySource("durable", "duplicate"))
		}
		events = append(events, decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"old-auth","Call":"provider-secret","Status":"pending"}}`).WithLegacySource("durable", "auth"))
		p := project(t, events)
		if _, err := fold(events); err != nil {
			t.Fatal(err)
		}
		citation, err := learning.VerifyEvidenceSource(old.Trajectory, p.Events, inc, ref)
		if err != nil {
			t.Fatal(err)
		}
		if ambiguous && citation.ToolCallID != "" {
			t.Fatal("ambiguous occurrence became attached evidence")
		}
		if !ambiguous && citation.ToolCallID != p.Events[0].ToolCall.ID {
			t.Fatal("proven original lost its attachment")
		}
		sparse, err := eventcodec.ProjectRecord(p.Events[0], inc, -1)
		if err != nil {
			t.Fatal(err)
		}
		citation, err = learning.VerifyEvidenceSource(old.Trajectory, []session.Event{sparse}, inc, ref)
		if err != nil || citation.ToolCallID != "" {
			t.Fatal("sparse evidence acquired attachment", err)
		}
		data, err := json.Marshal(p.Events[0])
		if err != nil {
			t.Fatal(err)
		}
		var display session.Event
		if err := json.Unmarshal(data, &display); err != nil {
			t.Fatal(err)
		}
		for _, unavailable := range [][]session.Event{nil, p.Events[1:], {display}} {
			if _, err := learning.VerifyEvidenceSource(old.Trajectory, unavailable, inc, ref); err == nil {
				t.Fatal("missing/pruned/display-only source verified")
			}
		}
	}
}
