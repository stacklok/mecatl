package agent

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestResumeApprovalMigratedSystemTempSummaryUsesDecodedStoredCall(t *testing.T) {
	data := []byte(`{
		"id":"legacy-system-temp", "state":"awaiting", "mode":"default", "limits":{}, "counters":{"Turns":1}, "created_at":"2025-01-02T03:04:05Z",
		"messages":[
			{"role":"assistant","tool_calls":[{"ID":"provider-reused","Name":"Shell","Args":{"command":"echo earlier"}}]},
			{"role":"tool","tool_result":{"CallID":"provider-reused","Content":"already ran"}},
			{"role":"assistant","tool_calls":[{"ID":"provider-reused","Name":"Shell","Args":{"command":"echo exact && echo done","timeout_ms":1000,"temp_scope":"system"}},{"ID":"other","Name":"Shell","Args":{"command":"must not run"}}]}
		],
		"pending":{"AskID":"separate-session-control:ask","Tool":"Shell","Args":{"command_summary":"old abbreviated display"},"Reason":"system scope","call":"provider-reused","origin":"permission"},
		"environment_ref":{"Kind":"mem","ID":"legacy-workspace","Revision":"v1"}
	}`)
	s, err := sessnap.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	ask, ok := s.PendingAsk()
	if !ok || ask.Call == "provider-reused" {
		t.Fatal("legacy pending was not translated")
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
	if bytes.Equal(before, data) {
		t.Fatal("fixture was not migrated before resume")
	}
}
