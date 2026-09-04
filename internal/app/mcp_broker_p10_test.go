package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestMCPBrokerToolsAreExplicitCatalogueInput(t *testing.T) {
	definition := mcpbroker.ToolDefinition{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}
	declaration := mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	catalogue, err := mcpbroker.Compile(declaration, []mcpbroker.ToolDefinition{definition}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := mcpbroker.New(catalogue, func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult("call", "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	attachment, _, err := runtime.AttachSession(context.Background(), "catalogue-session")
	if err != nil {
		t.Fatal(err)
	}

	provider := mockllm.New(mockllm.TextTurn("done"))
	reg := regForTest(provider, providerOpenAI, "test-model")
	factory := sessionEngineFactoryWithTools(Config{Model: "test-model"}, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "/workspace", session.ModeDefault, attachment.Tools())
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	if !result.Engine.HasTool(definition.Name) {
		t.Fatalf("explicit wrapper %q was not assembled", definition.Name)
	}
	if result.Engine.HasTool("mcp__global__fallback") {
		t.Fatal("unexpected global MCP fallback")
	}
}

func TestBuiltMountsFixedMCPBrokerHandlerBundle(t *testing.T) {
	built := &Built{
		MCPBrokerHandlers: mcpbroker.HandlerBundle{Callback: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})},
		MCPBrokerCallbackPath: "/oauth/callback",
	}
	mux := http.NewServeMux()
	if err := built.MountMCPBrokerHandlers(mux); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/oauth/callback", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("callback status = %d", response.Code)
	}
	if err := (&Built{}).MountMCPBrokerHandlers(http.NewServeMux()); err != nil {
		t.Fatalf("empty bundle mount = %v", err)
	}
}

func TestEmptyBrokerAuthoritySkipsUnconfiguredRuntime(t *testing.T) {
	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker != nil || !built.MCPBrokerHandlers.Empty() {
		t.Fatal("empty broker authority constructed broker resources")
	}
}

func TestBuiltOwnsBrokerRuntimeShutdown(t *testing.T) {
	authority := mcpauthority.NewBroker(mcpauthority.BrokerConfig{})
	built, err := Build(context.Background(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, MCPAuthority: authority, MCPBrokerCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult("call", "ok"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if built.MCPBroker == nil {
		t.Fatal("Build did not expose its process-owned broker")
	}
	built.Close()
	if _, _, err := built.MCPBroker.AttachSession(context.Background(), "after-shutdown"); err == nil {
		t.Fatal("broker accepted attachment after Built.Close")
	}
	built.Close()
}
