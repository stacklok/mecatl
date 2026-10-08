package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type connectorInspectorSpy struct {
	calls   int
	id      session.SessionID
	binding session.ExternalBinding
	result  brokercontract.ConnectorInventory
	err     error
}

type blockingConnectorInspector struct {
	connectorInspectorSpy
	entered chan struct{}
	release chan struct{}
}

func (s *blockingConnectorInspector) InspectConnectors(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (brokercontract.ConnectorInventory, error) {
	close(s.entered)
	<-s.release
	return s.connectorInspectorSpy.InspectConnectors(ctx, id, binding)
}

func (s *connectorInspectorSpy) InspectConnectors(_ context.Context, id session.SessionID, binding session.ExternalBinding) (brokercontract.ConnectorInventory, error) {
	s.calls++
	s.id = id
	s.binding = binding
	return s.result, s.err
}

func connectorOwner() *session.Principal {
	return &session.Principal{Issuer: "https://issuer.example", Subject: "owner", GrantType: session.GrantTypeUser}
}

func connectorService(t *testing.T, owner *session.Principal) (*Service, *connectorInspectorSpy) {
	t.Helper()
	store := memstore.New()
	sess := session.New("connector-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/private", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	sess.ExternalBinding = "private-binding"
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	spy := &connectorInspectorSpy{result: brokercontract.ConnectorInventory{Availability: "available", EnrollmentState: "not_started", TotalConnectors: 1, Connectors: []brokercontract.ConnectorStatus{{Name: "calendar", CatalogueState: "hidden"}}}}
	svc, err := NewService(Config{PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/workspace", Engine: brokerEngineResult().Engine, Store: store, OwnershipEnforced: true, MCPConnectorInspector: spy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, spy
}

func TestBrokerMCPStatus_Scenario2_TransportAuthority(t *testing.T) {
	for _, tc := range []struct {
		name              string
		owner, caller     *session.Principal
		disabled, unwired bool
		id                string
		code              codes.Code
		http              int
	}{
		{name: "owner", owner: connectorOwner(), caller: connectorOwner(), id: "connector-session", code: codes.OK, http: 200},
		{name: "ownership disabled", owner: connectorOwner(), caller: connectorOwner(), disabled: true, id: "connector-session", code: codes.FailedPrecondition, http: 409},
		{name: "missing principal", owner: connectorOwner(), id: "connector-session", code: codes.Unauthenticated, http: 401},
		{name: "ownerless", caller: connectorOwner(), id: "connector-session", code: codes.NotFound, http: 404},
		{name: "foreign", owner: connectorOwner(), caller: &session.Principal{Issuer: "https://issuer.example", Subject: "other", GrantType: session.GrantTypeUser}, id: "connector-session", code: codes.NotFound, http: 404},
		{name: "absent", owner: connectorOwner(), caller: connectorOwner(), id: "missing", code: codes.NotFound, http: 404},
		{name: "unwired", owner: connectorOwner(), caller: connectorOwner(), unwired: true, id: "connector-session", code: codes.FailedPrecondition, http: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, spy := connectorService(t, tc.owner)
			store := &connectorStoreSpy{SessionStore: svc.cfg.Store}
			svc.cfg.Store = store
			svc.cfg.OwnershipEnforced = !tc.disabled
			if tc.unwired {
				svc.cfg.MCPConnectorInspector = nil
			}
			ctx := session.WithPrincipal(t.Context(), tc.caller)
			out, err := NewHarnessServer(svc).ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: tc.id})
			if status.Code(err) != tc.code {
				t.Fatalf("gRPC = %v, want %v", err, tc.code)
			}
			if tc.code == codes.OK && (out.GetTotalConnectors() != 1 || spy.binding != "private-binding" || spy.id != session.SessionID(tc.id)) {
				t.Fatalf("wrong inventory or binding: %v", out)
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/sessions/"+tc.id+"/mcp/connectors", nil)
			rec := httptest.NewRecorder()
			NewHTTPHandler(svc).ServeHTTP(rec, req)
			if rec.Code != tc.http {
				t.Fatalf("HTTP = %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cacheable inventory response")
			}
			wantCalls := 0
			if tc.code == codes.OK {
				wantCalls = 2
			}
			if (tc.disabled || tc.caller == nil) && store.loads != 0 {
				t.Fatal("session lookup preceded deployment/principal gate")
			}
			if spy.calls != wantCalls {
				t.Fatalf("inspections = %d, want %d", spy.calls, wantCalls)
			}
		})
	}
	t.Run("invalid ids and non broker", func(t *testing.T) {
		svc, spy := connectorService(t, connectorOwner())
		ctx := session.WithPrincipal(t.Context(), connectorOwner())
		for _, id := range []string{"", "bad\n", strings.Repeat("a", 257)} {
			_, err := NewHarnessServer(svc).ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: id})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid id accepted: %v", err)
			}
			rec := httptest.NewRecorder()
			NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id)+"/mcp/connectors", nil))
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("invalid HTTP id %q = %d, cache-control=%q", id, rec.Code, rec.Header().Get("Cache-Control"))
			}
		}
		sess, err := svc.cfg.Store.Load(ctx, "connector-session")
		if err != nil {
			t.Fatal(err)
		}
		sess.ExternalBinding = ""
		if err := svc.cfg.Store.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
		_, err = NewHarnessServer(svc).ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: string(sess.ID)})
		if status.Code(err) != codes.FailedPrecondition || spy.calls != 0 {
			t.Fatalf("non broker: %v, calls %d", err, spy.calls)
		}
	})
}

func TestBrokerMCPStatus_Scenario2_InspectionSerializesCloseAndDelete(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*Service, context.Context, session.SessionID) error
	}{
		{name: "close", run: func(s *Service, _ context.Context, id session.SessionID) error { s.CloseSession(id); return nil }},
		{name: "delete", run: func(s *Service, ctx context.Context, id session.SessionID) error { return s.DeleteSession(ctx, id) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			svc, spy := connectorService(t, connectorOwner())
			blocker := &blockingConnectorInspector{connectorInspectorSpy: *spy, entered: make(chan struct{}), release: make(chan struct{})}
			svc.cfg.MCPConnectorInspector = blocker
			ctx := session.WithPrincipal(t.Context(), connectorOwner())
			inspected := make(chan error, 1)
			go func() {
				_, err := svc.ListSessionMcpConnectors(ctx, "connector-session")
				inspected <- err
			}()
			select {
			case <-blocker.entered:
			case <-time.After(time.Second):
				t.Fatal("inspection did not reach deterministic barrier")
			}
			finished := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				finished <- operation.run(svc, ctx, "connector-session")
			}()
			<-started
			select {
			case err := <-finished:
				t.Fatalf("%s completed while inspection held broker lock: %v", operation.name, err)
			default:
			}
			close(blocker.release)
			if err := <-inspected; err != nil {
				t.Fatalf("inspection: %v", err)
			}
			if err := <-finished; err != nil {
				t.Fatalf("%s: %v", operation.name, err)
			}
		})
	}
}

func TestBrokerMCPStatus_Scenario1_Capabilities(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, principal := range []*session.Principal{nil, connectorOwner()} {
			for _, wired := range []bool{false, true} {
				svc, _ := connectorService(t, connectorOwner())
				svc.cfg.OwnershipEnforced = enabled
				if !wired {
					svc.cfg.MCPConnectorInspector = nil
				}
				ctx := session.WithPrincipal(t.Context(), principal)
				want := enabled && principal != nil && wired
				caps := svc.CompatibilityInfo(ctx).GetCapabilities()
				if caps.GetMcpConnectorStatus() != want || caps.GetMcp() {
					t.Fatalf("capability %v want broker=%v direct=false", caps, want)
				}
				rec := httptest.NewRecorder()
				NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/compatibility", nil))
				// The compatibility response is protobuf JSON; the gRPC projection is authoritative.
				if rec.Code != http.StatusOK {
					t.Fatalf("compatibility HTTP: %d", rec.Code)
				}
				var httpCompatibility mecatlv1.GetCompatibilityInfoResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &httpCompatibility); err != nil {
					t.Fatal(err)
				}
				if httpCompatibility.GetCapabilities().GetMcpConnectorStatus() != want {
					t.Fatal("HTTP compatibility capability mismatch")
				}
				req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
				rec = httptest.NewRecorder()
				NewHTTPHandler(svc).ServeHTTP(rec, req)
				if rec.Code != http.StatusCreated {
					t.Fatalf("create HTTP: %d %s", rec.Code, rec.Body.String())
				}
				var created struct {
					Capabilities struct {
						Connector bool `json:"mcp_connector_status"`
					} `json:"capabilities"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.Capabilities.Connector {
					t.Fatal("create HTTP response unexpectedly echoed deployment capabilities")
				}
				h := NewHarnessServer(svc)
				grpcCreated, err := h.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
				if err != nil {
					t.Fatal(err)
				}
				grpcCompatibilityResponse, err := h.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
				if err != nil || grpcCompatibilityResponse.GetCapabilities().GetMcpConnectorStatus() != want {
					t.Fatalf("gRPC compatibility capability mismatch: %v", err)
				}
				if enabled && principal == nil {
					continue
				}
				id := grpcCreated.GetSessionId()
				_, err = h.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: id})
				if err != nil {
					t.Fatal(err)
				}
				_, err = h.RenameSession(ctx, &mecatlv1.RenameSessionRequest{SessionId: id, Title: "renamed"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = h.SetMode(ctx, &mecatlv1.SetModeRequest{SessionId: id, Mode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT})
				if err != nil {
					t.Fatal(err)
				}
				for _, caps := range []*mecatlv1.ServerCapabilities{grpcCompatibilityResponse.GetCapabilities()} {
					if caps.GetMcpConnectorStatus() != want {
						t.Fatal("session response capability mismatch")
					}
				}
			}
		}
	}
}

func TestBrokerMCPStatus_Scenario2_Redaction(t *testing.T) {
	t.Run("bounded broker wire projection", brokerConnectorWireProjection)
	svc, spy := connectorService(t, connectorOwner())
	ctx := session.WithPrincipal(t.Context(), connectorOwner())
	spy.err = errors.New("SECRET https://private.example/token")
	_, err := NewHarnessServer(svc).ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: "connector-session"})
	if status.Code(err) != codes.Internal || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe error: %v", err)
	}
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/sessions/connector-session/mcp/connectors", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("unsafe HTTP: %d %s", rec.Code, rec.Body.String())
	}
}
