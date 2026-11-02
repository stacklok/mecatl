package agent

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestResumeApprovalSystemTempSummaryUsesStoredCall(t *testing.T) {
	s := session.New("system-temp", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", []session.ToolCall{session.NewToolCall("earlier", "Shell", []byte(`{"command":"echo earlier"}`))})); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult("earlier", "already ran")}); err != nil {
		t.Fatal(err)
	}
	calls := []session.ToolCall{
		session.NewToolCall("target", "Shell", []byte(`{"command":"echo exact && echo done","timeout_ms":1000,"temp_scope":"system"}`)),
		session.NewToolCall("other", "Shell", []byte(`{"command":"must not run"}`)),
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", calls)); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{AskID: "separate-session-control:ask", Tool: "Shell", Call: calls[0].ID, Args: []byte(`{"command_summary":"old abbreviated display"}`), Reason: "system scope", Origin: session.ApprovalOriginPermission}
	if err := s.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	before, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	s, err = sessnap.Unmarshal(before)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
	catalog := tool.NewCatalog()
	catalog.MustRegister(NewShellTool())
	engine := NewEngine(Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog,
		Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Shell", Effect: governance.Allow}, {Tool: shellSystemTempToolName, Effect: governance.Ask}}, nil)})
	env := tool.MustEnvironment(s.EnvironmentRef, shellWS, testReadLedger(), runner)
	run := engine.ResumeApproval(t.Context(), s, env, ask.AskID, session.VerdictAllowOnce)
	var resolved session.ToolCallID
	for ev := range run.Events() {
		if ev.Type == session.EvApproval && ev.Approval != nil {
			resolved = ev.Approval.Call
		}
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("resumed exact capability approval unexpectedly reasked")
		}
	}
	if runner.command != "echo exact && echo done" || runner.scope != tool.TemporaryScopeSystem || resolved != ask.Call {
		t.Fatal("summary or wrong occurrence reached execution")
	}
	if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}
