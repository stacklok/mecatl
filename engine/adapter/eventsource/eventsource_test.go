package eventsource_test

import (
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// seq adapts a slice of events to the iter.Seq2 shape Fold consumes (all nil
// errors — the happy path).
func seq(evs []session.Event) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range evs {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func meta() eventsource.SessionMeta {
	return eventsource.SessionMeta{
		ID:        "s1",
		Mode:      session.ModeDefault,
		Limits:    session.Limits{},
		Workspace: "/ws",
		CreatedAt: time.Unix(0, 0),
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario2_EventFoldRejectsEmptyAuthorityClaim(t *testing.T) {
	t.Parallel()
	m := meta()
	m.Authority = &session.Authority{Provenance: "derived"}

	restored, err := eventsource.Fold(m, seq(nil))
	if err == nil {
		t.Fatal("empty authority claim folded successfully")
	}
	if restored != nil {
		t.Fatal("empty authority claim returned a session")
	}
	if !errors.Is(err, eventsource.ErrReconstruct) {
		t.Fatalf("error = %v, want ErrReconstruct", err)
	}

	legacy, err := eventsource.Fold(meta(), seq(nil))
	if err != nil {
		t.Fatalf("legacy authority-absent fold: %v", err)
	}
	if _, bound := legacy.BoundAuthority(); bound {
		t.Fatal("authority-absent legacy fold restored as bound")
	}
}

func TestFoldRestoresAdoptionMetadata(t *testing.T) {
	m := meta()
	m.AdoptionSourceID = "legacy-source"
	m.AdoptionRequestDigest = "request-digest"

	restored, err := eventsource.Fold(m, seq(nil))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := restored.Adoption; got == nil || got.AdoptionSourceID != m.AdoptionSourceID || got.AdoptionRequestDigest != m.AdoptionRequestDigest {
		t.Fatalf("Adoption = %+v, want source %q and digest %q", got, m.AdoptionSourceID, m.AdoptionRequestDigest)
	}
}

// ev is a terse Event constructor.
func toolCall(id, name, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

func assertRetryParity(t *testing.T, live, folded *session.Session) {
	t.Helper()
	liveSnap, err := sessnap.Of(live)
	if err != nil {
		t.Fatal(err)
	}
	foldedSnap, err := sessnap.Of(folded)
	if err != nil {
		t.Fatal(err)
	}
	if liveSnap.State != foldedSnap.State || liveSnap.StopReason != foldedSnap.StopReason ||
		liveSnap.Counters != foldedSnap.Counters || liveSnap.Usage == nil != (foldedSnap.Usage == nil) ||
		(liveSnap.Usage != nil && *liveSnap.Usage != *foldedSnap.Usage) ||
		liveSnap.Permanent != foldedSnap.Permanent || liveSnap.RetryDisposition != foldedSnap.RetryDisposition ||
		liveSnap.StreamProgress != foldedSnap.StreamProgress || liveSnap.RetryPending != foldedSnap.RetryPending ||
		liveSnap.RetryPendingDisposition != foldedSnap.RetryPendingDisposition || liveSnap.RetryPendingProgress != foldedSnap.RetryPendingProgress ||
		liveSnap.LastError != foldedSnap.LastError {
		t.Fatalf("snapshot metadata differs\n live: %+v\nfolded: %+v", liveSnap, foldedSnap)
	}
	liveMessages, _ := json.Marshal(live.Conversation.Messages)
	foldedMessages, _ := json.Marshal(folded.Conversation.Messages)
	if string(liveMessages) != string(foldedMessages) {
		t.Fatalf("conversation differs\n live: %s\nfolded: %s", liveMessages, foldedMessages)
	}
}

func TestFoldParityForCompleteErrorStop(t *testing.T) {
	live := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := live.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordAssistant(session.NewAssistantMessage("prior complete", "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := live.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordAssistant(session.NewAssistantMessage("clean error terminal", "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := live.Stop(session.StopError); err != nil {
		t.Fatal(err)
	}

	evs := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvMessageDelta, Text: "prior complete"},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvMessageDelta, Turn: 1, Text: "clean error terminal"},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopError, Error: "provider-declared failure", Disposition: session.RetryDispositionPermanent, Progress: session.StreamProgressComplete}},
	}
	folded, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatal(err)
	}
	assertRetryParity(t, live, folded)
	if folded.State != session.StateCompleted || folded.LastError() != "" || folded.FailurePermanence() {
		t.Fatalf("complete error stop retained failure state: state=%v last=%q permanent=%v", folded.State, folded.LastError(), folded.FailurePermanence())
	}
}

func TestFoldParityForIteratorError(t *testing.T) {
	live := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := live.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordAssistant(session.NewAssistantMessage("prior complete", "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := live.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := live.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressVisible); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordLastError("upstream reset"); err != nil {
		t.Fatal(err)
	}

	evs := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvMessageDelta, Text: "prior complete"},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvMessageDelta, Turn: 1, Text: "incomplete partial"},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopError, Error: "upstream reset", Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}},
	}
	folded, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatal(err)
	}
	assertRetryParity(t, live, folded)
}

func TestFoldDiscardsOnlyFailedPartialAssistant(t *testing.T) {
	call := toolCall("c1", "Read", `{"path":"a.go"}`)
	evs := []session.Event{
		{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "inspect"}},
		{Type: session.EvTurnStart},
		{Type: session.EvMessageDelta, Text: "checking"},
		{Type: session.EvToolCall, ToolCall: &call},
		{Type: session.EvToolResult, ToolResult: ptr(session.NewToolResult("c1", "ok"))},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvMessageDelta, Turn: 1, Text: "incomplete secret partial"},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopError, Error: "upstream reset", Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != session.StateFailed {
		t.Fatalf("state = %v", s.State)
	}
	if d, p := s.FailureMetadata(); d != session.RetryDispositionRetryable || p != session.StreamProgressVisible {
		t.Fatalf("failure metadata = (%v,%v)", d, p)
	}
	for _, msg := range s.Conversation.Messages {
		if strings.Contains(msg.Text, "incomplete secret partial") {
			t.Fatalf("failed partial assistant entered replay history: %+v", s.Conversation.Messages)
		}
	}
	if len(s.Conversation.Messages) != 3 || s.Conversation.Messages[1].Text != "checking" || s.Conversation.Messages[2].ToolResult == nil {
		t.Fatalf("prior completed tool cycle was not preserved: %+v", s.Conversation.Messages)
	}
}

func TestFoldFailedStepRetrySnapshotParity(t *testing.T) {
	baseEvents := []session.Event{
		{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "original"}},
		{Type: session.EvTurnStart},
		{Type: session.EvMessageDelta, Text: "prior completed turn"},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopError, Error: "reset", Usage: session.Usage{InputTokens: 7, OutputTokens: 4}, Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}},
		{Type: session.EvModelRetry, ModelRetry: &session.ModelRetryPayload{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}},
	}
	newPrepared := func(t *testing.T) *session.Session {
		t.Helper()
		s := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
		if err := s.RecordUserPrompt("original", nil); err != nil {
			t.Fatal(err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAssistant(session.NewAssistantMessage("prior completed turn", "", nil)); err != nil {
			t.Fatal(err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordUsage(session.Usage{InputTokens: 7, OutputTokens: 4}); err != nil {
			t.Fatal(err)
		}
		if err := s.Fail(); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressVisible); err != nil {
			t.Fatal(err)
		}
		if err := s.PrepareFailedStepRetry(); err != nil {
			t.Fatal(err)
		}
		return s
	}
	tests := []struct {
		name   string
		live   func(*testing.T) *session.Session
		events []session.Event
	}{
		{"pending crash before turn", newPrepared, baseEvents},
		{"deferred clean brake", newPrepared, append(append([]session.Event{}, baseEvents...), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopBudget, Progress: session.StreamProgressUnknown}})},
		{"running crash discards partial", func(t *testing.T) *session.Session {
			s := newPrepared(t)
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			return s
		}, append(append([]session.Event{}, baseEvents...), session.Event{Type: session.EvTurnStart}, session.Event{Type: session.EvMessageDelta, Text: "partial retry delta"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := tc.live(t)
			folded, err := eventsource.Fold(meta(), seq(tc.events))
			if err != nil {
				t.Fatal(err)
			}
			assertRetryParity(t, live, folded)
			for _, msg := range folded.Conversation.Messages {
				if strings.Contains(msg.Text, "partial retry delta") {
					t.Fatalf("partial retry entered history: %+v", folded.Conversation.Messages)
				}
			}
		})
	}
}

// asserts the conversation pairing, the assistant text, the tool call/result, the
// counters, the cumulative usage, and the completed terminal state.
//
// MUTATION-KILL: using a SINGLE EvResult.Usage instead of the SUM across runs would
// break the multi-run usage assertion in TestFoldMultiRunUsageIsCumulative; dropping
// EvCompactionArchive handling would lose the head turns in
// TestFoldRecoversCompactionArchiveHead.
func TestFoldStructuralConversation(t *testing.T) {
	call := toolCall("c1", "Read", `{"path":"a.go"}`)
	evs := []session.Event{
		{Type: session.EvSessionInit},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "let me "},
		{Type: session.EvMessageDelta, Turn: 0, Text: "look"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvToolResult, Turn: 0, ToolResult: ptr(session.NewToolResult("c1", "file contents"))},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvMessageDelta, Turn: 1, Text: "all done"},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: "all done", Usage: session.Usage{InputTokens: 15, OutputTokens: 5}}},
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	msgs := s.Conversation.Messages
	// assistant("let me look", [c1]) → tool(c1) → assistant("all done")
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != session.RoleAssistant || msgs[0].Text != "let me look" {
		t.Fatalf("msg0 = %+v, want assistant 'let me look'", msgs[0])
	}
	if len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].ID != "c1" {
		t.Fatalf("msg0 tool calls = %+v, want [c1]", msgs[0].ToolCalls)
	}
	if msgs[1].Role != session.RoleTool || msgs[1].ToolResult == nil || msgs[1].ToolResult.CallID != "c1" {
		t.Fatalf("msg1 = %+v, want tool result for c1", msgs[1])
	}
	if msgs[2].Role != session.RoleAssistant || msgs[2].Text != "all done" {
		t.Fatalf("msg2 = %+v, want assistant 'all done'", msgs[2])
	}
	// History must be provider-replayable.
	if err := session.ValidateToolPairing(msgs); err != nil {
		t.Fatalf("reconstructed history is not tool-pairing-valid: %v", err)
	}

	if s.State != session.StateCompleted {
		t.Fatalf("state = %q, want completed", s.State)
	}
	if r, _ := s.RecordedStopReason(); r != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", r)
	}
	if s.Usage != (session.Usage{InputTokens: 15, OutputTokens: 5}) {
		t.Fatalf("usage = %+v, want {15,5}", s.Usage)
	}
	// Reasoning is never event-carried — the contract limitation.
	if msgs[0].Reasoning != "" || msgs[0].ProviderPhase != "" {
		t.Fatalf("reconstructed assistant carries Reasoning/ProviderPhase it cannot have: %+v", msgs[0])
	}
}

// TestFoldMultiRunUsageIsCumulative drives a stream with TWO terminal EvResults (a
// reopened session). EvResult.Usage is PER-RUN, so cumulative session.Usage is the
// SUM. This exercises the per-run-vs-cumulative trap.
func TestFoldMultiRunUsageIsCumulative(t *testing.T) {
	evs := []session.Event{
		// Run 1.
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "first"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: session.Usage{InputTokens: 10, OutputTokens: 4}}},
		// Run 2 (after a Reopen) — a fresh per-run counter segment.
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "second"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: session.Usage{InputTokens: 7, OutputTokens: 3}}},
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	want := session.Usage{InputTokens: 17, OutputTokens: 7}
	if s.Usage != want {
		t.Fatalf("cumulative usage = %+v, want %+v (sum of both runs, NOT a single EvResult)", s.Usage, want)
	}
	// Counters reflect the LATEST run segment (per-run, like resetToIdle on Reopen).
	if s.Counters.Turns != 1 {
		t.Fatalf("counters.Turns = %d, want 1 (latest run segment)", s.Counters.Turns)
	}
	// Both assistant turns survive.
	if got := len(s.Conversation.Messages); got != 2 {
		t.Fatalf("got %d messages, want 2", got)
	}
}

// TestFoldRecoversCompactionArchiveHead asserts the pre-compaction head carried by
// EvCompactionArchive is recovered and prefixes the post-compaction turns.
func TestFoldRecoversCompactionArchiveHead(t *testing.T) {
	head := []session.Message{
		session.NewUserMessage("original task"),
		session.NewAssistantMessage("old turn 1", "", nil),
		session.NewAssistantMessage("old turn 2", "", nil),
	}
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		// The compaction archive lands the pre-compaction head.
		{Type: session.EvCompaction, Turn: 0, Text: "summary"},
		{Type: session.EvCompactionArchive, Turn: 0, CompactionArchive: &session.CompactionArchivePayload{Replaced: head}},
		{Type: session.EvMessageDelta, Turn: 0, Text: "post-compaction answer"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: session.Usage{InputTokens: 3, OutputTokens: 1}}},
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	msgs := s.Conversation.Messages
	// 3 head messages + 1 post-compaction assistant turn.
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4 (3 head + 1 post): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != session.RoleUser || msgs[0].Text != "original task" {
		t.Fatalf("head turn lost: msg0 = %+v", msgs[0])
	}
	if msgs[3].Text != "post-compaction answer" {
		t.Fatalf("post-compaction turn = %+v, want 'post-compaction answer'", msgs[3])
	}
}

// TestFoldUnansweredAskIsAwaiting asserts a trailing EvPermissionAsk with no
// following EvApproval/EvResult lands the session in StateAwaiting with the pending
// ask restored.
func TestFoldUnansweredAskIsAwaiting(t *testing.T) {
	call := toolCall("c1", "Bash", `{"command":"ls"}`)
	ask := session.PendingAsk{AskID: "s1:0:c1:r0", Tool: "Bash", Reason: "needs approval"}
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "running a command"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvPermissionAsk, Turn: 0, Ask: &ask},
		// No EvApproval, no EvResult — the process died parked on the ask.
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.State != session.StateAwaiting {
		t.Fatalf("state = %q, want awaiting", s.State)
	}
	got, ok := s.PendingAsk()
	if !ok {
		t.Fatalf("no pending ask restored")
	}
	if got.AskID != ask.AskID || got.Tool != "Bash" {
		t.Fatalf("pending = %+v, want %+v", got, ask)
	}
}

// TestFoldResolvedAskNotAwaiting asserts an ask FOLLOWED by an approval (then a
// terminal result) is NOT awaiting — the verdict cleared it.
func TestFoldResolvedAskNotAwaiting(t *testing.T) {
	call := toolCall("c1", "Bash", `{"command":"ls"}`)
	ask := session.PendingAsk{AskID: "s1:0:c1:r0", Tool: "Bash"}
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvPermissionAsk, Turn: 0, Ask: &ask},
		{Type: session.EvApproval, Turn: 0, Approval: &session.ApprovalPayload{AskID: ask.AskID, Verdict: session.VerdictStringAllowOnce, Tool: "Bash", Call: "c1"}},
		{Type: session.EvToolResult, Turn: 0, ToolResult: ptr(session.NewToolResult("c1", "ok"))},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.State != session.StateCompleted {
		t.Fatalf("state = %q, want completed (the ask was resolved)", s.State)
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatalf("ask should have been cleared by the approval")
	}
}

// TestFoldTerminalStateMapping covers the stop→state mapping for the error and
// cancelled terminals (the clean terminals are covered elsewhere).
func TestFoldTerminalStateMapping(t *testing.T) {
	cases := []struct {
		stop  session.StopReason
		state session.State
	}{
		{session.StopError, session.StateFailed},
		{session.StopCancelled, session.StateCancelled},
		{session.StopBudget, session.StateCompleted},
		{session.StopNoProgress, session.StateCompleted},
		{session.StopMaxTurns, session.StateCompleted},
	}
	for _, tc := range cases {
		evs := []session.Event{
			{Type: session.EvTurnStart, Turn: 0},
			{Type: session.EvMessageDelta, Turn: 0, Text: "x"},
			{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: tc.stop}},
		}
		s, err := eventsource.Fold(meta(), seq(evs))
		if err != nil {
			t.Fatalf("Fold(%s): %v", tc.stop, err)
		}
		if s.State != tc.state {
			t.Fatalf("stop %q => state %q, want %q", tc.stop, s.State, tc.state)
		}
	}
}

// TestFoldNoTerminalIsIdle asserts a stream that stops mid-run (no terminal result,
// no pending ask) reconstructs to a resumable idle session with intact history.
func TestFoldNoTerminalIsIdle(t *testing.T) {
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "interrupted mid-thought"},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.State != session.StateIdle {
		t.Fatalf("state = %q, want idle", s.State)
	}
	if len(s.Conversation.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(s.Conversation.Messages))
	}
}

// TestFoldStreamErrorPropagates asserts a per-item stream error is surfaced as
// ErrStream and aborts the fold.
func TestFoldStreamErrorPropagates(t *testing.T) {
	boom := errors.New("backend read fault")
	bad := func(yield func(session.Event, error) bool) {
		yield(session.Event{Type: session.EvTurnStart}, nil)
		yield(session.Event{}, boom)
	}
	_, err := eventsource.Fold(meta(), bad)
	if !errors.Is(err, eventsource.ErrStream) {
		t.Fatalf("err = %v, want ErrStream", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped backend fault", err)
	}
}

// TestFoldMetaIsApplied confirms the creation metadata (which no event carries) is
// applied to the reconstructed session.
func TestFoldMetaIsApplied(t *testing.T) {
	m := eventsource.SessionMeta{
		ID:         "sess-42",
		Mode:       session.ModePlan,
		Limits:     session.Limits{MaxTurns: 9},
		Workspace:  "/work/space",
		Profile:    "no-fs",
		ProviderID: "openrouter",
		ModelID:    "anthropic/claude",
		CreatedAt:  time.Unix(1700000000, 0),
	}
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "hi"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(m, seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.ID != "sess-42" || s.Mode != session.ModePlan || s.Workspace != "/work/space" ||
		s.Profile != "no-fs" || s.ProviderID != "openrouter" || s.ModelID != "anthropic/claude" ||
		s.Limits.MaxTurns != 9 || !s.CreatedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("meta not applied: %+v", s)
	}
}

// TestFoldRoundTripsThroughSnapshot is a belt-and-braces cross-check: a folded
// session must itself be snapshot-serializable (the host may persist it), proving the
// reconstructed aggregate is internally consistent.
func TestFoldRoundTripsThroughSnapshot(t *testing.T) {
	call := toolCall("c1", "Read", `{"path":"a.go"}`)
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "look"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvToolResult, Turn: 0, ToolResult: ptr(session.NewToolResult("c1", "ok"))},
		{Type: session.EvTurnStart, Turn: 1},
		{Type: session.EvMessageDelta, Turn: 1, Text: "done"},
		{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: session.Usage{InputTokens: 9, OutputTokens: 2}}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal folded session: %v", err)
	}
	if _, err := sessnap.Unmarshal(line); err != nil {
		t.Fatalf("Unmarshal folded session: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestFoldReconstructsTypedToolResult verifies that Fold reconstructs typed
// tool-result blocks (ToolResult.Parts) for free — the EvToolResult case feeds the
// *ToolResult (now carrying Parts) straight into session.NewToolMessage, so the
// blocks survive verbatim with no Fold-side change. This is the T5 "rides for
// free" verification: a typed ToolResult round-trips through the event log into a
// RoleTool message whose ToolResult.Parts is non-empty and matches.
func TestFoldReconstructsTypedToolResult(t *testing.T) {
	parts := []session.Content{
		session.NewTextBlock("file contents"),
		session.NewResourceLinkBlock("res://x", "name", "title", "desc", "text/plain", 42, []string{"admin"}),
		session.NewStructuredContentBlock(`{"k":"v"}`),
	}
	call := toolCall("c1", "Read", `{"path":"a.go"}`)
	evs := []session.Event{
		{Type: session.EvSessionInit},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvToolResult, Turn: 0, ToolResult: ptr(session.NewToolResultWithParts("c1", "summary", parts))},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: "done"}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	msgs := s.Conversation.Messages
	// assistant([c1]) → tool(c1, parts) → no further assistant turn (result text only).
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != session.RoleTool || msgs[1].ToolResult == nil {
		t.Fatalf("msg1 = %+v, want a tool message", msgs[1])
	}
	tr := msgs[1].ToolResult
	if tr.CallID != "c1" {
		t.Fatalf("CallID = %q, want c1", tr.CallID)
	}
	if tr.Content != "summary" {
		t.Fatalf("Content = %q, want summary", tr.Content)
	}
	if len(tr.Parts) != len(parts) {
		t.Fatalf("Parts len = %d, want %d", len(tr.Parts), len(parts))
	}
	for i, want := range parts {
		got := tr.Parts[i]
		if got.BlockKind != want.BlockKind {
			t.Errorf("Parts[%d] BlockKind = %q, want %q", i, got.BlockKind, want.BlockKind)
		}
		if got.Text != want.Text {
			t.Errorf("Parts[%d] Text = %q, want %q", i, got.Text, want.Text)
		}
		if got.URL != want.URL {
			t.Errorf("Parts[%d] URL = %q, want %q", i, got.URL, want.URL)
		}
		if got.Name != want.Name {
			t.Errorf("Parts[%d] Name = %q, want %q", i, got.Name, want.Name)
		}
	}
	// The reconstructed history must still be provider-replayable.
	if err := session.ValidateToolPairing(msgs); err != nil {
		t.Fatalf("reconstructed history is not tool-pairing-valid: %v", err)
	}
}

// TestFoldRoundTripsFailurePermanence asserts the event-sourced fold reconstructs
// the failure-permanence flag from the terminal EvResult (ADR 0038's reconstruction
// contract requires it, so a permanently-failed session's recover advisory fires on
// an event-log-SoR backend just as it does on the snapshot path). A StopError run
// with Permanent==true folds to StateFailed + FailurePermanence()==true; the same run
// with Permanent==false folds to StateFailed + FailurePermanence()==false. A clean
// (non-error) terminal with Permanent==true is meaningless and must NOT set the flag.
func TestFoldRoundTripsFailurePermanence(t *testing.T) {
	cases := []struct {
		name       string
		stop       session.StopReason
		permanent  bool
		wantState  session.State
		wantPerman bool
		// wantLastError pins the issue-#332 fold: the terminal EvResult.Error is
		// captured on StateFailed (StopError) runs, ignored on clean terminals.
		wantLastError string
	}{
		{"permanent error", session.StopError, true, session.StateFailed, true, "provider error"},
		{"transient error", session.StopError, false, session.StateFailed, false, "provider error"},
		// A Permanent flag / Error body on a clean terminal is meaningless; the fold
		// must ignore both.
		{"clean terminal ignores permanent flag", session.StopEndTurn, true, session.StateCompleted, false, ""},
	}
	for _, tc := range cases {
		evs := []session.Event{
			{Type: session.EvTurnStart, Turn: 0},
			{Type: session.EvMessageDelta, Turn: 0, Text: "boom"},
			{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{
				Stop:      tc.stop,
				Permanent: tc.permanent,
				Error:     "provider error",
				Usage:     session.Usage{InputTokens: 2, OutputTokens: 1},
			}},
		}
		s, err := eventsource.Fold(meta(), seq(evs))
		if err != nil {
			t.Fatalf("Fold(%s): %v", tc.name, err)
		}
		if s.State != tc.wantState {
			t.Fatalf("%s: state = %q, want %q", tc.name, s.State, tc.wantState)
		}
		if got := s.FailurePermanence(); got != tc.wantPerman {
			t.Fatalf("%s: FailurePermanence = %v, want %v", tc.name, got, tc.wantPerman)
		}
		if tc.stop == session.StopError && tc.permanent {
			if d, p := s.FailureMetadata(); d != session.RetryDispositionPermanent || p != session.StreamProgressUnknown {
				t.Fatalf("%s: FailureMetadata = (%v,%v), want (permanent,unknown)", tc.name, d, p)
			}
		}
		if got := s.LastError(); got != tc.wantLastError {
			t.Fatalf("%s: LastError = %q, want %q", tc.name, got, tc.wantLastError)
		}
	}
}

// TestFoldMultiRunPermanenceIsLastRun asserts that when a session was reopened after a
// transient failure and later ended in a different failure, the fold reports the LATEST
// run's permanence (mirroring how a snapshot captures the final state, not a mid-run).
func TestFoldMultiRunPermanenceIsLastRun(t *testing.T) {
	evs := []session.Event{
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopError, Permanent: true, Error: "perm"}},
		// Reopen: a new run begins.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "retry"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopError, Permanent: false, Error: "transient"}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.State != session.StateFailed {
		t.Fatalf("state = %q, want failed", s.State)
	}
	if s.FailurePermanence() {
		t.Fatalf("FailurePermanence = true, want false (last run was transient)")
	}
}
