package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestAgentDefinitionCreateTransportParity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		request   *mecatlv1.CreateSessionRequest
		wantCode  codes.Code
		wantCalls int32
	}{
		{name: "bound", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer"}, wantCalls: 1},
		{name: "literal model only", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer", ModelId: "alias-looking/literal"}, wantCalls: 1},
		{name: "explicit pair", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer", ProviderId: "requested-provider", ModelId: "requested-model"}, wantCalls: 1},
		{name: "ordinary omitted", request: &mecatlv1.CreateSessionRequest{}},
		{name: "unknown definition", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "missing"}, wantCode: codes.InvalidArgument, wantCalls: 1},
		{name: "client MCP", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer", McpServers: []*mecatlv1.McpServerSpec{{Name: "external", Url: "https://mcp.example.com", Type: "http"}}}, wantCode: codes.InvalidArgument},
		{name: "debug MCP", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer", DebugMcpServers: []string{"external"}}, wantCode: codes.InvalidArgument},
		{name: "debug target", request: &mecatlv1.CreateSessionRequest{AgentDefinitionName: "reviewer", DebugTargetSessionId: "target", Execution: &mecatlv1.ExecutionSelection{None: &mecatlv1.ExecutionNone{}}}, wantCode: codes.InvalidArgument},
	} {
		for _, transport := range []string{"grpc", "http"} {
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
				var calls atomic.Int32
				eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "shared-model"})
				store := memstore.New()
				svc, err := newPlacementTestService(server.Config{
					Engine: eng, Store: store, ClientMCPOnCreate: true,
					DefaultResolvedModel: server.ResolvedModel{ProviderID: "default-provider", ModelID: "default-model"},
					AgentDefSessionEngine: func(_ context.Context, sel server.ProviderSelector, _ server.SessionProfile, mode session.PermissionMode, _ session.Limits, name string) (server.SessionEngineResult, error) {
						calls.Add(1)
						if name != "reviewer" {
							return server.SessionEngineResult{}, fmt.Errorf("%w: unknown agent definition", server.ErrInvalidArgument)
						}
						if sel.ProviderID != tc.request.GetProviderId() || sel.ModelID != tc.request.GetModelId() {
							t.Errorf("factory selector = %+v, want literal request pair", sel)
						}
						return server.SessionEngineResult{
							AgentDefinitionName: name,
							Engine:              eng, ProviderID: "resolved-provider", ModelID: "resolved-model", BuiltForMode: mode,
							Authority: session.Authority{Provenance: "agent-def:test", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Ceiling: &governance.CapabilitySet{Tools: []string{"Read"}}},
							Close:     func() error { return nil },
						}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(svc.Close)
				auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "test-token"})
				var response *mecatlv1.CreateSessionResponse
				if transport == "grpc" {
					client, cleanup := dialGRPCSecure(t, svc, auth)
					defer cleanup()
					if _, err := client.CreateSession(t.Context(), tc.request); status.Code(err) != codes.Unauthenticated {
						t.Fatalf("unauthenticated create: %v", err)
					}
					response, err = client.CreateSession(bearerCtx(t.Context(), "test-token"), tc.request)
					if status.Code(err) != tc.wantCode {
						t.Fatalf("create code = %v, want %v: %v", status.Code(err), tc.wantCode, err)
					}
				} else {
					body, err := json.Marshal(tc.request)
					if err != nil {
						t.Fatal(err)
					}
					handler := auth.Middleware(server.NewHTTPHandler(svc))
					unauthenticated := httptest.NewRecorder()
					handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body)))
					if unauthenticated.Code != http.StatusUnauthorized {
						t.Fatalf("unauthenticated create = %d", unauthenticated.Code)
					}
					req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer test-token")
					if target := tc.request.GetDebugTargetSessionId(); target != "" {
						req.Header.Set("X-Mecatl-Session-Id", target)
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					wantStatus := http.StatusCreated
					if tc.wantCode != codes.OK {
						wantStatus = http.StatusBadRequest
					}
					if rec.Code != wantStatus {
						t.Fatalf("create status = %d, want %d: %s", rec.Code, wantStatus, rec.Body)
					}
					if tc.wantCode == codes.OK {
						response = new(mecatlv1.CreateSessionResponse)
						if err := json.Unmarshal(rec.Body.Bytes(), response); err != nil {
							t.Fatal(err)
						}
						if tc.request.GetAgentDefinitionName() == "" && bytes.Contains(rec.Body.Bytes(), []byte("resolved_agent_definition_name")) {
							t.Fatal("ordinary HTTP response must omit the binding echo")
						}
					}
				}
				if calls.Load() != tc.wantCalls {
					t.Fatalf("factory calls = %d, want %d", calls.Load(), tc.wantCalls)
				}
				if tc.wantCode != codes.OK {
					return
				}
				if response.GetResolvedAgentDefinitionName() != tc.request.GetAgentDefinitionName() {
					t.Fatalf("binding echo = %q", response.GetResolvedAgentDefinitionName())
				}
				wantProvider, wantModel := "default-provider", "default-model"
				if tc.request.GetAgentDefinitionName() != "" {
					wantProvider, wantModel = "resolved-provider", "resolved-model"
				}
				if rm := response.GetResolvedModel(); rm.GetProviderId() != wantProvider || rm.GetModelId() != wantModel {
					t.Fatalf("resolved model = %v, want %s/%s", rm, wantProvider, wantModel)
				}
				saved, err := store.Load(t.Context(), session.SessionID(response.GetSessionId()))
				if err != nil || saved.AgentDefinitionName != tc.request.GetAgentDefinitionName() {
					t.Fatalf("persisted binding: session=%v error=%v", saved, err)
				}
			})
		}
	}
}

func TestAgentDefinitionHTTPCreateRemainsStrict(t *testing.T) {
	svc := compatibilityInfoService(t, "", mockllm.New().Capabilities())
	t.Cleanup(svc.Close)
	for _, body := range []string{
		`{"agentDefinitionName":"reviewer"}`,
		`{"agent_definition_name":"reviewer","unknown":true}`,
		`{"agent_definition_name":"reviewer","agent_definition_name":"other"}`,
	} {
		rec := httptest.NewRecorder()
		server.NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create %s status = %d, want 400", body, rec.Code)
		}
	}
}
