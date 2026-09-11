package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestCallMcpWithQueryBrokerSupport_Scenario1_BrokerOnlyFactory(t *testing.T) {
	definition := mcpbroker.ToolDefinition{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}
	declaration := mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	catalogue, err := mcpbroker.Compile(declaration, []mcpbroker.ToolDefinition{definition}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := mcpbroker.New(catalogue, func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult("call", "ok"), nil
	}, mcpbroker.WithQueryCaller(func(context.Context, mcpbroker.SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error) {
		return session.NewToolResult("call", `"ok"`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	attachment, _, err := runtime.AttachSession(context.Background(), "catalogue-session")
	if err != nil {
		t.Fatal(err)
	}

	var request port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { request = req })}, mockllm.TextTurn("done"))
	reg := regForTest(provider, providerOpenAI, "test-model")
	factory := sessionEngineFactoryWithTools(Config{Model: "test-model"}, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	tools := attachment.Tools()
	queryAttachment, ok := attachment.(interface{ CallMcpWithQueryTool() tool.Tool })
	if !ok {
		t.Fatal("broker attachment does not expose query wrapper")
	}
	tools = append(tools, queryAttachment.CallMcpWithQueryTool())
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "/workspace", session.ModeDefault, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	if !result.Engine.HasTool(definition.Name) {
		t.Fatalf("explicit wrapper %q was not assembled", definition.Name)
	}
	if !result.Engine.HasTool("CallMcpWithQuery") {
		t.Fatal("broker-only factory did not assemble CallMcpWithQuery")
	}
	if query := tools[len(tools)-1]; query.Spec().Name != "CallMcpWithQuery" || query.Spec().Description != mcp.CallMcpWithQuerySpec().Description {
		t.Fatalf("broker query spec changed: %+v", query.Spec())
	}
	if result.Engine.HasTool("mcp__global__fallback") {
		t.Fatal("unexpected global MCP fallback")
	}
	env := memEnvironment("/workspace")
	sess := session.New("catalogue-session", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
	for range result.Engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "hello"}).Events() {
	}
	count := 0
	for _, spec := range request.Tools {
		if spec.Name == "CallMcpWithQuery" {
			count++
			want := mcp.CallMcpWithQuerySpec()
			if spec.Description != want.Description || !bytes.Equal(spec.Schema, want.Schema) {
				t.Fatalf("built model specification changed: %+v", spec)
			}
		}
	}
	if count != 1 {
		t.Fatalf("model received %d query wrappers", count)
	}
	empty, err := factory(t.Context(), server.ProviderSelector{}, nil, server.ProfileDefault, "/workspace", session.ModeDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if empty.Engine.HasTool("CallMcpWithQuery") {
		t.Fatal("query advertised with no eligible targets")
	}
}

func TestCallMcpWithQueryBrokerSupport_BrokerOnlyServiceRegistration(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		name := "no targets"
		if eligible {
			name = "eligible target"
		}
		t.Run(name, func(t *testing.T) {
			var request port.LLMRequest
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { request = req })}, mockllm.TextTurn("done"))
			var definitions []mcpbroker.ToolDefinition
			if eligible {
				definitions = []mcpbroker.ToolDefinition{{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}
			}
			built, err := Build(t.Context(), Config{
				Workspace: t.TempDir(), MockProvider: provider, NoSoul: true,
				MCPAuthority:        mcpauthority.NewBroker(mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}),
				MCPBrokerDiscovered: definitions,
				MCPBrokerCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
					t.Error("unexpected raw invocation")
					return session.ToolResult{}, nil
				},
				MCPBrokerQueryCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error) {
					t.Error("unexpected query invocation")
					return session.ToolResult{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(t.Context(), sess.ID, "hello")
			if err != nil {
				t.Fatal(err)
			}
			for range run.Events() {
			}
			built.Service.FinishRun(sess.ID, run)
			if len(request.Tools) == 0 {
				t.Fatal("model request was not observed")
			}
			count := 0
			for _, spec := range request.Tools {
				if spec.Name == "CallMcpWithQuery" {
					count++
					want := mcp.CallMcpWithQuerySpec()
					if spec.Description != want.Description || !bytes.Equal(spec.Schema, want.Schema) {
						t.Fatalf("built model specification changed: %+v", spec)
					}
				}
			}
			want := 0
			if eligible {
				want = 1
			}
			if count != want {
				t.Fatalf("model received %d query wrappers, want %d", count, want)
			}
		})
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

type staticBrokerAuthorityLoader struct {
	authority *mcpauthority.Result
}

func (l staticBrokerAuthorityLoader) LoadAuthority(*permconfig.MCPSection, mcpauthority.Mode, bool) (*mcpauthority.Result, error) {
	return l.authority, nil
}

func TestBuildRejectsProgrammaticMCPServersWithBrokerAuthority(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "direct authority",
			cfg:  Config{MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{})},
		},
		{
			name: "loader-resolved authority",
			cfg:  Config{MCPAuthorityLoader: staticBrokerAuthorityLoader{authority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{})}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.cfg
			cfg.Workspace = t.TempDir()
			cfg.UseMock = true
			cfg.NoSoul = true
			cfg.MCPServers = []mcp.ServerConfig{{Name: "global", URL: "http://127.0.0.1:1/mcp"}}

			_, err := Build(t.Context(), cfg)
			if err == nil || err.Error() != "broker MCP authority cannot be combined with programmatic MCPServers" {
				t.Fatalf("Build error = %v, want mixed broker authority and MCPServers rejection", err)
			}
		})
	}
}
