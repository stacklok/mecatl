package server

import (
	"bytes"
	"context"
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
	raw   []byte
	saves int
}

func (s *identityGuardStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if s.raw != nil {
		return sessnap.Unmarshal(s.raw)
	}
	return s.Store.Load(ctx, id)
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
	defer svc.Close()
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

func TestUnsupportedHostContinuationRejectsBeforeEffects(t *testing.T) {
	for name, raw := range map[string]string{
		"approval":      `{"id":"old","state":"awaiting","mode":"default","environment_ref":{"Kind":"mem","ID":"/same-root","Revision":"1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-old","Name":"Write"}]}],"pending":{"AskID":"old-ask","Tool":"Write","call":"provider-old","origin":"permission"}}`,
		"authorization": `{"id":"old","state":"authorizing","mode":"default","environment_ref":{"Kind":"mem","ID":"/same-root","Revision":"1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-old","Name":"Write"}]}],"pending_authorization":{"authorization":{"id":"old-auth","binding":"opaque"},"call":{"id":"provider-old","name":"Write"}}}`,
		"malformed":     `{"id":"old","state":"completed","mode":"default","environment_ref":{"Kind":"mem","ID":"/same-root","Revision":"1"},"messages":[{"role":"tool","tool_result":{"CallID":"orphan","Content":"old"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := &identityGuardStore{Store: memstore.New(), raw: []byte(raw)}
			broker := &identityGuardBroker{}
			placement := &revisionPlacementProvider{}
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			write := &identityContinuationTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(write)
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: store})
			learned := 0
			svc, err := NewService(Config{Engine: engine, Store: store, PlacementProvider: placement, PlacementScope: "test", SharedEngineRoot: "/same-root", MCPBroker: broker, ReplayApprovals: func(context.Context, *session.Session) { learned++ }})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			for _, load := range []func(context.Context, session.SessionID) (*session.Session, error){svc.GetSession, svc.LoadSession} {
				if _, err := load(t.Context(), "old"); err == nil {
					t.Fatal("unsupported continuation loaded")
				}
			}
			if _, err := svc.StartRun(t.Context(), "old", "new prompt"); err == nil {
				t.Fatal("unsupported snapshot started a run")
			}
			if err := svc.Approve(t.Context(), "old", "old-ask", session.VerdictAllowAlways); err == nil {
				t.Fatal("unsupported approval accepted")
			}
			if store.saves != 0 || broker.calls != 0 || len(placement.reattachCalls) != 0 || len(write.executed) != 0 || llm.Calls() != 0 || learned != 0 {
				t.Fatalf("unsupported snapshot caused effects: saves=%d broker=%d placement=%d tools=%d model=%d learning=%d", store.saves, broker.calls, len(placement.reattachCalls), len(write.executed), llm.Calls(), learned)
			}
		})
	}
}
