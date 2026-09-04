package server

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// TestWorkspaceEnrollmentRebindsAfterBrokerRestart pins the live Stage 3 failure:
// a session created by one process, then reached by /tools-connect after that
// process restarted, must still be able to enroll. A Runtime's binding prefix is
// random per process and its generation counter lives in memory, so a persisted
// ExternalBinding can never match a fresh incarnation; before the rebind seam
// every attach reported a mismatch, which surfaced as the misleading "workspace
// enrollment is not pending" and left the session unable to ever connect
// workspace services again.
func TestWorkspaceEnrollmentRebindsAfterBrokerRestart(t *testing.T) {
	newService := func(t *testing.T, store *memstore.Store, broker brokercontract.Service) *Service {
		t.Helper()
		svc, err := NewService(Config{
			Engine:            brokerEngineResult().Engine,
			Store:             store,
			PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
			NewID:     func() session.SessionID { return "restarted-broker-session" },
			MCPBroker: broker,
			RootAuthority: func(session.SessionKind) session.Authority {
				return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
			},
			SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
				return brokerEngineResult(), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}

	for _, tc := range []struct {
		name        string
		enrollFirst bool
	}{
		{name: "never enrolled"},
		{name: "pending enrollment lost with the incarnation", enrollFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			firstRuntime := testBrokerRuntime(t)
			defer firstRuntime.Close()
			first := newService(t, store, &enrollmentBroker{Service: firstRuntime})
			created, err := first.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			original := created.ExternalBinding
			if original == "" {
				t.Fatal("create did not stamp an external binding")
			}
			if tc.enrollFirst {
				if _, err := first.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
					t.Fatal(err)
				}
			}
			first.Close()

			// The restart: a brand-new Runtime with its own random binding prefix over
			// the SAME durable store, exactly as the mecak8s pod does on an image refresh.
			secondRuntime := testBrokerRuntime(t)
			defer secondRuntime.Close()
			second := newService(t, store, &enrollmentBroker{Service: secondRuntime})
			defer second.Close()

			projection, err := second.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("connect workspace services after a broker restart: %v", err)
			}
			if projection.Status != brokercontract.WorkspaceEnrollmentPending {
				t.Fatalf("status = %q, want pending", projection.Status)
			}
			reloaded, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.ExternalBinding == original {
				t.Fatal("the stale binding survived the rebind")
			}
			if _, ok := reloaded.PendingWorkspaceEnrollment(); !ok {
				t.Fatal("rebind did not persist a fresh pending enrollment")
			}
			// The rebound attachment is the live one, so the next call observes
			// instead of dead-ending on another mismatch.
			if _, err := second.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatalf("observe after rebind: %v", err)
			}
		})
	}
}
