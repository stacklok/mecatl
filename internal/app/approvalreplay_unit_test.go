package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// spyPolicy records every Learn call so a closure test can assert WHETHER Learn was
// invoked (the byID-miss fail-safe) and with WHICH call (correlation), independent of
// whether governance.LearnableRule would actually derive a rule from it. It is the
// non-vacuous oracle: a mutation that fabricates a call on the miss path is caught
// here even when that fabricated call happens to be unlearnable.
type spyPolicy struct{ learned []session.ToolCall }

func (*spyPolicy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, _ session.ToolCall, _ tool.WorkspaceReader) port.PermissionResult {
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Allow}}
}
func (p *spyPolicy) Learn(_ session.SessionID, c session.ToolCall) { p.learned = append(p.learned, c) }

var _ port.PermissionPolicy = (*spyPolicy)(nil)

// seedReplayFixture builds the closure-under-test over a SPY policy plus a session
// whose conversation holds ONE Write ToolCall (id callID) iff inHistory, and logs one
// allow-always EvApproval for that call. It returns the closure, the session, and the
// spy so a test can assert exactly which calls Learn was invoked on.
func seedReplayFixture(t *testing.T, callID session.ToolCallID, inHistory bool) (func(context.Context, *session.Session), *session.Session, *spyPolicy) {
	t.Helper()
	log := memstore.NewEventLog()
	spy := &spyPolicy{}
	replay := replayApprovals(log, spy, port.NopDiagnostics{})
	if replay == nil {
		t.Fatal("replayApprovals returned nil with a real log+policy")
	}

	sess := session.New("s-replay", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall(callID, "Write", json.RawMessage(`{"path":"note.txt","content":"x"}`))
	if inHistory {
		if err := sess.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
	}
	// Log one allow-always verdict for that call (the 3a emit shape: metadata only,
	// Call carries the opaque gated id).
	ev := session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{
		AskID:       "s-replay:0:" + string(callID) + ":r1",
		Verdict:     session.VerdictStringAllowAlways,
		Tool:        "Write",
		Call:        callID,
		AllowAlways: true,
	}}
	if err := log.Append(context.Background(), sess.ID, ev); err != nil {
		t.Fatalf("log append: %v", err)
	}
	return replay, sess, spy
}

// TestReplayApprovalsSkipsCompactedAwayCall pins the byID-miss FAIL-SAFE (SHOULD-ADD
// 6): when the allow-always verdict's gated ToolCall is no longer in the loaded
// conversation (e.g. compacted away), the closure must NOT call Learn at all — it has
// no real ToolCall (hence no args) to re-derive from, so it skips, leaving the session
// to RE-ASK. The alternative (Learning a fabricated/tool-only call) would risk a
// silent over-grant.
//
// MUTATION-KILL: add `policy.Learn(sess.ID, session.ToolCall{Name: ev.Approval.Tool})`
// on the byID-miss branch and this fails — Learn would be invoked despite the absent
// call. (The spy records the invocation regardless of whether the fabricated call is
// learnable, so the kill is non-vacuous.)
func TestReplayApprovalsSkipsCompactedAwayCall(t *testing.T) {
	replay, sess, spy := seedReplayFixture(t, "gone1", false /* call NOT in history */)
	replay(context.Background(), sess)
	if len(spy.learned) != 0 {
		t.Fatalf("replay invoked Learn %d time(s) for a compacted-away call, want 0 (fail-safe skip): %v", len(spy.learned), spy.learned)
	}
}

// TestReplayApprovalsLearnsPresentCall is the positive control: when the gated call IS
// in history, the closure invokes Learn EXACTLY once, with the REAL ToolCall (its args
// intact, sourced from history — not the metadata-only event). This makes the skip
// test above non-vacuous (the only difference is the call's presence) and pins the
// correlation (Learn sees the real call, args and all).
func TestReplayApprovalsLearnsPresentCall(t *testing.T) {
	replay, sess, spy := seedReplayFixture(t, "here1", true /* call in history */)
	replay(context.Background(), sess)
	if len(spy.learned) != 1 {
		t.Fatalf("replay invoked Learn %d time(s) for a present allow-always call, want 1", len(spy.learned))
	}
	got := spy.learned[0]
	if got.ID != "here1" || got.Name != "Write" || len(got.Args) == 0 {
		t.Fatalf("Learn got %+v, want the REAL ToolCall (id=here1, Write, non-empty args from history)", got)
	}
}

// TestReplayApprovalsIdempotent pins replay idempotency (NICE 8): the closure itself
// is safe to re-run. The Service gates it to once-per-id, but a double-run must learn
// the SAME single rule, never a duplicate — verified through the REAL permstore-backed
// policy (permstore.Record de-dups identical rules).
func TestReplayApprovalsIdempotent(t *testing.T) {
	log := memstore.NewEventLog()
	store := permstore.New()
	policy := permpolicy.NewPolicy(nil, store)
	replay := replayApprovals(log, policy, port.NopDiagnostics{})

	sess := session.New("s-dup", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall("dup1", "Write", json.RawMessage(`{"path":"note.txt","content":"x"}`))
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := log.Append(context.Background(), sess.ID, session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{
		AskID: "s-dup:0:dup1:r1", Verdict: session.VerdictStringAllowAlways, Tool: "Write", Call: "dup1", AllowAlways: true,
	}}); err != nil {
		t.Fatalf("log append: %v", err)
	}

	replay(context.Background(), sess)
	replay(context.Background(), sess)
	if rules := store.Rules(sess.ID); len(rules) != 1 {
		t.Fatalf("replay learned %d rule(s) after two runs, want exactly 1 (idempotent)", len(rules))
	}
}
