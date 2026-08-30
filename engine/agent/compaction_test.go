package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestHeuristicCompactorPreservesPaths checks the default compactor preserves the
// system prompt, the goal, and every touched file path, while truncating large
// tool-output bodies (gauntlet #12-lite).
func TestHeuristicCompactorPreservesPaths(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("fix the bug in handler.go"))
	conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
		session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"handler.go"}`)),
	}))
	conv.Append(session.NewToolMessage(session.NewToolResult("c1", strings.Repeat("X", 5000))))
	conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
		session.NewToolCall("c2", "Edit", json.RawMessage(`{"file_path":"util.go"}`)),
	}))
	conv.Append(session.NewToolMessage(session.NewToolResult("c2", "edited")))
	for i := 0; i < 10; i++ {
		conv.Append(session.NewUserMessage("more chatter"))
	}

	compacted, summary, err := agent.HeuristicCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Paths survive (in summary).
	for _, p := range []string{"handler.go", "util.go"} {
		if !strings.Contains(summary, p) {
			t.Fatalf("summary missing path %q: %s", p, summary)
		}
	}
	// System prompt and goal survive.
	var haveSystem, haveGoal bool
	for _, m := range compacted {
		if m.Role == session.RoleSystem && m.Text == "system rules" {
			haveSystem = true
		}
		if m.Role == session.RoleUser && strings.Contains(m.Text, "fix the bug") {
			haveGoal = true
		}
	}
	if !haveSystem {
		t.Fatalf("system prompt not preserved")
	}
	if !haveGoal {
		t.Fatalf("goal not preserved")
	}
	// Big bodies are dropped: no message should carry the 5000-char blob.
	for _, m := range compacted {
		if m.ToolResult != nil && len(m.ToolResult.Content) >= 5000 {
			t.Fatalf("large tool body survived compaction: %d chars", len(m.ToolResult.Content))
		}
	}
	// The result is shorter than the input.
	if len(compacted) >= len(conv.Messages) {
		t.Fatalf("compaction did not shrink history: %d -> %d", len(conv.Messages), len(compacted))
	}
}

// TestHeuristicCompactorNoOrphanedToolResultAtTailHead builds a conversation
// sized so the count-based cut (len-keep) lands ON a tool-result message whose
// matching assistant tool call sits ABOVE the cut (in the dropped head). Before
// the boundary-snap fix the tail STARTED on that orphaned result, which a
// provider rejects with HTTP 400. The compacted history must be tool-pairing
// valid and non-empty.
func TestHeuristicCompactorNoOrphanedToolResultAtTailHead(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("fix the bug"))
	// Build assistant-call / tool-result pairs. With KeepLastTurns chosen so the
	// cut bisects a pair, the tail's first message is a tool result.
	for i := 0; i < 5; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	// msgs: [sys, goal, A(a),T(a), A(b),T(b), A(c),T(c), A(d),T(d), A(e),T(e)]
	// len = 12. keep = 3 => cut = 9 => msgs[9] = T(d) (a tool result whose call
	// A(d) at index 8 is above the cut). Without snapping, tail starts on T(d).
	compacted, _, err := agent.HeuristicCompactor{KeepLastTurns: 3}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("compacted history is not tool-pairing valid: %v", err)
	}
	if len(compacted) == 0 {
		t.Fatalf("compacted history is empty")
	}
}

// TestHeuristicCompactorTailAllToolResults exercises the case where snapping must
// advance the cut all the way to len(msgs) because every message after the
// initial cut is a tool result. The tail ends up empty; the output is still
// non-empty (system + goal + summary) and orphan-free.
func TestHeuristicCompactorTailAllToolResults(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("goal"))
	// One assistant message requesting many calls, then a run of tool results.
	calls := make([]session.ToolCall, 0, 4)
	for i := 0; i < 4; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		calls = append(calls, session.NewToolCall(id, "Read", json.RawMessage(`{}`)))
	}
	conv.Append(session.NewAssistantMessage("", "", calls))
	for i := 0; i < 4; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	// msgs len = 7: [sys, goal, A(abcd), T(a),T(b),T(c),T(d)]. keep=4 => cut=3 =>
	// msgs[3..] are all tool results; snapping advances cut to len (empty tail).
	compacted, _, err := agent.HeuristicCompactor{KeepLastTurns: 4}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("compacted history is not tool-pairing valid: %v", err)
	}
	if len(compacted) == 0 {
		t.Fatalf("compacted history is empty")
	}
}

// TestHeuristicCompactorMultipleConsecutiveLeadingOrphans checks the snap loop
// consumes MULTIPLE consecutive leading tool results (a multi-call assistant turn
// bisected by the cut leaves several orphans at the tail head).
func TestHeuristicCompactorMultipleConsecutiveLeadingOrphans(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("goal"))
	calls := make([]session.ToolCall, 0, 3)
	for i := 0; i < 3; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		calls = append(calls, session.NewToolCall(id, "Read", json.RawMessage(`{}`)))
	}
	conv.Append(session.NewAssistantMessage("", "", calls))              // index 2
	conv.Append(session.NewToolMessage(session.NewToolResult("a", "x"))) // 3
	conv.Append(session.NewToolMessage(session.NewToolResult("b", "x"))) // 4
	conv.Append(session.NewToolMessage(session.NewToolResult("c", "x"))) // 5
	conv.Append(session.NewAssistantMessage("done", "", nil))            // 6
	// len=7, keep=4 => cut=3 => tail would start at T(a) with 3 consecutive
	// leading orphans T(a),T(b),T(c). Snapping must consume all three.
	compacted, _, err := agent.HeuristicCompactor{KeepLastTurns: 4}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("compacted history is not tool-pairing valid: %v", err)
	}
	// The trailing standalone assistant message (index 6) must still survive.
	var sawDone bool
	for _, m := range compacted {
		if m.Role == session.RoleAssistant && m.Text == "done" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("trailing assistant message did not survive snapping")
	}
}

// TestCascadeCompactorNoOrphanedToolResult feeds the same orphan-prone shape to
// the CascadeCompactor (offline: BudgetTokens 0 runs every deterministic tier
// once) and asserts the compacted history is tool-pairing valid.
func TestCascadeCompactorNoOrphanedToolResult(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("fix the bug"))
	for i := 0; i < 5; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, strings.Repeat("X", 2000))))
	}
	compacted, _, err := agent.CascadeCompactor{KeepLastTurns: 3}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("cascade compacted history is not tool-pairing valid: %v", err)
	}
	if len(compacted) == 0 {
		t.Fatalf("cascade compacted history is empty")
	}
}

// TestCascadeCompactorTailAllToolResults is cascade parity with the heuristic
// tail-all-tool-results case: the cut snaps to len (empty tail), output stays
// non-empty and orphan-free.
func TestCascadeCompactorTailAllToolResults(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("goal"))
	calls := make([]session.ToolCall, 0, 4)
	for i := 0; i < 4; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		calls = append(calls, session.NewToolCall(id, "Read", json.RawMessage(`{}`)))
	}
	conv.Append(session.NewAssistantMessage("", "", calls))
	for i := 0; i < 4; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	compacted, _, err := agent.CascadeCompactor{KeepLastTurns: 4}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("cascade compacted history is not tool-pairing valid: %v", err)
	}
	if len(compacted) == 0 {
		t.Fatalf("cascade compacted history is empty")
	}
}

// TestCascadeCompactorMultipleConsecutiveLeadingOrphans is cascade parity with the
// heuristic multiple-leading-orphans case: the snap consumes a run of consecutive
// leading tool results and the trailing standalone assistant message survives.
func TestCascadeCompactorMultipleConsecutiveLeadingOrphans(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("goal"))
	calls := make([]session.ToolCall, 0, 3)
	for i := 0; i < 3; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		calls = append(calls, session.NewToolCall(id, "Read", json.RawMessage(`{}`)))
	}
	conv.Append(session.NewAssistantMessage("", "", calls))
	conv.Append(session.NewToolMessage(session.NewToolResult("a", "x")))
	conv.Append(session.NewToolMessage(session.NewToolResult("b", "x")))
	conv.Append(session.NewToolMessage(session.NewToolResult("c", "x")))
	conv.Append(session.NewAssistantMessage("done", "", nil))
	compacted, _, err := agent.CascadeCompactor{KeepLastTurns: 4}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("cascade compacted history is not tool-pairing valid: %v", err)
	}
	var sawDone bool
	for _, m := range compacted {
		if m.Role == session.RoleAssistant && m.Text == "done" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("trailing assistant message did not survive cascade snapping")
	}
}

// recentTaskConversation builds a tool-heavy history whose most-recent USER task
// ("ACTUAL TASK: rename Foo to Bar") sits ABOVE a long run of assistant/tool
// messages, so the role-blind count-tail (keep=6) is ALL non-user and the task
// would fall into the dropped/summarised head under the old logic. The first user
// message ("original setup") is the goal pin. This is the bug repro both compactors
// must now defeat by back-snapping the tail to the recent user turn.
func recentTaskConversation() *session.Conversation {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("original setup"))
	// Several clean assistant/tool pairs (older settled work).
	for i := 0; i < 3; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	// The most-recent user instruction — the live task.
	conv.Append(session.NewUserMessage("ACTUAL TASK: rename Foo to Bar"))
	// A long run of trailing assistant/tool messages so the count-tail (keep=6) is
	// ENTIRELY non-user and the task falls outside it.
	for i := 0; i < 4; i++ {
		id := session.ToolCallID("t" + string(rune('0'+i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"g.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	return conv
}

// assertRecentTaskAndPinSurvive checks the most-recent user task survives verbatim
// as a RoleUser message, the first-user pin survives, and pairing holds.
func assertRecentTaskAndPinSurvive(t *testing.T, compacted []session.Message) {
	t.Helper()
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("compacted history is not tool-pairing valid: %v", err)
	}
	var sawTask, sawPin bool
	for _, m := range compacted {
		if m.Role == session.RoleUser && m.Text == "ACTUAL TASK: rename Foo to Bar" {
			sawTask = true
		}
		if m.Role == session.RoleUser && m.Text == "original setup" {
			sawPin = true
		}
	}
	if !sawTask {
		t.Fatalf("most-recent user task did not survive compaction (lost to the summarised head): %+v", compacted)
	}
	if !sawPin {
		t.Fatalf("first-user pin did not survive compaction")
	}
}

// TestHeuristicCompactorPreservesRecentUserTaskOutsideCountTail is the core bug
// repro for the heuristic compactor: the most-recent user task sits outside the
// role-blind count-tail, so the OLD logic summarised it away. The user-turn
// back-snap must keep it verbatim. Reverting snapCutToRecentUserTurn (or its wiring)
// makes sawTask false → red.
func TestHeuristicCompactorPreservesRecentUserTaskOutsideCountTail(t *testing.T) {
	conv := recentTaskConversation()
	compacted, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertRecentTaskAndPinSurvive(t, compacted)
}

// cascadeRecentTaskConversation is the cascade-specific core repro. Unlike the
// heuristic case, the no-LLM cascade's tier-1 snip keeps the more-recent HALF of the
// middle, so a task that happens to land in that half survives even WITHOUT the
// back-snap (a vacuous test). Here the task is deliberately placed in the OLDER half
// of the middle (right after the first-user pin, with a long trailing run so the snip
// midpoint falls well AFTER it): snip alone DROPS it, and only snapCutToRecentUserTurn
// pulls it into the verbatim tail. Reverting the back-snap therefore turns the cascade
// repro RED (mutation-proven), giving the DEFAULT offline compactor real protection.
func cascadeRecentTaskConversation() *session.Conversation {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("original setup"))
	// The live task sits at the OLDEST middle position (index 2), so tier-1 snip
	// (which keeps the newer half of the middle) drops it unless the back-snap saves it.
	conv.Append(session.NewUserMessage("ACTUAL TASK: rename Foo to Bar"))
	// A long trailing run of assistant/tool pairs: pushes the snip midpoint far past
	// the task AND keeps the count-tail (keep=6) entirely non-user.
	for i := 0; i < 10; i++ {
		id := session.ToolCallID("t" + string(rune('a'+i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"g.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	return conv
}

// TestCascadeCompactorPreservesRecentUserTaskOutsideCountTail is the cascade parity
// of the core bug repro (offline: BudgetTokens 0 runs every deterministic tier once,
// no LLM). The recent user task is in the OLDER half of the middle, so tier-1 snip
// drops it and ONLY the back-snap saves it into the preserved tail.
func TestCascadeCompactorPreservesRecentUserTaskOutsideCountTail(t *testing.T) {
	conv := cascadeRecentTaskConversation()
	compacted, _, err := agent.CascadeCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertRecentTaskAndPinSurvive(t, compacted)
}

// pairBulk appends n assistant-call/tool-result pairs with the given id prefix.
func pairBulk(conv *session.Conversation, prefix string, n int) {
	for i := 0; i < n; i++ {
		id := session.ToolCallID(prefix + string(rune('a'+i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
}

// assertGuarantee checks the GUARANTEED-survival contract: FIRST + RECENT survive
// verbatim, and MIDDLE does NOT (the honest best-effort half — a middle instruction
// beyond the back-snap's reach is summarised/dropped, never preserved verbatim).
func assertGuarantee(t *testing.T, compacted []session.Message) {
	t.Helper()
	if err := session.ValidateToolPairing(compacted); err != nil {
		t.Fatalf("pairing: %v", err)
	}
	var sawFirst, sawRecent, sawMiddle bool
	for _, m := range compacted {
		if m.Role != session.RoleUser {
			continue
		}
		switch m.Text {
		case "FIRST instruction":
			sawFirst = true
		case "RECENT instruction":
			sawRecent = true
		case "MIDDLE instruction":
			sawMiddle = true
		}
	}
	if !sawFirst {
		t.Fatalf("first user turn not preserved")
	}
	if !sawRecent {
		t.Fatalf("most-recent user turn not preserved")
	}
	// NEGATIVE half: the middle instruction is beyond the back-snap lookback, so it
	// must NOT survive as a verbatim RoleUser message — pins the honest best-effort
	// contract and guards against a future over-broad snap pulling it in.
	if sawMiddle {
		t.Fatalf("middle user instruction survived verbatim; the contract is best-effort, not verbatim, for superseded context")
	}
}

// TestHeuristicCompactorGuaranteesFirstAndRecentUserTurns: FIRST (pin) + RECENT
// (back-snap) survive, MIDDLE (beyond lookback) does not. RECENT is outside the
// role-blind count-tail (the trailing pairs are all non-user), so only the back-snap
// preserves it. MIDDLE sits before a > maxUserSnapLookback run, so the bounded
// back-snap cannot reach it.
func TestHeuristicCompactorGuaranteesFirstAndRecentUserTurns(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("FIRST instruction")) // pin
	conv.Append(session.NewUserMessage("MIDDLE instruction"))
	pairBulk(conv, "m", 16) // 32 msgs > maxUserSnapLookback(24): MIDDLE unreachable
	conv.Append(session.NewUserMessage("RECENT instruction"))
	pairBulk(conv, "r", 4) // 8 trailing non-user msgs: RECENT is outside the count-tail
	c, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertGuarantee(t, c)
}

// TestCascadeCompactorGuaranteesFirstAndRecentUserTurns is the cascade parity, with
// the geometry the no-LLM cascade needs to be NON-vacuous: RECENT is placed so tier-1
// snip (which keeps the newer half of the middle) DROPS it, yet it is still within
// maxUserSnapLookback of the cut so the back-snap reaches it. MIDDLE is beyond the
// lookback. Reverting the back-snap drops RECENT → red.
func TestCascadeCompactorGuaranteesFirstAndRecentUserTurns(t *testing.T) {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("FIRST instruction"))  // pin (head)
	conv.Append(session.NewUserMessage("MIDDLE instruction")) // oldest middle: snipped, beyond lookback
	pairBulk(conv, "a", 6)                                    // 12 msgs
	conv.Append(session.NewUserMessage("RECENT instruction")) // in the OLDER half of the middle
	pairBulk(conv, "b", 11)                                   // 22 trailing msgs
	// Layout: sys(0) FIRST(1) MIDDLE(2) [12 pairs idx3..14] RECENT(15) [22 pairs idx16..37]
	// len=38, keep=6 → count-cut=32. middle=idx2..31 (M=30 msgs); snip drops the older
	// HALF (~15) so RECENT (middle-index 13 < 15) is DROPPED by snip — without the
	// back-snap it does NOT survive. RECENT's distance to the cut is 32-15=17 < 24, so
	// the back-snap reaches it and snaps the tail to it. MIDDLE(2) is 30 back, beyond
	// maxUserSnapLookback(24), so it stays dropped (the negative half).
	c, _, err := agent.CascadeCompactor{}.Compact(context.Background(), conv)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertGuarantee(t, c)
}

// TestCompactorBackSnapBounded is TWO-SIDED so neither a snap-removal NOR a
// bound-removal mutation can pass it:
//   - a RECENT user turn WITHIN maxUserSnapLookback of the cut MUST survive verbatim
//     (the snap is active — removing it drops RECENT → red);
//   - an ANCIENT user turn BEYOND maxUserSnapLookback MUST NOT survive verbatim (the
//     bound clamps the walk — removing the bound would drag ANCIENT in → red).
//
// The conversation has exactly two user turns after the pin: ANCIENT (far back) and
// RECENT (near the cut), separated by a > lookback non-user run.
func TestCompactorBackSnapBounded(t *testing.T) {
	const (
		ancientText = "ANCIENT instruction"
		recentText  = "RECENT instruction"
	)
	// Layout (heuristic count-tail reasoning; cascade is parity):
	// sys(0) pin(1) ANCIENT(2) [16 pairs idx3..34] RECENT(35) [4 pairs idx36..43]
	// len=44, keep=6 → count-cut=38. Back-snap from idx37: finds RECENT(35) within
	// lookback(24) and snaps to it; ANCIENT(2) is ~35 back, beyond the bound, so the
	// walk gives up before reaching it. RECENT survives, ANCIENT does not.
	build := func() *session.Conversation {
		conv := &session.Conversation{}
		conv.Append(session.NewSystemMessage("system rules"))
		conv.Append(session.NewUserMessage("the goal")) // pin
		conv.Append(session.NewUserMessage(ancientText))
		pairBulk(conv, "x", 16) // 32 msgs > maxUserSnapLookback(24): ANCIENT unreachable
		conv.Append(session.NewUserMessage(recentText))
		pairBulk(conv, "r", 4) // 8 trailing non-user msgs: RECENT within lookback, outside count-tail
		return conv
	}

	verbatim := func(out []session.Message, text string) bool {
		for _, m := range out {
			if m.Role == session.RoleUser && m.Text == text {
				return true
			}
		}
		return false
	}

	check := func(t *testing.T, in, out []session.Message, checkSnapActive bool) {
		t.Helper()
		if err := session.ValidateToolPairing(out); err != nil {
			t.Fatalf("pairing: %v", err)
		}
		// The bound side applies to BOTH compactors: ANCIENT is beyond the lookback,
		// so the back-snap must NOT pull it in (a bound-removal mutation fails here).
		if verbatim(out, ancientText) {
			t.Fatalf("bound not enforced: the ANCIENT user turn (beyond maxUserSnapLookback) was dragged into the verbatim tail")
		}
		// The snap-active side is asserted only where it is NON-vacuous. For the
		// heuristic the role-blind count-tail is all non-user, so ONLY the back-snap can
		// preserve RECENT (a snap-removal mutation fails here). For the no-LLM cascade,
		// tier-1 snip happens to keep RECENT (it lands in the newer half of the middle),
		// so asserting it here would be vacuous — the cascade snap-active proof lives in
		// TestCascadeCompactorPreservesRecentUserTaskOutsideCountTail /
		// TestCascadeCompactorGuaranteesFirstAndRecentUserTurns, which place the turn in
		// the OLDER half so only the back-snap saves it.
		if checkSnapActive && !verbatim(out, recentText) {
			t.Fatalf("snap inactive: the RECENT user turn (within lookback) was not pulled into the verbatim tail")
		}
		// Compaction still shrank the history materially (the bound did not defeat it).
		if len(out) >= len(in) {
			t.Fatalf("compaction did not shrink history: input=%d output=%d", len(in), len(out))
		}
	}

	t.Run("heuristic", func(t *testing.T) {
		conv := build()
		in := append([]session.Message(nil), conv.Messages...)
		out, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), conv)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}
		check(t, in, out, true)
	})
	t.Run("cascade", func(t *testing.T) {
		conv := build()
		in := append([]session.Message(nil), conv.Messages...)
		out, _, err := agent.CascadeCompactor{}.Compact(context.Background(), conv)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}
		check(t, in, out, false)
	})
}

// TestCompactorRecentUserAdjacentToToolPair exercises the back-snap × forward-snap
// interaction: a recent user instruction sits OUTSIDE the role-blind count-tail with
// a tool-call/result pair between it and the cut, so the back-snap must reach BACK
// across that pair to anchor the tail on the user turn — and the forward-snap (which
// stays LAST) must then NOT re-orphan the pulled-in pair. Asserts the user text
// survives verbatim AND pairing holds. The heuristic count-tail (keep=6) is all
// non-user, so this is a genuine back-snap test (reverting the snap drops the turn).
func TestCompactorRecentUserAdjacentToToolPair(t *testing.T) {
	build := func() *session.Conversation {
		conv := &session.Conversation{}
		conv.Append(session.NewSystemMessage("system rules"))
		conv.Append(session.NewUserMessage("the goal"))
		// Older settled pairs (head/middle bulk).
		pairBulk(conv, "a", 3)
		// The recent user instruction, FOLLOWED by a tool-call/result pair, then more
		// trailing non-user pairs so the user turn falls OUTSIDE the keep=6 count-tail.
		conv.Append(session.NewUserMessage("DO THE THING now"))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall("z", "Read", json.RawMessage(`{"path":"z.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult("z", "body")))
		pairBulk(conv, "b", 3) // trailing non-user: pushes the user turn out of the count-tail
		conv.Append(session.NewAssistantMessage("ok", "", nil))
		return conv
	}

	check := func(t *testing.T, out []session.Message) {
		t.Helper()
		if err := session.ValidateToolPairing(out); err != nil {
			t.Fatalf("pairing: %v", err)
		}
		var saw bool
		for _, m := range out {
			if m.Role == session.RoleUser && m.Text == "DO THE THING now" {
				saw = true
			}
		}
		if !saw {
			t.Fatalf("recent user instruction outside the count-tail was not back-snapped into the verbatim tail")
		}
	}

	t.Run("heuristic", func(t *testing.T) {
		out, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), build())
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}
		check(t, out)
	})
	t.Run("cascade", func(t *testing.T) {
		out, _, err := agent.CascadeCompactor{}.Compact(context.Background(), build())
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}
		check(t, out)
	})
}

// hasSummaryInTail reports whether any synthesised-summary message (paths-summary OR
// tier-4) appears AFTER the last genuine non-summary content in out — i.e. whether a
// prior compaction's summary was dragged into the verbatim tail. We approximate "the
// tail" as "everything from the last summary message onward must be small": if a
// summary is followed by a large run of preserved turns, the back-snap anchored on it.
func countSummaryMessages(out []session.Message) int {
	n := 0
	for _, m := range out {
		if m.Role == session.RoleUser &&
			(strings.HasPrefix(m.Text, "[conversation compacted]") ||
				strings.HasPrefix(m.Text, "[earlier turns summarised]")) {
			n++
		}
	}
	return n
}

// TestCompactorReCompactionDoesNotAnchorOnPriorSummary is the re-compaction footgun
// guard (the isRecentUserTurn marker-skip). Compact once; append more assistant/tool
// turns with NO new user turn; compact again. The ONLY user-ish message between the
// first-user pin and the end is the prior compaction's synthesised summary — so if the
// back-snap treated it as a recent user turn it would anchor the verbatim tail on it
// and pull the entire post-summary history in, defeating the second compaction. The
// marker-skip prevents that.
//
// Mutation-proof: dropping the marker check in isRecentUserTurn (so the summary counts
// as a user turn) makes the second compaction NOT shrink — the (a) assertion fails.
func TestCompactorReCompactionDoesNotAnchorOnPriorSummary(t *testing.T) {
	// firstConv has exactly ONE user turn (the pin) and a SHORT tool-heavy run, so the
	// compacted output's only user-ish message (besides the pin) is the synthesised
	// summary, AND that summary stays close to the front (so on the second pass it sits
	// WITHIN maxUserSnapLookback of the cut — otherwise the lookback bound, not the
	// marker-skip, would be what prevents the false-anchor and the test would be
	// vacuous w.r.t. the marker-skip).
	firstConv := func() *session.Conversation {
		conv := &session.Conversation{}
		conv.Append(session.NewSystemMessage("system rules"))
		conv.Append(session.NewUserMessage("the original goal"))
		pairBulk(conv, "a", 7)
		return conv
	}
	// appendMore returns a fresh conversation seeded from `out` plus a SMALL run of new
	// pairs (no new user turn), the shape a SECOND compaction sees. Kept small so the
	// prior summary is within maxUserSnapLookback of the new cut — the back-snap WOULD
	// reach it (and anchor on it) if the marker-skip didn't exclude it.
	appendMore := func(out []session.Message) *session.Conversation {
		conv := &session.Conversation{}
		for _, m := range out {
			conv.Append(m)
		}
		pairBulk(conv, "b", 6)
		return conv
	}

	assertSecondShrinks := func(t *testing.T, secondIn, secondOut []session.Message) {
		t.Helper()
		if err := session.ValidateToolPairing(secondOut); err != nil {
			t.Fatalf("pairing: %v", err)
		}
		if len(secondOut) >= len(secondIn) {
			t.Fatalf("second compaction did not shrink (back-snap anchored on the prior summary): in=%d out=%d",
				len(secondIn), len(secondOut))
		}
		// The prior summary must not have spawned a SECOND verbatim-tail copy of the
		// post-summary bulk: at most one synthesised summary per compactor pass.
		if n := countSummaryMessages(secondOut); n == 0 {
			t.Fatalf("second compaction emitted no summary message at all: %+v", secondOut)
		}
	}

	t.Run("heuristic", func(t *testing.T) {
		first, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), firstConv())
		if err != nil {
			t.Fatalf("Compact #1: %v", err)
		}
		if countSummaryMessages(first) == 0 {
			t.Fatalf("first compaction produced no synthesised summary; the test needs one")
		}
		secondConv := appendMore(first)
		secondIn := append([]session.Message(nil), secondConv.Messages...)
		second, _, err := agent.HeuristicCompactor{}.Compact(context.Background(), secondConv)
		if err != nil {
			t.Fatalf("Compact #2: %v", err)
		}
		assertSecondShrinks(t, secondIn, second)
	})

	t.Run("cascade", func(t *testing.T) {
		first, _, err := agent.CascadeCompactor{}.Compact(context.Background(), firstConv())
		if err != nil {
			t.Fatalf("Compact #1: %v", err)
		}
		secondConv := appendMore(first)
		secondIn := append([]session.Message(nil), secondConv.Messages...)
		second, _, err := agent.CascadeCompactor{}.Compact(context.Background(), secondConv)
		if err != nil {
			t.Fatalf("Compact #2: %v", err)
		}
		assertSecondShrinks(t, secondIn, second)
	})

	t.Run("cascade tier-4", func(t *testing.T) {
		// Tier-4 variant: the prior summary is the tier-4 LLM summary
		// ("[earlier turns summarised]"), not the paths-summary. forceTier4 budget 1
		// makes tier 4 fire on both passes. Reverting the tier4SummaryMarker coverage
		// (so the tier-4 summary is NOT recognised) lets the second back-snap anchor on
		// the prior tier-4 summary → no shrink.
		mk := func() port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("## Goal\nthe original goal\n\n## Next steps\nNone."))
		}
		first, _, err := forceTier4(mk()).Compact(context.Background(), firstConv())
		if err != nil {
			t.Fatalf("Compact #1: %v", err)
		}
		// Confirm the first pass actually produced a tier-4 summary message.
		var sawTier4 bool
		for _, m := range first {
			if strings.HasPrefix(m.Text, "[earlier turns summarised]") {
				sawTier4 = true
			}
		}
		if !sawTier4 {
			t.Fatalf("first tier-4 pass produced no '[earlier turns summarised]' message: %+v", first)
		}
		secondConv := appendMore(first)
		secondIn := append([]session.Message(nil), secondConv.Messages...)
		second, _, err := forceTier4(mk()).Compact(context.Background(), secondConv)
		if err != nil {
			t.Fatalf("Compact #2: %v", err)
		}
		assertSecondShrinks(t, secondIn, second)
	})
}

// danglingTailConversation builds a history whose preserved tail ENDS on an
// assistant tool call with no following result (a dangling call). Both compactors
// preserve the tail verbatim, so the assembled slice inherits the dangling call —
// the post-assembly validator must trip and force abort-to-original. (The cut is
// well past the dangle so the head/middle split cannot accidentally drop it.)
func danglingTailConversation() *session.Conversation {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("system rules"))
	conv.Append(session.NewUserMessage("goal"))
	// A run of clean pairs to give the head/middle something to summarise.
	for i := 0; i < 3; i++ {
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, "body")))
	}
	// The final message dangles: an assistant tool call with NO following result.
	conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
		session.NewToolCall("dangle", "Read", json.RawMessage(`{"path":"g.go"}`)),
	}))
	return conv
}

// assertAbortedToOriginal checks the compactor returned the sentinel AND the
// original conv.Messages unchanged (the fallback contract).
func assertAbortedToOriginal(t *testing.T, conv *session.Conversation, out []session.Message, err error) {
	t.Helper()
	if !errors.Is(err, agent.ErrCompactionWouldOrphan) {
		t.Fatalf("err = %v, want ErrCompactionWouldOrphan", err)
	}
	if out == nil {
		t.Fatalf("aborted compaction returned a nil slice, want the original history")
	}
	if !reflect.DeepEqual(out, conv.Messages) {
		t.Fatalf("aborted compaction did not return the original history:\n got %+v\nwant %+v", out, conv.Messages)
	}
}

// TestHeuristicCompactorAbortsToOriginalOnOrphan checks the heuristic compactor
// aborts to the ORIGINAL history (returning ErrCompactionWouldOrphan) when its
// only possible output would be tool-pairing-invalid (a dangling tail call).
func TestHeuristicCompactorAbortsToOriginalOnOrphan(t *testing.T) {
	conv := danglingTailConversation()
	// KeepLastTurns 1 preserves only the dangling assistant call as the tail; the
	// summary head carries the matched pairs, so the assembled slice dangles.
	out, _, err := agent.HeuristicCompactor{KeepLastTurns: 1}.Compact(context.Background(), conv)
	assertAbortedToOriginal(t, conv, out, err)
}

// TestCascadeCompactorAbortsToOriginalOnOrphan is the cascade parity: a dangling
// tail call forces the cascade's finish self-validation to abort to original.
func TestCascadeCompactorAbortsToOriginalOnOrphan(t *testing.T) {
	conv := danglingTailConversation()
	out, _, err := agent.CascadeCompactor{KeepLastTurns: 1}.Compact(context.Background(), conv)
	assertAbortedToOriginal(t, conv, out, err)
}

// TestCompactionThroughLoopNeverOrphans is the end-to-end regression: it drives
// the REAL agent loop with the REAL HeuristicCompactor and a mockllm script that
// emits genuine tool calls, so the conversation accumulates assistant-call /
// tool-result pairs. A tiny ContextWindowTokens makes maybeCompact fire mid-run.
// The bug was: the count-based tail cut landed on a tool result whose call was
// dropped → orphaned replay → provider HTTP 400 → StateFailed (permanently
// bricked). With the fix the run completes, the final history is tool-pairing
// valid, and the session is NOT failed. This assertion FAILS if the bug regresses
// (ReplaceHistory now rejects an unpaired slice; absent the snap the compactor
// would have produced one and the assertion below would trip).
func TestCompactionThroughLoopNeverOrphans(t *testing.T) {
	mk := func(id string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Read", json.RawMessage(`{"path":"f.go"}`))
	}
	// Several tool-call turns build up paired history, then a final text turn ends
	// the run. maybeCompact runs at the start of each turn, so by the later turns
	// the accumulated pairs trip the threshold.
	llm := mockllm.New(
		mockllm.ToolCallTurn(mk("c1")),
		mockllm.ToolCallTurn(mk("c2")),
		mockllm.ToolCallTurn(mk("c3")),
		mockllm.ToolCallTurn(mk("c4")),
		mockllm.ToolCallTurn(mk("c5")),
		mockllm.TextTurn("done"),
	)

	// A capturing diag distinguishes a GENUINE compaction (the snap kept the
	// history paired, so ReplaceHistory accepted it) from abort-to-original (the
	// safety net firing the "continuing without compaction" WARN). Without this
	// assertion the test passes even with snapCutToTurnBoundary reverted, because
	// abort-to-original keeps the session healthy — proving only the net, not the
	// snap.
	//
	// KeepLastTurns is ODD (3) so the count cut (len-3) lands on a TOOL RESULT in
	// the alternating [prompt, A1,T1, A2,T2, ...] history the loop builds (tool
	// results sit at even indices ≥2; len-3 is even when len is odd, which it is
	// after each tool turn). Reverting the snap therefore makes the real compactor
	// emit an orphan → ReplaceHistory rejects → the WARN below fires → red.
	diag := newCapturingDiag()
	e := agent.NewEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t, readBodyTool()),
		Policy:          allowAll(),
		Model:           "m",
		Compactor:       agent.HeuristicCompactor{KeepLastTurns: 3},
		ContextWindow:   func() int { return 200 }, // threshold 160 chars/4: trips after ~2 big pairs
		CompactionRatio: 0.8,
		Diagnostics:     diag,
	})

	sess := session.New("s-compact", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate the files"})

	var sawCompaction bool
	for ev := range r.Events() {
		if ev.Type == session.EvCompaction {
			sawCompaction = true
		}
	}

	// The run must NOT have failed (a 400-from-orphan would land it in StateFailed).
	if sess.State == session.StateFailed {
		reason, _ := sess.StopReason()
		t.Fatalf("session reached StateFailed: %v", reason)
	}
	// The replayed history must be tool-pairing valid throughout.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("final conversation has unpaired tools: %v", err)
	}
	// Compaction GENUINELY succeeded — the snap kept the slice paired, so the loop
	// never fell back to the degraded "continuing without compaction" branch. This
	// is the assertion that distinguishes a real compaction from the safety net:
	// revert snapCutToTurnBoundary and the real compactor emits an unpaired slice,
	// the loop aborts to original, this WARN fires (and EvCompaction is skipped) —
	// turning BOTH checks below red. (Self-checked: reverting the snap fails here.)
	if rec, ok := findMsg(diag.snapshot(), "continuing without compaction"); ok {
		t.Fatalf("compaction aborted to original (degraded), want genuine compaction: %q", rec.msg)
	}
	if !sawCompaction {
		t.Fatalf("compaction never triggered; the test does not exercise the bug")
	}
}

// TestCarryoverSeededCompactsOnTurn0 proves the issue-#20 carryover path is safe
// when the SEEDED history already exceeds the compaction window: a fresh idle
// session is seeded (SeedHistory — the exact seam the service's seedCarryover uses
// on a session.ForkSnapshot) with a conversation large enough to trip the
// threshold, then drives ONE turn through the REAL engine loop (mockllm + memfs,
// the compaction harness). The loop's maybeCompact must fire on turn 0 over the
// seeded history and the turn must complete cleanly. Asserts the three carryover
// compaction invariants: (a) the run completes (no provider orphan-400 → no
// StateFailed), (b) the most-recent GENUINE user instruction survives VERBATIM
// (snapCutToRecentUserTurn), and (c) the resulting history stays tool-pairing-valid
// (session.ValidateToolPairing) — no orphaned tool result crosses into the replay.
// MUTATION-VERIFY: reverting snapCutToRecentUserTurn (the recent user task falls
// into the summarised head) turns sawTask red; a compaction that emits an orphan
// turns ValidateToolPairing red; a dangling seeded call (orphan) trips the
// SeedHistory pairing guard itself.
func TestCarryoverSeededCompactsOnTurn0(t *testing.T) {
	// Build a carryover-style source conversation: a genuine user instruction, then
	// many large assistant/tool pairs so the accumulated history crosses the
	// compaction threshold (ContextWindow 200, ratio 0.8 → threshold ~160 chars/4).
	const recentTask = "ACTUAL TASK: rename Foo to Bar"
	conv := &session.Conversation{}
	conv.Append(session.NewUserMessage("original setup"))
	for i := 0; i < 40; i++ {
		if i == 35 {
			conv.Append(session.NewUserMessage(recentTask))
		}
		id := session.ToolCallID(string(rune('a' + i)))
		conv.Append(session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall(id, "Read", json.RawMessage(`{"path":"f.go"}`)),
		}))
		conv.Append(session.NewToolMessage(session.NewToolResult(id, strings.Repeat("data ", 50))))
	}
	// Mirror the service carryover: ForkSnapshot (deep copy, strips trailing
	// orphans — here none) then SeedHistory into a FRESH idle session. SeedHistory
	// re-validates pairing, so the seeded history is provider-replayable.
	snap := session.ForkSnapshot(conv)
	sess := session.New("s-carry-compact", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := sess.SeedHistory(snap); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	if len(sess.Conversation.Messages) == 0 {
		t.Fatalf("seeded history is empty — the test does not exercise carryover compaction")
	}

	llm := mockllm.New(mockllm.TextTurn("done"))
	e := agent.NewEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t, readBodyTool()),
		Policy:          allowAll(),
		Model:           "m",
		Compactor:       agent.HeuristicCompactor{KeepLastTurns: 3},
		ContextWindow:   func() int { return 200 }, // threshold ~160: the seeded history trips it on turn 0
		CompactionRatio: 0.8,
	})

	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "continue the task"})
	var sawCompaction bool
	for ev := range r.Events() {
		if ev.Type == session.EvCompaction {
			sawCompaction = true
		}
	}

	// (a) The run completes WITHOUT a provider orphan-400 (which would land the
	// session in StateFailed) — the seeded over-window history compacted on turn 0.
	if sess.State == session.StateFailed {
		reason, _ := sess.StopReason()
		t.Fatalf("session reached StateFailed (orphan-400 on seeded replay): %v", reason)
	}
	if !sawCompaction {
		t.Fatalf("compaction never triggered on the seeded over-window history; the test does not exercise turn-0 compaction")
	}
	// (c) The compacted history is tool-pairing valid — no orphaned tool result.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("post-compaction history has unpaired tools: %v", err)
	}
	// (b) The most-recent genuine user instruction survives VERBATIM (the
	// snapCutToRecentUserTurn invariant — it must NOT fall into the summarised head).
	var sawTask bool
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == recentTask {
			sawTask = true
		}
	}
	if !sawTask {
		t.Fatalf("most-recent genuine user instruction did not survive turn-0 compaction verbatim: %+v", sess.Conversation.Messages)
	}
}

// TestContextWindowResolvedAtUse is the STRUCTURAL guard the whole resolve-at-use
// unification rests on: Deps.ContextWindow is a closure resolved at the point of use,
// NOT a value frozen at construction. An Engine built over a MUTABLE int must observe
// a post-construction change through Engine.ContextWindow() — proving a live-catalog
// Swap after the (shared, never-rebuilt) engine is constructed self-corrects on the
// next read.
//
// MUTATION-VERIFY: revert Deps.ContextWindow to a frozen int (resolved once in
// engineDepsForProvider / read once in NewEngine), and the post-mutation read below
// returns the stale construction-time value — this test fails.
func TestContextWindowResolvedAtUse(t *testing.T) {
	window := 128_000
	e := agent.NewEngine(agent.Deps{
		LLM:           mockllm.New(),
		Catalog:       catalogWith(t, readBodyTool()),
		Policy:        allowAll(),
		Model:         "m",
		ContextWindow: func() int { return window },
	})
	if got := e.ContextWindow(); got != 128_000 {
		t.Fatalf("pre-change ContextWindow() = %d, want 128000", got)
	}
	// Simulate the live model-catalog swap populating the model's real window AFTER
	// the engine was constructed.
	window = 1_050_000
	if got := e.ContextWindow(); got != 1_050_000 {
		t.Fatalf("post-change ContextWindow() = %d, want the live 1050000 (resolve-at-use — the engine reads the closure on every call)", got)
	}
}

// TestNilContextWindowDisablesCompaction pins the "nil resolver disables compaction"
// contract (the old "zero disables"): an Engine with a nil Deps.ContextWindow reports
// 0 and never compacts, even with a tiny history that would otherwise trip a small
// window.
func TestNilContextWindowDisablesCompaction(t *testing.T) {
	e := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: catalogWith(t, readBodyTool()),
		Policy:  allowAll(),
		Model:   "m",
		// ContextWindow nil → compaction disabled.
	})
	if got := e.ContextWindow(); got != 0 {
		t.Fatalf("nil-resolver ContextWindow() = %d, want 0 (disabled)", got)
	}
	sess := session.New("s-nilwin", session.ModeDefault, "/ws", session.Limits{MaxTurns: 2}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
	for ev := range r.Events() {
		if ev.Type == session.EvCompaction {
			t.Fatalf("compaction fired with a nil ContextWindow resolver (must be disabled)")
		}
	}
}

// TestCompactionEmitsNonDestructiveArchive is the cloud-native Phase 3b
// compaction-archive sub-gate at the loop level: a genuine compaction must emit
// EvCompactionArchive AFTER EvCompaction carrying the PRE-compaction conversation
// (the span ReplaceHistory dropped) — captured BEFORE the mutation. The archive
// must contain a tool call that compaction dropped from the live history, proving
// the capture is the pre-mutation slice and not the rewritten tail.
//
// MUTATION-KILL: move `archived := sess.Conversation.Messages` to AFTER
// ReplaceHistory in maybeCompact (so it reads the mutated, compacted conversation)
// and the archive no longer contains the dropped early tool call — the assertion
// that a dropped call is recoverable from the archive (but absent from the live
// history) then fails.
func TestCompactionEmitsNonDestructiveArchive(t *testing.T) {
	mk := func(id string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Read", json.RawMessage(`{"path":"f.go"}`))
	}
	// Build genuine paired history over several tool turns, then a final text turn.
	llm := mockllm.New(
		mockllm.ToolCallTurn(mk("c1")),
		mockllm.ToolCallTurn(mk("c2")),
		mockllm.ToolCallTurn(mk("c3")),
		mockllm.TextTurn("done"),
	)
	// A compactor that records the EXACT slice it was handed (the pre-compaction
	// history) and returns a TINY, tool-pairing-valid output — so compaction fires
	// EXACTLY ONCE (the compacted history is far under threshold, so no later turn
	// re-trips it), making the pre-vs-post distinction deterministic.
	rec := &recordingInputCompactor{out: []session.Message{session.NewUserMessage("goal")}}
	e := agent.NewEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t, readBodyTool()),
		Policy:          allowAll(),
		Model:           "m",
		Compactor:       rec,
		ContextWindow:   func() int { return 200 },
		CompactionRatio: 0.8,
		PromptBuilder:   func(prompt.Config) prompt.Layered { return prompt.Layered{} },
	})

	sess := session.New("s-archive", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "investigate the files"})

	compactions, archives := 0, 0
	lastWasCompaction := false
	var archived []session.Message
	for ev := range r.Events() {
		switch ev.Type {
		case session.EvCompaction:
			compactions++
			lastWasCompaction = true
			continue
		case session.EvCompactionArchive:
			archives++
			if !lastWasCompaction {
				t.Fatalf("EvCompactionArchive #%d did not immediately follow an EvCompaction notice", archives)
			}
			if ev.CompactionArchive == nil {
				t.Fatalf("EvCompactionArchive carries no payload")
			}
			archived = ev.CompactionArchive.Replaced
		}
		lastWasCompaction = false
	}

	if sess.State == session.StateFailed {
		reason, _ := sess.StopReason()
		t.Fatalf("session reached StateFailed: %v", reason)
	}
	if compactions != 1 {
		t.Fatalf("expected exactly one compaction, got %d (the test needs a single deterministic compaction)", compactions)
	}
	if archives != 1 {
		t.Fatalf("expected exactly one EvCompactionArchive, got %d", archives)
	}
	if len(archived) == 0 {
		t.Fatalf("archive is empty: the pre-compaction span was not captured")
	}

	// The archive MUST equal the slice the compactor was handed — i.e. the genuine
	// pre-compaction history captured BEFORE ReplaceHistory ran. A post-mutation
	// capture would instead equal the compacted output (rec.out, the tiny tail).
	if !reflect.DeepEqual(archived, rec.input) {
		t.Fatalf("archive is not the pre-compaction slice the compactor saw:\n archive=%v\n input=%v", archived, rec.input)
	}
	// And it must hold the early tool calls that compaction DROPPED from the live
	// history (c1/c2/c3 are gone from the compacted [goal] history) — the
	// non-destructive recovery the gate proves.
	archivedCalls := callIDsIn(archived)
	liveCalls := callIDsIn(sess.Conversation.Messages)
	recoveredDropped := false
	for id := range archivedCalls {
		if _, stillLive := liveCalls[id]; !stillLive {
			recoveredDropped = true
			break
		}
	}
	if !recoveredDropped {
		t.Fatalf("archive holds no tool call that compaction dropped from the live history; "+
			"archive ids=%v live ids=%v (capture must be the PRE-mutation slice)", keysOf(archivedCalls), keysOf(liveCalls))
	}
}

// recordingInputCompactor records the EXACT message slice it was handed (the
// pre-compaction history) and returns a fixed, tool-pairing-valid output. The
// recorded input is the oracle the archive must equal.
type recordingInputCompactor struct {
	input []session.Message
	out   []session.Message
}

func (c *recordingInputCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, error) {
	// Snapshot the input slice (the conversation the loop captures for the archive).
	c.input = append([]session.Message(nil), conv.Messages...)
	return c.out, "compacted summary", nil
}

// callIDsIn collects the set of assistant tool-call ids across a message slice.
func callIDsIn(msgs []session.Message) map[session.ToolCallID]struct{} {
	out := make(map[session.ToolCallID]struct{})
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			out[c.ID] = struct{}{}
		}
	}
	return out
}

// keysOf projects a tool-call-id set to a slice for a failure message.
func keysOf(set map[session.ToolCallID]struct{}) []session.ToolCallID {
	out := make([]session.ToolCallID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// fakeCompactor returns a fixed slice/error from Compact, for driving the loop's
// defensive paths deterministically.
type fakeCompactor struct {
	out []session.Message
	err error
}

func (f fakeCompactor) Compact(_ context.Context, _ *session.Conversation) ([]session.Message, string, error) {
	return f.out, "fake", f.err
}

// TestCompactionThroughLoopAbortsToOriginal drives the loop with a FAKE compactor
// (not the real HeuristicCompactor) in two variants, each exercising a distinct
// abort path, and asserts the run survives uncompacted: NO EvCompaction event,
// session NOT failed, and the conversation unchanged from the pre-compaction
// snapshot. This mirrors the live repro (compact-attempt → abort → survive
// instead of brick) and is the only end-to-end coverage of the loop's defensive
// branches.
func TestCompactionThroughLoopAbortsToOriginal(t *testing.T) {
	// An unpaired slice the loop must refuse: a tool result with no preceding call.
	unpaired := []session.Message{
		session.NewUserMessage("goal"),
		session.NewToolMessage(session.NewToolResult("ghost", "result")),
	}

	variants := []struct {
		name      string
		compactor agent.Compactor
	}{
		{
			// (a) NIL error + unpaired slice: exercises maybeCompact's defensive
			// ValidateToolPairing branch between Compact and ReplaceHistory.
			name:      "nil error unpaired slice",
			compactor: fakeCompactor{out: unpaired, err: nil},
		},
		{
			// (b) ErrCompactionWouldOrphan sentinel: exercises the sentinel branch
			// (reusing the Compact-error WARN).
			name:      "ErrCompactionWouldOrphan sentinel",
			compactor: fakeCompactor{out: nil, err: agent.ErrCompactionWouldOrphan},
		},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			llm := mockllm.New(mockllm.TextTurn("done"))
			e := agent.NewEngine(agent.Deps{
				LLM:             llm,
				Catalog:         catalogWith(t),
				Policy:          allowAll(),
				Model:           "m",
				Compactor:       v.compactor,
				ContextWindow:   func() int { return 10 }, // tiny: the prompt trips the threshold
				CompactionRatio: 0.8,
			})

			sess := session.New("s-abort", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
			bigPrompt := strings.Repeat("word ", 200)
			r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: bigPrompt})

			var sawCompaction, sawArchive bool
			for ev := range r.Events() {
				if ev.Type == session.EvCompaction {
					sawCompaction = true
				}
				if ev.Type == session.EvCompactionArchive {
					sawArchive = true
				}
			}

			// The history the model saw must equal what the prompt produced — the
			// compactor's (bad) output must NEVER have been applied.
			if sawCompaction {
				t.Fatalf("EvCompaction emitted despite abort-to-original")
			}
			// The degrade-and-continue branch emits NO archive (no compaction happened,
			// so there is no replaced span to record).
			if sawArchive {
				t.Fatalf("EvCompactionArchive emitted despite abort-to-original")
			}
			if sess.State == session.StateFailed {
				reason, _ := sess.StopReason()
				t.Fatalf("session reached StateFailed instead of surviving: %v", reason)
			}
			if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
				t.Fatalf("conversation was corrupted by the rejected compaction: %v", err)
			}
			// The conversation must NOT contain the fake compactor's ghost result.
			for _, m := range sess.Conversation.Messages {
				if m.ToolResult != nil && m.ToolResult.CallID == "ghost" {
					t.Fatalf("rejected compaction output leaked into history")
				}
			}
		})
	}
}

// readBodyTool returns a read-only Read tool whose body is large enough to grow
// the history quickly, for the through-loop compaction test.
func readBodyTool() *fakeTool {
	return &fakeTool{
		name:     "Read",
		readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, strings.Repeat("data ", 50)), nil
		},
	}
}

// recordingCompactor records that it was invoked and returns a minimal history.
type recordingCompactor struct{ called int }

func (c *recordingCompactor) Compact(_ context.Context, _ *session.Conversation) ([]session.Message, string, error) {
	c.called++
	return []session.Message{session.NewUserMessage("x")}, "compacted summary", nil
}

type compactionAccountingTool struct{ spec tool.ToolSpec }

func (t compactionAccountingTool) Spec() tool.ToolSpec { return t.spec }
func (compactionAccountingTool) ReadOnly() bool        { return true }
func (compactionAccountingTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "unused"), nil
}

func TestCompactionAccountsForCompleteRequest(t *testing.T) {
	const filler = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	for _, component := range []string{"system", "fragments", "tool schema", "typed tool-result parts"} {
		t.Run(component, func(t *testing.T) {
			cat := tool.NewCatalog()
			sess := session.New("accounting", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
			var assembler *countingAssembler
			buildCalls := 0
			deps := agent.Deps{
				Catalog:         cat,
				Policy:          allowAll(),
				Model:           "m",
				TokenCounter:    agent.HeuristicTokenCounter{CharsPerToken: 1},
				ContextWindow:   func() int { return 100 },
				CompactionRatio: 0.8,
				PromptBuilder: func(prompt.Config) prompt.Layered {
					buildCalls++
					if component == "system" {
						return prompt.Layered{StablePrefix: filler}
					}
					return prompt.Layered{}
				},
			}
			switch component {
			case "fragments":
				assembler = &countingAssembler{msg: filler}
				deps.Instructions = assembler
			case "tool schema":
				err := cat.Register(compactionAccountingTool{spec: tool.ToolSpec{
					Name: "LargeSchema", Schema: json.RawMessage(`{"type":"object","description":"` + filler + `"}`),
				}})
				if err != nil {
					t.Fatalf("register tool: %v", err)
				}
			case "typed tool-result parts":
				call := session.NewToolCall("call", "T", json.RawMessage(`{}`))
				withParts := []session.Message{
					session.NewUserMessage("seed"),
					session.NewAssistantMessage("", "", []session.ToolCall{call}),
					session.NewToolMessage(session.NewToolResultWithParts(call.ID, "short", []session.Content{{
						BlockKind: session.BlockStructuredContent, Text: filler,
					}})),
				}
				if err := sess.SeedHistory(withParts); err != nil {
					t.Fatalf("seed history: %v", err)
				}
			}

			persistedOnly := append([]session.Message(nil), sess.Conversation.Messages...)
			if component == "typed tool-result parts" {
				result := *persistedOnly[2].ToolResult
				result.Parts = nil
				persistedOnly[2] = session.NewToolMessage(result)
			}
			persistedOnly = append(persistedOnly, session.NewUserMessage("hi"))
			if got := deps.TokenCounter.CountMessages(persistedOnly); got >= 80 {
				t.Fatalf("persisted history without the tested component = %d, want below threshold", got)
			}

			rc := &recordingCompactor{}
			deps.Compactor = rc
			var sent port.LLMRequest
			deps.LLM = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				sent = req
			})}, mockllm.TextTurn("done"))
			e := agent.NewEngine(deps)
			r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
			for range r.Events() {
			}
			if rc.called == 0 {
				t.Fatal("compactor was not invoked")
			}
			if buildCalls != 1 {
				t.Fatalf("system prompt built %d times, want once", buildCalls)
			}
			if assembler != nil && assembler.called != 1 {
				t.Fatalf("instruction fragments assembled %d times, want once", assembler.called)
			}
			if len(sent.Messages) == 0 || sent.Messages[len(sent.Messages)-1].Text != "x" {
				t.Fatalf("provider request did not use compacted history: %+v", sent.Messages)
			}
			switch component {
			case "system":
				if sent.System.StablePrefix != filler {
					t.Fatal("measured system prompt was not preserved in provider request")
				}
			case "fragments":
				if sent.Messages[0].Text != filler {
					t.Fatal("ephemeral fragment was not preserved ahead of compacted history")
				}
			case "tool schema":
				if len(sent.Tools) != 1 || !strings.Contains(string(sent.Tools[0].Schema), filler) {
					t.Fatalf("measured tool schema was not preserved in provider request: %+v", sent.Tools)
				}
			}
		})
	}
}

func TestAutomaticCompactionRejectsGrowingDefaultHeuristicCandidate(t *testing.T) {
	const irreducible = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	sess := session.New("short", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	e := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t), Policy: allowAll(), Model: "m",
		Compactor: agent.HeuristicCompactor{}, TokenCounter: agent.HeuristicTokenCounter{CharsPerToken: 1},
		ContextWindow: func() int { return 100 }, CompactionRatio: 0.8,
		PromptBuilder: func(prompt.Config) prompt.Layered { return prompt.Layered{StablePrefix: irreducible} },
	})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
	for ev := range r.Events() {
		if ev.Type == session.EvCompaction || ev.Type == session.EvCompactionArchive {
			t.Fatalf("growing candidate emitted %s", ev.Type)
		}
	}
	for _, message := range sess.Conversation.Messages {
		if strings.Contains(message.Text, session.CompactionSummaryMarker) {
			t.Fatalf("short history accumulated a summary: %q", message.Text)
		}
	}
}

func TestAutomaticCascadeUsesLiveCompleteRequestBudget(t *testing.T) {
	call := session.NewToolCall("call", "Read", json.RawMessage(`{"path":"large"}`))
	messages := []session.Message{session.NewUserMessage("goal")}
	for i := 0; i < 70; i++ {
		messages = append(messages, session.NewAssistantMessage(strings.Repeat("old response ", 10), "", nil))
	}
	messages = append(messages,
		session.NewAssistantMessage("", "", []session.ToolCall{call}),
		session.NewToolMessage(session.NewToolResult(call.ID, strings.Repeat("tool body ", 300))),
	)
	for i := 0; i < 6; i++ {
		messages = append(messages, session.NewAssistantMessage("recent", "", nil))
	}
	sess := session.New("cascade-budget", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := sess.SeedHistory(messages); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	var sent port.LLMRequest
	e := agent.NewEngine(agent.Deps{
		LLM:     mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { sent = req })}, mockllm.TextTurn("done")),
		Catalog: catalogWith(t), Policy: allowAll(), Model: "m",
		Compactor:    agent.CascadeCompactor{BudgetTokens: 128_000, Counter: agent.HeuristicTokenCounter{CharsPerToken: 1}},
		TokenCounter: agent.HeuristicTokenCounter{CharsPerToken: 1}, ContextWindow: func() int { return 500 }, CompactionRatio: 0.8,
	})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "continue"})
	for range r.Events() {
	}
	for _, message := range sent.Messages {
		if message.ToolResult != nil && len(message.ToolResult.Content) > 300 {
			t.Fatalf("cascade used configured 128k budget instead of request-local small-window target: %d-byte tool result", len(message.ToolResult.Content))
		}
	}
}

// TestCompactionTriggersAtThreshold drives the loop with a tiny context window so
// the threshold trips, and asserts the Compactor runs and a compaction Event is
// emitted.
func TestCompactionTriggersAtThreshold(t *testing.T) {
	rc := &recordingCompactor{}
	llm := mockllm.New(mockllm.TextTurn("done"))
	e := agent.NewEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t),
		Policy:          allowAll(),
		Model:           "m",
		Compactor:       rc,
		ContextWindow:   func() int { return 10 }, // tiny: a long prompt blows past 80% of it
		CompactionRatio: 0.8,
	})

	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	bigPrompt := strings.Repeat("word ", 200) // ~250 tokens >> threshold of 8
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: bigPrompt})

	var sawCompaction bool
	for ev := range r.Events() {
		if ev.Type == session.EvCompaction {
			sawCompaction = true
		}
	}
	if rc.called == 0 {
		t.Fatalf("compactor was not invoked")
	}
	if !sawCompaction {
		t.Fatalf("no compaction event emitted")
	}
}

// countingTokenCounter records invocations and reports a fixed huge count so the
// loop's compaction trigger always trips through the injected counter.
type countingTokenCounter struct {
	calls int
	fixed int
}

func (*countingTokenCounter) Count(string) int { return 0 }
func (c *countingTokenCounter) CountMessages(_ []session.Message) int {
	c.calls++
	return c.fixed
}

// TestCompactionTriggerUsesInjectedCounter checks the loop drives its compaction
// threshold through the injected TokenCounter (not a hard-coded estimate): a
// counter reporting over-threshold trips compaction; one reporting under does not.
func TestCompactionTriggerUsesInjectedCounter(t *testing.T) {
	run := func(reported int) (compacted bool, counterCalls int) {
		rc := &recordingCompactor{}
		tc := &countingTokenCounter{fixed: reported}
		llm := mockllm.New(mockllm.TextTurn("done"))
		e := agent.NewEngine(agent.Deps{
			LLM:             llm,
			Catalog:         catalogWith(t),
			Policy:          allowAll(),
			Model:           "m",
			Compactor:       rc,
			TokenCounter:    tc,
			ContextWindow:   func() int { return 100 },
			CompactionRatio: 0.8, // threshold = 80
		})
		sess := session.New("s", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
		for range r.Events() {
		}
		return rc.called > 0, tc.calls
	}

	// Over threshold (90 >= 80): compaction trips, and the injected counter was used.
	over, overCalls := run(90)
	if !over {
		t.Fatalf("compaction did not trigger when counter reported over threshold")
	}
	if overCalls == 0 {
		t.Fatalf("injected counter was never consulted")
	}

	// Under threshold (10 < 80): compaction does not trip.
	under, _ := run(10)
	if under {
		t.Fatalf("compaction triggered when counter reported under threshold")
	}
}
