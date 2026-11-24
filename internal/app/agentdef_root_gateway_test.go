package app

import (
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestAgentBoundRootGetsItsDefinitionsGatewayTools drives the real
// agent-bound session factory with a session's gateway tools (#2938). Each
// root gets exactly the gateway tools of the servers its definition
// references, minus those it excludes, as a named helper does: they are in
// the catalog, in the authority and ceiling minted from it, and in the tools
// the model is offered. A server it does not reference, a definition that
// references none, and a tool without a gateway route grant nothing. The
// root calls its allowed tool, and a call of an excluded one never runs.
func TestAgentBoundRootGetsItsDefinitionsGatewayTools(t *testing.T) {
	var lookups, echoes, reads atomic.Int32
	gatewayTools := []tool.Tool{
		countingGatewayTool{fakeGatewayTool{"calendar", "lookup"}, &lookups},
		countingGatewayTool{fakeGatewayTool{"calendar", "echo"}, &echoes},
		countingGatewayTool{fakeGatewayTool{"github", "read"}, &reads},
		// A broker route that acts as the session; the server never passes
		// one, and the factory would not keep it if it did.
		notGatewayTool{fakeGatewayTool{"calendar", "admin"}},
	}
	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "no-echo", Tools: []string{"Read"}, MCPServers: []agents.AgentMCPServer{{Name: "calendar"}}, DisallowedTools: []string{"mcp__calendar__echo"}},
		{Name: "no-servers", Tools: []string{"Read"}},
		{Name: "tools-only", Tools: []string{"Read", "mcp__calendar__lookup"}},
	})
	all := []string{"mcp__calendar__admin", "mcp__calendar__echo", "mcp__calendar__lookup", "mcp__github__read"}

	for name, want := range map[string][]string{
		"no-echo":    {"mcp__calendar__lookup"},
		"no-servers": nil,
		"tools-only": nil,
	} {
		t.Run(name, func(t *testing.T) {
			lookups.Store(0)
			echoes.Store(0)
			reads.Store(0)
			var offered []string
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				if offered != nil {
					return
				}
				offered = []string{}
				for _, spec := range req.Tools {
					offered = append(offered, spec.Name)
				}
			})},
				mockllm.ToolCallTurn(session.ToolCall{ID: "c0", Name: "mcp__calendar__lookup", Args: json.RawMessage(`{}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "c1", Name: "mcp__calendar__echo", Args: json.RawMessage(`{}`)}),
				mockllm.TextTurn("done"))
			reg := regForTest(provider, providerOpenAI, "main-model")
			cfg := isolateConfig(t, Config{Model: "main-model"})
			factory := agentDefSessionEngineFactory(cfg, reg, provider, memstore.New(), testAgentDefRootPolicy(cfg), hookexec.New(nil), nil, nil, catalogAssets{agentReg: defs}, nil)
			ctx := server.ContextWithAgentDefGatewayTools(t.Context(), gatewayTools)
			res, err := factory(ctx, server.ProviderSelector{}, server.ProfileDefault, session.ModeDefault, session.Limits{}, name)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Close()

			for _, tn := range all {
				has := slices.Contains(want, tn)
				if res.Engine.HasTool(tn) != has {
					t.Errorf("catalog has %s = %v, want %v", tn, !has, has)
				}
				if slices.Contains(res.Authority.CapabilitySet.Tools, tn) != has {
					t.Errorf("authority allows %s = %v, want %v", tn, !has, has)
				}
				if res.Authority.Ceiling == nil || slices.Contains(res.Authority.Ceiling.Tools, tn) != has {
					t.Errorf("ceiling allows %s: want %v", tn, has)
				}
			}

			sess := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			drainRun(res.Engine.Run(t.Context(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"}))
			for _, tn := range all {
				if slices.Contains(offered, tn) != slices.Contains(want, tn) {
					t.Errorf("the model was offered %v, want the gateway tools %v", offered, want)
					break
				}
			}
			wantLookups := int32(0)
			if len(want) != 0 {
				wantLookups = 1
			}
			if lookups.Load() != wantLookups || echoes.Load() != 0 || reads.Load() != 0 {
				t.Fatalf("gateway tools ran: lookup %d, echo %d, read %d; want lookup %d and no other",
					lookups.Load(), echoes.Load(), reads.Load(), wantLookups)
			}
		})
	}
}

// notGatewayTool is a broker tool with no gateway route.
type notGatewayTool struct{ fakeGatewayTool }

func (notGatewayTool) GatewayServer() {}
