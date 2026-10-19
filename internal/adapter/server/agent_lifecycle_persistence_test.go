package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type agentLifecycleWriteTool struct{ lifecycleTool }

func (t *agentLifecycleWriteTool) Spec() tool.ToolSpec {
	spec := t.lifecycleTool.Spec()
	spec.Name = "Write"
	return spec
}

type agentLifecycleStore struct {
	*memstore.Store
	loads, saves int
	failCreate   bool
}

func (s *agentLifecycleStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loads++
	return s.Store.Load(ctx, id)
}

func (s *agentLifecycleStore) Save(ctx context.Context, sess *session.Session) error {
	s.saves++
	return s.Store.Save(ctx, sess)
}

func (s *agentLifecycleStore) Create(ctx context.Context, sess *session.Session) error {
	if s.failCreate {
		return errors.New("injected create failure")
	}
	return s.Store.Create(ctx, sess)
}

func TestAgentSuccessorAppliesFactoryRestrictionsAfterRestart(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "fork"
		if clear {
			name = "clear"
		}
		for _, failure := range []string{"", "wider-mode", "wider-limit", "persist"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
				store := memstore.New()
				creator := newAgentDefRetryService(t, store)
				creator.cfg.AgentDefSessionEngine = func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
					res := agentDefinitionEngineResult("reviewer")
					res.Limits = &session.Limits{MaxTurns: 10, MaxToolCalls: 10}
					res.Authority.CapabilitySet = governance.CapabilitySet{Tools: []string{"Write"}, FileSystem: true}
					ceiling := res.Authority.Clone().CapabilitySet
					res.Authority.Ceiling = &ceiling
					return res, nil
				}
				source, err := creator.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("source"), WithAgentDefinitionName("reviewer"))
				if err != nil {
					t.Fatal(err)
				}
				creator.Close()

				// A fresh service represents restart with a tighter definition. The
				// source remains default/10; only the unpublished successor may tighten.
				svc := newAgentDefRetryService(t, store)
				svc.cfg.NewID = func() session.SessionID { return "successor" }
				closed, calls := 0, 0
				write := &agentLifecycleWriteTool{}
				svc.cfg.AgentDefSessionEngine = func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, limits session.Limits, _ string) (SessionEngineResult, error) {
					calls++
					if mode != session.ModeDefault || limits != source.Limits {
						t.Fatalf("factory input = %s/%+v, want inherited default/10", mode, limits)
					}
					catalog := tool.NewCatalog()
					catalog.MustRegister(write)
					res := agentDefinitionEngineResult("reviewer")
					res.Engine = agent.NewEngine(agent.Deps{
						LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("write", "Write", []byte(`{"path":"file","content":"no"}`))), mockllm.TextTurn("must not reach a second turn")),
						Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Store: store,
					})
					res.BuiltForMode = session.ModePlan
					res.Limits = &session.Limits{MaxTurns: 1, MaxToolCalls: 10}
					res.ProviderID, res.ModelID, res.ReasoningEffort = "effective-provider", "raw/vendor:model@rev", "low"
					// A freshly resolved wider grant must never replace source authority.
					res.Authority.CapabilitySet.Tools = []string{"Write", "outside"}
					res.Authority.Ceiling.Tools = []string{"Write", "outside"}
					res.Close = func() error { closed++; return nil }
					if failure == "wider-mode" {
						res.BuiltForMode = session.ModeAccept
					}
					if failure == "wider-limit" {
						res.Limits.MaxToolCalls = 11
					}
					return res, nil
				}
				if failure == "persist" {
					svc.cfg.Store = &agentLifecycleStore{Store: store, failCreate: true}
				}
				var id session.SessionID
				if clear {
					id, err = svc.ClearSessionSuccessor(ctx, source.ID, SuccessorPlacement{})
				} else {
					id, err = svc.ForkSessionSuccessor(ctx, ForkSuccessorRequest{Source: source.ID})
				}
				if failure != "" {
					if err == nil || id != "" || closed != 1 || len(svc.sessionEngines) != 0 {
						t.Fatalf("failed successor = %q, %v, closes=%d, registrations=%d", id, err, closed, len(svc.sessionEngines))
					}
					if _, err := store.Load(ctx, "successor"); err == nil {
						t.Fatal("failed successor was published")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					successor, err := store.Load(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if successor.Mode != session.ModePlan || successor.Limits.MaxTurns != 1 || successor.AgentCreateIntent != "" ||
						successor.ProviderID != "effective-provider" || successor.ModelID != "raw/vendor:model@rev" || successor.ReasoningEffort != "low" ||
						!reflect.DeepEqual(successor.Authority, source.Authority) {
						t.Fatalf("successor configuration = %+v", successor)
					}
					run, err := svc.StartRun(ctx, id, "try to write")
					if err != nil {
						t.Fatal(err)
					}
					denied, limited := false, false
					for ev := range run.Events() {
						if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "plan mode") {
							denied = true
						}
						if ev.Result != nil && ev.Result.Stop == session.StopMaxTurns {
							limited = true
						}
					}
					svc.FinishRun(id, run)
					if !denied || !limited || write.calls.Load() != 0 {
						t.Fatalf("execution: plan denial=%v, turn cap=%v, writes=%d", denied, limited, write.calls.Load())
					}
				}
				if calls != 1 {
					t.Fatalf("factory calls = %d, want 1", calls)
				}
				unchanged, err := store.Load(ctx, source.ID)
				if err != nil || unchanged.Mode != source.Mode || unchanged.Limits != source.Limits || !reflect.DeepEqual(unchanged.Authority, source.Authority) {
					t.Fatalf("source changed: %+v, %v", unchanged, err)
				}
			})
		}
	}
}

func TestAgentRuntimeRebuildPreservesFixedConfiguration(t *testing.T) {
	for _, change := range []string{"none", "mode", "limits", "provider", "model", "effort"} {
		t.Run(change, func(t *testing.T) {
			store := memstore.New()
			svc := newAgentDefRetryService(t, store)
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
			revision := uint64(1)
			closed := map[uint64]int{}
			svc.cfg.OperationRevision = func(context.Context) (uint64, bool) { return revision, true }
			svc.cfg.AgentDefSessionEngine = func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
				builtRevision := revision
				res := agentDefinitionEngineResult("reviewer")
				res.RuntimeRevision = revision
				res.Limits = &session.Limits{MaxTurns: 10}
				res.ProviderID, res.ModelID, res.ReasoningEffort = "provider", "model", "low"
				res.Close = func() error { closed[builtRevision]++; return nil }
				if revision == 2 {
					switch change {
					case "mode":
						res.BuiltForMode = session.ModePlan
					case "limits":
						res.Limits.MaxTurns = 1
					case "provider":
						res.ProviderID = "different"
					case "model":
						res.ModelID = "different"
					case "effort":
						res.ReasoningEffort = "high"
					}
				}
				return res, nil
			}
			sess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("reviewer"))
			if err != nil {
				t.Fatal(err)
			}
			before := *sess
			// Run admission lazily binds usage attribution even when no rebuild is
			// needed; it must not change any lifetime configuration or authority.
			before.SetUsageAttribution(before.ProviderID, before.ModelID)
			prior := svc.sessionEngines[sess.ID]
			revision = 2
			_, _, _, err = svc.engineAndEnvironmentFor(ctx, sess)
			if change == "none" {
				if err != nil || svc.sessionEngines[sess.ID] == prior || closed[1] != 1 || closed[2] != 0 {
					t.Fatalf("compatible rebuild: err=%v, closes=%v", err, closed)
				}
			} else if !errors.Is(err, ErrFailedPrecondition) || svc.sessionEngines[sess.ID] != prior || closed[1] != 0 || closed[2] != 1 {
				t.Fatalf("contradictory rebuild: err=%v, closes=%v", err, closed)
			}
			if !reflect.DeepEqual(*sess, before) {
				t.Fatal("rebuild changed the live aggregate")
			}
			persisted, err := store.Load(ctx, sess.ID)
			if err != nil || persisted.Mode != before.Mode || persisted.Limits != before.Limits || persisted.ProviderID != before.ProviderID || persisted.ModelID != before.ModelID || persisted.ReasoningEffort != before.ReasoningEffort {
				t.Fatalf("rebuild changed durable configuration: %+v, %v", persisted, err)
			}
		})
	}
}

func TestAgentEnrollmentOwnershipPrecedesBindingGuard(t *testing.T) {
	store := &agentLifecycleStore{Store: memstore.New()}
	svc := newAgentDefRetryService(t, store.Store)
	svc.cfg.Store = store
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
	bound, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bound.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := bound.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, bound); err != nil {
		t.Fatal(err)
	}
	svc.cfg.SharedEngineRoot = "/workspace"
	ordinary, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	var attachments int32
	svc.cfg.MCPBroker = countingBrokerService{inner: runtime, attachCalled: &attachments}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "foreign"})
	for name, call := range map[string]func(context.Context, session.SessionID) (WorkspaceEnrollmentProjection, error){
		"connect": svc.ConnectWorkspaceServices,
		"cancel": func(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
			return svc.CancelWorkspaceEnrollment(ctx, id, "enrollment")
		},
		"retry": func(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
			return svc.RetryWorkspaceEnrollment(ctx, id, "enrollment")
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, id := range []session.SessionID{bound.ID, ordinary.ID, "absent"} {
				store.loads, store.saves = 0, 0
				projection, err := call(foreign, id)
				if !errors.Is(err, ErrNotFound) || err.Error() != ErrNotFound.Error() || projection != (WorkspaceEnrollmentProjection{}) || store.loads != 1 || store.saves != 0 {
					t.Fatalf("foreign %q: projection=%+v, err=%v, loads=%d saves=%d", id, projection, err, store.loads, store.saves)
				}
			}
			store.loads, store.saves = 0, 0
			if _, err := call(ctx, bound.ID); !errors.Is(err, ErrInvalidArgument) || store.loads != 1 || store.saves != 0 {
				t.Fatalf("owner rejection: err=%v, loads=%d saves=%d", err, store.loads, store.saves)
			}
			if got := atomic.LoadInt32(&attachments); got != 0 {
				t.Fatalf("rejected enrollment attached broker %d times", got)
			}
			persisted, err := store.Load(ctx, bound.ID)
			if err != nil || persisted.State != session.StateCompleted {
				t.Fatalf("rejection reopened session: %+v, %v", persisted, err)
			}
		})
	}
}
