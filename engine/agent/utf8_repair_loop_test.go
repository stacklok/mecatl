package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// invalidUTF8 is the orphaned-lead-byte sequence from issue #402: BSD `cat -t`
// turns a valid em dash (E2 80 94) into E2 4D 2D 5E 40 4D 2D 5E 54 — the E2
// lead byte is retained while its continuation bytes are rendered as ASCII,
// leaving an invalid sequence.
const invalidUTF8 = "\xe2M-^@M-^T"

// TestLoopRepairsInvalidUTF8ToolResult proves the loop normalizes a tool's
// invalid-UTF-8 result at the effective-payload choke point (issue #402), so
// the THREE consumers — the emitted EvToolResult, the recorded conversation
// (the model's replayed history), and the next-turn provider request — all
// carry the SAME U+FFFD-repaired text. A protobuf string field rejects invalid
// UTF-8 at marshal time, so without this repair the gRPC Converse stream dies
// with codes.Internal; this test pins the domain-side fix that keeps every
// view consistent.
func TestLoopRepairsInvalidUTF8ToolResult(t *testing.T) {
	bad := &fakeTool{name: "Bash", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "out "+invalidUTF8+" end"), nil
		}}
	cat := catalogWith(t, bad)

	// Capture every request the loop sends the provider; the SECOND request is
	// the one that replays the tool result back to the model.
	var mu sync.Mutex
	var reqs []port.LLMRequest
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			mu.Lock()
			reqs = append(reqs, r)
			mu.Unlock()
		})},
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Bash", `{"command":"x"}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "run it"})
	evs := drain(r)

	// 1. The emitted EvToolResult is repaired.
	var emitted *session.ToolResult
	for i := range evs {
		if evs[i].Type == session.EvToolResult && evs[i].ToolResult != nil && evs[i].ToolResult.CallID == "c1" {
			emitted = evs[i].ToolResult
			break
		}
	}
	if emitted == nil {
		t.Fatalf("no EvToolResult for c1 in %v", typesOf(evs))
	}
	if !utf8.ValidString(emitted.Content) {
		t.Fatalf("emitted Content invalid: %q", emitted.Content)
	}
	if !strings.ContainsRune(emitted.Content, '�') {
		t.Fatalf("emitted Content not repaired: %q", emitted.Content)
	}

	// 2. The recorded conversation (the model's replayed history) is repaired.
	var recorded *session.ToolResult
	for i := range sess.Conversation.Messages {
		m := &sess.Conversation.Messages[i]
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" {
			recorded = m.ToolResult
			break
		}
	}
	if recorded == nil {
		t.Fatalf("no recorded tool result for c1")
	}
	if recorded.Content != emitted.Content {
		t.Fatalf("recorded != emitted:\nrecorded %q\nemitted  %q", recorded.Content, emitted.Content)
	}

	// 3. The next-turn provider request carries the SAME repaired text.
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) < 2 {
		t.Fatalf("expected >=2 provider calls, got %d", len(reqs))
	}
	var modelFacing string
	for _, m := range reqs[1].Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" {
			modelFacing = m.ToolResult.Content
		}
	}
	if modelFacing == "" {
		t.Fatalf("no tool-role message for c1 in second request")
	}
	if modelFacing != emitted.Content {
		t.Fatalf("model-facing != emitted:\nmodel %q\nemitted %q", modelFacing, emitted.Content)
	}
}

// utf8HookRunner is a PreToolUse hook that returns invalid UTF-8 in the two
// values a hook subprocess controls: the block Message (its raw stdout, via
// hookexec blockMessage) and the Mutated args JSON.
type utf8HookRunner struct{ mutate bool }

func (h utf8HookRunner) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase != governance.PhasePreToolUse {
		return governance.HookOutcome{}, nil
	}
	if h.mutate {
		return governance.HookOutcome{Mutated: json.RawMessage(`{"path":"` + invalidUTF8 + `"}`)}, nil
	}
	return governance.HookOutcome{Block: true, Message: "denied " + invalidUTF8}, nil
}

// TestPreToolUseHookOutputIsRepaired closes the one path that reaches recorded
// state WITHOUT crossing execute's RepairToolResult: a PreToolUse hook's own
// output. A hook is an operator-deployed SUBPROCESS, so its stdout is the same
// arbitrary-bytes producer as a tool's — hookexec blockMessage returns it with
// only TrimSpace applied — and a blocked call is turned into a ToolError by four
// call sites (runOne, runReadBatch Phase 1, and both resolvePendingCall arms),
// none of which call execute. Mutated args are the same story: json.Valid
// ACCEPTS invalid UTF-8 inside a string literal, so a malformed payload would be
// adopted into c.Args verbatim.
//
// Without the repair in preHook the wire still survives (the mapper backstop
// catches it), but the in-memory conversation keeps the RAW bytes while the
// stream, the model view and the snapshot each get U+FFFD from a different
// mechanism — the recorded == streamed == model-view invariant holding by
// coincidence instead of by construction.
func TestPreToolUseHookOutputIsRepaired(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate bool
		want   func(t *testing.T, sess *session.Session, evs []session.Event, rec *argRecorder)
	}{
		{
			name: "block message",
			want: func(t *testing.T, sess *session.Session, evs []session.Event, _ *argRecorder) {
				t.Helper()
				// The blocked call is recorded as a ToolError carrying the hook's message.
				var rec string
				for i := range sess.Conversation.Messages {
					if m := &sess.Conversation.Messages[i]; m.ToolResult != nil {
						rec = m.ToolResult.Content
					}
				}
				assertRepaired(t, "recorded ToolError", rec)
				for _, ev := range evs {
					if ev.Type == session.EvHook {
						assertRepaired(t, "EvHook text", ev.Text)
					}
				}
			},
		},
		{
			// Assert on the args the TOOL actually ran with. That is the strongest
			// available sink: openCard deliberately emits the ORIGINAL args and the
			// conversation records what the model emitted, so the rewritten value
			// reaches only the executing tool and the ToolCallRecorder audit seam.
			name:   "mutated args",
			mutate: true,
			want: func(t *testing.T, _ *session.Session, _ []session.Event, rec *argRecorder) {
				t.Helper()
				args := rec.args()
				assertRepaired(t, "args the tool executed with", args)
				if !json.Valid([]byte(args)) {
					t.Errorf("repair broke the args JSON: %q", args)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &argRecorder{name: "Read", readOnly: true}
			llm := mockllm.New(
				mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a.go"}`)),
				mockllm.TextTurn("done"),
			)
			e := newEngine(agent.Deps{
				LLM: llm, Catalog: catalogWith(t, rec),
				Hooks: utf8HookRunner{mutate: tc.mutate},
			})
			sess := newSession(t, session.Limits{})
			evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			tc.want(t, sess, evs, rec)
		})
	}
}

func assertRepaired(t *testing.T, what, got string) {
	t.Helper()
	if got == "" {
		t.Fatalf("%s: empty — the test never reached the path it means to pin", what)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("%s still holds invalid UTF-8: %q", what, got)
	}
	if !strings.ContainsRune(got, '�') {
		t.Fatalf("%s not repaired to U+FFFD: %q", what, got)
	}
}
