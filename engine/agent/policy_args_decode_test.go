package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/search"
	"github.com/stacklok/mecatl/engine/adapter/webfetch"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// policyArgsRecorder is an allow-all PermissionPolicy that records, for every
// evaluated call, the Shell command and the resource pattern the governance
// package derives from the call's arguments: the values a configured rule would
// be matched against.
type policyArgsRecorder struct {
	inner port.PermissionPolicy

	mu        sync.Mutex
	evaluated []policyArgsObservation
}

type policyArgsObservation struct {
	tool    string
	command string
	pattern string
}

func newPolicyArgsRecorder() *policyArgsRecorder {
	return &policyArgsRecorder{inner: permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeCLI, Effect: governance.Allow}}, nil)}
}

func (p *policyArgsRecorder) Evaluate(ctx context.Context, id session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) port.PermissionResult {
	command, _ := governance.ShellCommandFromArgs(c.Args)
	rule, _ := governance.NewEvaluator(nil).LearnableRule(c.Name, c.Args)
	p.mu.Lock()
	p.evaluated = append(p.evaluated, policyArgsObservation{tool: c.Name, command: command, pattern: rule.Pattern})
	p.mu.Unlock()
	return p.inner.Evaluate(ctx, id, mode, c, ws)
}

func (*policyArgsRecorder) Learn(session.SessionID, session.ToolCall) {}

func (p *policyArgsRecorder) observations() []policyArgsObservation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]policyArgsObservation(nil), p.evaluated...)
}

func runPolicyArgsCall(t *testing.T, catalog *tool.Catalog, policy port.PermissionPolicy, env tool.Environment, call session.ToolCall) session.ToolResult {
	t.Helper()
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy})
	sess := session.New("policy-args", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Now())
	run := eng.Run(context.Background(), sess, env, RunRequest{Text: "run"})
	var result session.ToolResult
	seen := false
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk {
			// The recording policy allows every call; an ask means the test setup is
			// wrong, so stop rather than wait for an approval that never comes.
			run.Cancel()
			t.Errorf("unexpected permission ask: %+v", event.Ask)
		}
		if event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.CallID == call.ID {
			result = *event.ToolResult
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no tool result for %s", call.ID)
	}
	return result
}

// TestShellPolicyEvaluatesTheExecutedCommand asserts, across argument
// encodings, that the command the permission policy evaluates is the command the
// Shell runner receives, and that an ambiguous argument object is rejected before
// policy evaluation without reaching the runner.
func TestShellPolicyEvaluatesTheExecutedCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		args      string
		ambiguous bool
	}{
		{name: "canonical", args: `{"command":"echo canonical"}`},
		{name: "canonical with options", args: `{"command":"echo options","timeout_ms":1000,"temp_scope":"managed"}`},
		{name: "escaped canonical key", args: `{"\u0063ommand":"echo escaped"}`},
		{name: "cmd alias alongside command", args: `{"command":"echo primary","cmd":"echo alias"}`},
		{name: "cmd alias alone", args: `{"cmd":"echo alias"}`},
		{name: "repeated key", args: `{"command":"echo one","command":"echo two"}`, ambiguous: true},
		{name: "repeated key via escape", args: `{"command":"echo one","\u0063ommand":"echo two"}`, ambiguous: true},
		{name: "case variant of field", args: `{"Command":"echo variant"}`, ambiguous: true},
		{name: "case variant alongside field", args: `{"command":"echo one","COMMAND":"echo two"}`, ambiguous: true},
		{name: "field alongside case variant", args: `{"COMMAND":"echo one","command":"echo two"}`, ambiguous: true},
		{name: "case variant of option", args: `{"command":"echo one","Temp_Scope":"system"}`, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			catalog := tool.NewCatalog()
			catalog.MustRegister(NewShellTool())
			policy := newPolicyArgsRecorder()
			result := runPolicyArgsCall(t, catalog, policy, shellEnvRunner(runner), session.NewToolCall("c", tool.ShellToolName, []byte(tc.args)))

			runner.mu.Lock()
			executed := runner.command
			runner.mu.Unlock()
			observed := policy.observations()

			if tc.ambiguous {
				if len(observed) != 0 {
					t.Errorf("ambiguous arguments reached policy evaluation: %+v", observed)
				}
				if executed != "" {
					t.Errorf("ambiguous arguments executed %q", executed)
				}
				if !result.IsError || !strings.Contains(result.Content, "ambiguous") {
					t.Errorf("result = %+v, want an ambiguous-arguments tool error", result)
				}
				return
			}
			if executed == "" {
				// A call the tool refuses runs nothing, whatever was evaluated.
				if !result.IsError {
					t.Fatalf("call neither executed nor failed: %+v", result)
				}
				return
			}
			if len(observed) == 0 {
				t.Fatalf("command %q executed without a policy evaluation", executed)
			}
			for _, o := range observed {
				if o.command != executed {
					t.Errorf("policy evaluated command %q, runner executed %q", o.command, executed)
				}
			}
		})
	}
}

// TestFileToolPolicyEvaluatesTheExecutedPath asserts, across argument encodings,
// that the resource the permission policy evaluates for a file tool is the one
// the tool acts on, and that ambiguous argument objects are rejected before
// policy evaluation without touching the workspace.
func TestFileToolPolicyEvaluatesTheExecutedPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		tool      tool.Tool
		args      string
		ambiguous bool
		// wantRead is the file content a successful Read must return.
		wantRead string
		// wantFile, when set, is a path that must exist after a successful call.
		wantFile string
	}{
		{name: "read canonical", tool: fstools.ReadTool{}, args: `{"path":"a.txt"}`, wantRead: "alpha"},
		{name: "read repeated key", tool: fstools.ReadTool{}, args: `{"path":"a.txt","path":"b.txt"}`, ambiguous: true},
		{name: "read case variant alongside field", tool: fstools.ReadTool{}, args: `{"path":"a.txt","PATH":"b.txt"}`, ambiguous: true},
		{name: "read case variant of field", tool: fstools.ReadTool{}, args: `{"Path":"b.txt"}`, ambiguous: true},
		{name: "list case variant alongside field", tool: fstools.ListDirTool{}, args: `{"path":"dir","Path":"."}`, ambiguous: true},
		{name: "grep case variant alongside field", tool: fstools.GrepTool{}, args: `{"pattern":"alpha","path":"a.txt","Path":"b.txt"}`, ambiguous: true},
		{name: "glob case variant alongside field", tool: fstools.GlobTool{}, args: `{"pattern":"*.txt","PATTERN":"**"}`, ambiguous: true},
		{name: "write repeated key", tool: fstools.WriteTool{}, args: `{"path":"new-a.txt","path":"new-b.txt","content":"x"}`, ambiguous: true},
		{name: "remove case variant alongside field", tool: fstools.RemoveTool{}, args: `{"path":"a.txt","pAth":"b.txt"}`, ambiguous: true},
		{name: "copy canonical", tool: fstools.CopyTool{}, args: `{"source":"a.txt","destination":"copy.txt"}`, wantFile: "copy.txt"},
		{name: "copy unicode fold variant alongside field", tool: fstools.CopyTool{}, args: `{"source":"a.txt","destination":"c1.txt","de\u017ftination":"c2.txt"}`, ambiguous: true},
		{name: "move repeated key", tool: fstools.MoveTool{}, args: `{"source":"a.txt","destination":"m1.txt","destination":"m2.txt"}`, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := memfs.NewWorkspace("/ws")
			for path, body := range map[string]string{"a.txt": "alpha", "b.txt": "beta", "dir/c.txt": "gamma"} {
				if err := ws.Write(t.Context(), path, []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			catalog := tool.NewCatalog()
			catalog.MustRegister(tc.tool)
			policy := newPolicyArgsRecorder()
			name := tc.tool.Spec().Name
			result := runPolicyArgsCall(t, catalog, policy, testEnvironment(ws, nil), session.NewToolCall("c", name, []byte(tc.args)))
			observed := policy.observations()

			if tc.ambiguous {
				if len(observed) != 0 {
					t.Errorf("ambiguous arguments reached policy evaluation: %+v", observed)
				}
				if !result.IsError || !strings.Contains(result.Content, "ambiguous") {
					t.Errorf("result = %+v, want an ambiguous-arguments tool error", result)
				}
				for _, path := range []string{"a.txt", "b.txt"} {
					if _, err := ws.Read(t.Context(), path); err != nil {
						t.Errorf("workspace file %s changed: %v", path, err)
					}
				}
				for _, path := range []string{"new-a.txt", "new-b.txt", "c1.txt", "c2.txt", "m1.txt", "m2.txt"} {
					if _, err := ws.Read(t.Context(), path); err == nil {
						t.Errorf("ambiguous call created %s", path)
					}
				}
				return
			}
			if result.IsError {
				t.Fatalf("canonical call failed: %+v", result)
			}
			if len(observed) == 0 {
				t.Fatal("call executed without a policy evaluation")
			}
			if tc.wantRead != "" {
				if !strings.Contains(result.Content, tc.wantRead) {
					t.Fatalf("read content = %q, want %q", result.Content, tc.wantRead)
				}
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
					t.Fatal(err)
				}
				if observed[0].pattern != args.Path {
					t.Errorf("policy evaluated %q, tool read %q", observed[0].pattern, args.Path)
				}
			}
			if tc.wantFile != "" {
				if _, err := ws.Read(t.Context(), tc.wantFile); err != nil {
					t.Errorf("expected %s after call: %v", tc.wantFile, err)
				}
			}
		})
	}
}

// TestArgumentDecodeRejectsAmbiguityBeforeEveryTool asserts the ambiguity check
// is tool-agnostic: a call to a tool whose arguments are forwarded verbatim is
// rejected before policy evaluation and never reaches the tool.
func TestArgumentDecodeRejectsAmbiguityBeforeEveryTool(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"WebFetch", "WebSearch", "mcp__srv__fetch"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stub := &argsRecordingTool{name: name}
			catalog := tool.NewCatalog()
			catalog.MustRegister(stub)
			policy := newPolicyArgsRecorder()
			args := `{"url":"https://one.example","URL":"https://two.example","query":"q"}`
			result := runPolicyArgsCall(t, catalog, policy, memEnv("/ws"), session.NewToolCall("c", name, []byte(args)))
			if observed := policy.observations(); len(observed) != 0 {
				t.Errorf("ambiguous arguments reached policy evaluation: %+v", observed)
			}
			if stub.calls() != 0 {
				t.Errorf("ambiguous arguments reached the tool")
			}
			if !result.IsError || !strings.Contains(result.Content, "ambiguous") {
				t.Errorf("result = %+v, want an ambiguous-arguments tool error", result)
			}
		})
	}
}

type argsRecordingTool struct {
	name string
	mu   sync.Mutex
	n    int
}

func (s *argsRecordingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: s.name, Description: "records calls", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (*argsRecordingTool) ReadOnly() bool { return true }

func (s *argsRecordingTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return session.NewToolResult(in.ID, "ok"), nil
}

func (s *argsRecordingTool) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// runDenyingAsks runs one call, refusing every approval request, and reports the
// call's result and whether approval was requested.
func runDenyingAsks(t *testing.T, catalog *tool.Catalog, policy port.PermissionPolicy, env tool.Environment, call session.ToolCall) (result session.ToolResult, asked bool) {
	t.Helper()
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy})
	sess := session.New("policy-args", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Now())
	run := eng.Run(context.Background(), sess, env, RunRequest{Text: "run"})
	seen := false
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			asked = true
			run.Approve(event.Ask.AskID, session.VerdictDeny)
		}
		if event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.CallID == call.ID {
			result, seen = *event.ToolResult, true
		}
	}
	if !seen {
		t.Fatalf("no tool result for %s", call.ID)
	}
	return result, asked
}

// recordingSearchProvider records the queries WebSearch sends it.
type recordingSearchProvider struct {
	mu      sync.Mutex
	queries []string
}

func (p *recordingSearchProvider) Search(_ context.Context, q tool.SearchQuery) ([]tool.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, q.Query)
	return []tool.SearchResult{{Title: "result", URL: "https://example.test/", Snippet: "snippet"}}, nil
}

func (p *recordingSearchProvider) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.queries...)
}

// TestBuiltinToolDenyMatchesTheExecutedOperand runs real built-in tools whose
// calls carry an allowed decoy in an argument the tool ignores and another value
// in the argument it acts on. A Deny written for the executed operand must stop
// the call before the tool runs, and with no rule for the executed operand the
// Allow written for the decoy must not admit it. The canonical call is the
// positive control that the rules admit the allowed operand.
func TestBuiltinToolDenyMatchesTheExecutedOperand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		tool      func(*recordingSearchProvider) tool.Tool
		allow     string
		deny      string
		decoyArgs string
		canonical string
		// executed reports what the tool acted on, from the workspace result or
		// the provider.
		executed func(session.ToolResult, *recordingSearchProvider) string
	}{
		{
			name:      "Glob",
			tool:      func(*recordingSearchProvider) tool.Tool { return fstools.GlobTool{} },
			allow:     "public/**",
			deny:      "secret/**",
			decoyArgs: `{"path":"public/**","pattern":"secret/**"}`,
			canonical: `{"pattern":"public/**"}`,
			executed: func(res session.ToolResult, _ *recordingSearchProvider) string {
				if strings.Contains(res.Content, "secret/key.txt") {
					return "secret/**"
				}
				if strings.Contains(res.Content, "public/readme.txt") {
					return "public/**"
				}
				return ""
			},
		},
		{
			name:      "WebSearch",
			tool:      func(p *recordingSearchProvider) tool.Tool { return search.NewWebSearchTool(p) },
			allow:     "public *",
			deny:      "secret *",
			decoyArgs: `{"pattern":"public docs","query":"secret docs"}`,
			canonical: `{"query":"public docs"}`,
			executed: func(_ session.ToolResult, p *recordingSearchProvider) string {
				return strings.Join(p.seen(), ",")
			},
		},
		{
			// The denied call never reaches the network; the canonical call is not
			// run because it would.
			name:      "WebFetch",
			tool:      func(*recordingSearchProvider) tool.Tool { return webfetch.New() },
			allow:     "https://public.example/*",
			deny:      "https://secret.example/*",
			decoyArgs: `{"path":"https://public.example/","url":"https://secret.example/"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := func(args string, rules ...governance.Rule) (session.ToolResult, bool, *recordingSearchProvider) {
				ws := memfs.NewWorkspace("/ws")
				for path, body := range map[string]string{"public/readme.txt": "hello", "secret/key.txt": "token"} {
					if err := ws.Write(t.Context(), path, []byte(body)); err != nil {
						t.Fatal(err)
					}
				}
				provider := &recordingSearchProvider{}
				catalog := tool.NewCatalog()
				catalog.MustRegister(tc.tool(provider))
				result, asked := runDenyingAsks(t, catalog, permpolicy.NewPolicy(rules, nil), testEnvironment(ws, nil), session.NewToolCall("c", tc.name, []byte(args)))
				return result, asked, provider
			}

			allow := governance.Rule{Scope: governance.ScopeCLI, Tool: tc.name, Pattern: tc.allow, Effect: governance.Allow}
			deny := governance.Rule{Scope: governance.ScopeCLI, Tool: tc.name, Pattern: tc.deny, Effect: governance.Deny}
			for _, rc := range []struct {
				rules   []governance.Rule
				wantAsk bool
			}{
				{rules: []governance.Rule{allow, deny}},
				// No rule covers the executed operand, so the call needs approval;
				// the decoy's Allow must not admit it. The approval is refused.
				{rules: []governance.Rule{allow}, wantAsk: true},
			} {
				result, asked, provider := run(tc.decoyArgs, rc.rules...)
				if asked != rc.wantAsk {
					t.Errorf("decoy call under %d rule(s): asked = %v, want %v", len(rc.rules), asked, rc.wantAsk)
				}
				if !result.IsError || !strings.Contains(result.Content, "permission denied") {
					t.Fatalf("decoy call under %d rule(s): result = %+v, want a permission denial", len(rc.rules), result)
				}
				if tc.executed != nil {
					if got := tc.executed(result, provider); got != "" {
						t.Fatalf("decoy call under %d rule(s) acted on %q", len(rc.rules), got)
					}
				}
			}
			if tc.canonical == "" {
				return
			}
			result, asked, provider := run(tc.canonical, allow, deny)
			if asked {
				t.Fatal("the canonical call asked for approval")
			}
			if result.IsError {
				t.Fatalf("canonical call failed: %+v", result)
			}
			if got := tc.executed(result, provider); got == "" {
				t.Fatalf("canonical call did not act on the allowed operand: %+v", result)
			}
		})
	}
}
