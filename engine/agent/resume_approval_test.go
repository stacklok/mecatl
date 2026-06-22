package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// driveToAwaiting runs an engine until its first permission ask, captures the
// askID, then SNAPSHOT-round-trips the session through sessnap to model process
// death: the parked run + askRegistry are discarded; only the persisted awaiting
// snapshot survives. It returns the captured askID and the restored awaiting
// session, exactly what a different process would load from the store. The engine
// (and its scripted mockllm cursor) is fully consumed by this; the caller resumes
// with a FRESH engine.
func driveToAwaiting(t *testing.T, e *agent.Engine, sess *session.Session, ws tool.Workspace, prompt string) (askID string, restored *session.Session) {
	t.Helper()
	r := e.Run(context.Background(), sess, ws, prompt)
	var snap sessnap.Snapshot
	var snapErr error
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			// The run goroutine is now PARKED in askRegistry.await (it emitted this
			// ask and blocks for a verdict), so the session is quiescent in
			// StateAwaiting: snapshotting it from here is race-free and models the
			// relay's Persist-on-ask (a durable awaiting snapshot). This MUST precede
			// the Cancel, which drives the LIVE session to a cancelled terminal —
			// exactly the process-death the resume path recovers from.
			snap, snapErr = sessnap.Of(sess)
			r.Cancel()
		}
	}
	if askID == "" {
		t.Fatal("the first engine never raised a permission ask")
	}
	if snapErr != nil {
		t.Fatalf("snapshot awaiting session: %v", snapErr)
	}
	if snap.State != session.StateAwaiting {
		t.Fatalf("captured snapshot state = %q, want awaiting (the snapshot raced the ask)", snap.State)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("restore awaiting session: %v", err)
	}
	if restored.State != session.StateAwaiting {
		t.Fatalf("restored state = %q, want awaiting", restored.State)
	}
	return askID, restored
}

// resumeEvents drains a resumed run and returns its events.
func resumeEvents(r *agent.Run) []session.Event {
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
	}
	return evs
}

// resultsFor counts EvToolResult events emitted for callID: the number that are
// NON-error and the total. The exactly-once assertions read both.
func resultsFor(evs []session.Event, callID session.ToolCallID) (nonError int, total int) {
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == callID {
			total++
			if !ev.ToolResult.IsError {
				nonError++
			}
		}
	}
	return nonError, total
}

// TestResumeApprovalExecutesPendingExactlyOnce: an awaiting session resumed with
// AllowOnce executes the pending tool EXACTLY ONCE (the exactly-once core), records
// one non-error result, and drives to a clean StopEndTurn completion.
func TestResumeApprovalExecutesPendingExactlyOnce(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New()) // Write asks by default
	sess := session.New("s-resume-once", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	// Engine #1: emits a Write tool call (gated as Ask), parks awaiting.
	write1 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e1 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write1),
		Policy:  policy,
	})
	askID, restored := driveToAwaiting(t, e1, sess, memfs.NewWorkspace("/ws"), "go")

	// Engine #2 (the "restarted process"): a FRESH engine over the restored session.
	// Its mockllm needs only the CONTINUATION turn (the tool call already happened
	// pre-restart). A fresh execution counter proves the tool runs exactly once here.
	var ran atomic.Int64
	write2 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Add(1)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e2 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done after approval")),
		Catalog: catalogWith(t, write2),
		Policy:  policy,
	})
	r := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), askID, session.VerdictAllowOnce)
	evs := resumeEvents(r)

	if got := ran.Load(); got != 1 {
		t.Fatalf("Write executed %d time(s) on resume, want EXACTLY 1", got)
	}
	if ne, total := resultsFor(evs, "w1"); ne != 1 || total != 1 {
		t.Fatalf("EvToolResult for w1: non-error=%d total=%d, want exactly 1 non-error / 1 total", ne, total)
	}
	if rp := lastResult(t, evs); rp.Stop != session.StopEndTurn {
		t.Fatalf("resume stop = %q, want %q", rp.Stop, session.StopEndTurn)
	}
	if restored.State != session.StateCompleted {
		t.Fatalf("restored session state = %q, want completed", restored.State)
	}
	assertNoOrphanedToolCalls(t, restored.Conversation.Messages)
}

// TestResumeApprovalDenyDoesNotExecute: a deny-after-restart synthesizes a deny
// result for the pending call, NEVER executes the tool, and still completes.
func TestResumeApprovalDenyDoesNotExecute(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("s-resume-deny", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	write1 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e1 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write1),
		Policy:  policy,
	})
	askID, restored := driveToAwaiting(t, e1, sess, memfs.NewWorkspace("/ws"), "go")

	var ran atomic.Int64
	write2 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Add(1)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e2 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("acknowledged the denial")),
		Catalog: catalogWith(t, write2),
		Policy:  policy,
	})
	r := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), askID, session.VerdictDeny)
	evs := resumeEvents(r)

	if got := ran.Load(); got != 0 {
		t.Fatalf("Write executed %d time(s) on a DENY resume, want 0", got)
	}
	if _, total := resultsFor(evs, "w1"); total != 1 {
		t.Fatalf("EvToolResult for w1: total=%d, want exactly 1 (the deny result)", total)
	}
	// The single result for w1 must be an error (a deny result).
	if ne, _ := resultsFor(evs, "w1"); ne != 0 {
		t.Fatalf("the deny result for w1 should be an error result, but a non-error one was emitted")
	}
	if rp := lastResult(t, evs); rp.Stop != session.StopEndTurn {
		t.Fatalf("resume stop = %q, want %q", rp.Stop, session.StopEndTurn)
	}
	assertNoOrphanedToolCalls(t, restored.Conversation.Messages)
}

// TestResumeApprovalNotAwaiting: resuming a session that is NOT awaiting ends as
// StopError (never a silent clean complete).
func TestResumeApprovalNotAwaiting(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("s-not-awaiting", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	// A fresh idle session is NOT awaiting.
	e := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("unreachable")),
		Catalog: catalogWith(t),
		Policy:  policy,
	})
	r := e.ResumeApproval(context.Background(), sess, memfs.NewWorkspace("/ws"), "any:0:x:r1", session.VerdictAllowOnce)
	evs := resumeEvents(r)
	rp := lastResult(t, evs)
	if rp.Stop != session.StopError {
		t.Fatalf("resume of a non-awaiting session: stop = %q, want %q", rp.Stop, session.StopError)
	}
	if sess.State != session.StateFailed {
		t.Fatalf("non-awaiting resume left session in %q, want failed", sess.State)
	}
}

// TestResumeApprovalWrongAskID: a stale/wrong askID against an awaiting session is
// rejected (StopError) and executes nothing.
func TestResumeApprovalWrongAskID(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("s-wrong-ask", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	var ran atomic.Int64
	write1 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e1 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write1),
		Policy:  policy,
	})
	_, restored := driveToAwaiting(t, e1, sess, memfs.NewWorkspace("/ws"), "go")

	write2 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			ran.Add(1)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e2 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("unreachable")),
		Catalog: catalogWith(t, write2),
		Policy:  policy,
	})
	r := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), "stale:9:bogus:r99", session.VerdictAllowOnce)
	evs := resumeEvents(r)
	if got := ran.Load(); got != 0 {
		t.Fatalf("a wrong-askID resume executed the tool %d time(s), want 0", got)
	}
	if rp := lastResult(t, evs); rp.Stop != session.StopError {
		t.Fatalf("wrong-askID resume: stop = %q, want %q", rp.Stop, session.StopError)
	}
}

// TestResumeApprovalMultiToolSiblingCloseOut is the sibling-close-out correctness
// proof: a multi-tool turn parks on the MIDDLE call (3 calls, #2 the Ask). After
// restart-approve: #2 executes once, #1/#3 are closed out as synthetic aborted
// error results, ValidateToolPairing passes, the run completes.
func TestResumeApprovalMultiToolSiblingCloseOut(t *testing.T) {
	// ReadA/ReadB are allowed by an explicit by-name floor rule; Gate (the middle
	// call) has NO rule, so it falls through to the Ask floor and parks. Dispatch is
	// read-parallel/mutate-serial: the middle call is a MUTATING tool that asks, so it
	// parks as its own serial unit between the two read-only siblings.
	policy := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeBuiltinDefault, Tool: "ReadA", Effect: governance.Allow},
		{Scope: governance.ScopeBuiltinDefault, Tool: "ReadB", Effect: governance.Allow},
	}, permstore.New())
	sess := session.New("s-multi", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	read := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: true,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(in.ID, name+" ran"), nil
			}}
	}
	// Gate: a MUTATING tool with NO floor allow, so it asks under ModeDefault.
	gate1 := &fakeTool{name: "Gate", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "gate ran"), nil
		}}

	// The single assistant turn emits THREE calls in order: read#1, Gate, read#3.
	// Dispatch runs read#1, then hits Gate (mutating) which asks → parks. read#3 is
	// never reached pre-restart.
	e1 := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("r1", "ReadA", `{}`)),
			mockllm.ToolCallChunk(toolCall("g2", "Gate", `{}`)),
			mockllm.ToolCallChunk(toolCall("r3", "ReadB", `{}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		)),
		Catalog: catalogWith(t, read("ReadA"), gate1, read("ReadB")),
		Policy:  policy,
	})
	askID, restored := driveToAwaiting(t, e1, sess, memfs.NewWorkspace("/ws"), "go")

	var gateRan, readBRan atomic.Int64
	read2 := func(name string, ctr *atomic.Int64) *fakeTool {
		return &fakeTool{name: name, readOnly: true,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				if ctr != nil {
					ctr.Add(1)
				}
				return session.NewToolResult(in.ID, name+" ran"), nil
			}}
	}
	gate2 := &fakeTool{name: "Gate", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			gateRan.Add(1)
			return session.NewToolResult(in.ID, "gate ran"), nil
		}}
	e2 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done after gate")),
		Catalog: catalogWith(t, read2("ReadA", nil), gate2, read2("ReadB", &readBRan)),
		Policy:  policy,
	})
	r := e2.ResumeApproval(context.Background(), restored, memfs.NewWorkspace("/ws"), askID, session.VerdictAllowOnce)
	evs := resumeEvents(r)

	// #2 (Gate) executed exactly once.
	if got := gateRan.Load(); got != 1 {
		t.Fatalf("Gate executed %d time(s) on resume, want EXACTLY 1", got)
	}
	// #3 (ReadB) was CLOSED OUT, never dispatched.
	if got := readBRan.Load(); got != 0 {
		t.Fatalf("ReadB (a closed-out sibling) executed %d time(s), want 0 — siblings are closed out, not re-dispatched", got)
	}
	// g2 has exactly one non-error result; r1 (pre-restart) and r3 (closed-out) each
	// have an answer so the history pairs.
	if ne, total := resultsFor(evs, "g2"); ne != 1 || total != 1 {
		t.Fatalf("EvToolResult for g2: non-error=%d total=%d, want 1/1", ne, total)
	}
	// History must be tool-pairing valid (no dangling tool_use → no provider 400).
	if err := session.ValidateToolPairing(restored.Conversation.Messages); err != nil {
		t.Fatalf("resumed history is not tool-pairing valid: %v", err)
	}
	assertNoOrphanedToolCalls(t, restored.Conversation.Messages)
	if rp := lastResult(t, evs); rp.Stop != session.StopEndTurn {
		t.Fatalf("resume stop = %q, want %q", rp.Stop, session.StopEndTurn)
	}
}

// captureFirstAsk runs the engine, cancels at the first permission ask, and
// returns the captured PendingAsk. Offline (mockllm + memfs); models a host that
// pauses on the ask. It mirrors drainApproving's 10s watchdog so a wiring
// regression fails THIS test rather than hanging the whole suite.
func captureFirstAsk(t *testing.T, r *agent.Run) session.PendingAsk {
	t.Helper()
	var got *session.PendingAsk
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if got == nil {
					t.Fatal("the engine never raised a permission ask")
				}
				return *got
			}
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil && got == nil {
				ask := *ev.Ask
				got = &ask
				r.Cancel()
			}
		case <-deadline:
			r.Cancel()
			t.Fatal("timed out waiting for a permission ask (10s); likely a wiring regression")
		}
	}
}

// TestPendingAskCarriesGatedCallID (#148): a gated dispatch surfaces a PendingAsk
// whose .Call equals the dispatched ToolCall.ID — the REQUEST-half twin of
// ApprovalPayload.Call, so a host correlates the ask to its tool call without
// parsing the askID grammar.
func TestPendingAskCarriesGatedCallID(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New()) // Write asks by default
	sess := session.New("s-ask-call", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write),
		Policy:  policy,
	})
	ask := captureFirstAsk(t, e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go"))
	if ask.Call != "w1" {
		t.Fatalf("PendingAsk.Call = %q, want the gated ToolCall.ID %q", ask.Call, "w1")
	}
}

// TestRunOptionsAskIDDiscriminatorReplacesSerial (#117/ADR-0044, T5 positive
// case): a host-supplied colon-free discriminator REPLACES the "r<serial>"
// trailing askID component, making the askID reconstructable across processes.
func TestRunOptionsAskIDDiscriminatorReplacesSerial(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("s-disc-ok", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write),
		Policy:  policy,
	})
	r := e.RunContentWith(context.Background(), sess, memfs.NewWorkspace("/ws"), "go", nil,
		agent.RunOptions{AskIDDiscriminator: "run-42"})
	ask := captureFirstAsk(t, r)
	if !strings.HasSuffix(ask.AskID, ":run-42") {
		t.Fatalf("askID = %q, want it to end with the host discriminator %q", ask.AskID, ":run-42")
	}
	if !strings.HasPrefix(ask.AskID, "s-disc-ok:") {
		t.Fatalf("askID = %q, must preserve the consumed session-id prefix", ask.AskID)
	}
}

// TestAskIDDiscriminatorReconstructableAcrossRuns (#117/ADR-0044) proves the
// END-TO-END property the feature exists for: two INDEPENDENT runs (two separate
// RunContentWith→startRun→authorize→newAskID chains) over the SAME session id with
// the SAME AskIDDiscriminator and the SAME scripted tool-call mint a byte-IDENTICAL
// emitted PendingAsk.AskID — what a restarted/second process reconstructs from
// persisted state. This is stronger than the newAskID unit test: it exercises the
// full wiring (the resolved discriminator on Run.askDiscriminator flowing into the
// emitted askID), not just the formatter. The two runs use FRESH idle sessions with
// the same id so n (Counters.ToolCalls) and the scripted callID match.
func TestAskIDDiscriminatorReconstructableAcrossRuns(t *testing.T) {
	const discriminator = "run-42"
	mint := func() string {
		policy := permpolicy.NewPolicy(nil, permstore.New())
		// Same session id across both runs (a different PROCESS loading the same id).
		sess := session.New("s-reconstruct", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		write := &fakeTool{name: "Write", readOnly: false,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(in.ID, "wrote"), nil
			}}
		e := newEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
			Catalog: catalogWith(t, write),
			Policy:  policy,
		})
		r := e.RunContentWith(context.Background(), sess, memfs.NewWorkspace("/ws"), "go", nil,
			agent.RunOptions{AskIDDiscriminator: discriminator})
		return captureFirstAsk(t, r).AskID
	}
	first := mint()
	second := mint()
	if first != second {
		t.Fatalf("two independent runs over the same session id + discriminator must mint an IDENTICAL askID (cross-process reconstructability); got %q vs %q", first, second)
	}
	if !strings.HasSuffix(first, ":"+discriminator) {
		t.Fatalf("the reconstructable askID must carry the host discriminator suffix; got %q", first)
	}
}

// TestRunOptionsAskIDDiscriminatorColonFallsBack (#117/ADR-0044, T5 negative
// case): a colon-containing discriminator is IGNORED (it would make the askID
// grammar ambiguous) and the run falls back to the process-global "r<serial>"
// component — the minted askID must NOT embed the rejected value.
func TestRunOptionsAskIDDiscriminatorColonFallsBack(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("s-disc-colon", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	diag := newRecordingDiag()
	e := newEngine(agent.Deps{
		LLM:         mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog:     catalogWith(t, write),
		Policy:      policy,
		Diagnostics: diag,
	})
	r := e.RunContentWith(context.Background(), sess, memfs.NewWorkspace("/ws"), "go", nil,
		agent.RunOptions{AskIDDiscriminator: "a:b"})
	ask := captureFirstAsk(t, r)
	if strings.Contains(ask.AskID, ":a:b") {
		t.Fatalf("a colon-containing discriminator must be IGNORED, but askID embedded it: %q", ask.AskID)
	}
	// Fallback shape: trailing component is "r<serial>".
	last := ask.AskID[strings.LastIndex(ask.AskID, ":")+1:]
	if !strings.HasPrefix(last, "r") {
		t.Fatalf("colon-fallback askID trailing component = %q, want an \"r<serial>\" fallback", last)
	}
	// The rejection must be OPERATOR-VISIBLE, not silent: assert the WARN fired.
	line, ok := diag.findLine("falling back to run serial")
	if !ok {
		t.Fatal("colon-containing discriminator did not emit the fallback WARN diagnostic")
	}
	if line.level != port.LevelWarn {
		t.Fatalf("fallback diagnostic level = %v, want WARN", line.level)
	}
}
