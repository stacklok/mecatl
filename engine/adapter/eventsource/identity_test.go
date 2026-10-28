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
	es := []session.Event{decode(t, `{"Type":"hook","Text":"blocked","Hook":{"CallID":"provider-secret","Phase":"PreToolUse","Tool":"Read"}}`).WithLegacySource("display", "hook"), decode(t, `{"Type":"approval","Approval":{"Call":"provider-secret","Tool":"Read","Verdict":"allow_always","AllowAlways":true}}`).WithLegacySource("display", "approval"), decode(t, `{"Type":"subagent.tool","Subagent":{"ParentCallID":"provider-secret","ChildToolCallID":"child-secret","ToolName":"Read","Text":"bounded child preview","Args":"NEVER-FORWARD"}}`).WithLegacySource("display", "child")}
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
	if !strings.Contains(p.Events[2].Historical.Text, "bounded child preview") {
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
}

func TestLegacyAwaitingAndAmbiguousRejection(t *testing.T) {
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret","Args":{"path":"x"},"Reason":"approve"}}`)
	for _, ambiguous := range []bool{false, true} {
		es := []session.Event{decode(t, oldCall)}
		if ambiguous {
			es = append(es, decode(t, oldCall).WithLegacySource("", "second"))
		}
		es = append(es, ask)
		if ambiguous {
			requireFoldError(t, es, eventsource.ErrReconstruct)
			continue
		}
		s, err := fold(es)
		if err != nil {
			t.Fatal(err)
		}
		pending, ok := s.PendingAsk()
		if !ok || s.State != session.StateAwaiting || pending.Call == "provider-secret" || pending.Call != s.Conversation.Messages[0].ToolCalls[0].ID || !bytes.Equal(pending.Args, []byte(`{"path":"x"}`)) {
			t.Fatal("pending identity not proven")
		}
		raw, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := sessnap.Unmarshal(raw)
		if err != nil || saved.State != session.StateAwaiting {
			t.Fatal("pending identity lost at restart", err)
		}
	}
}

func TestLegacyExternalAuthorizationRejected(t *testing.T) {
	es := []session.Event{decode(t, oldCall), decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"auth","Call":"provider-secret","DisplayName":"broker","Status":"pending"}}`)}
	for range 2 {
		requireFoldError(t, es, eventsource.ErrReconstruct)
		es = append(es, decode(t, `{"Type":"authorization.resolved","Authorization":{"AuthorizationID":"auth","Call":"WRONG","Status":"closed"}}`))
	}
}

func TestLegacyArchiveIsIndependentAndReplayPrivate(t *testing.T) {
	archive := decode(t, `{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"assistant","Reasoning":"opaque-private","ToolCalls":[{"ID":"provider-secret","Name":"Read","Args":{}}]},{"Role":"tool","ToolResult":{"CallID":"provider-secret","Content":"archived"}}]}}`)
	p := project(t, []session.Event{decode(t, oldCall), decode(t, oldResult), archive.WithLegacySource("archive", "record")})
	ms := p.Events[2].CompactionArchive.Replaced
	if ms[0].ToolCalls[0].ID == p.Events[0].ToolCall.ID || ms[1].ToolResult.CallID != ms[0].ToolCalls[0].ID || ms[0].Replay == nil || ms[0].Replay.Provider == nil {
		t.Fatal("archive linked to live or lost replay")
	}
	if !bytes.Contains(ms[0].Replay.Provider.Data, []byte("opaque-private")) {
		t.Fatal("archive replay changed")
	}
	current := session.Event{Type: session.EvCompactionArchive, CompactionArchive: p.Events[2].CompactionArchive}
	stored, err := eventcodec.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := eventcodec.UnmarshalFormat(stored, eventcodec.Format)
	if err != nil || !reflect.DeepEqual(current, reloaded) {
		t.Fatal("current archive codec changed replay, bindings or provenance", err)
	}
	projected := project(t, []session.Event{reloaded})
	folded, err := fold(projected.Events)
	if err != nil || !reflect.DeepEqual(ms, folded.Conversation.Messages) {
		t.Fatal("current archive reconstruction changed replay, bindings or provenance", err)
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

func TestLegacyCompletedApprovalsAllowSequentialProviderKeyReuse(t *testing.T) {
	var events []session.Event
	for _, coordinate := range []string{"first", "second"} {
		events = append(events,
			decode(t, oldCall).WithLegacySource("durable", coordinate),
			decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret"}}`).WithLegacySource("durable", coordinate+"-ask"),
			decode(t, `{"Type":"approval","Approval":{"AskID":"ask","Tool":"Read","Call":"provider-secret","Verdict":"allow_once"}}`).WithLegacySource("durable", coordinate+"-approval"),
			decode(t, oldResult).WithLegacySource("durable", coordinate+"-result"))
	}
	p := project(t, events)
	if p.Events[0].ToolCall.ID == p.Events[4].ToolCall.ID || p.Events[1].Ask.Call != p.Events[0].ToolCall.ID || p.Events[5].Ask.Call != p.Events[4].ToolCall.ID {
		t.Fatal("sequential control identity collapsed")
	}
	s, err := fold(p.Events)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != session.StateIdle || session.ValidateToolPairing(s.Conversation.Messages) != nil {
		t.Fatal("completed legacy controls changed")
	}
}

func TestCanonicalAwaitingRejectsUnprovenReference(t *testing.T) {
	c := session.NewToolCall(session.NewToolCallID(), "Read", nil)
	es := []session.Event{{Type: session.EvToolCall, ToolCall: &c}, {Type: session.EvPermissionAsk, Ask: &session.PendingAsk{AskID: "ask", Tool: "Read", Call: "wrong"}}}
	if _, err := fold(es); !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatal("unproven canonical pending reference accepted", err)
	}
}
