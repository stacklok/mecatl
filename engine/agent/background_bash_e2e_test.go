package agent_test

// End-to-end background-Bash tests through the REAL loop (BACKGROUND-BASH
// feature): the agent BashTool's `background: true` flag detaches a command
// onto the parent run's child registry (family bash-cmd, ids "bashcmd-<callID>"),
// BashStatus is its sole channel (roster / live tail / collect-once / cancel),
// the finished job rides the family-aware turn-boundary harness note, and the
// run-end drain cancels a still-running job. Every test drives a scripted
// mockllm engine with a channel-controlled fake CommandStreamer (no real
// sleeps — timing is owned by the test's channels, exactly the discipline of
// background_test.go / noticenudge_test.go).

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// --- fake streaming command runner ------------------------------------------

// bgBashJobHandle is the test's handle on one RunStreaming invocation.
type bgBashJobHandle struct {
	started   chan struct{} // closed when the invocation parks
	release   chan struct{} // close to let the invocation finish
	released  chan struct{} // closed on the FIRST releaseOnce (observe "release happened")
	cancelled chan struct{} // closed if the invocation's ctx fires first
	done      chan struct{} // closed when the invocation's terminal is certain
}

// fakeBashStreamer is a controllable tool.CommandRunner + tool.CommandStreamer
// double. RunStreaming invocations park until the test releases them (writing
// the canned output, exiting 0) or their ctx dies (recording the cancel and
// returning ctx.Err()). Run is the foreground seam: it runs immediately and
// records every invocation.
type fakeBashStreamer struct {
	mu        sync.Mutex
	streaming int
	fgRuns    []string

	started  chan struct{} // closed on the FIRST RunStreaming invocation
	released chan struct{} // closed on the FIRST releaseOnce
	job      *bgBashJobHandle
}

func newFakeBashStreamer() *fakeBashStreamer {
	return &fakeBashStreamer{
		started:  make(chan struct{}),
		released: make(chan struct{}),
	}
}

// Run is the foreground path: immediate, recorded, canned-success.
func (f *fakeBashStreamer) Run(_ context.Context, command, _ string) (tool.CommandResult, error) {
	f.mu.Lock()
	f.fgRuns = append(f.fgRuns, command)
	f.mu.Unlock()
	return tool.CommandResult{Stdout: "fg: " + command + "\n"}, nil
}

// RunStreaming parks the invocation until released or cancelled. The returned
// done channel closes when the invocation's terminal is certain (release → the
// drive stores the result right after; ctx-cancel → the drive records the
// cancellation right after), which is the signal a parent anchor parks on to
// sequence "the job's terminal has genuinely landed" before a turn boundary.
func (f *fakeBashStreamer) RunStreaming(ctx context.Context, command, _ string, out io.Writer) (int, error) {
	h := &bgBashJobHandle{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		released:  make(chan struct{}),
		cancelled: make(chan struct{}),
		done:      make(chan struct{}),
	}
	f.mu.Lock()
	f.streaming++
	if f.job == nil {
		f.job = h
		close(f.started)
	}
	f.mu.Unlock()
	defer close(h.started)

	select {
	case <-h.release:
		_, _ = io.WriteString(out, "output of: "+command+"\n")
		close(h.done)
		return 0, nil
	case <-ctx.Done():
		close(h.cancelled)
		close(h.done)
		return 0, ctx.Err()
	}
}

// releaseOnce lets the (first) parked streaming invocation finish, exactly once.
func (f *fakeBashStreamer) releaseOnce() {
	f.mu.Lock()
	h := f.job
	f.mu.Unlock()
	if h == nil {
		return
	}
	select {
	case <-h.release:
	default:
		close(h.release)
	}
	select {
	case <-f.released:
	default:
		close(f.released)
	}
}

// streamingCalls reports how many RunStreaming invocations ever started.
func (f *fakeBashStreamer) streamingCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streaming
}

// foregroundCalls reports the recorded foreground Run commands.
func (f *fakeBashStreamer) foregroundCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fgRuns...)
}

// startThenReleaseTool parks until the fake runner's streaming invocation has
// genuinely started (the job is live in the registry), then releases it — the
// deterministic "the background job is parked, now let it finish" anchor a
// scripted parent turn sequences on. Driving the release from INSIDE dispatch
// (not the event stream) removes the race where a card-keyed release fires
// after a sibling wait_ms park has already missed its wake.
type startThenReleaseTool struct{ runner *fakeBashStreamer }

func (*startThenReleaseTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "StartThenRelease", Description: "waits for the job then releases it", Schema: emptyObjSchema}
}
func (*startThenReleaseTool) ReadOnly() bool { return true }
func (g *startThenReleaseTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	select {
	case <-g.runner.started:
	case <-ctx.Done():
		return session.NewToolResult(in.ID, "cancelled"), nil
	}
	g.runner.releaseOnce()
	return session.NewToolResult(in.ID, "released"), nil
}

// awaitSignalTool is already declared in background_test.go (same package):
// it parks until a channel closes — the deterministic "the background job has
// genuinely reached its park" anchor scripted parent turns sequence on.

// bashCatalogFor builds the main-engine catalog for these tests: the agent
// BashTool over the fake runner + the BashStatus companion.
func bashCatalogFor(t *testing.T, runner tool.CommandRunner, extra ...tool.Tool) *tool.Catalog {
	t.Helper()
	tools := append([]tool.Tool{agent.NewBashTool(runner), agent.NewBashStatusTool()}, extra...)
	return catalogWith(t, tools...)
}

// TestBackgroundBashHappyPath is the headline e2e: the model starts a
// background command, polls it RUNNING (live tail peek), the job finishes,
// the NEXT turn boundary records the family-aware harness note naming the job
// id, and BashStatus then collects the completed tail + exit line EXACTLY ONCE
// (a second collect reports already-delivered).
func TestBackgroundBashHappyPath(t *testing.T) {
	runner := newFakeBashStreamer()
	cat := bashCatalogFor(t, runner, &awaitSignalTool{ch: runner.started})

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("b1", "Bash", `{"command":"build","background":true}`)),
		// Park until the streaming invocation is genuinely live, so the poll
		// below deterministically observes a RUNNING job.
		mockllm.ToolCallTurn(toolCall("pw", "AwaitChild", `{}`)),
		mockllm.ToolCallTurn(toolCall("b2", "BashStatus", `{"job_id":"bashcmd-b1"}`)),
		// Roster wait (no job_id): parks until the job's terminal lands in the
		// registry but does NOT collect, so the NEXT turn boundary sees a
		// finished, uncollected job and injects the harness note.
		mockllm.ToolCallTurn(toolCall("b4", "BashStatus", `{"wait_ms":30000}`)),
		// Collect the finished result (exactly once)...
		mockllm.ToolCallTurn(toolCall("b5", "BashStatus", `{"job_id":"bashcmd-b1"}`)),
		// ...and a second collect reports already-delivered.
		mockllm.ToolCallTurn(toolCall("b6", "BashStatus", `{"job_id":"bashcmd-b1"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")

	evs := drainObserving(t, r, func(ev session.Event) {
		// Release the job only once the RUNNING poll's result has been recorded:
		// the roster wait (b4) then parks on a job that can finish, and the
		// boundary after b4 deterministically sees it done and uncollected.
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "b2" {
			runner.releaseOnce()
		}
	})
	results := resultByCallID(evs)

	// The background call returned IMMEDIATELY with the started-result naming
	// the bashcmd- job id and the BashStatus channel.
	started := results["b1"]
	if started == nil || started.IsError {
		t.Fatalf("background Bash call must return a non-error started-result, got %+v", started)
	}
	for _, want := range []string{"job id: bashcmd-b1", "BashStatus", "cancelled if it is still running when this run ends"} {
		if !strings.Contains(started.Content, want) {
			t.Fatalf("started-result must mention %q, got %q", want, started.Content)
		}
	}

	// The RUNNING poll: state + command + live tail peek, NOT marked delivered.
	running := results["b2"]
	if running == nil || running.IsError {
		t.Fatalf("running poll must succeed, got %+v", running)
	}
	for _, want := range []string{"bashcmd-b1", "running", "command: build"} {
		if !strings.Contains(running.Content, want) {
			t.Fatalf("running poll must mention %q, got %q", want, running.Content)
		}
	}

	// The turn-boundary harness note: EXACTLY ONE message, the bash clause
	// naming the job id + its stop label, and no delegation clause.
	wantNotice := "[harness note: 1 background command(s) finished: bashcmd-b1 (end_turn). " +
		"Collect each output with BashStatus before relying on it.]"
	noticeIdx := userMessageEqual(sess.Conversation.Messages, wantNotice)
	if len(noticeIdx) != 1 {
		t.Fatalf("exactly ONE exact bash-finished notice expected, got %d in history:\n%v",
			len(noticeIdx), sess.Conversation.Messages)
	}
	if all := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(all) != 1 {
		t.Fatalf("the notice must never repeat; found %d harness notes", len(all))
	}
	// The notice precedes the model's collection turn (it is what prompts it).
	collectIdx := assistantWithCall(sess.Conversation.Messages, "b5")
	if collectIdx == -1 || noticeIdx[0] > collectIdx {
		t.Fatalf("notice (idx %d) must precede the collection turn (idx %d)", noticeIdx[0], collectIdx)
	}

	// Collection delivers the retained tail + the exit line, exactly once.
	collected := results["b5"]
	if collected == nil || collected.IsError {
		t.Fatalf("collection must succeed, got %+v", collected)
	}
	if !strings.Contains(collected.Content, "output of: build") || !strings.Contains(collected.Content, "[exit code: 0]") {
		t.Fatalf("collected result must carry the tail + exit line, got %q", collected.Content)
	}
	second := results["b6"]
	if second == nil || !strings.Contains(second.Content, "already delivered") || !strings.Contains(second.Content, "bashcmd-b1") {
		t.Fatalf("second collection must report already-delivered with the id, got %+v", second)
	}
	if strings.Contains(second.Content, "output of: build") {
		t.Fatalf("second collection must NOT re-deliver the tail, got %q", second.Content)
	}

	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got stop %q (err %q)", got.Stop, got.Error)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("history with the injected notice must stay pairing-valid: %v", err)
	}
}

// TestBackgroundBashCancel drives the cancel verb: the model cancels a live
// job through BashStatus, the fake runner's ctx fires (the process-group seam
// in production), the job lands StopCancelled, and the collect reports the
// cancellation.
func TestBackgroundBashCancel(t *testing.T) {
	runner := newFakeBashStreamer()
	cat := bashCatalogFor(t, runner, &awaitSignalTool{ch: runner.started})

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("b1", "Bash", `{"command":"slow","background":true}`)),
		mockllm.ToolCallTurn(toolCall("pw", "AwaitChild", `{}`)),
		mockllm.ToolCallTurn(toolCall("b2", "BashStatus", `{"cancel":"bashcmd-b1"}`)),
		// Wait for the cancellation to land in the registry, then collect.
		mockllm.ToolCallTurn(toolCall("b3", "BashStatus", `{"job_id":"bashcmd-b1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drainObserving(t, r, nil)
	results := resultByCallID(evs)

	// The cancel confirmation names the job and does not wait for the terminal.
	confirm := results["b2"]
	if confirm == nil || confirm.IsError || !strings.Contains(confirm.Content, "cancellation requested") {
		t.Fatalf("cancel must return a non-error confirmation, got %+v", confirm)
	}

	// The fake runner's ctx fired — the production process-group kill seam.
	select {
	case <-runner.job.cancelled:
	default:
		t.Fatalf("the cancelled job's ctx must have fired")
	}

	// The job collected as cancelled (notice was delivered-skip: the collecting
	// wait consumed the terminal before any boundary saw it).
	collected := results["b3"]
	if collected == nil || !strings.Contains(collected.Content, "canceled") {
		t.Fatalf("collected cancelled job must report the cancellation, got %+v", collected)
	}
	// The cancelled job's terminal may ride a harness note (the boundary sees it
	// finished before the wait collects it) — the contract under test is the
	// cancel + ctx kill, not delivered-skip.
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
}

// TestBackgroundBashRunEndDrain is the D10/drain contract for a bash job: a
// would-be clean end with a LIVE job draws the background-pending nudge ONCE
// (naming the bash job id), the model ends again, the run terminates cleanly,
// and the drain cancels the job (the fake saw its ctx fire).
func TestBackgroundBashRunEndDrain(t *testing.T) {
	runner := newFakeBashStreamer() // never released: the job outlives every parent turn
	cat := bashCatalogFor(t, runner, &awaitSignalTool{ch: runner.started})

	llm := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("b1", "Bash", `{"command":"watch","background":true}`),
			toolCall("pw", "AwaitChild", `{}`), // job genuinely mid-flight at the clean end
		),
		mockllm.TextTurn("done (ignoring the nudge)"),
		mockllm.TextTurn("still done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drainObserving(t, r, nil)

	// The pending nudge fired EXACTLY ONCE, the bash clause naming the job id.
	wantNudge := "[harness note: 1 background command(s) still running: bashcmd-b1. " +
		"Collect or wait for them with BashStatus, cancel them, or finish — " +
		"anything still running when you finish will be cancelled.]"
	if nudgeIdx := userMessageEqual(sess.Conversation.Messages, wantNudge); len(nudgeIdx) != 1 {
		t.Fatalf("exactly ONE exact bash pending-nudge expected, got %d:\n%v",
			len(nudgeIdx), sess.Conversation.Messages)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("the second clean end must terminate normally, got %q", got.Stop)
	}

	// The run-end drain cancelled the job: the fake's ctx fired.
	select {
	case <-runner.job.cancelled:
	default:
		t.Fatalf("the run-end drain must cancel the live job (ctx cancel)")
	}
}

// TestBackgroundBashPermissionGate drives the start through an Ask policy: the
// background call pauses on EvPermissionAsk, the fake runner has ZERO
// invocations before the Allow verdict, and the job spawns after it.
func TestBackgroundBashPermissionGate(t *testing.T) {
	runner := newFakeBashStreamer()
	cat := bashCatalogFor(t, runner, &startThenReleaseTool{runner: runner})
	policy := permpolicy.NewPolicy(nil, nil) // no rule → Ask on the Bash call

	// Turn 1: the gated Bash call + a start-then-release anchor in the SAME read
	// batch. Both pause on asks (Ask-by-default policy); b1's ask resolves first,
	// its job spawns and parks, then the anchor (itself ask-approved) waits for
	// that start and releases it — so by turn 2 the job is finishing, and b2's
	// wait collects a real terminal. The release is script-driven (inside
	// dispatch), never keyed on the event stream, so it cannot miss a park.
	llm := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("b1", "Bash", `{"command":"build","background":true}`),
			toolCall("rel", "StartThenRelease", `{}`),
		),
		mockllm.ToolCallTurn(toolCall("b2", "BashStatus", `{"job_id":"bashcmd-b1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")

	var sawBashAsk bool
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			// Correlate the ask to the BASH call via its structured Call field
			// (grammar-free); before the verdict the command must not have run.
			if ev.Ask.Call == "b1" {
				sawBashAsk = true
				if got := runner.streamingCalls(); got != 0 {
					t.Errorf("runner must have ZERO invocations before the Allow verdict, got %d", got)
				}
			}
			r.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	})
	if !sawBashAsk {
		t.Fatalf("the background Bash call must pause on EvPermissionAsk")
	}

	// After Allow the job spawned and was collectable.
	if got := runner.streamingCalls(); got != 1 {
		t.Fatalf("exactly one streaming invocation after Allow, got %d", got)
	}
	runner.releaseOnce()
	collected := resultByCallID(evs)["b2"]
	if collected == nil || collected.IsError || !strings.Contains(collected.Content, "output of: build") {
		t.Fatalf("the approved job must be collectable, got %+v", collected)
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
}

// TestBackgroundBashForegroundParity pins the foreground half: a Bash call
// with no background flag runs synchronously through the same engine, returns
// the ordinary combined output, and leaves NO registry entry (BashStatus's
// roster is empty).
func TestBackgroundBashForegroundParity(t *testing.T) {
	runner := newFakeBashStreamer()
	cat := bashCatalogFor(t, runner)

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("b1", "Bash", `{"command":"echo hi"}`)),
		mockllm.ToolCallTurn(toolCall("b2", "BashStatus", `{}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drainObserving(t, r, nil)
	results := resultByCallID(evs)

	fg := results["b1"]
	if fg == nil || fg.IsError || !strings.Contains(fg.Content, "fg: echo hi") || !strings.Contains(fg.Content, "[exit code: 0]") {
		t.Fatalf("foreground Bash must return the ordinary synchronous result, got %+v", fg)
	}
	if got := runner.foregroundCalls(); len(got) != 1 || got[0] != "echo hi" {
		t.Fatalf("the foreground runner must have run the command once, got %v", got)
	}
	if got := runner.streamingCalls(); got != 0 {
		t.Fatalf("a foreground call must never reach the streaming seam, got %d", got)
	}

	roster := results["b2"]
	if roster == nil || roster.IsError || !strings.Contains(roster.Content, "No background commands") {
		t.Fatalf("the roster must be empty after a foreground-only run, got %+v", roster)
	}
	if all := userMessagesContaining(sess.Conversation.Messages, harnessNoteMarker); len(all) != 0 {
		t.Fatalf("a foreground-only run must inject no harness note, found %d", len(all))
	}
	if got := lastResult(t, evs); got.Stop != session.StopEndTurn {
		t.Fatalf("run must end cleanly, got %q", got.Stop)
	}
}
