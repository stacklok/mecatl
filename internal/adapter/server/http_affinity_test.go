package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestSessionAffinityAndHandoff_Scenario3_HTTPRouteInventory(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	h := server.NewHTTPHandler(svc)

	for _, route := range []struct {
		name, method, path, body string
	}{
		{"get", http.MethodGet, "/v1/sessions/route-id", ""},
		{"transcript", http.MethodGet, "/v1/sessions/route-id/transcript", ""},
		{"mode", http.MethodPost, "/v1/sessions/route-id/mode", `{"mode":"default"}`},
		{"close", http.MethodDelete, "/v1/sessions/route-id", ""},
		{"rename", http.MethodPost, "/v1/sessions/route-id/rename", `{"title":"title"}`},
		{"delete", http.MethodPost, "/v1/sessions/route-id/delete", ""},
		{"compact", http.MethodPost, "/v1/sessions/route-id/compact", ""},
		{"prompt", http.MethodPost, "/v1/sessions/route-id/prompt", `{"text":"prompt"}`},
		{"retry", http.MethodPost, "/v1/sessions/route-id/retry", ""},
		{"approve", http.MethodPost, "/v1/sessions/route-id/approve", `{"ask_id":"ask"}`},
		{"plan approve", http.MethodPost, "/v1/sessions/route-id/plan:approve", ""},
		{"cancel", http.MethodPost, "/v1/sessions/route-id/cancel", ""},
		{"cancel child", http.MethodPost, "/v1/sessions/route-id/cancel-child", `{"child_id":"child"}`},
		{"fork", http.MethodPost, "/v1/sessions/route-id/fork", ""},
		{"adoption preflight", http.MethodPost, "/v1/sessions/route-id/adoption:preflight", `{"workspace":"/adopted","environment_kind":"local","environment_id":"/adopted","provider_id":"provider","model_id":"model","profile":""}`},
		{"adopt", http.MethodPost, "/v1/sessions/route-id/adopt", `{"workspace":"/adopted","environment_kind":"local","environment_id":"/adopted","provider_id":"provider","model_id":"model","profile":"","idempotency_key":"key"}`},
		{"reflect", http.MethodPost, "/v1/sessions/route-id/reflect", ""},
		{"events", http.MethodGet, "/v1/sessions/route-id/events", ""},
		{"watch", http.MethodGet, "/v1/sessions/route-id/watch", ""},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, header := range []string{"", "route-id"} {
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				if header != "" {
					req.Header.Set(port.SessionIDHeaderName, header)
				}
				resp := httptest.NewRecorder()
				h.ServeHTTP(resp, req)
				if resp.Code == http.StatusBadRequest {
					t.Fatalf("%s with affinity header %q status = 400, want handler dispatch: %s", route.name, header, resp.Body.String())
				}
			}
		})
	}
}

func TestADR_0290_HTTPHeaderFailureIsNonDisclosing(t *testing.T) {
	h := server.NewHTTPHandler(newService(t, mockllm.New(), allowRules()))

	for _, tc := range []struct {
		name    string
		headers []string
	}{
		{"duplicate", []string{"route-id", "route-id"}},
		{"illegal", []string{"route-id\nforged"}},
		{"mismatch", []string{"other-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/sessions/route-id", nil)
			for _, value := range tc.headers {
				req.Header.Add(port.SessionIDHeaderName, value)
			}
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.Code)
			}
			var problem struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
				Error  string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Code != "invalid_argument" {
				t.Errorf("problem code = %q, want invalid_argument", problem.Code)
			}
			body := problem.Detail + problem.Error
			for _, secret := range append(tc.headers, "route-id") {
				if strings.Contains(body, secret) {
					t.Errorf("response disclosed affinity value %q in %#v", secret, problem)
				}
			}
		})
	}
}

func TestADR_0290_HTTPDecodedPathEquality(t *testing.T) {
	h := server.NewHTTPHandler(newService(t, mockllm.New(), allowRules()))

	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{"decoded exact", "decoded id", http.StatusNotFound},
		{"encoded mismatch", "decoded%20id", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/sessions/decoded%20id", nil)
			req.Header.Set(port.SessionIDHeaderName, tc.header)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status = %d, want %d", resp.Code, tc.want)
			}
		})
	}
}

func TestADR_0290_AffinityHeaderGrantsNoAuthority(t *testing.T) {
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model"})
	svc, err := server.NewService(server.Config{
		Engine: engine, Store: memstore.New(), Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }, OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "owner", GrantType: session.GrantTypeUser}
	sess, err := svc.CreateSession(session.WithPrincipal(context.Background(), owner), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+string(sess.ID), nil)
	req.Header.Set(port.SessionIDHeaderName, string(sess.ID))
	intruder := &session.Principal{Issuer: "https://issuer.example", Subject: "intruder", GrantType: session.GrantTypeUser}
	req = req.WithContext(session.WithPrincipal(req.Context(), intruder))
	resp := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("header-matched intruder status = %d, want 404", resp.Code)
	}
}
