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
	const prohibited = `{"assessment":"prohibited","concerns":[{"ref":"C1","category":"data_exfiltration","rationale":"member read is unsafe","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`
	// wantReason pins how the narrower member was stopped. For the action
	// review it names a PARSED prohibited verdict; a malformed verdict fails
	// closed with "could not approve this action" instead.
	for _, tc := range []struct {
		name, phase, agent string
		wantLeak           bool
		wantReason         string
	}{
		{"action-narrow", "pre", "narrow", false, "contextual guardrail requires human approval for this action"},
		{"action-inherit", "pre", "", true, ""},
		{"inbound-narrow", "post", "narrow", false, "tool result withheld by contextual guardrail"},
		{"inbound-inherit", "post", "", true, ""},
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
			if tc.wantReason != "" && !strings.Contains(read.Content, tc.wantReason) {
				t.Fatalf("member Read result = %q, want the reason %q", read.Content, tc.wantReason)
			}
		})
	}
}

// TestAutoParentUnpinnedTeamMemberInheritsEnforcingChecker drives the real
// composition: an auto session forms a Team whose member names no definition.
// The member inherits auto and runs under the parent's enforcing checker, not
// as plain allow-all: a prohibited Read is blocked and an acceptable one runs.
func TestAutoParentUnpinnedTeamMemberInheritsEnforcingChecker(t *testing.T) {
	const canary = "MEMBER-READ-CANARY"
	for _, tc := range []struct {
		name, verdict string
		wantLeak      bool
	}{
		{"prohibited", `{"assessment":"prohibited","concerns":[{"ref":"C1","category":"data_exfiltration","rationale":"member read is unsafe","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`, false},
		{"acceptable", `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "readme.txt"), []byte(canary+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// One provider serves parent, member, and checker in call order.
			provider := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("team", "Team", json.RawMessage(`{"goal":"inspect","members":[{"name":"lead","role":"read readme.txt"}]}`))),
				mockllm.ToolCallTurn(session.NewToolCall("member-read", "Read", json.RawMessage(`{"path":"readme.txt"}`))),
				mockllm.TextTurn(tc.verdict),
				mockllm.TextTurn("member done"),
				mockllm.TextTurn("team report"),
				mockllm.TextTurn("parent done"),
			)
			built, err := buildIsolated(t, t.Context(), Config{
				Workspace: workspace, NoSoul: true, UseMock: true, MockProvider: provider,
				Model: "main-model", GuardrailsModel: "guardrail-model", EnableTeams: true,
				PermissionMode: "auto", PermissionModeFlagSet: true,
				GuardrailsRules: []GuardrailRule{{Match: "Read", Phases: []string{"pre"}, Mode: "block"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			parent, err := built.Service.CreateSession(t.Context(), session.ModeAuto, session.Limits{})
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
			if memberSess.Mode != session.ModeAuto {
				t.Fatalf("member mode = %q, want %q", memberSess.Mode, session.ModeAuto)
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
				t.Fatalf("member Read result leaked=%t, want %t (an auto member must be gated by the checker): %q", leaked, tc.wantLeak, read.Content)
			}
		})
	}
}
