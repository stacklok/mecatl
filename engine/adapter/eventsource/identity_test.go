package eventsource_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func decode(t *testing.T, raw string) session.Event {
	t.Helper()
	e, err := eventcodec.Unmarshal([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func project(t *testing.T, es []session.Event) eventsource.Projection {
	t.Helper()
	p, err := eventsource.Project(es, session.LegacyIncarnationID("test", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func fold(es []session.Event) (*session.Session, error) {
	return eventsource.Fold(eventsource.SessionMeta{ID: "test", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}, CreatedAt: time.Unix(0, 0)}, seq(es))
}

const oldCall = `{"Type":"tool.call","RunID":"fixture-run","Seq":90,"ToolCall":{"ID":"provider-secret","Name":"Read","Args":{"path":"x"},"item_id":"private-item"}}`
const oldResult = `{"Type":"tool.result","Seq":2,"ToolResult":{"CallID":"provider-secret","Content":"hello","Parts":[{"BlockKind":"image","Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}`

func TestOldNewMixedAndReencode(t *testing.T) {
	call := session.NewToolCall(session.NewToolCallID(), "Read", json.RawMessage(`{}`))
	es := []session.Event{{Type: session.EvToolCall, ToolCall: &call}, {Type: session.EvToolResult, ToolResult: ptr(session.NewToolResult(call.ID, "new"))}}
	es = append(es, decode(t, oldCall).WithLegacySource("fixture", "first-call"), decode(t, oldResult).WithLegacySource("fixture", "first-result"), decode(t, oldCall).WithLegacySource("fixture", "second-call"), decode(t, oldResult).WithLegacySource("fixture", "second-result"))
	p := project(t, es)
	first, second := p.Events[2].ToolCall.ID, p.Events[4].ToolCall.ID
	if first == second || first == call.ID || first == "provider-secret" || p.Events[3].ToolResult.CallID != first || p.Events[5].ToolResult.CallID != second {
		t.Fatal("bad occurrence identity")
	}
	if len(p.Events) != len(es) || p.Events[2].Seq != 90 || p.Events[3].Seq != 2 {
		t.Fatal("order/sequence changed")
	}
	for i, e := range p.Events {
		raw, err := eventcodec.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		es[i], err = eventcodec.Unmarshal(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	again := project(t, es)
	if !reflect.DeepEqual(p, again) {
		t.Fatal("reencode lost private provenance or semantics")
	}
	if p.Events[2].LegacyIdentity().Ordinal != -1 || p.Events[3].LegacyIdentity().Ordinal != -1 {
		t.Fatal("retained index mistaken for original evidence ordinal")
	}
	public, err := json.Marshal(p.Events[2])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, []byte("provider-secret")) || bytes.Contains(public, []byte("private-item")) {
		t.Fatal("private pairing data projected")
	}
	s, err := fold(es)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := sessnap.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Conversation.Messages, saved.Conversation.Messages) {
		t.Fatal("snapshot restart changed folded history")
	}
}

func TestSourceIsolationAndUnsupportedContent(t *testing.T) {
	es := []session.Event{decode(t, oldCall).WithLegacySource("source-a", "record-0"), decode(t, oldResult).WithLegacySource("source-b", "record-1")}
	es = append(es, decode(t, `{"Type":"hook","Text":"blocked","Hook":{"CallID":"provider-secret","Phase":"PreToolUse","Tool":"Read"}}`), decode(t, `{"Type":"approval","Approval":{"Call":"provider-secret","Tool":"Read","Verdict":"allow_always","AllowAlways":true}}`), decode(t, `{"Type":"subagent.tool","Subagent":{"ParentCallID":"provider-secret","ChildToolCallID":"child-secret","ToolName":"Read","Text":"bounded child preview","Args":"NEVER-FORWARD"}}`))
	p := project(t, es)
	for _, e := range p.Events {
		if e.Type != session.EvHistorical || e.Historical == nil || !e.Historical.AttachmentUnavailable || e.Historical.RecordID == "" || e.ToolCall != nil || e.ToolResult != nil || e.Approval != nil || e.Hook != nil || e.Subagent != nil {
			t.Fatalf("unsafe projection: %+v", e)
		}
		public, _ := json.Marshal(e)
		for _, secret := range []string{"provider-secret", "child-secret", "NEVER-FORWARD"} {
			if bytes.Contains(public, []byte(secret)) {
				t.Fatalf("private data leaked: %s", public)
			}
		}
	}
	if p.Events[1].Historical.Text != "hello" || len(p.Events[1].Historical.Parts) != 1 || !strings.Contains(p.Events[4].Historical.Text, "bounded child preview") {
		t.Fatal("content dropped")
	}
	s, err := fold(es)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := sessnap.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range saved.Conversation.Messages {
		if h := session.HistoricalMessage(m); h != nil && h.Kind == string(session.EvToolResult) {
			found = len(h.Parts) == 1 && h.Text == "hello"
		}
	}
	if !found {
		t.Fatal("unattached media lost after snapshot save")
	}
	llm, err := port.ProjectMessages(saved.Conversation.Messages)
	if err != nil {
		t.Fatal(err)
	}
	modelBytes, err := json.Marshal(llm)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(modelBytes, []byte("bounded child preview")) {
		t.Fatal("historical delegation display entered parent model context")
	}
	// Equal contents at different positions remain distinct records.
	same := project(t, []session.Event{decode(t, oldResult), decode(t, oldResult)})
	if same.Events[0].Historical.RecordID == same.Events[1].Historical.RecordID {
		t.Fatal("content-hash merging")
	}
}

func TestLegacyAwaitingAndAmbiguousAbandonment(t *testing.T) {
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret","Args":{"path":"x"},"Reason":"approve"}}`)
	for _, ambiguous := range []bool{false, true} {
		es := []session.Event{decode(t, oldCall)}
		if ambiguous {
			es = append(es, decode(t, oldCall))
		}
		es = append(es, ask)
		s, err := fold(es)
		if err != nil {
			t.Fatal(err)
		}
		if ambiguous {
			if s.State != session.StateIdle {
				t.Fatal("ambiguous ask not abandoned", s.State)
			}
			if _, ok := s.PendingAsk(); ok {
				t.Fatal("ambiguous pending authority")
			}
		} else {
			pending, ok := s.PendingAsk()
			if !ok || s.State != session.StateAwaiting || pending.Call == "provider-secret" || pending.Call != s.Conversation.Messages[0].ToolCalls[0].ID || !bytes.Equal(pending.Args, []byte(`{"path":"x"}`)) {
				t.Fatal("pending identity not proven")
			}
		}
		raw, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := sessnap.Unmarshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if ambiguous && (saved.State != session.StateIdle) {
			t.Fatal("abandonment lost at restart")
		}
	}
}

func TestLegacyExternalAuthorizationAbandoned(t *testing.T) {
	es := []session.Event{decode(t, oldCall), decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"auth","Call":"provider-secret","DisplayName":"broker","Status":"pending"}}`)}
	for range 2 {
		s, err := fold(es)
		if err != nil || s.State != session.StateIdle {
			t.Fatal("legacy public authorization not abandoned", s, err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		es = append(es, decode(t, `{"Type":"authorization.resolved","Authorization":{"AuthorizationID":"auth","Call":"WRONG","Status":"closed"}}`))
	}
}

func TestLegacyArchiveIsIndependentAndReplayPrivate(t *testing.T) {
	archive := decode(t, `{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"assistant","Reasoning":"opaque-private","ToolCalls":[{"ID":"provider-secret","Name":"Read","Args":{}}]},{"Role":"tool","ToolResult":{"CallID":"provider-secret","Content":"archived"}}]}}`)
	p := project(t, []session.Event{decode(t, oldCall), decode(t, oldResult), archive})
	ms := p.Events[2].CompactionArchive.Replaced
	if ms[0].ToolCalls[0].ID == p.Events[0].ToolCall.ID || ms[1].ToolResult.CallID != ms[0].ToolCalls[0].ID || ms[0].Replay == nil || ms[0].Replay.Provider == nil {
		t.Fatal("archive linked to live or lost replay")
	}
	if !bytes.Contains(ms[0].Replay.Provider.Data, []byte("opaque-private")) {
		t.Fatal("archive replay changed")
	}
}

func TestCanonicalRepeatedDeliveryPreservesIdentity(t *testing.T) {
	// Reconstruction preserves repeated delivery of an existing canonical call.
	c := session.NewToolCall(session.NewToolCallID(), "Read", json.RawMessage(`{}`))
	e := session.Event{Type: session.EvToolCall, ToolCall: &c}
	p := project(t, []session.Event{e, e})
	if p.Events[0].ToolCall.ID != c.ID || p.Events[1].ToolCall.ID != c.ID {
		t.Fatal("same invocation reminted")
	}
}

func TestEventSnapshotAnchorsAreIndependent(t *testing.T) {
	s, err := sessnap.Unmarshal([]byte(`{"id":"test","state":"idle","mode":"default","environment_ref":{"kind":"local","id":"/ws","revision":"v1"},"created_at":"1970-01-01T00:00:00Z","messages":[{"role":"assistant","tool_calls":[{"ID":"provider-secret","Name":"Read","Args":{"path":"x"}}]},{"role":"tool","tool_result":{"CallID":"provider-secret","Content":"hello"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := project(t, []session.Event{decode(t, oldCall), decode(t, oldResult)})
	if s.Conversation.Messages[0].ToolCalls[0].ID == p.Events[0].ToolCall.ID {
		t.Fatal("manufactured snapshot/log crosswalk")
	}
}

func TestCanonicalAwaitingRejectsUnprovenReference(t *testing.T) {
	c := session.NewToolCall(session.NewToolCallID(), "Read", nil)
	es := []session.Event{{Type: session.EvToolCall, ToolCall: &c}, {Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "ask", Tool: "Read", Call: "wrong"}}}
	if _, err := fold(es); !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatal("unproven canonical pending reference accepted", err)
	}
}
