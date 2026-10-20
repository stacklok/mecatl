package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
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

func TestAbandonedContinuationRealCompositionLoadAndPrompt(t *testing.T) {
	ctx := t.Context()
	workspace, storeDir := t.TempDir(), t.TempDir()
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("fresh-provider", "Write", []byte(`{"path":"old.txt","content":"new work"}`))),
		mockllm.TextTurn("new prompt completed"),
	)
	cfg := isolateConfig(t, Config{Workspace: workspace, StoreDir: storeDir, MemoryDir: t.TempDir(), NoSoul: true,
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(Config, string, string, string) port.LLMProvider { return provider },
	})
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	created, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := sessnap.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	delete(wire, "identity_schema")
	wire["state"] = json.RawMessage(`"awaiting"`)
	wire["messages"] = json.RawMessage(`[{"role":"assistant","tool_calls":[{"ID":"old-provider","Name":"Write","Args":{"path":"old.txt","content":"abandoned"}},{"ID":"old-provider","Name":"Write","Args":{"path":"other.txt","content":"abandoned"}}]}]`)
	wire["pending"] = json.RawMessage(`{"AskID":"obsolete-ask","Tool":"Write","call":"old-provider","origin":"permission"}`)
	encoded, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := sessnap.Unmarshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, migrated); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(storeDir, "sid-v1", "*.session.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("snapshot fixture paths: %v %v", paths, err)
	}
	current, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(current, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["snapshot"] = encoded
	current, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], current, 0600); err != nil {
		t.Fatal(err)
	}
	legacyApproval, err := eventcodec.Unmarshal([]byte(`{"Type":"approval","Approval":{"AskID":"obsolete-ask","Call":"old-provider","Tool":"Write","Origin":"permission","Verdict":"allow_always","AllowAlways":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, created.ID, legacyApproval); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		loaded, err := built.Service.LoadSession(ctx, created.ID)
		if err != nil || loaded.State != session.StateIdle || provider.Calls() != 0 {
			t.Fatalf("pure composition load = %+v %v model calls=%d", loaded, err, provider.Calls())
		}
		retained := 0
		for event, err := range store.Read(ctx, created.ID) {
			if err != nil {
				t.Fatal(err)
			}
			p := event.LegacyIdentity()
			if p == nil || !bytes.Equal(p.Payload, legacyApproval.LegacyIdentity().Payload) {
				t.Fatal("load rewrote or replaced poisoned durable approval")
			}
			retained++
		}
		if retained != 1 {
			t.Fatalf("poisoned durable approval was not retained: %d", retained)
		}
		grants := permstore.New()
		replayApprovals(store, permpolicy.NewPolicy(nil, grants), nil)(ctx, loaded)
		if len(grants.Rules(created.ID)) != 0 {
			t.Fatal("loaded abandoned source could establish a permission grant from the durable approval")
		}
		if err := built.Service.Approve(ctx, created.ID, "obsolete-ask", session.VerdictAllowAlways); err == nil {
			t.Fatal("stale allow-always accepted")
		}
		if _, err := built.Service.RetryFailedRun(ctx, created.ID); err == nil {
			t.Fatal("stale retry accepted")
		}
		if err := store.Save(ctx, loaded); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(workspace, "old.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("load/control wrote old tool target: %v", err)
		}
	}
	run, err := built.Service.StartRun(ctx, created.ID, "separate new prompt")
	if err != nil {
		t.Fatal(err)
	}
	asked, completed := false, false
	for event := range run.Events() {
		if event.Ask != nil {
			asked = true
			if err := built.Service.Approve(ctx, created.ID, event.Ask.AskID, session.VerdictDeny); err != nil {
				t.Fatal(err)
			}
		}
		if event.Result != nil {
			completed = event.Result.Stop == session.StopEndTurn && event.Result.Error == ""
		}
	}
	built.Service.FinishRun(created.ID, run)
	if !asked || !completed || provider.Calls() != 2 {
		t.Fatalf("new prompt: asked=%v completed=%v calls=%d", asked, completed, provider.Calls())
	}
	if _, err := os.Stat(filepath.Join(workspace, "old.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale permission granted authority to new work: %v", err)
	}
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
	log, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	store := log
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
