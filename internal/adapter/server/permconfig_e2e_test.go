package server_test

import (
	"context"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// seededWorkspaces returns a server.WorkspaceFactory that builds a memfs
// Workspace per root, seeding the file content registered for that root in
// files[root] (path -> content). This lets a test give two sessions DIFFERENT
// per-project `.mecatl/settings.yaml` content under the same Service.
func seededWorkspaces(files map[string]map[string]string) server.WorkspaceFactory {
	return func(root string) tool.Workspace {
		ws := memfs.NewWorkspace(root)
		for p, content := range files[root] {
			_ = ws.Write(context.Background(), p, []byte(content))
		}
		return ws
	}
}

// newConfigService builds a Service whose engine policy carries a permconfig
// Resolver (file-based config, re-resolved per session workspace root) plus the
// built-in defaultRules-style floor (Write asks). The workspace factory seeds
// per-root settings from files.
func newConfigService(t *testing.T, llm *mockllm.Provider, opts permconfig.Options, files map[string]map[string]string, tools ...tool.Tool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range tools {
		cat.MustRegister(tl)
	}
	resolver := permconfig.New(opts)
	if resolver == nil {
		t.Fatal("expected a non-nil resolver for these opts")
	}
	// Built-in floor at the lowest scope: Write asks, Read allows — a config allow
	// can loosen the Write ask per workspace.
	floor := []governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "Write", Effect: governance.Ask},
		{Scope: governance.ScopeBuiltinDefault, Tool: "Read", Effect: governance.Allow},
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicyWithResolver(floor, nil, resolver),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      memstore.New(),
		Workspaces: seededWorkspaces(files),
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

const writeAllowYAML = "permissions:\n  allow:\n    - \"Write\"\n"
const writeDenyYAML = "permissions:\n  deny:\n    - \"Write\"\n"

// runWriteSession drives one session at the given workspace through a single
// Write tool call and returns the events. It auto-approves any permission.ask so
// the run always terminates (so an ASK is observable but never deadlocks).
func runWriteSession(t *testing.T, client mecatlv1.HarnessServiceClient, workspace string) (sawAsk bool, sawDeny bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", workspace, err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			break // EOF or stream end
		}
		ev := resp.GetEvent()
		switch ev.GetType() {
		case "permission.ask":
			sawAsk = true
			_ = stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{AskId: ev.GetAsk().GetAskId(), Allow: true},
				},
			})
		case "tool.result":
			// A deny is surfaced as a tool.result with an error/deny body.
			if r := ev.GetToolResult(); r != nil && r.GetIsError() {
				sawDeny = true
			}
		case "result":
			_ = stream.CloseSend()
			return sawAsk, sawDeny
		}
	}
	return sawAsk, sawDeny
}

// THE ACCEPTANCE PROOF (issue #13): two sessions, the SAME Service + policy, but
// different per-project `.mecatl/settings.yaml` under their workspaces, resolve
// the SAME Write tool call differently — session A (project allow, trusted)
// auto-allows with NO ask; session B (no config) falls through to the built-in
// ASK.
func TestE2EPerSessionConfigDiverges(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	// One Write turn + one text turn PER session (two sessions).
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("a1", "Write", `{"path":"x"}`)),
		mockllm.TextTurn("done A"),
		mockllm.ToolCallTurn(call("b1", "Write", `{"path":"x"}`)),
		mockllm.TextTurn("done B"),
	)
	files := map[string]map[string]string{
		"/repo-allow": {".mecatl/settings.yaml": writeAllowYAML},
		// /repo-plain has no settings file: Write should fall to the built-in ask.
	}
	svc := newConfigService(t, llm, permconfig.Options{Conventional: true, TrustProject: true}, files, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	// Session A: trusted project ALLOW → no ask, tool runs.
	askA, _ := runWriteSession(t, client, "/repo-allow")
	if askA {
		t.Fatalf("session A (project allow) should NOT ask")
	}
	if !write.ran() {
		t.Fatalf("session A Write should have run")
	}

	// Session B: no config → built-in ask fires. Drive it manually so we can prove
	// the ask actually GATES (not cosmetic): the Write tool must NOT have run at the
	// moment the ask is surfaced, and the permission.ask must precede the tool.result.
	cs, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession(B): %v", err)
	}
	stream, err := client.Converse(context.Background())
	if err != nil {
		t.Fatalf("Converse(B): %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt(B): %v", err)
	}
	var order []string
	var askB bool
	for {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		ev := resp.GetEvent()
		order = append(order, ev.GetType())
		switch ev.GetType() {
		case "permission.ask":
			askB = true
			// THE GATE: the second Write must not have executed before approval. The
			// first session's Write already ran, so check the call count delta instead
			// of a bare ran() bool: writeRunsBeforeBApproval is the run count now.
			if writeRunsAtBAsk := write.runs(); writeRunsAtBAsk != 1 {
				t.Fatalf("session B Write must NOT run before approval; runs=%d (expected the 1 from session A only)", writeRunsAtBAsk)
			}
			_ = stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{AskId: ev.GetAsk().GetAskId(), Allow: true},
				},
			})
		case "result":
			_ = stream.CloseSend()
		}
	}
	if !askB {
		t.Fatalf("session B (no config) should ASK for Write; events: %v", order)
	}
	// Ordering: the ask must precede the tool.result for session B's call.
	if !indexBefore(order, "permission.ask", "tool.result") {
		t.Fatalf("permission.ask must precede tool.result (the ask gates the call); events: %v", order)
	}
	// And after approval the Write does run (the second run).
	if write.runs() != 2 {
		t.Fatalf("session B Write should run after approval; total runs=%d, want 2", write.runs())
	}
}

// indexBefore reports whether the first occurrence of a precedes the first
// occurrence of b in the event-type sequence (both must be present).
func indexBefore(seq []string, a, b string) bool {
	ai, bi := -1, -1
	for i, s := range seq {
		if s == a && ai == -1 {
			ai = i
		}
		if s == b && bi == -1 {
			bi = i
		}
	}
	return ai != -1 && bi != -1 && ai < bi
}

// A PROJECT DENY is honored e2e: even a trusted project's deny blocks the call
// (deny is absolute), so the tool never runs and no ask is raised.
func TestE2EProjectDenyHonored(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("d1", "Write", `{"path":"x"}`)),
		mockllm.TextTurn("done"),
	)
	files := map[string]map[string]string{
		"/repo-deny": {".mecatl/settings.yaml": writeDenyYAML},
	}
	svc := newConfigService(t, llm, permconfig.Options{Conventional: true, TrustProject: true}, files, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	sawAsk, sawDeny := runWriteSession(t, client, "/repo-deny")
	if sawAsk {
		t.Fatalf("a project deny must NOT raise an ask")
	}
	if !sawDeny {
		t.Fatalf("expected a deny tool.result")
	}
	if write.ran() {
		t.Fatalf("a denied Write must not run")
	}
}

// An UNTRUSTED project's ALLOW is IGNORED e2e: the project allows Write, but the
// Service is built TrustProject=false, so the allow is dropped and the built-in
// ASK fires instead.
func TestE2EUntrustedProjectAllowIgnored(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("u1", "Write", `{"path":"x"}`)),
		mockllm.TextTurn("done"),
	)
	files := map[string]map[string]string{
		"/repo-allow": {".mecatl/settings.yaml": writeAllowYAML},
	}
	svc := newConfigService(t, llm, permconfig.Options{Conventional: true, TrustProject: false}, files, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	sawAsk, _ := runWriteSession(t, client, "/repo-allow")
	if !sawAsk {
		t.Fatalf("an untrusted project's allow must be ignored; the built-in ASK should fire")
	}
}

// A CLAUDE-IMPORTED allow is honored e2e: a trusted project's
// .claude/settings.json allow loosens the built-in Write ask (no ask raised).
func TestE2EClaudeImportHonored(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"x"}`)),
		mockllm.TextTurn("done"),
	)
	files := map[string]map[string]string{
		"/repo-claude": {".claude/settings.json": `{"permissions":{"allow":["Write"]}}`},
	}
	svc := newConfigService(t, llm,
		permconfig.Options{Conventional: true, ImportClaude: true, TrustProject: true}, files, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	sawAsk, _ := runWriteSession(t, client, "/repo-claude")
	if sawAsk {
		t.Fatalf("a Claude-imported project allow should loosen the built-in ask (no ask expected)")
	}
	if !write.ran() {
		t.Fatalf("the Claude-imported allow should let Write run")
	}
}
