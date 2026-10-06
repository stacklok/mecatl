package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
)

func TestRetiredHostBrokerStateFailsBeforeActivationOrMutation(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, marker := range []string{"binding", "pending", "custody", "conflicting-pending"} {
			t.Run(marker+map[bool]string{false: "-direct", true: "-remote"}[remote], func(t *testing.T) {
				store := memstore.New()
				owner := &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser}
				ctx := session.WithPrincipal(t.Context(), owner)
				sess := session.New("retired", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
				sess.Owner = owner
				if err := sess.BindAuthority(session.Authority{Provenance: "test", CapabilitySet: governance.CapabilitySet{FileSystem: true}}); err != nil {
					t.Fatal(err)
				}
				if marker == "binding" {
					sess.ExternalBinding = "persisted-old-binding"
				}
				if marker == "conflicting-pending" {
					if err := sess.AdoptBrokerCatalogue(session.BrokerSessionRef(hostProofRef(1)), session.BrokerCatalogueRef(hostProofRef(2)), time.Now().Add(time.Hour), nil); err != nil {
						t.Fatal(err)
					}
				}
				if marker == "custody" {
					var owner, workload, profile [32]byte
					owner[0], workload[0], profile[0] = 1, 2, 3
					custody, err := session.NewBrokerCredentialCustody(hostProofRef(5), sess.Incarnation(), owner, workload, profile, []string{"private"}, time.Now().Add(time.Hour).UTC())
					if err != nil {
						t.Fatal(err)
					}
					if err := sess.RestoreBrokerCredentialCustody(custody); err != nil {
						t.Fatal(err)
					}
				}
				if marker == "pending" || marker == "conflicting-pending" {
					if err := sess.BeginWorkspaceEnrollment(session.PendingWorkspaceEnrollment{ID: "old-enrollment", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Save(ctx, sess); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Load(ctx, sess.ID); err != nil {
					t.Fatalf("fixture restore: %v", err)
				}
				cfg := Config{Engine: brokerEngineResult().Engine, Store: store, OwnershipEnforced: true, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test"}
				if remote {
					cfg.SessionBroker = &retirementUnusedClient{}
					cfg.SessionEngineWithTools = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
						t.Fatal("engine activated")
						return SessionEngineResult{}, nil
					}
				}
				svc, err := NewService(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				check := func(err error) {
					t.Helper()
					if !errors.Is(err, ErrFailedPrecondition) || !strings.Contains(err.Error(), "Unsupported broker session after retirement; create a new session.") {
						t.Fatalf("retirement error: %v", err)
					}
				}
				_, err = svc.GetSession(ctx, sess.ID)
				check(err)
				_, err = svc.LoadSession(ctx, sess.ID)
				check(err)
				_, err = svc.ConnectWorkspaceServices(ctx, sess.ID)
				check(err)
				_, err = svc.StartRunContent(ctx, sess.ID, "must not activate", nil)
				check(err)
				control := MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: "old-control"}
				_, err = svc.MCPAuthorizationPresentation(ctx, sess.ID, control)
				check(err)
				_, err = svc.RecheckMCPAuthorization(ctx, sess.ID, control)
				check(err)
				_, err = svc.CancelMCPAuthorization(ctx, sess.ID, control)
				check(err)
				_, err = svc.RenameSession(ctx, sess.ID, "changed")
				check(err)
				check(svc.DeleteSession(ctx, sess.ID))
				check(svc.DeleteSessionForRetention(ctx, sess.ID))
				saved, err := store.Load(ctx, sess.ID)
				if err != nil || saved.ExternalBinding != sess.ExternalBinding || saved.Title != sess.Title || saved.State != sess.State {
					t.Fatal("retirement mutated stored state")
				}
				if _, err := svc.GetSession(session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "foreign"}), sess.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("foreign disclosure: %v", err)
				}
			})
		}
	}
}

type retirementUnusedClient struct {
	mcpbrokergrpc.SessionHostClient
}

func TestRetirementDoesNotClassifyDirectPendingAuthorization(t *testing.T) {
	sess := session.New("direct", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	call := session.NewToolCall("direct-call", "mcp__direct__echo", []byte(`{}`))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForAuthorization(session.PendingAuthorization{Call: call, Authorization: session.ExternalAuthorization{ID: "direct-auth", Binding: "direct-controller", DisplayName: "direct", ExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if err := rejectRetiredBrokerSession(sess); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{Engine: brokerEngineResult().Engine, Store: store, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.GetSession(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
}
