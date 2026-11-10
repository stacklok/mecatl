package eventcodec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
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

func TestLegacyUnsupportedRecordsReturnSkip(t *testing.T) {
	for _, raw := range []string{
		`{"Type":"tool.call","ToolCall":{"ID":"provider-secret","Name":"Read","Args":{},"item_id":"private-item"}}`,
		`{"Type":"tool.result","ToolResult":{"CallID":"provider-secret","Content":"hello","Parts":[{"BlockKind":"image","Data":"AQI="}]}}`,
		`{"Type":"tool.result.available","ToolResult":{"CallID":"provider-secret"}}`,
		`{"Type":"permission.ask","Ask":{"AskID":"ask","Call":"provider-secret"}}`,
		`{"Type":"permission.retract"}`, `{"Type":"approval"}`, `{"Type":"hook"}`,
		`{"Type":"authorization.required"}`, `{"Type":"authorization.resolved"}`,
		`{"Type":"subagent.start"}`, `{"Type":"subagent.tool","Subagent":{"ParentCallID":"provider-secret","ChildToolCallID":"private-child"}}`,
		`{"Type":"subagent.end"}`, `{"Type":"team.start"}`, `{"Type":"team.member"}`,
		`{"Type":"team.tasks"}`, `{"Type":"team.findings"}`, `{"Type":"team.end"}`,
		`{"Type":"parallel.start"}`, `{"Type":"parallel.branch"}`, `{"Type":"parallel.end"}`,
		`{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"assistant","ToolCalls":[{"ID":"provider-secret"}]}]}}`,
	} {
		for _, format := range []string{"", "eventlog-json/1"} {
			var e session.Event
			var err error
			if format == "" {
				e, err = eventcodec.Unmarshal([]byte(raw))
			} else {
				e, err = eventcodec.UnmarshalFormat([]byte(raw), format)
			}
			if !errors.Is(err, eventcodec.ErrSkipLegacyRecord) || !reflect.DeepEqual(e, session.Event{}) {
				t.Fatalf("decode %s (%q) = %+v, %v", raw, format, e, err)
			}
		}
	}
}

func TestLegacyAttachmentsCannotHideBehindNeutralType(t *testing.T) {
	for _, attachment := range []string{
		`"ToolCall":{"ID":"provider-secret"}`, `"ToolResult":{"CallID":"provider-secret"}`,
		`"Ask":{"Call":"provider-secret"}`, `"Hook":{"CallID":"provider-secret"}`,
		`"Approval":{"Call":"provider-secret"}`, `"Authorization":{"Call":"provider-secret"}`,
		`"Subagent":{"ParentCallID":"provider-secret"}`, `"Team":{"ParentCallID":"provider-secret"}`,
		`"Parallel":{"ParentCallID":"provider-secret"}`, `"CompactionArchive":{"Replaced":[{"Role":"tool","ToolResult":{"CallID":"provider-secret"}}]}`,
		`"toolCALL":{"ID":"provider-secret"}`,
	} {
		e, err := eventcodec.Unmarshal([]byte(`{"Type":"message.delta","Text":"harmless",` + attachment + `}`))
		if !errors.Is(err, eventcodec.ErrSkipLegacyRecord) || !reflect.DeepEqual(e, session.Event{}) {
			t.Fatalf("attachment escaped: %+v, %v", e, err)
		}
	}
}

func TestLegacyNeutralFieldsConvertWithoutPrivateCarrier(t *testing.T) {
	raw := `{"Type":"user_prompt","RunID":"run","Seq":17,"Turn":2,"Text":"prompt","UserPrompt":{"Text":"prompt","Parts":[{"Kind":"image","Data":"AQI="}]},"ToolCall":null,"ToolResult":null,"Ask":null}`
	e := decode(t, raw)
	if e.Type != session.EvUserPrompt || e.RunID != "run" || e.Seq != 17 || e.Turn != 2 || e.UserPrompt.Text != "prompt" || !bytes.Equal(e.UserPrompt.Parts[0].Data, []byte{1, 2}) {
		t.Fatalf("neutral content changed: %+v", e)
	}
	stored, err := eventcodec.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("legacy_identity")) || !reflect.DeepEqual(e, decode(t, string(stored))) {
		t.Fatal("neutral event reencoding changed")
	}
}

func TestMalformedAndUnknownLegacyRecordsAreNotSkips(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{`, `{} `, `{"identity_schema":0}`, `{"identity_schema":3}`, `{"identity_schema":null}`,
		`{"Type":"tool.call","ToolCall":{"ID":1}}`, `{"Type":"tool.call","ToolCall":{"item_id":1}}`,
		`{"Type":"tool.result","ToolResult":{"CallID":[]}}`, `{"Type":"permission.ask","Ask":{"Call":{}}}`,
		`{"Type":"authorization.required","Authorization":{"Call":false}}`, `{"Type":"hook","Hook":{"CallID":1}}`,
		`{"Type":"compaction.archive","CompactionArchive":{"Replaced":false}}`,
		`{"Type":"subagent.tool","Subagent":42}`, `{"Type":"team.member","Team":[]}`,
		`{"Type":"message.delta","Text":42}`, `{"Type":"unknown"}`,
		`{"Type":"message.delta","Unknown":{"ID":"provider-secret"}}`,
		`{"Type":"tool.call","Unknown":{}}`, `{"Type":"historical"}`,
		`{"Type":"message.delta","Historical":{}}`, `{"Type":"message.delta","legacy_identity":{}}`,
	} {
		if _, err := eventcodec.Unmarshal([]byte(raw)); err == nil || errors.Is(err, eventcodec.ErrSkipLegacyRecord) {
			t.Fatalf("wanted ordinary error for %s, got %v", raw, err)
		}
	}
	for _, tc := range []struct{ raw, format string }{
		{`{"Type":"tool.call"}`, eventcodec.Format},
		{`{"identity_schema":2,"Type":"tool.call"}`, "eventlog-json/1"},
		{`{"Type":"tool.call"}`, "unknown"},
	} {
		if _, err := eventcodec.UnmarshalFormat([]byte(tc.raw), tc.format); err == nil || errors.Is(err, eventcodec.ErrSkipLegacyRecord) {
			t.Fatalf("format mismatch skipped: %v", err)
		}
	}
}
