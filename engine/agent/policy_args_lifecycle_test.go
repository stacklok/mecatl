package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// The tests in this file pin the argument check on each path that authorizes a
// call after its first evaluation: a PreToolUse rewrite, an approval resumed
// from a paused session, and a read batch that mixes ambiguous and canonical
// calls. Each asserts that ambiguous arguments reach neither the permission
// policy nor the tool, with a canonical positive control.

// argsRewriteHook is a PreToolUse hook that replaces every call's arguments.
type argsRewriteHook struct{ mutated string }

func (h argsRewriteHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse {
		return governance.HookOutcome{Mutated: json.RawMessage(h.mutated)}, nil
	}
	return governance.HookOutcome{}, nil
}

// policyArgsWorkspace returns a workspace holding a.txt ("alpha") and b.txt ("beta").
func policyArgsWorkspace(t *testing.T) tool.Workspace {
	t.Helper()
	ws := memfs.NewWorkspace("/ws")
	for path, body := range map[string]string{"a.txt": "alpha", "b.txt": "beta"} {
		if err := ws.Write(t.Context(), path, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// assertNotEvaluated fails when the policy evaluated a call with args.
func assertNotEvaluated(t *testing.T, policy *policyArgsRecorder, args string) {
	t.Helper()
	for _, o := range policy.observations() {
		if o.args == args {
			t.Errorf("ambiguous arguments reached policy evaluation: %s", args)
		}
	}
}

func assertAmbiguousResult(t *testing.T, result session.ToolResult) {
	t.Helper()
	if !result.IsError || !strings.Contains(result.Content, "ambiguous tool arguments") {
		t.Errorf("result = %+v, want an ambiguous-arguments tool error", result)
	}
}

// TestHookMutatedAmbiguousArgsAreRejected pins that a PreToolUse rewrite into an
// ambiguous argument object is refused before the effective call is evaluated
// or executed, on the serial path (Shell) and in a read batch (Read).
func TestHookMutatedAmbiguousArgsAreRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		tool      tool.Tool
		original  string
		mutated   string
		ambiguous bool
		// want is what the canonical rewrite runs: the Shell command or the
		// content Read returns.
		want string
	}{
		{name: "shell repeated key", tool: NewShellTool(), original: `{"command":"echo original"}`, mutated: `{"command":"echo shown","command":"echo hidden"}`, ambiguous: true},
		{name: "shell case variant", tool: NewShellTool(), original: `{"command":"echo original"}`, mutated: `{"command":"echo shown","COMMAND":"echo hidden"}`, ambiguous: true},
		{name: "shell canonical rewrite", tool: NewShellTool(), original: `{"command":"echo original"}`, mutated: `{"command":"echo rewritten"}`, want: "echo rewritten"},
		{name: "read repeated key", tool: fstools.ReadTool{}, original: `{"path":"a.txt"}`, mutated: `{"path":"a.txt","path":"b.txt"}`, ambiguous: true},
		{name: "read case variant", tool: fstools.ReadTool{}, original: `{"path":"a.txt"}`, mutated: `{"path":"a.txt","PATH":"b.txt"}`, ambiguous: true},
		{name: "read canonical rewrite", tool: fstools.ReadTool{}, original: `{"path":"a.txt"}`, mutated: `{"path":"b.txt"}`, want: "beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			catalog := tool.NewCatalog()
			catalog.MustRegister(tc.tool)
			policy := newPolicyArgsRecorder()
			call := port.LLMToolCall{ID: "c", Name: tc.tool.Spec().Name, Args: []byte(tc.original)}
			eng := NewEngine(Deps{LLM: mockllm.New(mockllm.LLMToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy, Hooks: argsRewriteHook{mutated: tc.mutated}})
			env := testEnvironment(policyArgsWorkspace(t), runner)
			sess := session.New("hook-mutation", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Now())
			var result session.ToolResult
			seenResult := false
			for event := range eng.Run(context.Background(), sess, env, RunRequest{Text: "run"}).Events() {
				if event.Type == session.EvPermissionAsk {
					t.Fatalf("unexpected permission ask: %+v", event.Ask)
				}
				if event.Type == session.EvToolResult && event.ToolResult != nil {
					if seenResult {
						t.Fatalf("multiple tool results for provider call %q", call.ID)
					}
					result = *event.ToolResult
					seenResult = true
				}
			}
			if !seenResult {
				t.Fatalf("no tool result for provider call %q", call.ID)
			}
			var canonical session.ToolCallID
			for _, message := range sess.Conversation.Messages {
				if message.Replay == nil {
					continue
				}
				for _, binding := range message.Replay.Bindings {
					if binding.PairingID == call.ID {
						canonical = binding.CallID
					}
				}
			}
			if canonical == "" || result.CallID != canonical {
				t.Fatalf("provider call %q result ID = %q, want canonical ID %q", call.ID, result.CallID, canonical)
			}
			runner.mu.Lock()
			executed := runner.command
			runner.mu.Unlock()

			if tc.ambiguous {
				assertNotEvaluated(t, policy, tc.mutated)
				assertAmbiguousResult(t, result)
				if executed != "" || strings.Contains(result.Content, "alpha") || strings.Contains(result.Content, "beta") {
					t.Fatalf("ambiguous rewrite executed: command=%q result=%q", executed, result.Content)
				}
				return
			}
			var evaluated bool
			for _, o := range policy.observations() {
				evaluated = evaluated || o.args == tc.mutated
			}
			if !evaluated {
				t.Errorf("the canonical rewrite %s was not evaluated", tc.mutated)
			}
			if result.IsError || (executed != tc.want && !strings.Contains(result.Content, tc.want)) {
				t.Fatalf("canonical rewrite: command=%q result=%+v, want %q", executed, result, tc.want)
			}
		})
	}
}

// TestResumedApprovalWithAmbiguousArgsIsRejected pins that approving a paused
// permission ask re-checks the arguments: a pending call with an ambiguous
// argument object, as an earlier version could have persisted, and a canonical
// pending call that a PreToolUse hook rewrites into an ambiguous one are both
// refused after the approval without running.
func TestResumedApprovalWithAmbiguousArgsIsRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		pending   string
		mutated   string
		ambiguous string
	}{
		{name: "ambiguous pending call", pending: `{"command":"echo shown","command":"echo hidden"}`, ambiguous: `{"command":"echo shown","command":"echo hidden"}`},
		{name: "hook rewrite on resume", pending: `{"command":"echo approved"}`, mutated: `{"command":"echo approved","Command":"echo hidden"}`, ambiguous: `{"command":"echo approved","Command":"echo hidden"}`},
		{name: "canonical pending call", pending: `{"command":"echo approved"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := session.NewToolCall(session.NewToolCallID(), tool.ShellToolName, []byte(tc.pending))
			sess := session.New("resume", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, session.Limits{MaxTurns: 3}, time.Now())
			if err := sess.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := sess.RecordAssistant(session.NewAssistantMessage("", []session.ToolCall{call})); err != nil {
				t.Fatal(err)
			}
			const askID = "resume:0:pending:r1"
			if err := sess.PauseForApproval(session.PendingAsk{AskID: askID, Call: call.ID, Tool: call.Name, Args: call.Args, Origin: session.ApprovalOriginPermission}); err != nil {
				t.Fatal(err)
			}
			runner := &fakeShellRunner{res: tool.CommandResult{Stdout: "ok"}}
			catalog := tool.NewCatalog()
			catalog.MustRegister(NewShellTool())
			policy := newPolicyArgsRecorder()
			deps := Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalog, Policy: policy}
			if tc.mutated != "" {
				deps.Hooks = argsRewriteHook{mutated: tc.mutated}
			}
			var result session.ToolResult
			for event := range NewEngine(deps).ResumeApproval(context.Background(), sess, shellEnvRunner(runner), askID, session.VerdictAllowOnce).Events() {
				if event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.CallID == call.ID {
					result = *event.ToolResult
				}
			}
			runner.mu.Lock()
			executed := runner.command
			runner.mu.Unlock()

			if tc.ambiguous != "" {
				assertNotEvaluated(t, policy, tc.ambiguous)
				assertAmbiguousResult(t, result)
				if executed != "" {
					t.Fatalf("approved ambiguous call executed %q", executed)
				}
				return
			}
			if result.IsError || executed != "echo approved" {
				t.Fatalf("approved canonical call: command=%q result=%+v", executed, result)
			}
		})
	}
}

// TestReadBatchRejectsOnlyTheAmbiguousCall pins that in a read batch mixing
// canonical and ambiguous calls, only the ambiguous call is refused: it is never
// evaluated or read, and its canonical siblings are evaluated and run.
func TestReadBatchRejectsOnlyTheAmbiguousCall(t *testing.T) {
	t.Parallel()
	const ambiguous = `{"path":"a.txt","Path":"b.txt"}`
	calls := []port.LLMToolCall{
		{ID: "first", Name: "Read", Args: []byte(`{"path":"a.txt"}`)},
		{ID: "ambiguous", Name: "Read", Args: []byte(ambiguous)},
		{ID: "last", Name: "Read", Args: []byte(`{"path":"b.txt"}`)},
	}
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	policy := newPolicyArgsRecorder()
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.LLMToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy})
	env := testEnvironment(policyArgsWorkspace(t), nil)
	sess := session.New("read-batch", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Now())
	results := map[session.ToolCallID]session.ToolResult{}
	for event := range eng.Run(context.Background(), sess, env, RunRequest{Text: "run"}).Events() {
		if event.Type == session.EvPermissionAsk {
			t.Fatalf("unexpected permission ask: %+v", event.Ask)
		}
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			results[event.ToolResult.CallID] = *event.ToolResult
		}
	}

	canonical := make(map[session.ProviderCallID]session.ToolCallID)
	for _, message := range sess.Conversation.Messages {
		if message.Replay == nil {
			continue
		}
		for _, binding := range message.Replay.Bindings {
			canonical[binding.PairingID] = binding.CallID
		}
	}
	ambiguousID, ok := canonical["ambiguous"]
	if !ok {
		t.Fatal("missing canonical ID for ambiguous provider call")
	}
	assertNotEvaluated(t, policy, ambiguous)
	assertAmbiguousResult(t, results[ambiguousID])
	if got := results[ambiguousID].Content; strings.Contains(got, "alpha") || strings.Contains(got, "beta") {
		t.Fatalf("ambiguous call read a file: %q", got)
	}
	if len(policy.observations()) != 2 {
		t.Errorf("policy evaluated %d calls, want the 2 canonical ones: %+v", len(policy.observations()), policy.observations())
	}
	for providerID, want := range map[session.ProviderCallID]string{"first": "alpha", "last": "beta"} {
		id, ok := canonical[providerID]
		if !ok {
			t.Errorf("missing canonical ID for provider call %q", providerID)
			continue
		}
		if res := results[id]; res.IsError || !strings.Contains(res.Content, want) {
			t.Errorf("canonical sibling %s = %+v, want content %q", providerID, res, want)
		}
	}
}
