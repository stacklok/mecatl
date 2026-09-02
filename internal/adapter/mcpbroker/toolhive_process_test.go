package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestToolHiveConstructionPreservesProtectedOrderAndPrivateMapping(t *testing.T) {
	profiles := []ToolHiveProfile{
		protectedToolHiveProfile("GitHub_Cloud"),
		{Name: "public", URL: "https://public.example/mcp", Auth: authNone},
		protectedToolHiveProfile("Calendar_API"),
	}

	construction, err := compileToolHiveConstruction(profiles, "https://broker.example/v1/mcp/broker")
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	if got := []string{construction.upstreams[0].Name, construction.upstreams[1].Name}; !reflect.DeepEqual(got, []string{"github-cloud", "calendar-api"}) {
		t.Fatalf("protected upstream order = %v", got)
	}
	if got := construction.protectedBackends; !reflect.DeepEqual(got, []string{"GitHub_Cloud", "Calendar_API"}) {
		t.Fatalf("protected backend order = %v", got)
	}
	if got := construction.providerByBackend["GitHub_Cloud"]; got != "github-cloud" {
		t.Fatalf("GitHub provider = %q", got)
	}
	if got := construction.providerByBackend["Calendar_API"]; got != "calendar-api" {
		t.Fatalf("Calendar provider = %q", got)
	}
	if construction.backends[0].AuthConfig.UpstreamInject.ProviderName != "github-cloud" || construction.backends[2].AuthConfig.UpstreamInject.ProviderName != "calendar-api" {
		t.Fatalf("backend provider mapping = %#v", construction.backends)
	}
}

func TestToolHiveConstructionRejectsInvalidProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []ToolHiveProfile
	}{
		{"duplicate backend", []ToolHiveProfile{{Name: "same", URL: "https://one.example/mcp", Auth: authNone}, {Name: "SAME", URL: "https://two.example/mcp", Auth: authNone}}},
		{"duplicate provider", []ToolHiveProfile{protectedToolHiveProfile("name"), protectedToolHiveProfile("name_")}},
		{"missing oauth", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth}}},
		{"missing client", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{Issuer: "https://issuer.example"}}}},
		{"partial endpoints", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{ClientID: "client", AuthorizationEndpoint: "https://issuer.example/authorize"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileToolHiveConstruction(test.profiles, "https://broker.example/v1/mcp/broker"); !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("compileToolHiveConstruction error = %v, want ErrInvalidCatalogue", err)
			}
		})
	}
}

func TestToolHiveProcessAnonymousDiscoveryOmitsProtectedAndStaticTools(t *testing.T) {
	var anonymousRequests, protectedRequests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "status", &anonymousRequests)
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		protectedRequests.Add(1)
		http.Error(w, "authorization required", http.StatusUnauthorized)
	}))
	t.Cleanup(protected.Close)
	private := protectedToolHiveProfile("GitHub_API")
	private.URL = protected.URL
	private.Static = []StaticTool{{Name: "reviewed", Description: "comparison only", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}

	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{
		{Name: "public", URL: anonymous.URL, Auth: authNone}, private,
	}})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if anonymousRequests.Load() == 0 {
		t.Fatal("anonymous upstream was not discovered")
	}
	if got := protectedRequests.Load(); got != 0 {
		t.Fatalf("protected startup requests = %d, want 0", got)
	}
	if got := process.Runtime.catalogue.Specs(); len(got) != 1 || got[0].Name != "mcp__public__status" {
		t.Fatalf("startup catalogue = %#v, want anonymous tool only", got)
	}
	if got := process.construction.staticByBackend["GitHub_API"]; len(got) != 1 || got[0].Name != "reviewed" {
		t.Fatalf("protected static declarations = %#v", got)
	}
}

func TestProcessRollbackDrainsRuntimeThenResourcesAndCancelsContext(t *testing.T) {
	runtime, err := New(&Catalogue{}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	process := &Process{Runtime: runtime, ctx: ctx, resources: []ownedResource{
		{name: "context", close: func() error { order = append(order, "context"); cancel(); return nil }},
		{name: "auth", close: func() error { order = append(order, "auth"); return nil }},
		{name: "vmcp", close: func() error { order = append(order, "vmcp"); return nil }},
	}}
	process.rollback()
	if got := strings.Join(order, ","); got != "vmcp,auth,context" {
		t.Fatalf("resource rollback order = %q", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("process context remained live after rollback")
	}
	if _, _, err := runtime.AttachSession(t.Context(), "after-rollback"); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("runtime after rollback = %v, want contract.ErrStateUnavailable", err)
	}
}

func TestProcessCloseDrainsRuntimeThenResourcesCancelsContextAndIsIdempotent(t *testing.T) {
	runtime, err := New(&Catalogue{}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	process := &Process{Runtime: runtime, ctx: ctx, resources: []ownedResource{
		{name: "context", close: func() error { order = append(order, "context"); cancel(); return nil }},
		{name: "auth", close: func() error { order = append(order, "auth"); return nil }},
		{name: "vmcp", close: func() error {
			if _, _, attachErr := runtime.AttachSession(t.Context(), "during-close"); !errors.Is(attachErr, contract.ErrStateUnavailable) {
				t.Errorf("runtime was not closed before resources: %v", attachErr)
			}
			order = append(order, "vmcp")
			return nil
		}},
	}}
	if err := process.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := strings.Join(order, ","); got != "vmcp,auth,context" {
		t.Fatalf("Close resource order = %q", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("process context remained live after Close")
	}
	if _, _, err := runtime.AttachSession(t.Context(), "after-close"); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("runtime after Close = %v, want contract.ErrStateUnavailable", err)
	}
}

func protectedToolHiveProfile(name string) ToolHiveProfile {
	return ToolHiveProfile{Name: name, URL: "https://" + strings.ToLower(strings.Trim(name, "_")) + ".example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{
		AuthorizationEndpoint: "https://issuer.example/authorize",
		TokenEndpoint:         "https://issuer.example/token",
		ClientID:              name + "-client",
		Scopes:                []string{"openid"},
	}}
}

func toolHiveDiscoveryServer(t *testing.T, toolName string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: toolName, Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: toolName}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, struct{}, error) {
		return &mcpsdk.CallToolResult{}, struct{}{}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}
