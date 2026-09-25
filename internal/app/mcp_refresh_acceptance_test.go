package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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
	built, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, MockProvider: provider, NoSoul: true,
		AllowAllTools:      true,
		GuardrailsDisabled: true,
		MCPServers:         []mcp.ServerConfig{{Name: "live", URL: newMCPTestServer(t)}},
	})
	if err != nil {
		t.Fatalf("Build with published MCP runtime: %v", err)
	}
	defer built.Close()

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
	if got := drainRun(run); got != "after complete" {
		t.Fatalf("after-refresh final text = %q", got)
	}
	built.Service.FinishRun(persisted.ID, run)
	afterRefresh, err := built.Service.GetSession(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("GetSession after execution: %v", err)
	}
	assertMCPRefreshToolResult(t, afterRefresh, "after-refresh", "echo:after")
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
