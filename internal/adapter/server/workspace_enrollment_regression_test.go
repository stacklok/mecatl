package server

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestWorkspaceEnrollmentRegistrationOverlapRequiresAuthority(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authority     []string
		registrations []string
		wantOverlap   bool
	}{
		{name: "authorized overlap", authority: []string{"prior"}, registrations: []string{"prior"}, wantOverlap: true},
		{name: "excluded authority", registrations: []string{"prior"}},
		{name: "unrelated registration", authority: []string{"prior"}, registrations: []string{"other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, overlap := workspaceEnrollmentRegistrationOverlap([]string{"prior"}, tc.authority, tc.registrations)
			if overlap != tc.wantOverlap || overlap && key != "prior" {
				t.Fatalf("overlap = (%q, %t), want (%q, %t)", key, overlap, "prior", tc.wantOverlap)
			}
		})
	}
}

func TestCallSessionEnginePreservesFrozenKeyNilness(t *testing.T) {
	for _, keys := range [][]string{nil, {}} {
		wantNil := keys == nil
		var gotNil bool
		svc := &Service{cfg: Config{SessionContextEngine: func(_ context.Context, _ session.SessionID, _ *session.Principal, _ ExecutionWorkspaceAcquirer, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, _ []tool.Tool, got []string, _ session.SessionID) (SessionEngineResult, error) {
			gotNil = got == nil
			return SessionEngineResult{}, nil
		}}}
		if _, err := svc.callSessionEngine(t.Context(), "s", nil, nil, ProviderSelector{}, nil, ProfileDefault, "", session.ModeDefault, nil, keys); err != nil {
			t.Fatal(err)
		}
		if gotNil != wantNil {
			t.Fatalf("forwarded keys nil = %t, want %t", gotNil, wantNil)
		}
	}
}

func TestCreateCarryoverRejectsPerSessionRegistrationCollision(t *testing.T) {
	svc, _, source := newWorkspaceEnrollmentHTTPService(t, false)
	const key = "mcp__prior__tool"
	replacePersistedBrokerLedger(t, svc, source, []string{key}, true)
	before, err := svc.cfg.Store.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeAuthority, _ := before.BoundAuthority()
	beforeKeys, beforePresent := before.WorkspaceEnrollmentBrokerKeys()

	var closed bool
	svc.cfg.SessionEngineWithTools = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
		return SessionEngineResult{
			Engine:                    brokerEngineResult().Engine,
			NonBrokerRegistrationKeys: []string{key},
			Close:                     func() error { closed = true; return nil },
		}, nil
	}
	svc.cfg.NewID = func() session.SessionID { return "carryover-collision-target" }
	_, err = svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSourceSession(source.ID))
	var collision *WorkspaceEnrollmentCollisionError
	if !errors.As(err, &collision) || collision.Key != key || !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("carryover create collision = %v, want typed failed-precondition %q", err, key)
	}
	if !closed {
		t.Fatal("rejected per-session engine candidate was not closed")
	}
	if stored, loadErr := svc.cfg.Store.Load(t.Context(), "carryover-collision-target"); loadErr == nil || stored != nil {
		t.Fatalf("rejected carryover session was persisted: session=%v err=%v", stored, loadErr)
	}
	after, err := svc.cfg.Store.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterAuthority, _ := after.BoundAuthority()
	afterKeys, afterPresent := after.WorkspaceEnrollmentBrokerKeys()
	if !reflect.DeepEqual(afterAuthority, beforeAuthority) || beforePresent != afterPresent || !slices.Equal(beforeKeys, afterKeys) {
		t.Fatalf("collision changed source authority/provenance: authority=%#v keys=%q present=%t", afterAuthority, afterKeys, afterPresent)
	}
}

func TestCreateCarryoverSharedEngineChecksOnlyAuthorizedBrokerKeys(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authority     []string
		wantCollision bool
	}{
		{name: "authorized key collides", authority: []string{"Read", "mcp__prior__tool"}, wantCollision: true},
		{name: "excluded key is preserved", authority: []string{"Read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			catalog := tool.NewCatalog()
			if err := catalog.Register(enrollmentTool{name: "mcp__prior__tool"}); err != nil {
				t.Fatal(err)
			}
			shared := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil)})
			svc, err := NewService(Config{
				Engine: shared, Store: store, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/workspace",
				NewID: func() session.SessionID { return "shared-carryover-target" },
				RootAuthority: func(session.SessionKind) session.Authority {
					return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			source := session.New("shared-carryover-source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
			if err := source.BindAuthority(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: tc.authority}, Provenance: "test"}); err != nil {
				t.Fatal(err)
			}
			if err := source.RestoreWorkspaceEnrollmentBrokerKeys([]string{"mcp__prior__tool"}, true); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(t.Context(), source); err != nil {
				t.Fatal(err)
			}

			created, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSourceSession(source.ID))
			var collision *WorkspaceEnrollmentCollisionError
			if tc.wantCollision {
				if !errors.As(err, &collision) || collision.Key != "mcp__prior__tool" || !errors.Is(err, ErrFailedPrecondition) {
					t.Fatalf("shared-engine collision = %v, want typed failed-precondition", err)
				}
				if stored, loadErr := store.Load(t.Context(), "shared-carryover-target"); loadErr == nil || stored != nil {
					t.Fatalf("rejected shared-engine session was persisted: session=%v err=%v", stored, loadErr)
				}
				return
			}
			if err != nil || created == nil {
				t.Fatalf("excluded broker key blocked shared-engine create: session=%v err=%v", created, err)
			}
			stored, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			authority, _ := stored.BoundAuthority()
			if !slices.Equal(authority.CapabilitySet.Tools, tc.authority) {
				t.Fatalf("shared-engine carryover widened excluded authority: got %v, want %v", authority.CapabilitySet.Tools, tc.authority)
			}
		})
	}
}

func TestLoadSessionWithMPCRetainsPriorEngineOnEnrollmentCollision(t *testing.T) {
	svc, _, created := newWorkspaceEnrollmentHTTPService(t, false)
	const key = "mcp__prior__tool"
	replacePersistedBrokerLedger(t, svc, created, []string{key}, true)
	before, err := svc.cfg.Store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeAuthority, _ := before.BoundAuthority()
	beforeKeys, beforePresent := before.WorkspaceEnrollmentBrokerKeys()
	svc.cfg.SessionEngine = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
		return brokerEngineResult(), nil
	}
	var closed bool
	svc.cfg.SessionEngineWithTools = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
		return SessionEngineResult{
			Engine:                    brokerEngineResult().Engine,
			NonBrokerRegistrationKeys: []string{key},
			Close:                     func() error { closed = true; return nil },
		}, nil
	}
	svc.mu.Lock()
	priorEngine := svc.sessionEngines[created.ID]
	svc.mu.Unlock()
	_, err = svc.LoadSessionWithMCP(t.Context(), created.ID, []mcp.ServerConfig{{Name: "client"}})
	var collision *WorkspaceEnrollmentCollisionError
	if !errors.As(err, &collision) || collision.Key != key || !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("MCP load collision = %v, want typed failed-precondition %q", err, key)
	}
	if !closed {
		t.Fatal("rejected MCP-load engine candidate was not closed")
	}
	svc.mu.Lock()
	gotEngine := svc.sessionEngines[created.ID]
	svc.mu.Unlock()
	if gotEngine != priorEngine {
		t.Fatal("MCP-load collision displaced the existing session engine")
	}
	after, err := svc.cfg.Store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterAuthority, _ := after.BoundAuthority()
	afterKeys, afterPresent := after.WorkspaceEnrollmentBrokerKeys()
	if !reflect.DeepEqual(afterAuthority, beforeAuthority) || beforePresent != afterPresent || !slices.Equal(beforeKeys, afterKeys) {
		t.Fatalf("MCP-load collision changed authority/provenance: authority=%#v keys=%q present=%t", afterAuthority, afterKeys, afterPresent)
	}
}

func TestCallSessionEngineCollisionSettlementClearsEnrollment(t *testing.T) {
	svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
	const key = "mcp__prior__tool"
	replacePersistedBrokerLedger(t, svc, created, []string{key}, true)
	svc.cfg.SessionContextEngine = func(_ context.Context, _ session.SessionID, _ *session.Principal, _ ExecutionWorkspaceAcquirer, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, _ []tool.Tool, keys []string, _ session.SessionID) (SessionEngineResult, error) {
		if slices.Contains(keys, "mcp__replacement__tool") {
			return SessionEngineResult{}, &WorkspaceEnrollmentCollisionError{Key: key}
		}
		return SessionEngineResult{}, nil
	}
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__replacement__tool"}})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: catalogue}
	_, err = svc.ConnectWorkspaceServices(t.Context(), created.ID)
	var collision *WorkspaceEnrollmentCollisionError
	if !errors.As(err, &collision) || collision.Key != key || !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("context factory collision = %v, want typed failed-precondition", err)
	}
	stored, err := svc.cfg.Store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := stored.PendingWorkspaceEnrollment(); pending {
		t.Fatal("context-factory collision left enrollment pending in durable session")
	}
}
