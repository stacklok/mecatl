package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0290_HTTPCreateSessionDerivedAffinity(t *testing.T) {
	h := server.NewHTTPHandler(newService(t, mockllm.New(), allowRules()))
	for _, tc := range []struct {
		name, body string
		headers    []string
		want       int
	}{
		{name: "source exact", body: `{"workspace":"/ws","source_session_id":"source"}`, headers: []string{"source"}, want: http.StatusNotFound},
		{name: "debug exact", body: `{"profile":"no-fs","debug_target_session_id":"target"}`, headers: []string{"target"}, want: http.StatusNotFound},
		{name: "source headerless compatibility", body: `{"workspace":"/ws","source_session_id":"source"}`, want: http.StatusNotFound},
		{name: "no derived reference rejects header", body: `{"workspace":"/ws"}`, headers: []string{"source"}, want: http.StatusBadRequest},
		{name: "mismatch", body: `{"workspace":"/ws","source_session_id":"source"}`, headers: []string{"other"}, want: http.StatusBadRequest},
		{name: "duplicate", body: `{"workspace":"/ws","source_session_id":"source"}`, headers: []string{"source", "source"}, want: http.StatusBadRequest},
		{name: "ambiguous dual reference", body: `{"workspace":"/ws","source_session_id":"source","debug_target_session_id":"target"}`, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(tc.body))
			for _, affinity := range tc.headers {
				req.Header.Add(sessionaffinity.HeaderName, affinity)
			}
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
			}
		})
	}
}

func TestSessionAffinityAndHandoff_Scenario3_HTTPRouteInventory(t *testing.T) {
	type outcome struct {
		body        string
		contentType string
		status      int
	}
	request := func(h http.Handler, route struct{ name, method, path, body string }, headers ...string) outcome {
		t.Helper()
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		for _, header := range headers {
			req.Header.Add(sessionaffinity.HeaderName, header)
		}
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		return outcome{body: resp.Body.String(), contentType: resp.Header().Get("Content-Type"), status: resp.Code}
	}

	var commonFailure *outcome
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
			h := server.NewHTTPHandler(newService(t, mockllm.New(), allowRules()))
			baseline := request(h, route)
			exact := request(h, route, "route-id")
			if exact != baseline {
				t.Fatalf("exact affinity outcome = %#v, want headerless baseline %#v", exact, baseline)
			}
			for name, headers := range map[string][]string{
				"mismatch":  {"other-id"},
				"duplicate": {"route-id", "route-id"},
			} {
				t.Run(name, func(t *testing.T) {
					got := request(h, route, headers...)
					if got.status != http.StatusBadRequest {
						t.Fatalf("outcome = %#v, want pre-dispatch 400", got)
					}
					if commonFailure == nil {
						captured := got
						commonFailure = &captured
					} else if got != *commonFailure {
						t.Fatalf("affinity failure = %#v, want common pre-dispatch outcome %#v", got, *commonFailure)
					}
				})
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
				req.Header.Add(sessionaffinity.HeaderName, value)
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
			req.Header.Set(sessionaffinity.HeaderName, tc.header)
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
	req.Header.Set(sessionaffinity.HeaderName, string(sess.ID))
	intruder := &session.Principal{Issuer: "https://issuer.example", Subject: "intruder", GrantType: session.GrantTypeUser}
	req = req.WithContext(session.WithPrincipal(req.Context(), intruder))
	resp := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("header-matched intruder status = %d, want 404", resp.Code)
	}
}
