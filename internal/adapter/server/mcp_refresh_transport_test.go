package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type refreshTransportFailureStore struct {
	*memstore.Store
	mu         sync.Mutex
	loads      int
	failLoadAt int
	saveErr    error
}

func (s *refreshTransportFailureStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.mu.Lock()
	s.loads++
	fail := s.failLoadAt == s.loads
	s.mu.Unlock()
	if fail {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, errors.New("PRIVATE_MARKER /private/reload"))
	}
	return s.Store.Load(ctx, id)
}

func (s *refreshTransportFailureStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	err := s.saveErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Store.Save(ctx, sess)
}

func (s *refreshTransportFailureStore) reset(failLoadAt int, saveErr error) {
	s.mu.Lock()
	s.loads = 0
	s.failLoadAt = failLoadAt
	s.saveErr = saveErr
	s.mu.Unlock()
}

func TestMCPRefreshPrivateFailuresAreRedactedByTransports(t *testing.T) {
	const private = "PRIVATE_MARKER /private/reconcile"
	for _, failure := range []string{"initial-load", "locked-reload", "reconcile", "save"} {
		failure := failure
		for _, transport := range []string{"grpc", "http"} {
			transport := transport
			t.Run(failure+"/"+transport, func(t *testing.T) {
				store := &refreshTransportFailureStore{Store: memstore.New()}
				eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
				reconcileErr := error(nil)
				svc, err := newPlacementTestService(server.Config{
					Engine: eng, Store: store,
					RootAuthority: func(session.SessionKind) session.Authority {
						return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
					},
					MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) {
						if reconcileErr != nil {
							return server.MCPRefreshSnapshot{}, reconcileErr
						}
						return server.MCPRefreshSnapshot{Revision: 2, ToolNames: []string{"Read", "mcp__new__tool"}}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(svc.Close)
				sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "initial-load":
					store.reset(1, nil)
				case "locked-reload":
					store.reset(2, nil)
				case "reconcile":
					store.reset(0, nil)
					reconcileErr = fmt.Errorf("%w: %s", server.ErrInternal, private)
				case "save":
					store.reset(0, errors.New(private))
				}

				var serialized string
				if transport == "grpc" {
					_, callErr := server.NewHarnessServer(svc).RefreshMcpSources(t.Context(), &mecatlv1.RefreshMcpSourcesRequest{SessionId: string(sess.ID)})
					if status.Code(callErr) != codes.Internal {
						t.Fatalf("gRPC code = %s, want Internal: %v", status.Code(callErr), callErr)
					}
					serialized = status.Convert(callErr).Message()
				} else {
					req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/mcp-refresh", nil)
					rec := httptest.NewRecorder()
					server.NewHTTPHandler(svc).ServeHTTP(rec, req)
					if rec.Code != http.StatusInternalServerError {
						t.Fatalf("HTTP status = %d, want 500: %s", rec.Code, rec.Body.String())
					}
					serialized = rec.Body.String()
				}
				if strings.Contains(serialized, "PRIVATE_MARKER") || strings.Contains(serialized, "/private/") {
					t.Fatalf("private failure leaked over %s: %s", transport, serialized)
				}
			})
		}
	}
}

func TestMCPSourceReconciliation_Scenario4_DirectTransportStatusMatrix(t *testing.T) {
	var refreshCalls int
	statusSnapshot := server.MCPSourceStatus{
		Sources:  []source.SourceInfo{{Name: "static", Kind: "static", Enabled: true}},
		Revision: 41, Stale: true, Reconciling: true,
	}
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
		},
		MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) {
			refreshCalls++
			statusSnapshot.Revision = 99 // a coalesced successor may publish before delivery
			switch refreshCalls {
			case 1:
				return server.MCPRefreshSnapshot{Revision: 42, Changed: true, ToolNames: []string{"Read"}}, nil
			case 3:
				return server.MCPRefreshSnapshot{Revision: 43, ToolNames: []string{"Read", "mcp__new__authority"}}, nil
			case 4:
				return server.MCPRefreshSnapshot{Revision: 44, Changed: true, ToolNames: []string{"Read", "mcp__new__both"}}, nil
			default:
				return server.MCPRefreshSnapshot{Revision: 42, ToolNames: []string{"Read"}}, nil
			}
		},
		MCPStatus: func() server.MCPSourceStatus { return statusSnapshot },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	compat, err := client.GetCompatibilityInfo(context.Background(), &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil || !compat.GetCapabilities().GetMcpRefresh() || compat.GetCapabilities().GetWorkspaceEnrollment() {
		t.Fatalf("direct refresh capabilities=%+v err=%v", compat.GetCapabilities(), err)
	}
	listed, err := client.ListMcpSources(context.Background(), &mecatlv1.ListMcpSourcesRequest{})
	if err != nil {
		t.Fatalf("ListMcpSources: %v", err)
	}
	if listed.GetRevision() != 41 || !listed.GetStale() || !listed.GetReconciling() || len(listed.GetSources()) != 1 || refreshCalls != 0 {
		t.Fatalf("cached list=%+v refreshCalls=%d", listed, refreshCalls)
	}
	refreshed, err := client.RefreshMcpSources(context.Background(), &mecatlv1.RefreshMcpSourcesRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("RefreshMcpSources: %v", err)
	}
	if refreshed.GetRevision() != 42 || !refreshed.GetChanged() || refreshCalls != 1 {
		t.Fatalf("refresh=%+v refreshCalls=%d", refreshed, refreshCalls)
	}

	httpSrv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpSrv.Close()
	req, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/sessions/"+string(sess.ID)+"/mcp-refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpSrv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST refresh: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST refresh status=%d", resp.StatusCode)
	}
	var body mecatlv1.RefreshMcpSourcesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if body.GetRevision() != 42 || body.GetChanged() {
		t.Fatalf("second no-op refresh revision=%d changed=%v", body.GetRevision(), body.GetChanged())
	}

	// Authority-only and runtime+authority changes both report changed through the
	// actual routers, while retaining the request-local pinned revision even though
	// the cached status has already advanced to 99.
	authorityOnly, err := client.RefreshMcpSources(context.Background(), &mecatlv1.RefreshMcpSourcesRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("authority-only gRPC refresh: %v", err)
	}
	if authorityOnly.GetRevision() != 43 || !authorityOnly.GetChanged() {
		t.Fatalf("authority-only response=%+v", authorityOnly)
	}
	bothReq, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/sessions/"+string(sess.ID)+"/mcp-refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	bothResp, err := httpSrv.Client().Do(bothReq)
	if err != nil {
		t.Fatalf("both-change HTTP refresh: %v", err)
	}
	defer bothResp.Body.Close()
	if bothResp.StatusCode != http.StatusOK {
		t.Fatalf("both-change status=%d", bothResp.StatusCode)
	}
	var bothBody mecatlv1.RefreshMcpSourcesResponse
	if err := json.NewDecoder(bothResp.Body).Decode(&bothBody); err != nil {
		t.Fatalf("decode both-change response: %v", err)
	}
	if bothBody.GetRevision() != 44 || !bothBody.GetChanged() {
		t.Fatalf("both-change revision=%d changed=%v", bothBody.GetRevision(), bothBody.GetChanged())
	}
	persisted, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("load refreshed session: %v", err)
	}
	authority, _ := persisted.BoundAuthority()
	if !authority.CapabilitySet.Contains(governance.CapabilitySet{Tools: []string{"mcp__new__authority", "mcp__new__both"}}) {
		t.Fatalf("transport refresh did not preserve both grants: %+v", authority)
	}

	badReq, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/sessions/"+string(sess.ID)+"/mcp-refresh", bytes.NewBufferString(`{"ignored":true}`))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := httpSrv.Client().Do(badReq)
	if err != nil {
		t.Fatalf("POST body: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("nonempty body status=%d want 400", badResp.StatusCode)
	}
}
