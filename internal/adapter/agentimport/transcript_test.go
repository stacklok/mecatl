package agentimport

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestParseCodexUsesCanonicalMessagesAndDropsProviderState(t *testing.T) {
	raw := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"codex-123","cwd":"/work/project"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"duplicate event"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"private instructions"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"opaque"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-1"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
	}, "\n")

	got, err := Parse(SourceCodex, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ExternalID != "codex-123" || got.Workspace != "/work/project" {
		t.Fatalf("metadata = %#v", got)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !got.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %s, want %s", got.CreatedAt, want)
	}
	assertMessages(t, got.Messages, []session.Role{session.RoleUser, session.RoleAssistant}, []string{"fix the build", "Done."})
	if got.Title != "fix the build" {
		t.Fatalf("Title = %q", got.Title)
	}
}

func TestParseCodexFallsBackToEvents(t *testing.T) {
	raw := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`
	got, err := Parse(SourceCodex, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	assertMessages(t, got.Messages, []session.Role{session.RoleUser, session.RoleAssistant}, []string{"hello", "hi"})
}

func TestParseClaudeCodeTextOnlyAndCoalescesAroundTools(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"user","sessionId":"claude-123","cwd":"/work/project","timestamp":"2026-02-03T04:05:06Z","message":{"role":"user","content":"diagnose this"}}`,
		`{"type":"assistant","sessionId":"claude-123","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"I will inspect."},{"type":"tool_use","id":"tool-1"}]}}`,
		`{"type":"user","sessionId":"claude-123","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"output"}]}}`,
		`{"type":"assistant","sessionId":"claude-123","message":{"role":"assistant","content":[{"type":"text","text":"It is fixed."}]}}`,
		`{"type":"assistant","isSidechain":true,"sessionId":"claude-123","message":{"role":"assistant","content":[{"type":"text","text":"sidechain"}]}}`,
		`{"type":"user","isMeta":true,"sessionId":"claude-123","message":{"role":"user","content":"internal metadata"}}`,
		`{"type":"user","isCompactSummary":true,"sessionId":"claude-123","message":{"role":"user","content":"compacted context"}}`,
	}, "\n")

	got, err := Parse(SourceClaudeCode, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	assertMessages(t, got.Messages, []session.Role{session.RoleUser, session.RoleAssistant}, []string{"diagnose this", "I will inspect.\n\nIt is fixed."})
	if got.ExternalID != "claude-123" || got.Title != "diagnose this" {
		t.Fatalf("metadata = %#v", got)
	}
}

func TestParseRejectsUnknownSource(t *testing.T) {
	if _, err := Parse("other", strings.NewReader("")); err == nil {
		t.Fatal("Parse accepted unknown source")
	}
}

func TestParseRejectsMalformedJSONL(t *testing.T) {
	raw := `{"type":"event_msg","payload":{"type":"user_message","message":"ok"}}` + "\n" +
		`this is not json`
	if _, err := Parse(SourceCodex, strings.NewReader(raw)); err == nil ||
		!strings.Contains(err.Error(), "decode JSONL line 2") {
		t.Fatalf("Parse error = %v", err)
	}
}

func assertMessages(t *testing.T, got []session.Message, roles []session.Role, texts []string) {
	t.Helper()
	if len(got) != len(roles) || len(got) != len(texts) {
		t.Fatalf("len(Messages) = %d, want %d: %#v", len(got), len(roles), got)
	}
	for i := range got {
		if got[i].Role != roles[i] || got[i].Text != texts[i] {
			t.Errorf("Messages[%d] = (%q, %q), want (%q, %q)", i, got[i].Role, got[i].Text, roles[i], texts[i])
		}
		if got[i].Reasoning != "" || len(got[i].ToolCalls) != 0 || got[i].ToolResult != nil {
			t.Errorf("Messages[%d] retained provider-private state: %#v", i, got[i])
		}
	}
}
