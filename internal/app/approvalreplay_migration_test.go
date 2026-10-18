package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type migratedApprovalWrite struct{ calls int }

func (*migratedApprovalWrite) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "Write"} }
func (*migratedApprovalWrite) ReadOnly() bool      { return false }
func (w *migratedApprovalWrite) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	w.calls++
	return session.NewToolResult(c.ID, "written"), nil
}

func TestMigratedAllowAlwaysResumeSaveReloadReplay(t *testing.T) {
	s, err := sessnap.Unmarshal([]byte(`{"id":"migrated-approval","state":"awaiting","mode":"default","counters":{"Turns":1},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-era-call","Name":"Write","Args":{"path":"note.txt","content":"x"}}]}],"pending":{"AskID":"old-ask","Tool":"Write","Args":{"path":"note.txt","content":"x"},"call":"provider-era-call","origin":"permission"},"environment_ref":{"Kind":"mem","ID":"/ws","Revision":"in-tree-v1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ask, ok := s.PendingAsk()
	if !ok || ask.Call == "provider-era-call" || s.Conversation.Messages[0].Legacy == nil {
		t.Fatal("fixture did not migrate a proven pending call")
	}
	log := memstore.NewEventLog()
	// An old log association cannot authorize the migrated snapshot occurrence.
	old, err := eventcodec.Unmarshal([]byte(`{"Type":"approval","Approval":{"Call":"provider-era-call","Tool":"Write","Origin":"permission","Verdict":"allow_always","AllowAlways":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if old.LegacyIdentity() == nil {
		t.Fatal("fixture lost legacy event provenance")
	}
	if err := log.Append(t.Context(), s.ID, old); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(t.Context(), s.ID, session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{Call: "provider-era-call", Tool: "Write", Origin: session.ApprovalOriginPermission, Verdict: session.VerdictStringAllowAlways, AllowAlways: true}}); err != nil {
		t.Fatal(err)
	}
	// A log-local legacy identity is not proof even if its projected spelling
	// happens to collide with a snapshot's canonical ID.
	collisionLog := memstore.NewEventLog()
	collision := session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{Call: ask.Call, Tool: "Write", Origin: session.ApprovalOriginPermission, Verdict: session.VerdictStringAllowAlways, AllowAlways: true}}
	collision = collision.WithLegacyIdentity(*old.LegacyIdentity())
	if err := collisionLog.Append(t.Context(), s.ID, collision); err != nil {
		t.Fatal(err)
	}
	spy := &spyPolicy{}
	replayApprovals(collisionLog, spy, nil)(t.Context(), s)
	if len(spy.learned) != 0 {
		t.Fatal("unproven legacy event ID collision granted authority")
	}
	before := permstore.New()
	replayApprovals(log, permpolicy.NewPolicy(nil, before), nil)(t.Context(), s)
	if len(before.Rules(s.ID)) != 0 {
		t.Fatal("provider-era event granted authority")
	}
	w := &migratedApprovalWrite{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(w)
	e := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, permstore.New())})
	env := tool.MustEnvironment(s.EnvironmentRef, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	run := e.ResumeApproval(t.Context(), s, env, ask.AskID, session.VerdictAllowAlways)
	approved, finished := false, false
	for ev := range run.Events() {
		if ev.Approval != nil {
			approved = ev.Approval.Call == ask.Call && ev.Approval.AllowAlways
		}
		if ev.Result != nil {
			finished = ev.Result.Stop == session.StopEndTurn && ev.Result.Error == ""
		}
		if err := log.Append(t.Context(), s.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !approved || !finished || w.calls != 1 {
		t.Fatalf("resume: approval=%v finished=%v calls=%d", approved, finished, w.calls)
	}
	store := memstore.New()
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	s, err = store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Conversation.Messages[0].Legacy == nil {
		t.Fatal("test lost persistent verification provenance")
	}
	rules := permstore.New()
	replayApprovals(log, permpolicy.NewPolicy(nil, rules), port.NopDiagnostics{})(t.Context(), s)
	if got := rules.Rules(s.ID); len(got) != 1 || got[0].Tool != "Write" || got[0].Pattern != "note.txt" {
		t.Fatalf("replayed rules = %+v", got)
	}
}
