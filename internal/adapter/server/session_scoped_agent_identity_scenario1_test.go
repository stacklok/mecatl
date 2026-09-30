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

// countingBrokerService wraps a REAL brokercontract.Service (testBrokerRuntime's
// adapterbroker.Runtime — the reference implementation, never a mock-framework
// mock) and counts AttachSession calls, so a test can assert the broker path was
// never reached at all rather than merely that its RESULT was ignored.
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

// TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped pins
// AC1.10: Config.MCPBroker attachment is skipped ENTIRELY for an agent-bound
// session at session-creation time — createPerSessionEngine's agent-def
// branch never reaches the MCPBroker != nil code at all, so
// AttachSession must never be called, even though a broker IS configured.
func TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	var attachCalled int32

	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		MCPBroker:         countingBrokerService{inner: runtime, attachCalled: &attachCalled},
		AgentDefSessionEngine: func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
		// Wired so that IF the agent-def branch ever regressed into the ordinary
		// broker/callSessionEngine path, CreateSession would still succeed and
		// this test would fail on the precise attachCalled assertion below,
		// rather than on an unrelated "no session-engine factory configured"
		// config error.
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	_, err = svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := atomic.LoadInt32(&attachCalled); got != 0 {
		t.Fatalf("MCPBroker.AttachSession was called %d time(s) for an agent-bound session — it must be skipped entirely (AC1.10)", got)
	}
}
