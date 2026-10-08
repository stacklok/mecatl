package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// TestMCPSourceReconciliation_Scenario4_RealBuildPublicationAndExplicitGrant
// covers the production Build-owned reconciler and Service grant path together.
// The session predates the published MCP runtime, so publication alone must not
// widen its durable authority; explicit refresh then makes the same live tool
// executable without rebuilding the process.
func TestMCPSourceReconciliation_Scenario4_RealBuildPublicationAndExplicitGrant(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	workspace := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "sessions")

	before, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, UseMock: true, NoSoul: true,
	})
	if err != nil {
		t.Fatalf("Build before MCP publication: %v", err)
	}
	persisted, err := before.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		before.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	before.Close()

	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("before-refresh", "mcp__live__echo", []byte(`{"text":"before"}`))),
		mockllm.TextTurn("before complete"),
		mockllm.ToolCallTurn(session.NewToolCall("after-refresh", "mcp__live__echo", []byte(`{"text":"after"}`))),
		mockllm.TextTurn("after complete"),
	)
	backend, err := url.Parse(newMCPTestServer(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	var available atomic.Bool
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer flaky.Close()
	built, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, MockProvider: provider, NoSoul: true,
		MCPServers: []mcp.ServerConfig{{Name: "live", URL: newMCPTestServer(t)}, {Name: "flaky", URL: flaky.URL}},
	})
	if err != nil {
		t.Fatalf("Build with published MCP runtime: %v", err)
	}
	defer built.Close()
	if status := built.Service.ListMcpSources(ctx); len(status.Sources) != 1 || len(status.Sources[0].Diagnostics) != 1 {
		t.Fatalf("partial Build status = %+v", status)
	}

	run, err := built.Service.StartRun(ctx, persisted.ID, "call newly published tool before grant")
	if err != nil {
		t.Fatalf("StartRun before refresh: %v", err)
	}
	if got := drainRun(run); got != "before complete" {
		t.Fatalf("before-refresh final text = %q", got)
	}
	built.Service.FinishRun(persisted.ID, run)
	beforeRefresh, err := built.Service.GetSession(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("GetSession before refresh: %v", err)
	}
	assertMCPRefreshToolResult(t, beforeRefresh, "before-refresh", "absent from the capability set")

	result, err := built.Service.RefreshMcpSources(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("RefreshMcpSources: %v", err)
	}
	if result.Revision == 0 || !result.Changed {
		t.Fatalf("refresh result = %+v, want pinned nonzero revision and authority change", result)
	}
	granted, err := built.Service.GetSession(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("GetSession after refresh: %v", err)
	}
	authority, bound := granted.BoundAuthority()
	if !bound || !slices.Contains(authority.CapabilitySet.Tools, "mcp__live__echo") {
		t.Fatalf("authority after explicit refresh = %+v, bound=%v", authority, bound)
	}

	run, err = built.Service.StartRun(ctx, persisted.ID, "call newly granted tool")
	if err != nil {
		t.Fatalf("StartRun after refresh: %v", err)
	}
	var asked bool
	var final string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			asked = true
			if err := run.Approve(ev.Ask.AskID, session.VerdictAllowOnce); err != nil {
				t.Fatal(err)
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if !asked || final != "after complete" {
		t.Fatalf("partial startup tool permission ask=%v final=%q", asked, final)
	}
	built.Service.FinishRun(persisted.ID, run)
	afterRefresh, err := built.Service.GetSession(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("GetSession after execution: %v", err)
	}
	assertMCPRefreshToolResult(t, afterRefresh, "after-refresh", "echo:after")

	available.Store(true)
	recovered, err := built.Service.RefreshMcpSources(ctx, persisted.ID)
	if err != nil || !recovered.Changed || recovered.Revision == result.Revision {
		t.Fatalf("recovery refresh = (%+v, %v)", recovered, err)
	}
	status := built.Service.ListMcpSources(ctx)
	if len(status.Sources) != 1 || len(status.Sources[0].Diagnostics) != 0 {
		t.Fatalf("recovered status = %+v", status)
	}
	updated, err := built.Service.GetSession(ctx, persisted.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, bound = updated.BoundAuthority()
	if !bound || !slices.Contains(authority.CapabilitySet.Tools, "mcp__flaky__echo") {
		t.Fatalf("recovered peer missing from session authority: %+v", authority)
	}
}

func assertMCPRefreshToolResult(t *testing.T, sess *session.Session, callID, contains string) {
	t.Helper()
	for _, msg := range sess.Conversation.Messages {
		if msg.ToolResult != nil && msg.ToolResult.CallID == session.ToolCallID(callID) {
			if !strings.Contains(msg.ToolResult.Content, contains) {
				t.Fatalf("tool result %q = %q, want substring %q", callID, msg.ToolResult.Content, contains)
			}
			return
		}
	}
	t.Fatalf("no tool result for call %q", callID)
}
