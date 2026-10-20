package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type migratedControlPolicy struct {
	lifecycleAllowPolicy
	learns int
}

func (p *migratedControlPolicy) Learn(session.SessionID, session.ToolCall) { p.learns++ }

type migratedGuardrailTool struct{ executed []session.ToolCall }

func (*migratedGuardrailTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Shell", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*migratedGuardrailTool) ReadOnly() bool { return false }
func (t *migratedGuardrailTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.executed = append(t.executed, call)
	return session.NewToolResult(call.ID, "legacy hook action executed"), nil
}

type migratedGuardrailHooks struct{ pre, learned int }

func (h *migratedGuardrailHooks) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase == governance.PhasePreToolUse {
		h.pre++
		return governance.HookOutcome{Block: true, Message: "must not rerun the approved hook"}, nil
	}
	return governance.HookOutcome{}, nil
}
func (h *migratedGuardrailHooks) LearnHookApproval(context.Context, governance.HookEvent) {
	h.learned++
}

func migratedControlFixture(t *testing.T, name string, ref session.EnvironmentRef, legacyHook bool) *session.Session {
	t.Helper()
	raw, err := os.ReadFile("../../../engine/adapter/sessnap/testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	// Before contextual reviews, hook approvals had an action kind but no review
	// identity. Keep that supported executable control distinct from a lost review.
	if legacyHook {
		raw = bytes.Replace(raw, []byte(`"ReviewID":"review-1", `), nil, 1)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["kind"] = json.RawMessage(`"main"`)
	wire["environment_ref"], err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessnap.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestMigratedPlanHostResumeRetainsModeSemantics(t *testing.T) {
	for _, target := range []session.PermissionMode{session.ModeDefault, session.ModeAccept, session.ModePlan} {
		t.Run(string(target), func(t *testing.T) {
			store := memstore.New()
			placement := &revisionPlacementProvider{}
			sess := migratedControlFixture(t, "legacy-awaiting-plan", placement.binding("1").Ref, false)
			llm := mockllm.New(mockllm.TextTurn("approved plan executed"))
			policy := &migratedControlPolicy{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(agent.NewPresentPlanTool())
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: policy, Interactive: true, Store: store})
			svc, err := NewService(Config{Engine: engine, Store: store, PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root"})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			ask, ok := sess.PendingAsk()
			if !ok || ask.Origin != session.ApprovalOriginPlan || ask.Call == "plan-call" {
				t.Fatal("fixture did not retain a proven canonical plan ask")
			}
			for range 2 {
				if err := store.Save(t.Context(), sess); err != nil {
					t.Fatal(err)
				}
				sess, err = svc.LoadSession(t.Context(), sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				loadedAsk, ok := sess.PendingAsk()
				if !ok || !reflect.DeepEqual(ask, loadedAsk) || sess.Mode != session.ModePlan || llm.Calls() != 0 {
					t.Fatal("load changed or executed the pending plan")
				}
			}
			events, err := svc.ApprovePlan(t.Context(), sess.ID, target, "")
			if err != nil {
				t.Fatal(err)
			}
			var stops []session.StopReason
			var paired bool
			for event := range events {
				if event.ToolResult != nil && event.ToolResult.CallID == ask.Call {
					paired = true
				}
				if event.Result != nil {
					stops = append(stops, event.Result.Stop)
					if event.Result.Error != "" {
						t.Fatalf("plan continuation failed: %s", event.Result.Error)
					}
				}
			}
			wantStops, wantCalls := []session.StopReason{session.StopPlanApproved, session.StopEndTurn}, 1
			if target == session.ModePlan {
				wantStops, wantCalls = []session.StopReason{session.StopPlanIterate}, 0
			}
			loaded, err := store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !paired || !reflect.DeepEqual(stops, wantStops) || llm.Calls() != wantCalls || loaded.Mode != target || loaded.State != session.StateCompleted || policy.learns != 0 {
				t.Fatalf("migrated plan semantics: paired=%v stops=%v calls=%d mode=%s state=%s learned=%d", paired, stops, llm.Calls(), loaded.Mode, loaded.State, policy.learns)
			}
			if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigratedGuardrailHostResumeRetainsScopeAndRestartSafety(t *testing.T) {
	for _, legacyHook := range []bool{true, false} {
		name := "contextual-review-unavailable"
		if legacyHook {
			name = "legacy-hook"
		}
		t.Run(name, func(t *testing.T) {
			store := memstore.New()
			placement := &revisionPlacementProvider{}
			sess := migratedControlFixture(t, "legacy-awaiting-guardrail", placement.binding("1").Ref, legacyHook)
			llm := mockllm.New(mockllm.TextTurn("continued after guardrail resolution"))
			policy, hooks := &migratedControlPolicy{}, &migratedGuardrailHooks{}
			shell := &migratedGuardrailTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(shell)
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: policy, Hooks: hooks, Interactive: true, Store: store})
			svc, err := NewService(Config{Engine: engine, Store: store, PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root"})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			ask, ok := sess.PendingAsk()
			if !ok || ask.Origin != session.ApprovalOriginHookGuardrail || ask.Call == "shell-call" || ask.Guardrail == nil || !ask.Guardrail.SessionOnly || !ask.Guardrail.RepeatAvailable {
				t.Fatal("fixture did not retain a proven scoped guardrail control")
			}
			retainedArgs := bytes.Clone(sess.Conversation.Messages[0].ToolCalls[0].Args)
			for range 2 {
				if err := store.Save(t.Context(), sess); err != nil {
					t.Fatal(err)
				}
				sess, err = svc.LoadSession(t.Context(), sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				loadedAsk, ok := sess.PendingAsk()
				if !ok || !reflect.DeepEqual(ask, loadedAsk) || llm.Calls() != 0 || len(shell.executed) != 0 {
					t.Fatal("load changed guardrail scope or executed the continuation")
				}
			}
			for _, wrong := range []agent.ApprovalResolution{
				{AskID: ask.AskID, ReviewID: "different-review", Kind: ask.Guardrail.Kind, Verdict: session.VerdictAllowAlways},
				{AskID: ask.AskID, ReviewID: ask.Guardrail.ReviewID, Kind: session.GuardrailApprovalResultRelease, Verdict: session.VerdictAllowOnce},
			} {
				if _, err := svc.ResolveApprovalRun(t.Context(), sess.ID, wrong, ""); !errors.Is(err, agent.ErrApprovalIntentMismatch) {
					t.Fatalf("guardrail scope mismatch accepted: %v", err)
				}
				unchanged, err := store.Load(t.Context(), sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				pending, ok := unchanged.PendingAsk()
				if !ok || !reflect.DeepEqual(pending, ask) || llm.Calls() != 0 || len(shell.executed) != 0 {
					t.Fatal("invalid control consumed or executed the scoped ask")
				}
			}
			run, err := svc.ResolveApprovalRun(t.Context(), sess.ID, agent.ApprovalResolution{AskID: ask.AskID, ReviewID: ask.Guardrail.ReviewID, Kind: ask.Guardrail.Kind, Verdict: session.VerdictAllowAlways}, "")
			if err != nil || run == nil {
				t.Fatalf("matching guardrail control: %v", err)
			}
			var result *session.ToolResult
			var stop session.StopReason
			for event := range run.Events() {
				if event.ToolResult != nil && event.ToolResult.CallID == ask.Call {
					result = event.ToolResult
				}
				if event.Result != nil {
					stop = event.Result.Stop
				}
			}
			svc.FinishRun(sess.ID, run)
			if result == nil || stop != session.StopEndTurn || llm.Calls() != 1 || hooks.pre != 0 || hooks.learned != 0 || policy.learns != 0 {
				t.Fatalf("scoped guardrail resume: result=%+v stop=%s calls=%d hook=%+v permissions=%d", result, stop, llm.Calls(), hooks, policy.learns)
			}
			if legacyHook {
				if len(shell.executed) != 1 || shell.executed[0].ID != ask.Call || !bytes.Equal(shell.executed[0].Args, retainedArgs) || result.IsError {
					t.Fatal("legacy hook resume did not execute exactly the retained action without a new grant")
				}
			} else if len(shell.executed) != 0 || !result.IsError || !strings.Contains(result.Content, "contextual guardrail approval state is unavailable") {
				t.Fatal("persisted scope fabricated lost contextual review authority")
			}
		})
	}
}
