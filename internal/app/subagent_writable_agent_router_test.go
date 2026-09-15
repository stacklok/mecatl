package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// writableRouteReq records the model, prompt, and scoped tool surface of one request.
type writableRouteReq struct {
	model, system string
	tools         []string
}

// TestWritableRoutableDefFullBuildE2E drives the production Build → Service path. It
// proves one classifier decision rebuilds the writable named specialist on the routed
// model without losing its prompt or scoped Write tool, and that Write runs directly in
// the parent workspace rather than a fork.
func TestWritableRoutableDefFullBuildE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	agentsDir := t.TempDir()
	mcpURL := newMCPTestServer(t)
	const bodyMarker = "WRITABLE-ROUTED-SPECIALIST-BODY"
	if err := os.WriteFile(filepath.Join(agentsDir, "writer.md"), []byte("---\nname: writer\ndescription: writable specialist\ntools: [Read, Write, Shell]\nmcpServers:\n  - name: main\n---\n"+bodyMarker), 0o644); err != nil {
		t.Fatalf("write agent def: %v", err)
	}

	var (
		mu   sync.Mutex
		reqs []writableRouteReq
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:             workspace,
		NoSoul:                true,
		Model:                 "gpt-5",
		Shell:                 "/bin/sh",
		AgentsDirs:            []string{agentsDir},
		MCPServers:            []mcp.ServerConfig{{Name: "main", URL: mcpURL}},
		RouterCategories:      routerTaxonomyCategories(),
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				toolNames := make([]string, 0, len(r.Tools))
				for _, spec := range r.Tools {
					toolNames = append(toolNames, spec.Name)
				}
				mu.Lock()
				reqs = append(reqs, writableRouteReq{model: r.Model, system: r.System.Render(), tools: toolNames})
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"write the routed result","mode":"read-write","agent":"writer"}`))),
				mockllm.TextTurn(`{"category":"large"}`),
				mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", []byte(`{"path":"routed.txt","content":"written in parent\n"}`))),
				mockllm.ToolCallTurn(session.NewToolCall("b1", "Shell", []byte(`{"command":"printf 'bash in parent\\n' > bash-routed.txt"}`))),
				mockllm.ToolCallTurn(session.NewToolCall("m1", "mcp__main__echo", []byte(`{"text":"authority retained"}`))),
				mockllm.TextTurn("writable routed specialist done"),
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	final, starts := drainRunWithSubagentStart(run)
	if final != "parent done" {
		t.Fatalf("terminal text = %q, want parent done", final)
	}
	if len(starts) != 1 {
		t.Fatalf("subagent starts = %+v, want exactly one", starts)
	}
	start := starts[0]
	if start.RoutedCategory != "large" || start.RoutedModel != routerLarge || start.RoutingReason != "" || start.Model != routerLarge {
		t.Fatalf("subagent start routing metadata = %+v", start)
	}

	mu.Lock()
	gotReqs := append([]writableRouteReq(nil), reqs...)
	mu.Unlock()
	// parent → classifier → child Write → child Shell → child reference-MCP call → child
	// summary → parent. Exactly seven requests proves the writable named delegation was
	// classified exactly once and retained the referenced MCP tool after routing.
	if len(gotReqs) != 7 {
		t.Fatalf("recorded %d requests (%+v), want 7", len(gotReqs), gotReqs)
	}
	for _, i := range []int{2, 3, 4, 5} {
		if gotReqs[i].model != routerLarge {
			t.Fatalf("routed child request %d model = %q, want %q", i, gotReqs[i].model, routerLarge)
		}
		if !strings.Contains(gotReqs[i].system, bodyMarker) {
			t.Fatalf("routed child request %d lost specialist body; system=%q", i, gotReqs[i].system)
		}
		if !slices.Contains(gotReqs[i].tools, "Write") || !slices.Contains(gotReqs[i].tools, "Shell") || !slices.Contains(gotReqs[i].tools, "mcp__main__echo") {
			t.Fatalf("routed child request %d lost scoped writable/reference-MCP tools; tools=%v", i, gotReqs[i].tools)
		}
		if slices.Contains(gotReqs[i].tools, "Edit") {
			t.Fatalf("routed child request %d used generic writable explorer catalog; tools=%v", i, gotReqs[i].tools)
		}
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "routed.txt")); err != nil {
		t.Fatalf("direct write did not land in parent workspace: %v", err)
	} else if string(got) != "written in parent\n" {
		t.Fatalf("routed.txt = %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "bash-routed.txt")); err != nil {
		t.Fatalf("main-bound Shell did not mutate the parent workspace: %v", err)
	} else if string(got) != "bash in parent\n" {
		t.Fatalf("bash-routed.txt = %q", got)
	}
	child, err := built.Service.GetSession(ctx, session.SessionID("subagent-"+string(sess.ID)+"-c1"))
	if err != nil {
		t.Fatalf("GetSession(routed child): %v", err)
	}
	authority, bound := child.BoundAuthority()
	if !bound || !authority.CapabilitySet.DirectWrite || !authority.CapabilitySet.AllowsTool("Write") || !authority.CapabilitySet.AllowsTool("Shell") || !authority.CapabilitySet.AllowsTool("mcp__main__echo") || authority.CapabilitySet.AllowsTool("Edit") {
		t.Fatalf("routed writable specialist authority = %+v, bound=%t", authority, bound)
	}
	assertNoSiblingForkDir(t, workspace)
}

// TestBuildAgentWritableModelEngineFactoryGuards pins the composition-side defensive
// policy. The router gate normally bypasses these defs; the factory itself must still
// reject pinned, known provider-switched, and inline-MCP defs. An unknown provider keeps
// resolveProviderModel's established loud fallback and remains on the parent provider.
func TestBuildAgentWritableModelEngineFactoryGuards(t *testing.T) {
	parent := mockllm.New()
	other := mockllm.New()
	provReg := twoProviderReg(parent, providerOpenAI, "gpt-5", other, providerOpenRouter)
	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "pinned", Model: "gpt-4.1", Tools: []string{"Write"}},
		{Name: "switched", Provider: providerOpenRouter, Tools: []string{"Write"}},
		{Name: "inline", MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: "http://127.0.0.1:1/mcp"}}},
		{Name: "unknown-provider", Provider: "not-installed", Tools: []string{"Write"}},
	})
	cfg := Config{Model: "gpt-5", Diagnostics: port.NopDiagnostics{}}
	if got := routableAgentNames(provReg, defs, providerOpenAI); !slices.Equal(got, []string{"unknown-provider"}) {
		t.Fatalf("router eligibility = %v, want only the unknown-provider fallback def", got)
	}
	_, factory := buildAgentWritableEngineFactories(context.Background(), cfg, provReg, parent,
		providerOpenAI, "gpt-5", defs, nil, hookexec.New(nil), nil)

	for _, name := range []string{"pinned", "switched", "inline"} {
		if eng, ok := factory(name, routerLarge); ok || eng != nil {
			t.Fatalf("factory(%q, routed) = (%v,%v), want defensive decline", name, eng, ok)
		}
	}
	eng, ok := factory("unknown-provider", routerLarge)
	if !ok || eng == nil {
		t.Fatal("unknown provider must preserve the established fallback to the parent provider")
	}
	if eng.Model() != routerLarge {
		t.Fatalf("unknown-provider fallback model = %q, want routed parent-provider model %q", eng.Model(), routerLarge)
	}
}
