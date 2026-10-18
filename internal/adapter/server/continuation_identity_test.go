package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

func TestMigrationHoldHostInspectionAndControlsAreOwnerSafeAndInert(t *testing.T) {
	for _, state := range []session.State{session.StateAwaiting, session.StateAuthorizing} {
		t.Run(string(state), func(t *testing.T) {
			owner := &session.Principal{Issuer: "test", Subject: "owner"}
			ctx := session.WithPrincipal(t.Context(), owner)
			foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "foreign"})
			store := &identityGuardStore{Store: memstore.New()}
			placement := &revisionPlacementProvider{}
			broker := &identityGuardBroker{}
			learned := 0
			svc, err := NewService(Config{Engine: repairEngine(), Store: store, OwnershipEnforced: true,
				PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root", MCPBroker: broker,
				ReplayApprovals: func(context.Context, *session.Session) { learned++ },
			})
			if err != nil {
				t.Fatal(err)
			}
			s := session.New("held-host", session.ModeDefault, placement.binding("1").Ref, session.Limits{}, time.Unix(0, 0))
			if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
				t.Fatal(err)
			}
			s.Conversation.Append(session.NewUserMessage("inspectable"))
			pending, authorization := []byte(nil), []byte(nil)
			if state == session.StateAwaiting {
				pending = []byte(`{"AskID":"held-ask","Tool":"Write","call":"ambiguous-provider","Args":{"path":"held"}}`)
			} else {
				authorization = []byte(`{"authorization":{"id":"held-auth","status":"pending"},"call":{"id":"ambiguous-provider","name":"protected","args":"e30="}}`)
			}
			payload, err := json.Marshal(struct {
				Version       int           `json:"version"`
				State         session.State `json:"state"`
				Pending       []byte        `json:"pending,omitempty"`
				Authorization []byte        `json:"pending_authorization,omitempty"`
			}{1, state, pending, authorization})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RestoreIdentityMigrationHold(state, session.IdentityMigrationHold{Reason: "legacy-continuation-unproven", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, s); err != nil {
				t.Fatal(err)
			}
			store.saves = 0
			before, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			for _, load := range []func(context.Context, session.SessionID) (*session.Session, error){svc.GetSession, svc.LoadSession} {
				loaded, err := load(ctx, s.ID)
				if err != nil || loaded.CheckIdentityReady() != session.ErrMigrationHeld {
					t.Fatalf("inspection = %v", err)
				}
				if _, err := load(foreign, s.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("foreign inspection = %v", err)
				}
			}
			for name, action := range map[string]func() error{
				"prompt":  func() error { _, err := svc.StartInteractiveRunContent(ctx, s.ID, "must not run", nil); return err },
				"retry":   func() error { _, err := svc.RetryFailedRun(ctx, s.ID); return err },
				"approve": func() error { return svc.Approve(ctx, s.ID, "control:ask", session.VerdictAllowAlways) },
				"mode":    func() error { _, err := svc.SetMode(ctx, s.ID, session.ModeAccept); return err },
				"delete":  func() error { return svc.DeleteSession(ctx, s.ID) },
				"recheck": func() error {
					_, err := svc.RecheckMCPAuthorization(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "auth"})
					return err
				},
				"cancel auth": func() error {
					_, err := svc.CancelMCPAuthorization(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "auth"})
					return err
				},
				"presentation": func() error {
					_, err := svc.MCPAuthorizationPresentation(ctx, s.ID, MCPAuthorizationControl{SessionID: s.ID, AuthorizationID: "auth"})
					return err
				},
				"exact ask": func() error {
					return svc.validatePersistedRunAsk(s, s.RunID(), agent.ApprovalResolution{AskID: "control:ask", Verdict: session.VerdictAllowAlways}, false)
				},
				"reopen":       func() error { _, err := svc.reopenLoadedSession(ctx, s); return err },
				"interruption": func() error { _, _, err := svc.interruptRestoredAuthorizationLocked(ctx, s); return err },
				"granted":      func() error { _, err := svc.continueGrantedAuthorizationLocked(ctx, s); return err },
				"settle":       func() error { return svc.settleAuthorizationLocked(ctx, s.ID) },
			} {
				t.Run(name, func(t *testing.T) {
					if err := action(); !errors.Is(err, session.ErrMigrationHeld) {
						t.Fatalf("held control = %v", err)
					}
				})
			}
			if _, err := svc.StartRun(foreign, s.ID, "foreign"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign prompt = %v", err)
			}
			if store.saves != 0 || learned != 0 || broker.calls != 0 || len(placement.reattachCalls) != 0 {
				t.Fatalf("effects: saves=%d learned=%d broker=%d placement=%d", store.saves, learned, broker.calls, len(placement.reattachCalls))
			}
			loaded, err := store.Load(ctx, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := sessnap.Marshal(loaded)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("persisted held bytes changed")
			}
		})
	}
}
