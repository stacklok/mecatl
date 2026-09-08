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
func TestSingletonBrokerRemediation_Scenario2_FreshClientPrePromptRecovery(t *testing.T) {
	store := memstore.New()
	firstRuntime := testBrokerRuntime(t)
	secondRuntime := testBrokerRuntime(t)
	firstBroker := &enrollmentBroker{Service: firstRuntime}
	secondBroker := &enrollmentBroker{Service: secondRuntime}
	var factoryCalls, firstCloses, secondCloses int
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "factory-recovery" },
		MCPBroker: firstBroker, MCPBrokerClose: func() error { firstCloses++; return firstRuntime.Close() },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			factoryCalls++
			return secondBroker, func() error { secondCloses++; return secondRuntime.Close() }, nil
		},
		WorkspaceEnrollment: true,
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
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	staleBinding := created.ExternalBinding
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatalf("begin pre-restart enrollment: %v", err)
	}
	svc.closeSessionLocal(created.ID)
	// The long-lived client has reconnected to a replacement broker process. Its
	// fresh binding proves confirmed state loss; only then may Service invoke the
	// composition-owned factory and retire that stale client generation.
	firstBroker.Service = secondRuntime
	firstBroker.attachment = nil
	projection, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || projection.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("pre-prompt recovery = %+v, %v", projection, err)
	}
	reloaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 || firstCloses != 1 || reloaded.ExternalBinding == staleBinding {
		t.Fatalf("factory calls=%d stale closes=%d binding=%q, want one replacement and a fresh binding", factoryCalls, firstCloses, reloaded.ExternalBinding)
	}
	svc.Drain()
	svc.Close()
	if secondCloses != 1 {
		t.Fatalf("replacement client closes = %d, want exactly 1", secondCloses)
	}
}

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
