package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// callerSeparationTestEnv wraps a throwaway memfs Workspace into an Environment
// for these tests' non-FS tools (InspectSubagent/InspectMember/Ask).
func callerSeparationTestEnv() tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, memfs.NewWorkspace("/ws"), nil)
}

type callerSeparationAskTool struct{}

func (callerSeparationAskTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "Ask"} }
func (callerSeparationAskTool) ReadOnly() bool      { return true }
func (callerSeparationAskTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "done"), nil
}

func callerSeparationLiveService(t *testing.T) (*server.Service, context.Context, context.Context) {
	t.Helper()
	catalog := tool.NewCatalog()
	catalog.MustRegister(callerSeparationAskTool{})
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("ask", "Ask", json.RawMessage(`{}`)))),
			Catalog: catalog,
			Policy:  permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Ask}}, nil),
			Model:   "test-model",
		}),
		Store:             memstore.New(),
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(0, 0) },
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc,
		session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}),
		session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser})
}

func callerSeparationAwaitingRun(owner context.Context, t *testing.T, svc *server.Service) (*session.Session, *agent.Run, string) {
	t.Helper()
	sess, err := svc.CreateSession(owner, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(owner, sess.ID, "ask")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			return sess, run, ev.Ask.AskID
		}
	}
	t.Fatal("run ended without a permission ask")
	return nil, nil, ""
}

// TestCallerSeparation_Scenario3_RepeatedForeignPromptIsNotFound pins AC3.1:
// every prompt request re-evaluates the session owner before run entry.
func TestCallerSeparation_Scenario3_RepeatedForeignPromptIsNotFound(t *testing.T) {
	svc, aliceCtx, bobCtx := callerSeparationLiveService(t)
	sess, err := svc.CreateSession(aliceCtx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.GetSession(aliceCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.StartRun(bobCtx, sess.ID, "foreign"); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign StartRun attempt %d = %v, want ErrNotFound", i+1, err)
		}
	}
	after, err := svc.GetSession(aliceCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("foreign prompts changed Alice history: before=%d after=%d", len(before.Conversation.Messages), len(after.Conversation.Messages))
	}
	if _, ok := svc.LookupRun(sess.ID); ok {
		t.Fatal("foreign prompts created a live run")
	}
}

// TestCallerSeparation_Scenario3_LiveRunVerbsAreOwnerChecked pins AC3.2: live
// run operations authorize before inspecting or signalling the in-memory run.
func TestCallerSeparation_Scenario3_LiveRunVerbsAreOwnerChecked(t *testing.T) {
	svc, aliceCtx, bobCtx := callerSeparationLiveService(t)
	sess, run, askID := callerSeparationAwaitingRun(aliceCtx, t, svc)
	defer func() {
		run.Cancel()
		for range run.Events() {
		}
		svc.FinishRun(sess.ID, run)
	}()

	// The owner sees the ordinary control-path responses and may persist the
	// parked run. A foreign caller below must instead see only ErrNotFound.
	svc.Persist(aliceCtx, sess.ID)
	before, err := svc.GetSession(aliceCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != session.StateAwaiting {
		t.Fatalf("owner Persist state = %s, want awaiting", before.State)
	}
	if _, err := svc.SetMode(aliceCtx, sess.ID, session.ModePlan); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("owner SetMode while awaiting = %v, want ErrInvalidArgument", err)
	}
	if _, err := svc.ApprovePlan(aliceCtx, sess.ID, session.ModeDefault, ""); !errors.Is(err, server.ErrNotAwaitingPlan) {
		t.Fatalf("owner ApprovePlan on a live non-plan run = %v, want ErrNotAwaitingPlan", err)
	}
	if _, err := svc.ApproveRun(bobCtx, sess.ID, askID, session.VerdictAllowOnce, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign ApproveRun allow = %v, want ErrNotFound", err)
	}
	if _, err := svc.ApproveRun(bobCtx, sess.ID, askID, session.VerdictDeny, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign ApproveRun deny = %v, want ErrNotFound", err)
	}
	if err := svc.Cancel(bobCtx, sess.ID, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign Cancel = %v, want ErrNotFound", err)
	}
	svc.Persist(bobCtx, sess.ID)
	if _, err := svc.SetMode(bobCtx, sess.ID, session.ModePlan); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign SetMode = %v, want ErrNotFound", err)
	}
	if _, err := svc.ApprovePlan(bobCtx, sess.ID, session.ModeDefault, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign ApprovePlan = %v, want ErrNotFound", err)
	}
	unchanged, err := svc.GetSession(aliceCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.State != before.State || unchanged.Mode != before.Mode || len(unchanged.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("foreign live-run verbs changed Alice's persisted state: before=%s/%s/%d after=%s/%s/%d", before.State, before.Mode, len(before.Conversation.Messages), unchanged.State, unchanged.Mode, len(unchanged.Conversation.Messages))
	}

	if _, err := svc.ApproveRun(aliceCtx, sess.ID, askID, session.VerdictDeny, ""); err != nil {
		t.Fatalf("owner ApproveRun deny: %v", err)
	}

	ownerSvc, ownerCtx, _ := callerSeparationLiveService(t)
	ownerSess, ownerRun, _ := callerSeparationAwaitingRun(ownerCtx, t, ownerSvc)
	if err := ownerSvc.Cancel(ownerCtx, ownerSess.ID, ""); err != nil {
		t.Fatalf("owner Cancel: %v", err)
	}
	for range ownerRun.Events() {
	}
	ownerSvc.FinishRun(ownerSess.ID, ownerRun)
}

// TestCallerSeparation_Scenario3_ModelFacingHandlesAreOwnerChecked pins AC3.3:
// a model cannot use another caller's child or team transcript handle.
func TestCallerSeparation_Scenario3_ModelFacingHandlesAreOwnerChecked(t *testing.T) {
	store := memstore.New()
	child := session.New("subagent-alice", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := child.RestoreLabels(&session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := child.RecordUserPrompt("ALICE SECRET", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	inspect := agent.NewInspectSubagentToolWithOwnership(store, true)
	member := session.New(agent.MemberSessionID("team-alice", "researcher"), session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := member.RestoreLabels(&session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := member.RecordUserPrompt("TEAM SECRET", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	aliceCtx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	bobCtx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser})
	ownerRes, err := inspect.Execute(aliceCtx, session.NewToolCall("owner-inspect", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-alice"}`)), callerSeparationTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	if ownerRes.IsError || !strings.Contains(ownerRes.Content, "ALICE SECRET") {
		t.Fatalf("owner subagent inspect = %+v, want Alice's transcript", ownerRes)
	}
	res, err := inspect.Execute(bobCtx, session.NewToolCall("inspect", "InspectSubagent", json.RawMessage(`{"agent_id":"subagent-alice"}`)), callerSeparationTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || strings.Contains(res.Content, "ALICE SECRET") || !strings.Contains(res.Content, "no transcript") {
		t.Fatalf("foreign subagent inspect = %+v, want the ordinary absent-handle result", res)
	}
	inspectMember := agent.NewInspectMemberToolWithOwnership(store, true)
	ownerTeamRes, err := inspectMember.Execute(aliceCtx, session.NewToolCall("owner-inspect-team", "InspectMember", json.RawMessage(`{"team_id":"team-alice","member":"researcher"}`)), callerSeparationTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	if ownerTeamRes.IsError || !strings.Contains(ownerTeamRes.Content, "TEAM SECRET") {
		t.Fatalf("owner team inspect = %+v, want Alice's transcript", ownerTeamRes)
	}
	teamRes, err := inspectMember.Execute(bobCtx, session.NewToolCall("inspect-team", "InspectMember", json.RawMessage(`{"team_id":"team-alice","member":"researcher"}`)), callerSeparationTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !teamRes.IsError || strings.Contains(teamRes.Content, "TEAM SECRET") || !strings.Contains(teamRes.Content, "no transcript") {
		t.Fatalf("foreign team inspect = %+v, want the ordinary absent-handle result", teamRes)
	}
}

// TestCallerSeparation_Scenario3_ForeignLiveRunReplayIsNotFound pins AC3.5:
// ownership is checked again after the owner's run reaches its terminal state.
func TestCallerSeparation_Scenario3_ForeignLiveRunReplayIsNotFound(t *testing.T) {
	svc, aliceCtx, bobCtx := callerSeparationLiveService(t)
	sess, run, _ := callerSeparationAwaitingRun(aliceCtx, t, svc)
	if err := svc.Cancel(bobCtx, sess.ID, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign Cancel while live = %v, want ErrNotFound", err)
	}
	run.Cancel()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	if err := svc.Cancel(bobCtx, sess.ID, ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign Cancel after completion = %v, want ErrNotFound", err)
	}
}

// TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerChecked pins AC3.6:
// Bob cannot open Alice's live event subscription (the gRPC StreamSessionLive
// feed) — the refusal is absence-shaped and registers no subscriber, so no
// event Alice's run produces is ever fanned to him. Alice can open her own.
func TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerChecked(t *testing.T) {
	svc, aliceCtx, bobCtx := callerSeparationLiveService(t)
	sess, err := svc.CreateSession(aliceCtx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	if ch, unsub, err := svc.Subscribe(bobCtx, sess.ID); !errors.Is(err, server.ErrNotFound) {
		if unsub != nil {
			unsub()
		}
		t.Fatalf("foreign Subscribe = (%v, %v), want ErrNotFound", ch, err)
	}

	aliceCh, aliceUnsub, err := svc.Subscribe(aliceCtx, sess.ID)
	if err != nil {
		t.Fatalf("owner Subscribe: %v", err)
	}
	defer aliceUnsub()

	// The behavioral half: Bob's refused Subscribe must have registered NO
	// subscriber at all. Publish on Alice's session and confirm the only
	// channel that ever sees it is Alice's own.
	probe := session.Event{Type: session.EvNoProgress, Text: "owner-checked-live-probe"}
	svc.PublishSessionEvent(sess.ID, probe)

	select {
	case got, ok := <-aliceCh:
		if !ok || got.Text != probe.Text {
			t.Fatalf("Alice's subscription = (%+v, %v), want the probe event", got, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("Alice's subscription never received the probe event")
	}
}
