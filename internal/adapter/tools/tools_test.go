package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// tools_test.go covers the COMPOSED catalog this package assembles: the mixed
// filesystem (via engine/adapter/fstools aliases) + web/MCP bundle, and the
// no-FS surface. The filesystem tool BODIES are unit-tested in their own module
// (engine/adapter/fstools). The real-osfs e2e of the FS tools lives in
// abspath_tools_test.go / read_readroots_test.go (they need internal/adapter/osfs
// and so cannot live in the engine module).

// call builds a ToolCall with JSON args marshalled from m.
func call(t *testing.T, name string, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return session.NewToolCall(session.ToolCallID("id-"+name), name, raw)
}

// exec runs a tool and fails the test on a harness-level (Go) error.
func exec(t *testing.T, tl tool.Tool, in session.ToolCall, ws tool.Workspace) session.ToolResult {
	t.Helper()
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: ws.Root()}, ws, nil)
	res, err := tl.Execute(context.Background(), in, env)
	if err != nil {
		t.Fatalf("%s: unexpected harness error: %v", tl.Spec().Name, err)
	}
	return res
}

func TestReadOnlyFlags(t *testing.T) {
	// Bash is excluded from All() (it requires a CommandRunner); these are the
	// always-available tools. Bash's ReadOnly is asserted separately.
	want := map[string]bool{
		"Read":             true,
		"Edit":             false,
		"Write":            false,
		"Grep":             true,
		"Glob":             true,
		"WebFetch":         true,
		"FetchMcpResource": true,
	}
	got := map[string]bool{}
	for _, tl := range All() {
		got[tl.Spec().Name] = tl.ReadOnly()
	}
	if len(got) != len(want) {
		t.Fatalf("All() returned %d tools, want %d", len(got), len(want))
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s.ReadOnly() = %v, want %v", name, got[name], w)
		}
	}
	// Bash is mutating.
	if NewBashTool().ReadOnly() {
		t.Error("Bash.ReadOnly() = true, want false")
	}
}

// TestAllExcludesBash documents that the always-available catalog has NO Bash:
// command execution is optional and added only when a runner is configured.
func TestAllExcludesBash(t *testing.T) {
	for _, tl := range All() {
		if tl.Spec().Name == "Bash" {
			t.Fatal("All() must not include Bash (it requires a CommandRunner)")
		}
	}
}

// TestNoFSExcludesFileTools pins the no-FS core surface: NoFS() is exactly
// {WebFetch, FetchMcpResource} — no file-touching tool (Read/Edit/Write/Grep/
// Glob) and no Bash may ever appear in it. FetchMcpResource (issue #223 Phase
// 2) is an outbound read that needs no filesystem, so it rides alongside
// WebFetch. This is the anti-drift pin for the "no-fs" session profile's core
// tier: a tool added to All() does NOT automatically reach NoFS().
func TestNoFSExcludesFileTools(t *testing.T) {
	got := NoFS()
	if len(got) != 2 {
		t.Fatalf("NoFS() = %d tools, want exactly 2 (WebFetch, FetchMcpResource)", len(got))
	}
	names := map[string]bool{}
	for _, tl := range got {
		names[tl.Spec().Name] = true
	}
	for _, want := range []string{"WebFetch", "FetchMcpResource"} {
		if !names[want] {
			t.Errorf("NoFS() missing %q", want)
		}
	}
	banned := map[string]bool{"Read": true, "Edit": true, "Write": true, "Grep": true, "Glob": true, BashToolName: true}
	for _, tl := range got {
		if banned[tl.Spec().Name] {
			t.Errorf("NoFS() includes file/shell tool %q — the no-FS profile must never carry it", tl.Spec().Name)
		}
	}
}

func TestAllAndRegister(t *testing.T) {
	if len(All()) != 7 {
		t.Fatalf("All() = %d tools, want 7", len(All()))
	}
	cat := tool.NewCatalog()
	if err := Register(cat); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"Read", "Edit", "Write", "Grep", "Glob", "WebFetch", "FetchMcpResource"} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("catalog missing %q after Register", name)
		}
	}
	// Register does NOT add Bash; the catalog is shell-less until NewBashTool is
	// registered explicitly.
	if _, ok := cat.Lookup("Bash"); ok {
		t.Error("Register added Bash; it must be opt-in via NewBashTool")
	}
	// Adding the optional Bash tool succeeds (the runner is bound to the
	// Environment at Execute time, so NewBashTool takes no runner now).
	cat.MustRegister(NewBashTool())
	if _, ok := cat.Lookup("Bash"); !ok {
		t.Error("catalog missing Bash after explicit NewBashTool registration")
	}
	// Re-registering must collide.
	if err := Register(cat); err == nil {
		t.Error("re-Register did not return a duplicate error")
	}
}

func TestSpecsHaveDocs(t *testing.T) {
	for _, tl := range All() {
		s := tl.Spec()
		if s.Name == "" {
			t.Error("tool with empty name")
		}
		if len(s.Description) < 80 {
			t.Errorf("%s: description too short to be onboarding docs (%d chars)", s.Name, len(s.Description))
		}
		var js any
		if err := json.Unmarshal(s.Schema, &js); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", s.Name, err)
		}
	}
}

func TestWebFetchRejectsNonHTTPURLWithoutNetwork(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, NewWebFetchTool(), call(t, "WebFetch", map[string]any{"url": "file:///etc/passwd"}), ws)
	if !res.IsError {
		t.Error("WebFetch must reject a non-HTTP URL")
	}
	if !strings.Contains(res.Content, "http or https") {
		t.Errorf("WebFetch content = %q", res.Content)
	}
}
