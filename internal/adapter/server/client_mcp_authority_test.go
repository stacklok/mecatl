package server_test

import (
	"context"
	"slices"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// clientMCPServiceWithRealAuthority is clientMCPServiceUnreachable's sibling,
// built specifically to catch what that fixture cannot: every other test in
// this package leaves Config.RootAuthority unset, so sess.BoundAuthority()
// never binds (session.Authority{}'s zero Provenance means RestoreLabels never
// calls BindAuthority) and authorizeExecution's very first check
// ("!bound => skip enforcement") exits before the capability set is ever
// consulted. Those tests would pass identically whether or not a client-mounted
// tool were ever added to that set -- they cannot see this bug.
//
// This fixture binds a REAL root authority (non-empty Provenance) and wires
// the REAL localauthority.Evaluator (the same one a default/unconfigured
// mecated deployment runs, per selectAuthorityEvaluator's "", "local" case) as
// the session engine's AuthorityEvaluator, so a client-mounted tool call goes
// through the exact enforcement path production does.
func clientMCPServiceWithRealAuthority(t *testing.T, llm *mockllm.Provider) *server.Service {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("shared")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine:            shared,
		Store:             memstore.New(),
		SharedEngineRoot:  "/ws",
		Now:               func() time.Time { return time.Unix(0, 0) },
		ClientMCPOnCreate: true,
		// Bound (non-empty Provenance), otherwise session.Authority is never
		// considered "bound" and authority enforcement never engages at all --
		// see the fixture's doc comment above for why every other test's
		// fixture leaves this unset.
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{
				CapabilitySet: governance.CapabilitySet{RemainingDelegationDepth: 1},
				Provenance:    "test_root",
			}
		},
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			cat := tool.NewCatalog()
			var mounted []string
			var mountedTools []string
			for _, s := range specs {
				name := "mcp__" + s.Name + "__ping"
				cat.MustRegister(&scriptTool{name: name, readOnly: true, content: "pong from " + s.Name})
				mounted = append(mounted, s.Name)
				mountedTools = append(mountedTools, name)
			}
			return server.SessionEngineResult{
				Engine: agent.NewEngine(agent.Deps{
					LLM:                llm,
					Catalog:            cat,
					Policy:             permpolicy.NewPolicy(allowRules(), permstore.New()),
					Model:              "test-model",
					AuthorityEvaluator: localauthority.New(),
				}),
				MountedClientMCP:      mounted,
				MountedClientMCPTools: mountedTools,
				Close:                 func() error { return nil },
			}, nil
		},
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestMountedClientMCPToolsAreAuthorized reproduces, in a fast deterministic
// unit test, the exact failure a real mecated hit for any SDK client.tool()
// callback under the default "local" authority evaluator: the tool mounted,
// dispatch reached the local MCP server, and the model's call was STILL
// refused with "tool ... denied by authority: tool is absent from the
// capability set" -- because root authority is minted once per session KIND,
// with no visibility into what a session's own client just mounted.
func TestMountedClientMCPToolsAreAuthorized(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "mcp__notes__ping", `{}`)),
		mockllm.TextTurn("used the client server"),
	)
	svc := clientMCPServiceWithRealAuthority(t, llm)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
		McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
	})
	if err != nil {
		t.Fatalf("CreateSession with mcp_servers: %v", err)
	}

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "use it"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()

	events := recvAll(t, stream)
	idx := slices.IndexFunc(events, func(e *mecatlv1.Event) bool { return e.GetType() == "tool.result" })
	if idx == -1 {
		t.Fatalf("no tool.result event: %v", typesOf(events))
	}
	result := events[idx].GetToolResult()
	if result.GetIsError() {
		t.Fatalf("client-mounted tool call was denied: %s", result.GetContent())
	}
	if result.GetContent() != "pong from notes" {
		t.Fatalf("tool result = %q, want the real tool output", result.GetContent())
	}
	if res := lastResult(t, events); res.GetText() != "used the client server" {
		t.Fatalf("result = %+v", res)
	}
}
