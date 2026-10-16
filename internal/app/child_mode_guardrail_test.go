package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
)

// TestYoloParentNarrowerTeamMemberKeepsEnforcingChecker drives the real
// composition: a yolo session forms a Team whose member definition pins
// permissionMode: default, and the checker rejects the member's Read. The
// shared reviewer must enforce for the narrower member (action blocked, unsafe
// inbound content withheld) while an inheriting member stays advisory, like
// its yolo parent.
func TestYoloParentNarrowerTeamMemberKeepsEnforcingChecker(t *testing.T) {
	const canary = "MEMBER-READ-CANARY"
	const prohibited = `{"assessment":"prohibited","concerns":["unsafe"],"evidence":[],"missing_evidence":[]}`
	for _, tc := range []struct {
		name, phase, agent string
		wantLeak           bool
	}{
		{"action-narrow", "pre", "narrow", false},
		{"action-inherit", "pre", "", true},
		{"inbound-narrow", "post", "narrow", false},
		{"inbound-inherit", "post", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "readme.txt"), []byte(canary+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			agentsDir := t.TempDir()
			def := "---\nname: narrow\ndescription: narrow reader\npermissionMode: default\n---\nRead files when asked."
			if err := os.WriteFile(filepath.Join(agentsDir, "narrow.md"), []byte(def), 0o644); err != nil {
				t.Fatal(err)
			}
			member := `{"name":"lead","role":"read readme.txt"}`
			if tc.agent != "" {
				member = `{"name":"lead","role":"read readme.txt","agent":"` + tc.agent + `"}`
			}
			// One provider serves parent, member, and checker in call order.
			provider := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("team", "Team", json.RawMessage(`{"goal":"inspect","members":[`+member+`]}`))),
				mockllm.ToolCallTurn(session.NewToolCall("member-read", "Read", json.RawMessage(`{"path":"readme.txt"}`))),
				mockllm.TextTurn(prohibited),
				mockllm.TextTurn("member done"),
				mockllm.TextTurn("team report"),
				mockllm.TextTurn("parent done"),
			)
			built, err := buildIsolated(t, t.Context(), Config{
				Workspace: workspace, NoSoul: true, UseMock: true, MockProvider: provider,
				Model: "main-model", GuardrailsModel: "guardrail-model", EnableTeams: true,
				AgentsDirs: []string{agentsDir}, PermissionMode: "yolo", PermissionModeFlagSet: true,
				GuardrailsRules: []GuardrailRule{{Match: "Read", Phases: []string{tc.phase}, Mode: "block"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			parent, err := built.Service.CreateSession(t.Context(), session.ModeYolo, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(t.Context(), parent.ID, "inspect")
			if err != nil {
				t.Fatal(err)
			}
			var events []session.Event
			for ev := range run.Events() {
				events = append(events, ev)
			}
			built.Service.FinishRun(parent.ID, run)
			memberSess, err := built.Service.GetSession(t.Context(), teamMemberSessionID(t, events))
			if err != nil {
				t.Fatal(err)
			}
			wantMode := session.ModeYolo
			if tc.agent != "" {
				wantMode = session.ModeDefault
			}
			if memberSess.Mode != wantMode {
				t.Fatalf("member mode = %q, want %q", memberSess.Mode, wantMode)
			}
			var read *session.ToolResult
			for _, m := range memberSess.Conversation.Messages {
				if m.ToolResult != nil && m.ToolResult.CallID == "member-read" {
					read = m.ToolResult
				}
			}
			if read == nil {
				t.Fatalf("member Read produced no recorded result: %+v", memberSess.Conversation.Messages)
			}
			if leaked := strings.Contains(read.Content, canary); leaked != tc.wantLeak {
				t.Fatalf("member Read result leaked=%t, want %t (enforcement must follow the member's own mode): %q", leaked, tc.wantLeak, read.Content)
			}
		})
	}
}
