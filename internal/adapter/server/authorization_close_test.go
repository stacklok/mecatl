package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// oneSessionFailsStore fails Load for exactly one configured id and otherwise
// delegates to the wrapped store. It models a transient infrastructure error
// on a single session's settlement during shutdown.
type oneSessionFailsStore struct {
	*memstore.Store
	failID session.SessionID
	loads  int
}

func (s *oneSessionFailsStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if id == s.failID {
		s.loads++
		return nil, errors.New("simulated transient load failure")
	}
	return s.Store.Load(ctx, id)
}

// TestCloseCompletesWhenOneSessionSettlementFails pins that a single session's
// external-authorization settlement failure during shutdown (P1-7) must not
// abort the mandatory cleanup (lease release, subscription close, engine
// close, shutdownComplete) for every other session.
func TestCloseCompletesWhenOneSessionSettlementFails(t *testing.T) {
	store := &oneSessionFailsStore{Store: memstore.New(), failID: "authorizing-failing"}
	closedEngines := 0
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a synthetic per-session engine for both ids so Close's engine-close
	// phase has real work to prove it still runs.
	svc.mu.Lock()
	svc.authorizationExpiry["authorizing-failing"] = &authorizationExpiry{}
	svc.authorizationExpiry["authorizing-ok"] = &authorizationExpiry{}
	svc.sessionEngines["authorizing-failing"] = &sessionEngine{close: func() error { closedEngines++; return nil }}
	svc.sessionEngines["authorizing-ok"] = &sessionEngine{close: func() error { closedEngines++; return nil }}
	svc.mu.Unlock()

	svc.Close()

	if store.loads == 0 {
		t.Fatal("the failing session's settlement was never attempted")
	}
	if closedEngines != 2 {
		t.Fatalf("closed engines = %d, want 2 (Close must not abort cleanup for the other session)", closedEngines)
	}
	svc.mu.Lock()
	complete := svc.shutdownComplete
	remainingExpiry := len(svc.authorizationExpiry)
	svc.mu.Unlock()
	if !complete {
		t.Fatal("shutdownComplete was never set; one session's settlement failure aborted the rest of Close")
	}
	if remainingExpiry != 0 {
		t.Fatalf("authorizationExpiry not drained: %d entries remain", remainingExpiry)
	}
}
