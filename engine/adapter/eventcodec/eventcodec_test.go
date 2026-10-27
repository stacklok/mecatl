package eventcodec_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
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

const oldCall = `{"Type":"tool.call","RunID":"fixture-run","Seq":90,"ToolCall":{"ID":"provider-secret","Name":"Read","Args":{"path":"x"},"item_id":"private-item"}}`
const oldResult = `{"Type":"tool.result","Seq":2,"ToolResult":{"CallID":"provider-secret","Content":"hello","Parts":[{"BlockKind":"image","Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}`

func TestCanonicalRoundTrip(t *testing.T) {
	call := session.NewToolCall(session.NewToolCallID(), "Read", json.RawMessage(`{}`))
	result := session.NewToolResult(call.ID, "new")
	for _, e := range []session.Event{{Type: session.EvToolCall, ToolCall: &call}, {Type: session.EvToolResult, ToolResult: &result}} {
		raw, err := eventcodec.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"identity_schema":2`)) {
			t.Fatal(string(raw))
		}
		if restored := decode(t, string(raw)); !reflect.DeepEqual(e, restored) {
			t.Fatal("new event changed")
		}
	}
}

func TestMalformedSchemaAndLegacyDTOs(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"identity_schema":0}`, `{"identity_schema":3}`, `{"identity_schema":null}`, `{"ToolCall":{"ID":1}}`, `{"ToolResult":{"CallID":[]}}`} {
		if _, err := eventcodec.Unmarshal([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestLegacyPrivateDecodeAndReencode(t *testing.T) {
	for _, raw := range []string{oldCall, oldResult, `{"Type":"hook","Hook":{"CallID":"provider-secret","Tool":"Read"}}`, `{"Type":"subagent.tool","Subagent":{"ParentCallID":"provider-secret","ChildToolCallID":"child-secret","ToolName":"Read","Text":"bounded child preview","Args":"NEVER-FORWARD"}}`} {
		e := decode(t, raw)
		if e.Type != session.EvHistorical || e.Historical == nil || !e.Historical.AttachmentUnavailable || e.ToolCall != nil || e.ToolResult != nil || e.Hook != nil || e.Subagent != nil {
			t.Fatalf("unsafe decode: %+v", e)
		}
		public, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"provider-secret", "private-item", "child-secret", "NEVER-FORWARD"} {
			if bytes.Contains(public, []byte(secret)) {
				t.Fatalf("private data leaked: %s", public)
			}
		}
		stored, err := eventcodec.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		reloaded := decode(t, string(stored))
		if !reflect.DeepEqual(e, reloaded) || !bytes.Equal(reloaded.LegacyIdentity().Payload, []byte(raw)) {
			t.Fatal("private provenance changed")
		}
	}
}

func TestLegacyTeamDetailIsStandalone(t *testing.T) {
	e := decode(t, `{"Type":"team.member","Team":{"Member":"reviewer","InnerKind":"tool.result","ToolName":"Read","Text":"preview","Detail":"unique bounded team result","ParentCallID":"private-parent","ChildToolCallID":"private-child"}}`)
	h := e.Historical
	if h == nil || !strings.Contains(h.Text, "unique bounded team result") || !h.AttachmentUnavailable || e.Team != nil || e.ToolResult != nil {
		t.Fatalf("lost detail or guessed link: %+v", e)
	}
	if strings.Contains(h.Text, "private-") {
		t.Fatal("private delegation references leaked")
	}
}

func TestSingleCursorProjection(t *testing.T) {
	inc := session.LegacyIncarnationID("test", 0, nil)
	orphan := decode(t, oldResult).WithLegacySource("durable-source", "record-17")
	safe, err := eventcodec.ProjectRecord(orphan, inc, -1)
	if err != nil {
		t.Fatal(err)
	}
	if safe.ToolResult != nil || safe.Type != session.EvHistorical || safe.Historical.RecordID == "" || safe.LegacyIdentity().Ordinal != -1 {
		t.Fatal("cursor projected orphan or guessed iterator ordinal")
	}
	if _, err := eventcodec.ProjectRecord(decode(t, oldResult), inc, -1); err == nil {
		t.Fatal("source-less record got guessed identity")
	}
	archived := decode(t, `{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"tool","ToolResult":{"CallID":"orphan","Content":"archived","Parts":[{"BlockKind":"image","Kind":"image","Data":"AQI="}]}}]}}`).WithLegacySource("durable-source", "record-18")
	archived, err = eventcodec.ProjectRecord(archived, inc, -1)
	if err != nil {
		t.Fatal(err)
	}
	h := session.HistoricalMessage(archived.CompactionArchive.Replaced[0])
	if h == nil || h.Text != "archived" || len(h.Parts) != 1 {
		t.Fatal("archive orphan content dropped")
	}
}

func TestLegacySparseControlsNeverAcquireAuthority(t *testing.T) {
	inc := session.LegacyIncarnationID("test", 0, nil)
	call := decode(t, oldCall).WithLegacySource("durable", "call")
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"private-ask","Tool":"Read","Call":"provider-secret","Reason":"readable approval"}}`).WithLegacySource("durable", "ask")
	auth := decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"private-auth","Call":"provider-secret","DisplayName":"readable service","Status":"pending"}}`).WithLegacySource("durable", "auth")
	boundCall := call
	c := session.NewToolCall("persisted-canonical-binding", "Read", nil)
	boundCall.Type, boundCall.ToolCall, boundCall.Historical = session.EvToolCall, &c, nil
	boundAsk := ask
	boundAsk.Type, boundAsk.Ask, boundAsk.Historical = session.EvPermissionAsk, &session.PendingAsk{AskID: "private-ask", Tool: "Read", Call: c.ID}, nil
	for _, e := range []session.Event{call, ask, auth, boundCall, boundAsk} {
		data, err := eventcodec.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		sparse, err := eventcodec.ProjectRecord(decode(t, string(data)), inc, -1)
		if err != nil {
			t.Fatal(err)
		}
		if sparse.Type != session.EvHistorical || sparse.Historical == nil || !sparse.Historical.AttachmentUnavailable || sparse.Ask != nil || sparse.Authorization != nil || sparse.ToolCall != nil {
			t.Fatal("sparse record acquired authority")
		}
		if sparse.LegacyIdentity().Coordinate != e.LegacyIdentity().Coordinate || sparse.LegacyIdentity().Ordinal != -1 {
			t.Fatal("sparse coordinate changed")
		}
		if e.LegacyIdentity().Coordinate != "call" && !strings.Contains(sparse.Historical.Text, "readable") {
			t.Fatal("standalone control text lost")
		}
	}
}

func TestDecodeLegacyRecordFacts(t *testing.T) {
	call, err := eventcodec.DecodeLegacyRecord([]byte(oldCall))
	if err != nil || call.Type != session.EvToolCall || call.ToolCall == nil || call.ToolCall.ID != "provider-secret" || call.ToolCall.Name != "Read" || !bytes.Equal(call.ToolCall.Args, []byte(`{"path":"x"}`)) {
		t.Fatalf("call facts = %+v, %v", call, err)
	}
	result, err := eventcodec.DecodeLegacyRecord([]byte(oldResult))
	if err != nil || result.ToolResult == nil || result.ToolResult.CallID != "provider-secret" || result.ToolResult.Content != "hello" || len(result.ToolResult.Parts) != 1 {
		t.Fatalf("result facts = %+v, %v", result, err)
	}
	ask, err := eventcodec.DecodeLegacyRecord([]byte(`{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret","Args":{ "z":1, "a":2 },"Reason":"approve"}}`))
	if err != nil || ask.Ask == nil || ask.Ask.Call != "provider-secret" || ask.Ask.AskID != "ask" || !bytes.Equal(ask.Ask.Args, []byte(`{ "z":1, "a":2 }`)) {
		t.Fatalf("ask facts = %+v, %v", ask, err)
	}
	auth, err := eventcodec.DecodeLegacyRecord([]byte(`{"Type":"authorization.required","Authorization":{"AuthorizationID":"auth","Call":"provider-secret","Status":"pending","DisplayName":"service"}}`))
	if err != nil || auth.Authorization == nil || auth.Authorization.Call != "provider-secret" || auth.Authorization.AuthorizationID != "auth" || auth.Authorization.Status != session.AuthorizationPending {
		t.Fatalf("authorization facts = %+v, %v", auth, err)
	}
	archive := `{ "Replaced": [ {"Role":"assistant","Reasoning":"private", "Text":"unchanged"} ] }`
	record, err := eventcodec.DecodeLegacyRecord([]byte(`{"Type":"compaction.archive","CompactionArchive":` + archive + `}`))
	if err != nil || !bytes.Equal(record.CompactionArchive, []byte(archive)) {
		t.Fatal("raw archive changed", err)
	}
	for _, raw := range []string{`{"ToolCall":{"ID":1}}`, `{"ToolCall":{"item_id":1}}`, `{"ToolResult":{"CallID":[]}}`, `{"Ask":{"Call":{}}}`, `{"Authorization":{"Call":false}}`, `{"Hook":{"CallID":1}}`} {
		if _, err := eventcodec.DecodeLegacyRecord([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed legacy DTO: %s", raw)
		}
	}
}
