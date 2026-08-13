package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestBuildAgentWritableEngineFactoryRebuildsDefScopeWritable proves the writable-
// specialist factory rebuilds the def's SCOPED engine with allowMutating=true on the
// def's resolved model: Engine.Model() is the def's model (NOT an override — there is no
// per-call model on the writable path), and the engine's catalog carries Edit/Write
// (allowMutating=true kept them). The catalog is private, so the Edit/Write presence is
// proven end-to-end by TestBuildSubagentToolWritableAgentEditsParentTree below (a child
// that calls Write succeeds against the real workspace). The def's prompt body is
// composed into the Role (verified by the shared buildAgentDefEngine path's scoped-names
// tests); here we assert the model + that the factory returns a non-nil engine for a
// known def and (nil,false) for an unknown one.
func TestBuildAgentWritableEngineFactoryRebuildsDefScopeWritable(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	// A def with a SCOPED tool allowlist (Read + Write — Write survives because
	// allowMutating=true) and a body.
	def := agents.AgentDef{Name: "reviewer", Description: "reviews", Tools: []string{"Read", "Write"}, Body: "You are a reviewer.", Model: "claude-default"}
	agentReg := agents.NewRegistry([]agents.AgentDef{def})
	cfg := Config{Model: "claude-default", Diagnostics: port.NopDiagnostics{}}

	factory := buildAgentWritableEngineFactory(context.Background(), cfg, reg, prov, providerAnthropic, "claude-default",
		agentReg, nil, hookexec.New(nil), nil)

	// An empty agent name is unroutable.
	if _, ok := factory(""); ok {
		t.Fatal("empty agent must be unroutable (ok=false)")
	}
	// An unknown agent is unroutable.
	if _, ok := factory("ghost"); ok {
		t.Fatal("unknown agent must be unroutable (ok=false)")
	}

	eng, ok := factory("reviewer")
	if !ok || eng == nil {
		t.Fatalf("factory(reviewer) = (%v, %v), want a non-nil engine", eng, ok)
	}
	// Engine.Model() is the DEF's resolved model (no per-call override on the writable path).
	if got := eng.Model(); got != "claude-default" {
		t.Fatalf("writable-specialist engine Model() = %q, want the def's resolved model %q", got, "claude-default")
	}
}

// TestBuildSubagentToolWritableAgentEditsParentTree is the COMPOSITION-LEVEL E2E: it
// drives the REAL buildSubagentTool (which wires WithAgentWritableEngineFactory
// internally) and executes a Subagent call setting BOTH `agent` and `mode:"read-write"`,
// proving the wiring is correct end-to-end — the writable specialist runs its REAL Write
// tool against the REAL parent repo (direct-write, no fork, no merge), the file lands in
// the repo, and gauntlet #7 holds (the parent never sees the child's intermediate tool
// content). A wiring regression (wrong option, missing append, factory not threaded,
// allowMutating not plumbed) would fail here.
func TestBuildSubagentToolWritableAgentEditsParentTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	// A def "reviewer" with Write in its allowlist. The writable-specialist factory
	// rebuilds its scoped engine with allowMutating=true, so Write SURVIVES scoping and
	// the child can call it against the real workspace.
	def := agents.AgentDef{Name: "reviewer", Description: "reviews", Tools: []string{"Read", "Write"}, Body: "You are a reviewer."}
	agentReg := agents.NewRegistry([]agents.AgentDef{def})

	// The child provider calls the real Write tool, then emits a summary.
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", []byte(`{"path":"beta.txt","content":"written by the writable specialist\n"}`))),
		mockllm.TextTurn("did the work"),
	)
	task, closeFn := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agentReg, nil, nil, nil, nil, catalogAssets{}, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentWS := osfsWSForTest(t, repo)
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"implement the fix","mode":"read-write","agent":"reviewer"}`)),
		testEnvironment(parentWS, buildCommandRunner(cfg)))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("read-write+agent through buildSubagentTool must succeed, got error: %q", res.Content)
	}
	// The edit landed DIRECTLY in the real repo (proving direct-write + allowMutating=true
	// kept Write in the scoped catalog — a read-only scoping would have dropped Write and
	// the child's Write call would have been an unknown-tool error).
	if got, rerr := os.ReadFile(filepath.Join(repo, "beta.txt")); rerr != nil {
		t.Fatalf("the writable specialist's edit did not land in the real repo: %v", rerr)
	} else if !strings.Contains(string(got), "written by the writable specialist") {
		t.Fatalf("beta.txt content unexpected: %q", got)
	}
	// No sibling fork directory was created (direct-write, no fork).
	assertNoSiblingForkDir(t, repo)
	// The result honestly notes the child had direct write access (capability, not a
	// claim of mutation — "had direct write access to your workspace").
	if !strings.Contains(res.Content, "had direct write access to your workspace") {
		t.Fatalf("result must note the child had direct write access, got:\n%s", res.Content)
	}
	// It must NOT claim an edit occurred (the old false phrasing).
	if strings.Contains(res.Content, "edited your workspace directly") {
		t.Fatalf("result must NOT claim the child 'edited your workspace directly' (capability, not mutation), got:\n%s", res.Content)
	}
	// gauntlet #7: the child's intermediate tool result text never appears in the
	// parent-visible result (only the final summary folds back).
	if strings.Contains(res.Content, "wrote beta.txt") {
		t.Fatalf("parent result leaked the child's intermediate tool result text (gauntlet #7): %q", res.Content)
	}
}

// TestBuildAgentWritableEngineFactoryDeclinesInlineMCP proves the v1 scope limit (same
// as the agent+model path): a def with an INLINE MCP server (URL set, !IsReference) is
// declined by the writable-specialist factory (nil, false) — the inline manager's live
// session must outlive a per-call engine, and the per-call path has no process-lifetime
// owner for one. A reference-only def is NOT declined (it borrows the process-lifetime
// mainMgr).
func TestBuildAgentWritableEngineFactoryDeclinesInlineMCP(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	inlineDef := agents.AgentDef{
		Name:       "inline-reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: "http://127.0.0.1:1/sse"}},
	}
	refDef := agents.AgentDef{
		Name:       "ref-reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "main"}}, // reference (no URL)
	}
	agentReg := agents.NewRegistry([]agents.AgentDef{inlineDef, refDef})
	cfg := Config{Model: "claude-default", Diagnostics: port.NopDiagnostics{}}

	factory := buildAgentWritableEngineFactory(context.Background(), cfg, reg, prov, providerAnthropic, "claude-default",
		agentReg, nil, hookexec.New(nil), nil)

	// Inline MCP server ⇒ declined (the v1 scope limit).
	if eng, ok := factory("inline-reviewer"); ok || eng != nil {
		t.Fatalf("a def with an inline MCP server must be declined (nil, false), got (%v, %v)", eng, ok)
	}
	// Reference-only MCP server ⇒ NOT declined (borrows mainMgr, no new connection).
	eng, ok := factory("ref-reviewer")
	if !ok || eng == nil {
		t.Fatalf("a reference-only MCP def must NOT be declined (it borrows mainMgr), got (%v, %v)", eng, ok)
	}
}

// TestBuildAgentWritableEngineFactoryReferenceMCPSupported is the positive companion: a
// def with a reference-only MCP server produces a writable-specialist engine whose
// catalog includes the referenced server's tools (borrows mainMgr). Proven by the factory
// returning a non-nil engine for the reference-only def (the catalog is private; the
// reference-MCP tools reaching the child is the shared defMCPTools path, verified
// elsewhere — here we assert the factory succeeds and the engine is non-nil).
func TestBuildAgentWritableEngineFactoryReferenceMCPSupported(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	refDef := agents.AgentDef{
		Name:       "ref-reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "main"}}, // reference (no URL)
	}
	agentReg := agents.NewRegistry([]agents.AgentDef{refDef})
	cfg := Config{Model: "claude-default", Diagnostics: port.NopDiagnostics{}}

	factory := buildAgentWritableEngineFactory(context.Background(), cfg, reg, prov, providerAnthropic, "claude-default",
		agentReg, nil, hookexec.New(nil), nil)

	eng, ok := factory("ref-reviewer")
	if !ok || eng == nil {
		t.Fatalf("a reference-only MCP def must produce a writable-specialist engine, got (%v, %v)", eng, ok)
	}
	if eng.Model() != "claude-default" {
		t.Fatalf("writable-specialist engine Model() = %q, want the def's resolved model", eng.Model())
	}
}
