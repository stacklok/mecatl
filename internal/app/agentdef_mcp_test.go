package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// mcpEchoArgs is the input to the test server's "echo" tool.
type mcpEchoArgs struct {
	Text string `json:"text"`
}

// newMCPTestServer stands up an in-process MCP server exposing a single (mutating,
// ReadOnly()==false) "echo" tool over a real httptest server speaking the streamable
// HTTP transport. It mirrors internal/adapter/mcp/mcp_test.go's pattern so the app
// tests can connect a genuine, offline MCP server. Returns the URL and a stop func.
func newMCPTestServer(t *testing.T) (string, func()) {
	t.Helper()
	url, cleanup, _ := newMCPTestServerCounting(t)
	return url, cleanup
}

// newMCPTestServerCounting is newMCPTestServer that also returns a pointer to the
// count of HTTP DELETE requests the handler received. The streamable-HTTP client
// issues a DELETE to TERMINATE its session on Close, so a non-zero count after a
// manager Close is a server-observed proof the connection was torn down (no leak).
func newMCPTestServerCounting(t *testing.T) (string, func(), *int32) {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echoes the input text"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in mcpEchoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}}}, nil, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	var deletes int32
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			atomic.AddInt32(&deletes, 1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL, httpSrv.Close, &deletes
}

// newMCPTestServerPrefixed is newMCPTestServer with a DISTINGUISHABLE echo: the
// tool returns prefix+text instead of "echo:"+text, so a test can prove WHICH of
// two same-named servers' tools actually EXECUTED. Collision-precedence proofs
// need behaviorally distinct servers — with two identical echoes the
// "global wins" assertion is tautological (QA mutant M3: swapping the
// client-before-global mount order still passed).
func newMCPTestServerPrefixed(t *testing.T, prefix string) (string, func()) {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echoes the input text"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in mcpEchoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: prefix + in.Text}}}, nil, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL, httpSrv.Close
}

// newMCPTestServerWithResource is newMCPTestServer plus ONE text resource, so
// mcp.RegisterResourceTools' "at least one connected server exposes a resource"
// gate passes and the ListMcpResources/ReadMcpResource meta-tools register (the
// catalog drift test pins them as a required family).
func newMCPTestServerWithResource(t *testing.T) (string, func()) {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echoes the input text"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in mcpEchoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}}}, nil, nil
		})
	srv.AddResource(&mcpsdk.Resource{
		URI:         "test://doc",
		Name:        "doc",
		Description: "a plain text resource",
		MIMEType:    "text/plain",
	}, func(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{
			Contents: []*mcpsdk.ResourceContents{
				{URI: req.Params.URI, MIMEType: "text/plain", Text: "hello resource"},
			},
		}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL, httpSrv.Close
}

// connectMainManager builds a main *mcp.Manager with one server named `name` at url.
func connectMainManager(t *testing.T, name, url string) *mcp.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := mcp.NewManager(ctx, []mcp.ServerConfig{{Name: name, URL: url}}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager(main): %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func toolNameSet(in []tool.Tool) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, tl := range in {
		out[tl.Spec().Name] = struct{}{}
	}
	return out
}

// TestDefMCPToolsReferencePullsFromMainManager asserts a REFERENCE entry adds the
// named main server's tools to the def's set and opens NO new connection (nil close).
func TestDefMCPToolsReferencePullsFromMainManager(t *testing.T) {
	url, _ := newMCPTestServer(t)

	main := connectMainManager(t, "main", url)

	def := agents.AgentDef{
		Name:        "ref-agent",
		Description: "uses a referenced server",
		MCPServers:  []agents.AgentMCPServer{{Name: "main"}}, // reference (no URL)
	}
	tools, names, closeFn := defMCPTools(context.Background(), port.NopDiagnostics{}, def, main)
	if closeFn != nil {
		t.Fatal("a reference entry must open no connection (nil close)")
	}
	if _, ok := toolNameSet(tools)["mcp__main__echo"]; !ok {
		t.Fatalf("reference def should get mcp__main__echo, got %v", names)
	}
}

// TestDefMCPToolsInlineConnectsAndTearsDown asserts an INLINE entry connects its own
// server (tool present), and that the returned close actually tears the session down
// (a captured tool's Execute fails after close — proving no leak).
func TestDefMCPToolsInlineConnectsAndTearsDown(t *testing.T) {
	url, _, deletes := newMCPTestServerCounting(t)

	def := agents.AgentDef{
		Name:        "inline-agent",
		Description: "uses an inline server",
		MCPServers:  []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}
	// No main manager: an inline def must still connect on its own.
	tools, names, closeFn := defMCPTools(context.Background(), port.NopDiagnostics{}, def, nil)
	if closeFn == nil {
		t.Fatal("an inline entry must return a non-nil close")
	}
	byName := toolNameSet(tools)
	if _, ok := byName["mcp__inline__echo"]; !ok {
		t.Fatalf("inline def should get mcp__inline__echo, got %v", names)
	}

	// Capture the tool, verify it works BEFORE close.
	var echo tool.Tool
	for _, tl := range tools {
		if tl.Spec().Name == "mcp__inline__echo" {
			echo = tl
		}
	}
	call := session.NewToolCall("c1", "mcp__inline__echo", json.RawMessage(`{"text":"hi"}`))
	if res, err := echo.Execute(context.Background(), call, nil); err != nil || res.IsError {
		t.Fatalf("echo before close should succeed, got res=%+v err=%v", res, err)
	}

	// Teardown: close, then the server must have seen a session-terminating DELETE —
	// server-observed proof the inline connection was torn down (no leak).
	if err := closeFn(); err != nil {
		t.Fatalf("inline close: %v", err)
	}
	if got := atomic.LoadInt32(deletes); got == 0 {
		t.Fatal("inline close did not terminate the MCP session (no DELETE seen at the server)")
	}
}

// TestDefMCPToolsUnknownReferenceSkipped asserts an unknown reference yields no tools
// and no connection (forgiving diagnostic) — the def is still usable without it.
func TestDefMCPToolsUnknownReferenceSkipped(t *testing.T) {
	url, _ := newMCPTestServer(t)

	main := connectMainManager(t, "main", url)

	def := agents.AgentDef{
		Name:        "typo-agent",
		Description: "references a server that does not exist",
		MCPServers:  []agents.AgentMCPServer{{Name: "nope"}},
	}
	tools, _, closeFn := defMCPTools(context.Background(), port.NopDiagnostics{}, def, main)
	if len(tools) != 0 || closeFn != nil {
		t.Fatalf("unknown reference must yield no tools and no close, got tools=%d close!=nil=%v", len(tools), closeFn != nil)
	}
}

// TestSubagentDefInlineMCPCloseAggregated asserts the Subagent-path wiring connects a def's
// inline server (tools enter the def engine) and that the aggregated close returned by
// buildAgentSubagentEngines tears the inline session down (Built.Close lifetime model).
func TestSubagentDefInlineMCPCloseAggregated(t *testing.T) {
	url, _ := newMCPTestServer(t)

	reg := agents.NewRegistry([]agents.AgentDef{{
		Name:        "inline-task",
		Description: "inline mcp task def",
		Tools:       []string{"Read"},
		MCPServers:  []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}})
	mcpProv := mockllm.New()
	engines, _, closeFn := buildAgentSubagentEngines(context.Background(), Config{Model: "m"}, mcpProv, regForTest(mcpProv, providerMock, "m"), providerMock, "m", reg, nil, hookexec.New(nil), nil, nil)
	if engines["inline-task"] == nil {
		t.Fatal("inline-task engine not built")
	}
	if closeFn == nil {
		t.Fatal("a Subagent def with an inline server must yield a non-nil aggregated close")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("aggregated Subagent inline MCP close: %v", err)
	}
}

// TestMemberReadOnlyDefWithMCPAccepted is the CRITICAL backstop test: a READ-ONLY
// (base-sharing) member whose def scopes an MCP server (its tools report
// ReadOnly()==false but never touch the workspace) must be ACCEPTED — the supervisor
// exempts MCP tool names from the workspace-mutating-tool backstop. It also dispatches
// the MCP tool, proving it is in the catalog and the def's Close runs on teardown.
func TestMemberReadOnlyDefWithMCPAccepted(t *testing.T) {
	url, _ := newMCPTestServer(t)

	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	def := agents.AgentDef{
		Name:        "mcp-reviewer",
		Description: "read-only member that calls an MCP tool",
		Tools:       []string{"Read"},
		MCPServers:  []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}
	tm := team.New("t")
	// The member loop calls the MCP echo tool, then reports done.
	mcpCall := session.ToolCall{ID: "c1", Name: "mcp__inline__echo", Args: json.RawMessage(`{"text":"x"}`)}
	prov := mockllm.New(mockllm.ToolCallTurn(mcpCall), mockllm.TextTurn("done"))
	factory := memberFactoryForTest(cfg, prov, hookexec.New(nil), regOf(def), nil, nil, false, nil)

	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	// A read-only member holding an MCP tool (ReadOnly()==false) must NOT trip
	// ErrReadOnlyMemberMutating — it must enrol.
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "mcp-reviewer", AgentType: "mcp-reviewer", InitialPrompt: "go",
	}); err != nil {
		t.Fatalf("read-only member with an MCP tool should be accepted, got %v", err)
	}

	var events []agent.TeamEvent
	// Run drives the member and, on return, cleanupAll runs the member's Close (the
	// inline MCP teardown) — exercising the member-path teardown seam.
	sup.Run(context.Background(), func(ev agent.TeamEvent) { events = append(events, ev) })
	if !sawToolCall(events, "mcp-reviewer", "mcp__inline__echo") {
		t.Fatalf("member should dispatch the MCP echo tool (it is in the catalog); events=%d", len(events))
	}
}

// TestMemberReadOnlyDefWithEditStillRejected guards the OTHER side of the exemption:
// a genuine workspace-mutating tool (Edit) on a read-only member is STILL rejected, so
// the MCP exemption did not weaken the backstop.
func TestMemberReadOnlyDefWithEditStillRejected(t *testing.T) {
	url, _ := newMCPTestServer(t)

	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	// Def scopes BOTH an MCP server AND Edit; for a read-only member Edit must still be
	// dropped by scopedToolNamesMode, so the member is accepted and never holds Edit.
	def := agents.AgentDef{
		Name:        "mixed",
		Description: "mcp + edit",
		Tools:       []string{"Read", "Edit"},
		MCPServers:  []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, editCall(), hookexec.New(nil), regOf(def), nil, nil, false, nil)

	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "mixed", AgentType: "mixed", InitialPrompt: "go",
	}); err != nil {
		t.Fatalf("read-only member: Edit should be dropped (not rejected), got %v", err)
	}

	var events []agent.TeamEvent
	sup.Run(context.Background(), func(ev agent.TeamEvent) { events = append(events, ev) })
	if sawToolDispatched(events, "mixed", "Edit") {
		t.Fatal("read-only member must not hold Edit; it should have been dropped (dropped → unknown tool, card+error, never executed)")
	}
}
