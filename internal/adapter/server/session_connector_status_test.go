package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type sessionConnectorSpy struct {
	mcpbrokergrpc.SessionHostClient
	ref       c.SessionRef
	cat       c.CatalogueRef
	calls     int
	inventory *c.ConnectorInventory
	err       error
}

func (s *sessionConnectorSpy) InspectConnectors(_ context.Context, ref c.SessionRef, cat c.CatalogueRef) (c.ConnectorInventory, error) {
	s.calls++
	s.ref = ref
	s.cat = cat
	if s.inventory != nil {
		return *s.inventory, s.err
	}
	return c.ConnectorInventory{Availability: c.AvailabilityUnavailable, EnrollmentState: c.EnrollmentUnknown, TotalConnectors: 1, Connectors: []c.ConnectorStatus{{Name: "public", CatalogueState: c.CatalogueUnknown}}}, s.err
}
func connectorOwner() *session.Principal {
	return &session.Principal{Issuer: "https://identity.example", Subject: "owner", GrantType: session.GrantTypeUser}
}

func connectorService(t *testing.T, owner *session.Principal) (*Service, *sessionConnectorSpy) {
	t.Helper()
	store := memstore.New()
	sess := session.New("connector-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	sess.Owner = owner
	if err := sess.BindAuthority(session.Authority{Provenance: "test", CapabilitySet: governance.CapabilitySet{FileSystem: true}}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AdoptBrokerCatalogue(session.BrokerSessionRef(hostProofRef(1)), session.BrokerCatalogueRef(hostProofRef(2)), time.Now().Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	spy := &sessionConnectorSpy{}
	svc, err := NewService(Config{Engine: brokerEngineResult().Engine, Store: store, OwnershipEnforced: true, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", SessionBroker: spy, SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
		return brokerEngineResult(), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, spy
}

func TestSessionBrokerConnectorStatusSavedRefs(t *testing.T) {
	svc, legacy := connectorService(t, connectorOwner())
	spy := &sessionConnectorSpy{}
	svc.cfg.SessionBroker = spy
	owner := session.WithPrincipal(t.Context(), connectorOwner())
	sess, err := svc.cfg.Store.Load(owner, "connector-session")
	if err != nil {
		t.Fatal(err)
	}
	access, _ := sess.BrokerAccess()
	ref, cat := access.Session, access.Catalogue
	if !svc.capabilitiesFor(owner).McpConnectorStatus || svc.capabilitiesFor(t.Context()).McpConnectorStatus {
		t.Fatal("capability authentication")
	}
	result, err := svc.ListSessionMcpConnectors(owner, sess.ID)
	if err != nil || result.TotalConnectors != 1 || spy.ref != ref || spy.cat != cat || spy.calls != 1 || legacy.calls != 0 {
		t.Fatalf("saved refs: %#v %v", result, err)
	}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: connectorOwner().Issuer, Subject: "other", GrantType: session.GrantTypeUser})
	if _, err := svc.ListSessionMcpConnectors(foreign, sess.ID); !errors.Is(err, ErrNotFound) || spy.calls != 1 {
		t.Fatalf("foreign owner: %v", err)
	}
	spy.err = c.ErrStateUnavailable
	if _, err := svc.ListSessionMcpConnectors(owner, sess.ID); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("stale refs: %v", err)
	}
}
