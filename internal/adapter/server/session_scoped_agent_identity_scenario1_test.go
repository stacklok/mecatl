package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// countingBrokerService counts attempts, not just successful broker attachments.
type countingBrokerService struct {
	inner        brokercontract.Service
	attachCalled *int32
}

func (c countingBrokerService) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	atomic.AddInt32(c.attachCalled, 1)
	return c.inner.AttachSession(ctx, id)
}

func (c countingBrokerService) DeleteSession(ctx context.Context, id session.SessionID) (brokercontract.DeleteOutcome, error) {
	return c.inner.DeleteSession(ctx, id)
}

// newAgentDefBrokerService exercises agent-bound entrypoints with a live broker.
func newAgentDefBrokerService(t *testing.T) (*Service, *int32) {
	t.Helper()
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	var attachCalled int32

	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		MCPBroker:         countingBrokerService{inner: runtime, attachCalled: &attachCalled},
		AgentDefSessionEngine: func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
			return agentDefinitionEngineResult("release-reviewer"), nil
		},
		// Keep the ordinary path working so regressions fail the attach assertion,
		// not an unrelated missing-factory configuration check.
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, &attachCalled
}

func TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped(t *testing.T) {
	svc, attachCalled := newAgentDefBrokerService(t)
	_, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := atomic.LoadInt32(attachCalled); got != 0 {
		t.Fatalf("MCPBroker.AttachSession was called %d time(s) for an agent-bound session — it must be skipped entirely (AC1.10)", got)
	}
}
