package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type identityGuardStore struct {
	*memstore.Store
	saves int
}

func (s *identityGuardStore) Save(ctx context.Context, sess *session.Session) error {
	s.saves++
	return s.Store.Save(ctx, sess)
}

type identityGuardBroker struct{ calls int }

func (b *identityGuardBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.calls++
	return nil, "", brokercontract.ErrStateUnavailable
}
func (b *identityGuardBroker) DeleteSession(context.Context, session.SessionID) (brokercontract.DeleteOutcome, error) {
	b.calls++
	return brokercontract.DeleteDeleted, nil
}

type identityContinuationTool struct{ executed []session.ToolCall }

func (*identityContinuationTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Write", Schema: []byte(`{"type":"object"}`)}
}
func (*identityContinuationTool) ReadOnly() bool { return false }
func (t *identityContinuationTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.executed = append(t.executed, call)
	return session.NewToolResult(call.ID, "written"), nil
}

type identityGuardCommands struct {
	commandDiscoverySpy
	activations int
}

func (c *identityGuardCommands) BorrowWithExecutionWorkspace(ctx context.Context, id session.SessionID, owner *session.Principal, profile string, _ ExecutionWorkspaceAcquirer) (CommandSourceBinding, func(), error) {
	return c.Borrow(ctx, id, owner, profile)
}

func (c *identityGuardCommands) ActivateWithExecutionWorkspace(context.Context, session.SessionID, *session.Principal, string, ExecutionWorkspaceAcquirer) error {
	c.activations++
	return nil
}

func TestResumeApprovalHostCanonicalSameToolUsesRetainedArguments(t *testing.T) {
	store := memstore.New()
	write := &identityContinuationTool{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(write)
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: store})
	placement := &revisionPlacementProvider{}
	svc, err := NewService(Config{Engine: engine, Store: store, PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root"})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New("same-tool-host", session.ModeDefault, placement.binding("1").Ref, session.Limits{}, time.Unix(0, 0))
	calls := []session.ToolCall{
		session.NewToolCall("canonical-first", "Write", []byte(`{"path":"first"}`)),
		session.NewToolCall("canonical-target", "Write", []byte(`{"path":"target","content":"retained"}`)),
		session.NewToolCall("canonical-last", "Write", []byte(`{"path":"last"}`)),
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", calls)); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{AskID: "same-tool-host:control", Call: calls[1].ID, Tool: "Write", Args: []byte(`{"command_summary":"not executable"}`), Origin: session.ApprovalOriginPermission}
	if err := s.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	run, err := svc.ApproveRun(t.Context(), s.ID, ask.AskID, session.VerdictAllowOnce, "")
	if err != nil || run == nil {
		t.Fatalf("resume = %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(s.ID, run)
	if len(write.executed) != 1 || write.executed[0].ID != ask.Call || !bytes.Equal(write.executed[0].Args, calls[1].Args) {
		t.Fatal("host resumed a different occurrence or projection")
	}
	loaded, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestProvableLegacyHostApprovalSurvivesLoad(t *testing.T) {
	store := memstore.New()
	write := &identityContinuationTool{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(write)
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: store})
	placement := &revisionPlacementProvider{}
	svc, err := NewService(Config{Engine: engine, Store: store, PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sessnap.Unmarshal([]byte(`{"id":"legacy-host-ask","kind":"main","state":"awaiting","mode":"default","environment_ref":{"Kind":"mem","ID":"/same-root","Revision":"1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-old","Name":"Write","Args":{"path":"retained","content":"original"}}]}],"pending":{"AskID":"legacy-control","Tool":"Write","call":"provider-old","Args":{"display":"not executable"},"origin":"permission"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		s, err = svc.LoadSession(t.Context(), s.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	ask, ok := s.PendingAsk()
	if !ok || s.State != session.StateAwaiting || ask.Call == "provider-old" || len(write.executed) != 0 {
		t.Fatal("load did not preserve inert proven approval")
	}
	run, err := svc.ApproveRun(t.Context(), s.ID, ask.AskID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.FinishRun(s.ID, run)
	if len(write.executed) != 1 || write.executed[0].ID != ask.Call || !bytes.Equal(write.executed[0].Args, s.Conversation.Messages[0].ToolCalls[0].Args) {
		t.Fatal("legacy control did not execute exactly the proven occurrence and arguments")
	}
}

func TestProvableLegacyHostAuthorizationSurvivesLoad(t *testing.T) {
	now := func() time.Time { return time.Unix(1, 0) }
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, now, nil)
	s, err := sessnap.Unmarshal([]byte(`{"id":"authorization-session","kind":"main","state":"authorizing","mode":"default","external_binding":"broker-binding","environment_ref":{"Kind":"local","ID":"/repo","Revision":"in-tree-v1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-old","Name":"protected","Args":{"value":1}}]}],"pending_authorization":{"authorization":{"id":"authorization:1","display_name":"Calendar","binding":"opaque-binding","expires_at":"2030-01-01T00:00:00Z"},"call":{"id":"provider-old","name":"protected","args":"eyJ2YWx1ZSI6MX0="}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.store.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		s, err = f.svc.LoadSession(t.Context(), s.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	pending, ok := s.PendingAuthorization()
	if !ok || s.State != session.StateAuthorizing || pending.Call.ID == "provider-old" || f.attach.tool.calls.Load() != 0 {
		t.Fatal("load did not preserve inert proven authorization")
	}
	control := MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), s.ID, control)
	if err != nil || result.Run == nil {
		t.Fatalf("valid authorization: %+v %v", result, err)
	}
	events := drainLifecycleRun(t, f.svc, result)
	if f.attach.tool.calls.Load() != 1 {
		t.Fatal("valid authorization did not execute exactly once")
	}
	assertAuthorizationResultBeforeResolved(t, events, pending.Call.ID)
}

func TestAbandonedHostContinuationLoadsAndStaleControlsStayInert(t *testing.T) {
	for _, name := range []string{"ask", "authorization", "candidate-held", "candidate-held-event"} {
		t.Run(name, func(t *testing.T) {
			owner := &session.Principal{Issuer: "test", Subject: "owner"}
			ctx := session.WithPrincipal(t.Context(), owner)
			foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "foreign"})
			store := &identityGuardStore{Store: memstore.New()}
			placement := &revisionPlacementProvider{}
			broker := &identityGuardBroker{}
			commands := &identityGuardCommands{}
			learned, factories := 0, 0
			write := &identityContinuationTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(write)
			llm := mockllm.New(mockllm.TextTurn("new work completed"))
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: store})
			svc, err := NewService(Config{Engine: engine, Store: store, OwnershipEnforced: true,
				PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root", MCPBroker: broker, Commands: commands,
				ReplayApprovals: func(context.Context, *session.Session) { learned++ },
				SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
					return SessionEngineResult{Engine: engine}, nil
				},
				SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
					factories++
					return SessionEngineResult{Engine: engine}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"id":"abandoned-host","state":"awaiting","mode":"default","messages":[{"role":"assistant","text":"inspectable","tool_calls":[{"ID":"duplicate","Name":"Write","Args":{"path":"old"}},{"ID":"duplicate","Name":"Write","Args":{"path":"other"}}]}],"pending":{"ask_id":"held-ask","call":"duplicate","tool":"Write"}}`)
			if name == "authorization" {
				raw = []byte(`{"id":"abandoned-host","state":"authorizing","mode":"default","messages":[{"role":"assistant","tool_calls":[{"ID":"duplicate","Name":"Write"},{"ID":"duplicate","Name":"Write"}]}],"pending_authorization":{"authorization":{"id":"held-auth","binding":"private-binding"},"call":{"id":"duplicate","name":"Write","args":"e30="}}}`)
			} else if strings.HasPrefix(name, "candidate-") {
				raw, err = os.ReadFile("../../../engine/adapter/sessnap/testdata/" + name + ".json")
				if err != nil {
					t.Fatal(err)
				}
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			wire["kind"], _ = json.Marshal(session.SessionKindMain)
			wire["environment_ref"], _ = json.Marshal(placement.binding("1").Ref)
			wire["owner"], _ = json.Marshal(owner)
			raw, err = json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			s, err := sessnap.Unmarshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			const notice = "The prior continuation was abandoned during upgrade. Prior tool effects and completion may be unknown."
			for range 2 {
				store.saves = 0
				before, err := sessnap.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				for _, load := range []func(context.Context, session.SessionID) (*session.Session, error){svc.GetSession, svc.LoadSession} {
					loaded, err := load(ctx, s.ID)
					if err != nil || loaded.State != session.StateIdle {
						t.Fatalf("load = %+v, %v", loaded, err)
					}
					encoded, err := sessnap.Marshal(loaded)
					notices := 0
					for _, message := range loaded.Conversation.Messages {
						if message.Text == notice {
							notices++
						}
					}
					if err != nil || notices != 1 || bytes.Contains(encoded, []byte("identity_hold")) {
						t.Fatal("lost or duplicated abandonment notice")
					}
					if _, err := load(foreign, s.ID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("foreign inspection = %v", err)
					}
				}
				for action, control := range map[string]func() error{
					"retry":         func() error { _, err := svc.RetryFailedRun(ctx, s.ID); return err },
					"approve":       func() error { return svc.Approve(ctx, s.ID, "held-ask", session.VerdictAllowAlways) },
					"candidate ask": func() error { return svc.Approve(ctx, s.ID, "obsolete-ask", session.VerdictAllowAlways) },
					"recheck": func() error {
						_, err := svc.RecheckMCPAuthorization(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "held-auth"})
						return err
					},
					"cancel auth": func() error {
						_, err := svc.CancelMCPAuthorization(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "held-auth"})
						return err
					},
					"presentation": func() error {
						_, err := svc.MCPAuthorizationPresentation(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "held-auth"})
						return err
					},
				} {
					if err := control(); err == nil {
						t.Fatalf("stale %s accepted", action)
					}
				}
				if commands.activations != 0 || factories != 0 {
					t.Fatalf("pure load activated commands=%d factories=%d", commands.activations, factories)
				}
				if store.saves != 0 || learned != 0 || broker.calls != 0 || len(placement.reattachCalls) != 0 || len(write.executed) != 0 || llm.Calls() != 0 {
					t.Fatalf("load/control effects: saves=%d learned=%d broker=%d placement=%d tools=%d model=%d", store.saves, learned, broker.calls, len(placement.reattachCalls), len(write.executed), llm.Calls())
				}
				s, err = store.Load(ctx, s.ID)
				if err != nil {
					t.Fatal(err)
				}
				after, err := sessnap.Marshal(s)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("stale controls mutated history")
				}
				if err := store.Save(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.SetMode(ctx, s.ID, session.ModeAccept); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.RenameSession(ctx, s.ID, "usable after upgrade"); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.StartRun(foreign, s.ID, "foreign"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign prompt = %v", err)
			}
			run, err := svc.StartRun(ctx, s.ID, "separately admitted new work")
			if err != nil {
				t.Fatal(err)
			}
			for range run.Events() {
			}
			svc.FinishRun(s.ID, run)
			if commands.activations != 0 || factories != 1 || learned != 1 {
				t.Fatalf("admitted prompt bypassed lazy command binding or failed runtime admission: commands=%d factories=%d replay=%d", commands.activations, factories, learned)
			}
			if llm.Calls() != 1 || len(write.executed) != 0 {
				t.Fatalf("new prompt revived old work: tools=%d model=%d", len(write.executed), llm.Calls())
			}
		})
	}
}
