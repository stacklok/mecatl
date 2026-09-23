package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestSession(limits Limits) *Session {
	return New("s1", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/tmp/ws", Revision: "in-tree-v1"}, limits, time.Unix(0, 0))
}

// mustOK fails the test immediately when a session-aggregate transition errors.
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected transition error: %v", err)
	}
}

func TestValidLifecycle_IdleRunningAwaitingRunningCompleted(t *testing.T) {
	s := newTestSession(Limits{})
	if s.State != StateIdle {
		t.Fatalf("new session state = %q, want idle", s.State)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if s.State != StateRunning {
		t.Fatalf("after BeginTurn state = %q, want running", s.State)
	}
	if err := s.RecordAssistant(NewAssistantMessage("hi", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.PauseForApproval(PendingAsk{AskID: "a1", Tool: "Edit"}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if s.State != StateAwaiting {
		t.Fatalf("after PauseForApproval state = %q, want awaiting", s.State)
	}
	ask, ok := s.PendingAsk()
	if !ok || ask.AskID != "a1" {
		t.Fatalf("PendingAsk = %+v, %v", ask, ok)
	}
	if _, err := s.ResumeWith(); err != nil {
		t.Fatalf("ResumeWith: %v", err)
	}
	if s.State != StateRunning {
		t.Fatalf("after ResumeWith state = %q, want running", s.State)
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatalf("PendingAsk should be cleared after ResumeWith")
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if s.State != StateCompleted {
		t.Fatalf("after Complete state = %q, want completed", s.State)
	}
	if r, ok := s.StopReason(); !ok || r != StopEndTurn {
		t.Fatalf("StopReason = %q, %v; want end_turn,true", r, ok)
	}
}

func TestSetMode(t *testing.T) {
	// Idle: a mode change is legal and takes effect.
	s := newTestSession(Limits{})
	if err := s.SetMode(ModePlan); err != nil {
		t.Fatalf("SetMode idle: %v", err)
	}
	if s.Mode != ModePlan {
		t.Fatalf("mode = %q, want plan", s.Mode)
	}

	// Running: a mid-turn change is rejected.
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.SetMode(ModeAccept); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("SetMode running = %v, want ErrIllegalTransition", err)
	}
	if s.Mode != ModePlan {
		t.Fatalf("mode changed mid-run to %q", s.Mode)
	}

	// Awaiting: also rejected.
	if err := s.RecordAssistant(NewAssistantMessage("hi", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.PauseForApproval(PendingAsk{AskID: "a1", Tool: "Edit"}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if err := s.SetMode(ModeDefault); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("SetMode awaiting = %v, want ErrIllegalTransition", err)
	}

	// Terminal (completed): legal again (applies to the next reopened run).
	if _, err := s.ResumeWith(); err != nil {
		t.Fatalf("ResumeWith: %v", err)
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := s.SetMode(ModeDefault); err != nil {
		t.Fatalf("SetMode completed: %v", err)
	}
	if s.Mode != ModeDefault {
		t.Fatalf("mode = %q, want default", s.Mode)
	}
}

func TestIllegalTransitions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Session)
		op    func(*Session) error
	}{
		{
			name:  "RecordAssistant from idle",
			setup: func(*Session) {},
			op:    func(s *Session) error { return s.RecordAssistant(NewAssistantMessage("x", "", nil)) },
		},
		{
			name:  "RecordToolResults from idle",
			setup: func(*Session) {},
			op:    func(s *Session) error { return s.RecordToolResults([]ToolResult{NewToolResult("c", "ok")}) },
		},
		{
			name:  "PauseForApproval from idle",
			setup: func(*Session) {},
			op:    func(s *Session) error { return s.PauseForApproval(PendingAsk{}) },
		},
		{
			name:  "BeginTurn from awaiting",
			setup: func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) },
			op:    func(s *Session) error { return s.BeginTurn() },
		},
		{
			name:  "Complete after completed",
			setup: func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() },
			op:    func(s *Session) error { return s.Complete() },
		},
		{
			name:  "Cancel after completed",
			setup: func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() },
			op:    func(s *Session) error { return s.Cancel() },
		},
		{
			name:  "Fail after cancelled",
			setup: func(s *Session) { _ = s.Cancel() },
			op:    func(s *Session) error { return s.Fail() },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			tc.setup(s)
			err := tc.op(s)
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("err = %v, want ErrIllegalTransition", err)
			}
		})
	}
}

func TestResumeWithNoPendingAsk(t *testing.T) {
	s := newTestSession(Limits{})
	_ = s.BeginTurn()
	if _, err := s.ResumeWith(); !errors.Is(err, ErrNoPendingAsk) {
		t.Fatalf("err = %v, want ErrNoPendingAsk", err)
	}
}

func TestCancelFromEachNonTerminalState(t *testing.T) {
	states := []struct {
		name  string
		setup func(*Session)
		want  State
	}{
		{"idle", func(*Session) {}, StateIdle},
		{"running", func(s *Session) { _ = s.BeginTurn() }, StateRunning},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }, StateAwaiting},
	}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			st.setup(s)
			if s.State != st.want {
				t.Fatalf("precondition state = %q, want %q", s.State, st.want)
			}
			if err := s.Cancel(); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			if s.State != StateCancelled {
				t.Fatalf("after Cancel state = %q, want cancelled", s.State)
			}
			if r, ok := s.StopReason(); !ok || r != StopCancelled {
				t.Fatalf("StopReason = %q,%v; want cancelled,true", r, ok)
			}
		})
	}
}

func TestCancelFromTerminalIsIllegal(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"completed", func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() }},
		{"failed", func(s *Session) { _ = s.Fail() }},
		{"cancelled", func(s *Session) { _ = s.Cancel() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Cancel(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Cancel from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestStopConditionsTrip(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		drive  func(*Session)
		want   StopReason
	}{
		{
			name:   "max turns",
			limits: Limits{MaxTurns: 2},
			drive:  func(s *Session) { _ = s.BeginTurn(); _ = s.BeginTurn() },
			want:   StopMaxTurns,
		},
		{
			name:   "max tool calls",
			limits: Limits{MaxToolCalls: 2},
			drive: func(s *Session) {
				_ = s.BeginTurn()
				_ = s.RecordToolResults([]ToolResult{NewToolResult("a", "ok"), NewToolResult("b", "ok")})
			},
			want: StopMaxToolCalls,
		},
		{
			name:   "max consecutive failures",
			limits: Limits{MaxConsecutiveFailures: 2},
			drive: func(s *Session) {
				_ = s.BeginTurn()
				_ = s.RecordToolResults([]ToolResult{NewToolError("a", "boom"), NewToolError("b", "boom")})
			},
			want: StopMaxConsecutiveFailures,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(tc.limits)
			tc.drive(s)
			r, ok := s.StopReason()
			if !ok || r != tc.want {
				t.Fatalf("StopReason = %q,%v; want %q,true", r, ok, tc.want)
			}
		})
	}
}

func TestStopConditionsDoNotTripUnderLimit(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 5, MaxToolCalls: 5, MaxConsecutiveFailures: 5})
	_ = s.BeginTurn()
	_ = s.RecordToolResults([]ToolResult{NewToolResult("a", "ok")})
	if r, ok := s.StopReason(); ok {
		t.Fatalf("StopReason = %q,%v; want none,false", r, ok)
	}
}

func TestConsecutiveFailuresResetOnSuccess(t *testing.T) {
	s := newTestSession(Limits{MaxConsecutiveFailures: 2})
	_ = s.BeginTurn()
	_ = s.RecordToolResults([]ToolResult{NewToolError("a", "boom")})
	_ = s.RecordToolResults([]ToolResult{NewToolResult("b", "ok")})
	if s.Counters.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0 after success", s.Counters.ConsecutiveFailures)
	}
	if _, ok := s.StopReason(); ok {
		t.Fatalf("StopReason should not trip after reset")
	}
}

func TestRecordedStopReasonDoesNotDerive(t *testing.T) {
	// A session whose limit would trip must NOT report a recorded reason until one
	// is explicitly set; RecordedStopReason performs no derivation.
	s := newTestSession(Limits{MaxTurns: 1})
	_ = s.BeginTurn() // trips MaxTurns under StopReason's derivation

	if r, ok := s.RecordedStopReason(); ok {
		t.Fatalf("RecordedStopReason = %q,%v; want none,false (no derivation)", r, ok)
	}
	// StopReason (derived) still sees the tripped limit.
	if r, ok := s.StopReason(); !ok || r != StopMaxTurns {
		t.Fatalf("StopReason = %q,%v; want max_turns,true", r, ok)
	}

	if err := s.Stop(StopMaxToolCalls); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if r, ok := s.RecordedStopReason(); !ok || r != StopMaxToolCalls {
		t.Fatalf("RecordedStopReason = %q,%v; want max_tool_calls,true", r, ok)
	}
}

func TestLimitTrippedIgnoresRecordedReason(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 1})
	if r, ok := s.LimitTripped(); ok {
		t.Fatalf("LimitTripped before turn = %q,%v; want none,false", r, ok)
	}
	_ = s.BeginTurn()
	if r, ok := s.LimitTripped(); !ok || r != StopMaxTurns {
		t.Fatalf("LimitTripped = %q,%v; want max_turns,true", r, ok)
	}
	// Recording a terminal reason does not change the pure limit predicate.
	_ = s.Cancel()
	if r, ok := s.LimitTripped(); !ok || r != StopMaxTurns {
		t.Fatalf("LimitTripped after Cancel = %q,%v; want max_turns,true", r, ok)
	}
}

func TestRecordUserPromptAppendsInstructionsThenPrompt(t *testing.T) {
	s := newTestSession(Limits{})
	instr := []Message{NewUserMessage("AGENTS.md"), NewUserMessage("CLAUDE.md")}
	if err := s.RecordUserPrompt("hello", instr); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	msgs := s.Conversation.Messages
	if len(msgs) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(msgs))
	}
	if msgs[0].Text != "AGENTS.md" || msgs[1].Text != "CLAUDE.md" || msgs[2].Text != "hello" {
		t.Fatalf("messages = %+v; want instructions then prompt", msgs)
	}
	if msgs[2].Role != RoleUser {
		t.Fatalf("prompt role = %q, want user", msgs[2].Role)
	}
}

func TestRecordUserPromptFromTerminalIsIllegal(t *testing.T) {
	s := newTestSession(Limits{})
	_ = s.BeginTurn()
	_ = s.Complete()
	if err := s.RecordUserPrompt("x", nil); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestReplaceHistoryAtomicWhileRunning(t *testing.T) {
	s := newTestSession(Limits{})
	_ = s.BeginTurn()
	_ = s.RecordAssistant(NewAssistantMessage("old", "", nil))
	compacted := []Message{NewSystemMessage("summary"), NewUserMessage("continue")}
	if err := s.ReplaceHistory(compacted); err != nil {
		t.Fatalf("ReplaceHistory: %v", err)
	}
	if !reflect.DeepEqual(s.Conversation.Messages, compacted) {
		t.Fatalf("messages = %+v, want %+v", s.Conversation.Messages, compacted)
	}
}

func TestReplaceHistoryOnlyWhileRunning(t *testing.T) {
	s := newTestSession(Limits{}) // idle
	if err := s.ReplaceHistory(nil); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

// TestReplaceHistoryRejectsUnpairedHistory pins the aggregate-level guard: while
// running, ReplaceHistory refuses a tool-pairing-invalid slice (a leading orphan
// tool result) — wrapping the pairing error — and leaves the conversation
// untouched, but accepts a clean paired slice and actually applies the swap.
func TestReplaceHistoryRejectsUnpairedHistory(t *testing.T) {
	s := newTestSession(Limits{})
	_ = s.BeginTurn()
	_ = s.RecordAssistant(NewAssistantMessage("old", "", nil))
	before := append([]Message(nil), s.Conversation.Messages...)

	// (a) Leading-orphan slice: a tool result whose call never preceded it.
	orphan := []Message{
		NewUserMessage("goal"),
		NewToolMessage(NewToolResult("c1", "result")),
	}
	err := s.ReplaceHistory(orphan)
	if err == nil {
		t.Fatalf("ReplaceHistory accepted an unpaired (orphan) history")
	}
	if got := ValidateToolPairing(orphan); got == nil || !strings.Contains(err.Error(), "orphaned tool result") {
		t.Fatalf("error %v does not wrap the pairing message", err)
	}
	if !reflect.DeepEqual(s.Conversation.Messages, before) {
		t.Fatalf("conversation mutated despite rejected replacement: %+v", s.Conversation.Messages)
	}

	// (b) Clean paired slice: accepted, and the swap is applied.
	clean := []Message{
		NewSystemMessage("summary"),
		NewUserMessage("continue"),
		NewAssistantMessage("", "", []ToolCall{NewToolCall("c1", "Read", nil)}),
		NewToolMessage(NewToolResult("c1", "ok")),
	}
	if err := s.ReplaceHistory(clean); err != nil {
		t.Fatalf("ReplaceHistory rejected a clean paired history: %v", err)
	}
	if !reflect.DeepEqual(s.Conversation.Messages, clean) {
		t.Fatalf("clean replacement not applied: %+v", s.Conversation.Messages)
	}
}

func TestExplicitStopReasonTakesPrecedence(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 1})
	_ = s.BeginTurn() // would trip MaxTurns
	if err := s.Stop(StopMaxToolCalls); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if r, _ := s.StopReason(); r != StopMaxToolCalls {
		t.Fatalf("StopReason = %q, want explicit max_tool_calls", r)
	}
}

// TestStopBudgetIsCleanReopenableTerminal pins that StopBudget is a CLEAN terminal,
// parallel to StopNoProgress: Stop(StopBudget) drives the session to COMPLETED (not
// failed) and the completed session is Reopen-recoverable.
func TestStopBudgetIsCleanReopenableTerminal(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Stop(StopBudget); err != nil {
		t.Fatalf("Stop(StopBudget): %v", err)
	}
	if s.State != StateCompleted {
		t.Fatalf("state after Stop(StopBudget) = %q, want completed (clean terminal)", s.State)
	}
	if r, ok := s.RecordedStopReason(); !ok || r != StopBudget {
		t.Fatalf("recorded stop = %q (ok=%v), want %q", r, ok, StopBudget)
	}
	if err := s.Reopen(); err != nil {
		t.Fatalf("Reopen after StopBudget: %v (a budget terminal must stay recoverable)", err)
	}
	if s.State != StateIdle {
		t.Fatalf("state after Reopen = %q, want idle", s.State)
	}
}

// TestStopPlanApprovedIsCleanTerminal pins that StopPlanApproved is a CLEAN terminal,
// parallel to StopBudget / StopNoProgress / StopStructuredOutput: Stop(StopPlanApproved)
// drives the session to COMPLETED (not failed) and the completed session is
// Reopen-recoverable. Emitted when an operator approves a presented plan (issue #206).
func TestStopPlanApprovedIsCleanTerminal(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Stop(StopPlanApproved); err != nil {
		t.Fatalf("Stop(StopPlanApproved): %v", err)
	}
	if s.State != StateCompleted {
		t.Fatalf("state after Stop(StopPlanApproved) = %q, want completed (clean terminal)", s.State)
	}
	if r, ok := s.RecordedStopReason(); !ok || r != StopPlanApproved {
		t.Fatalf("recorded stop = %q (ok=%v), want %q", r, ok, StopPlanApproved)
	}
	if err := s.Reopen(); err != nil {
		t.Fatalf("Reopen after StopPlanApproved: %v (a plan-approved terminal must stay recoverable)", err)
	}
	if s.State != StateIdle {
		t.Fatalf("state after Reopen = %q, want idle", s.State)
	}
}

// TestStopPlanIterateIsCleanTerminal pins that StopPlanIterate is a CLEAN terminal,
// parallel to StopPlanApproved / StopBudget / StopNoProgress / StopStructuredOutput:
// Stop(StopPlanIterate) drives the session to COMPLETED (not failed) and the completed
// session is Reopen-recoverable. Emitted when an operator chooses to iterate on a
// presented plan (issue #206: the plan-approval gate's deny/iterate verdict) — the run
// ends so the operator's next prompt drives the revision.
func TestStopPlanIterateIsCleanTerminal(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Stop(StopPlanIterate); err != nil {
		t.Fatalf("Stop(StopPlanIterate): %v", err)
	}
	if s.State != StateCompleted {
		t.Fatalf("state after Stop(StopPlanIterate) = %q, want completed (clean terminal)", s.State)
	}
	if r, ok := s.RecordedStopReason(); !ok || r != StopPlanIterate {
		t.Fatalf("recorded stop = %q (ok=%v), want %q", r, ok, StopPlanIterate)
	}
	if err := s.Reopen(); err != nil {
		t.Fatalf("Reopen after StopPlanIterate: %v (a plan-iterate terminal must stay recoverable)", err)
	}
	if s.State != StateIdle {
		t.Fatalf("state after Reopen = %q, want idle", s.State)
	}
}

func TestReopenFromCompletedReturnsToIdleAndResetsCounters(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 5})
	// Drive one turn and complete cleanly.
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordAssistant(NewAssistantMessage("first answer", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if s.State != StateCompleted || s.Counters.Turns != 1 {
		t.Fatalf("precondition: state=%q turns=%d, want completed/1", s.State, s.Counters.Turns)
	}
	convLen := len(s.Conversation.Messages)

	if err := s.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if s.State != StateIdle {
		t.Fatalf("after Reopen state = %q, want idle", s.State)
	}
	if s.Counters != (Counters{}) {
		t.Fatalf("after Reopen counters = %+v, want zero (per-prompt budget)", s.Counters)
	}
	if r, ok := s.RecordedStopReason(); ok {
		t.Fatalf("after Reopen recorded stop reason = %q, want none", r)
	}
	if len(s.Conversation.Messages) != convLen {
		t.Fatalf("Reopen dropped conversation history: len=%d, want %d", len(s.Conversation.Messages), convLen)
	}
	// The reopened session accepts a new prompt and another turn (continuation).
	if err := s.RecordUserPrompt("second prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt after Reopen: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn after Reopen: %v", err)
	}
	if s.Counters.Turns != 1 {
		t.Fatalf("turns after reopened BeginTurn = %d, want 1 (counter was reset)", s.Counters.Turns)
	}
}

func TestReopenIllegalFromNonCompletedStates(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"idle", func(*Session) {}},
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }},
		{"failed", func(s *Session) { _ = s.Fail() }},
		{"cancelled", func(s *Session) { _ = s.Cancel() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Reopen(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Reopen from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestRehomeFromIdleRepointsEnvironment(t *testing.T) {
	s := newTestSession(Limits{})
	if s.State != StateIdle {
		t.Fatalf("precondition: state = %q, want idle", s.State)
	}
	ref := EnvironmentRef{Kind: EnvKindLocal, ID: "/tmp/fresh-fork", Revision: "fork-2"}
	if err := s.Rehome(ref); err != nil {
		t.Fatalf("Rehome: %v", err)
	}
	if s.EnvironmentRef != ref {
		t.Fatalf("after Rehome ref = %+v, want %+v", s.EnvironmentRef, ref)
	}
	if s.State != StateIdle {
		t.Fatalf("after Rehome state = %q, want idle (unchanged)", s.State)
	}
}

func TestRehomeIllegalFromNonIdleStates(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }},
		{"completed", func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() }},
		{"failed", func(s *Session) { _ = s.Fail() }},
		{"cancelled", func(s *Session) { _ = s.Cancel() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Rehome(EnvironmentRef{Kind: EnvKindLocal, ID: "/tmp/fresh-fork", Revision: "fork-2"}); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Rehome from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestInterruptFromCancelledReturnsToIdleAndResetsCounters(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 5})
	if err := s.RecordUserPrompt("first prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordAssistant(NewAssistantMessage("partial answer", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if s.State != StateCancelled || s.Counters.Turns != 1 {
		t.Fatalf("precondition: state=%q turns=%d, want cancelled/1", s.State, s.Counters.Turns)
	}
	convLen := len(s.Conversation.Messages)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if s.State != StateIdle {
		t.Fatalf("after Interrupt state = %q, want idle", s.State)
	}
	if s.Counters != (Counters{}) {
		t.Fatalf("after Interrupt counters = %+v, want zero (per-prompt budget)", s.Counters)
	}
	if r, ok := s.RecordedStopReason(); ok {
		t.Fatalf("after Interrupt recorded stop reason = %q, want none", r)
	}
	// No orphaned tool calls here, so history is preserved unchanged.
	if len(s.Conversation.Messages) != convLen {
		t.Fatalf("Interrupt changed clean history: len=%d, want %d", len(s.Conversation.Messages), convLen)
	}
	// The interrupted session accepts a new prompt and another turn.
	if err := s.RecordUserPrompt("second prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt after Interrupt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn after Interrupt: %v", err)
	}
	if s.Counters.Turns != 1 {
		t.Fatalf("turns after interrupted BeginTurn = %d, want 1 (counter was reset)", s.Counters.Turns)
	}
}

func TestInterruptIllegalFromNonCancelledStates(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"idle", func(*Session) {}},
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }},
		{"completed", func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() }},
		{"failed", func(s *Session) { _ = s.Fail() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Interrupt(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Interrupt from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestInterruptClosesOutOrphanedToolCalls(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	before := len(s.Conversation.Messages)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 2 {
		t.Fatalf("appended %d tool results, want 2", len(added))
	}
	wantIDs := []ToolCallID{"call-a", "call-b"}
	for i, m := range added {
		if m.Role != RoleTool {
			t.Fatalf("appended[%d] role = %q, want tool", i, m.Role)
		}
		if m.ToolResult == nil {
			t.Fatalf("appended[%d] has nil ToolResult", i)
		}
		if m.ToolResult.CallID != wantIDs[i] {
			t.Fatalf("appended[%d] CallID = %q, want %q (in order)", i, m.ToolResult.CallID, wantIDs[i])
		}
		if !m.ToolResult.IsError {
			t.Fatalf("appended[%d] IsError = false, want true (cancellation sentinel)", i)
		}
		// The synthetic text is durable MODEL-FACING replayed history: the
		// cancel seam keeps its cancellation attribution BYTE-IDENTICAL
		// (Recover uses a different, failure-accurate message).
		if m.ToolResult.Content != "tool call interrupted by cancellation" {
			t.Fatalf("appended[%d] content = %q, want the byte-identical cancellation wording", i, m.ToolResult.Content)
		}
	}
}

func TestInterruptPartialToolResultsCloseOutRemainder(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	// call-a already answered before the cancel.
	if err := s.RecordToolResults([]ToolResult{NewToolResult("call-a", "ok")}); err != nil {
		t.Fatalf("RecordToolResults: %v", err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	before := len(s.Conversation.Messages)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 1 {
		t.Fatalf("appended %d tool results, want 1 (only the missing call-b)", len(added))
	}
	if added[0].ToolResult == nil || added[0].ToolResult.CallID != "call-b" {
		t.Fatalf("appended result = %+v, want a synthetic result for call-b", added[0].ToolResult)
	}
	if !added[0].ToolResult.IsError {
		t.Fatalf("appended call-b IsError = false, want true")
	}
}

func TestInterruptNoOpWhenHistoryClean(t *testing.T) {
	t.Run("assistant with no tool calls", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("just answer", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := s.RecordAssistant(NewAssistantMessage("the answer", "", nil)); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Interrupt(); err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Interrupt appended to clean history: len=%d, want %d", len(s.Conversation.Messages), before)
		}
	})

	t.Run("trailing user prompt (cancel before first token)", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("a prompt", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		// Cancel before any assistant message is recorded.
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Interrupt(); err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Interrupt appended to dangling-user history: len=%d, want %d", len(s.Conversation.Messages), before)
		}
	})

	t.Run("trailing user after a clean prior assistant+results", func(t *testing.T) {
		// Multi-turn: user → assistant(call) → toolresult → user → cancel. The prior
		// assistant turn is FULLY answered, and the trailing message is a user prompt
		// (no orphan), so Interrupt must be a no-op — a different code path from the
		// [user]-only and [assistant-no-calls] cases above.
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("turn one", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{NewToolCall("call-a", "read_file", nil)})); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
		if err := s.RecordToolResults([]ToolResult{NewToolResult("call-a", "ok")}); err != nil {
			t.Fatalf("RecordToolResults: %v", err)
		}
		if err := s.Complete(); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := s.Reopen(); err != nil {
			t.Fatalf("Reopen: %v", err)
		}
		if err := s.RecordUserPrompt("turn two", nil); err != nil {
			t.Fatalf("RecordUserPrompt turn two: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn turn two: %v", err)
		}
		// Cancel before any assistant message for turn two.
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Interrupt(); err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Interrupt appended despite a clean prior turn + trailing user: len=%d, want %d", len(s.Conversation.Messages), before)
		}
	})
}

// TestInterruptClosesOnlyTrailingOrphan asserts that when an earlier assistant turn
// is fully answered and only the trailing assistant turn is orphaned, Interrupt
// closes out ONLY the trailing orphan and leaves the earlier (answered) turn
// untouched — closeOutInterruptedTurn scopes to the LAST assistant message.
func TestInterruptClosesOnlyTrailingOrphan(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("turn one", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// First assistant turn: a tool call that IS answered.
	if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{NewToolCall("call-old", "read_file", nil)})); err != nil {
		t.Fatalf("RecordAssistant turn one: %v", err)
	}
	if err := s.RecordToolResults([]ToolResult{NewToolResult("call-old", "ok")}); err != nil {
		t.Fatalf("RecordToolResults: %v", err)
	}
	// Second assistant turn: an UNANSWERED tool call (the orphan).
	if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{NewToolCall("call-new", "list_dir", nil)})); err != nil {
		t.Fatalf("RecordAssistant turn two: %v", err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	before := len(s.Conversation.Messages)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 1 {
		t.Fatalf("appended %d tool results, want exactly 1 (only the trailing orphan)", len(added))
	}
	if added[0].ToolResult == nil || added[0].ToolResult.CallID != "call-new" {
		t.Fatalf("appended result = %+v, want a synthetic result for the trailing call-new", added[0].ToolResult)
	}
	if !added[0].ToolResult.IsError {
		t.Fatalf("appended call-new IsError = false, want true")
	}
	// The earlier answered turn must NOT receive a second (duplicate) result.
	var oldResults int
	for _, m := range s.Conversation.Messages {
		if m.Role == RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "call-old" {
			oldResults++
		}
	}
	if oldResults != 1 {
		t.Fatalf("call-old has %d tool results, want 1 (earlier turn untouched)", oldResults)
	}
}

// --- Recover (failed → idle, issue #51) -------------------------------------
//
// Recover is the third terminal-recovery seam, the sibling of Reopen
// (completed→idle) and Interrupt (cancelled→idle). It makes retry POSSIBLE
// (a structurally valid, provider-replayable history), not guaranteed — a
// permanent-cause failure simply fails again on the retried run.

func TestRecoverFromFailedReturnsToIdleAndResetsCounters(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 5})
	if err := s.RecordUserPrompt("first prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordAssistant(NewAssistantMessage("partial answer", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if s.State != StateFailed || s.Counters.Turns != 1 {
		t.Fatalf("precondition: state=%q turns=%d, want failed/1", s.State, s.Counters.Turns)
	}
	convLen := len(s.Conversation.Messages)

	if err := s.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if s.State != StateIdle {
		t.Fatalf("after Recover state = %q, want idle", s.State)
	}
	if s.Counters != (Counters{}) {
		t.Fatalf("after Recover counters = %+v, want zero (per-prompt budget)", s.Counters)
	}
	if r, ok := s.RecordedStopReason(); ok {
		t.Fatalf("after Recover recorded stop reason = %q, want none", r)
	}
	// No orphaned tool calls here, so history is preserved unchanged.
	if len(s.Conversation.Messages) != convLen {
		t.Fatalf("Recover changed clean history: len=%d, want %d", len(s.Conversation.Messages), convLen)
	}
	// The recovered session accepts a new prompt and another turn (retry).
	if err := s.RecordUserPrompt("second prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt after Recover: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn after Recover: %v", err)
	}
	if s.Counters.Turns != 1 {
		t.Fatalf("turns after recovered BeginTurn = %d, want 1 (counter was reset)", s.Counters.Turns)
	}
}

// TestRecoverIllegalFromNonFailedStates pins the NO-WIDENING invariant: Recover
// is failed-only — notably it stays illegal from completed (Reopen's seam) and
// cancelled (Interrupt's seam), so the three recovery methods never overlap.
func TestRecoverIllegalFromNonFailedStates(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"idle", func(*Session) {}},
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }},
		{"completed", func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() }},
		{"cancelled", func(s *Session) { _ = s.Cancel() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Recover(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Recover from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestRecoverClosesOutOrphanedToolCalls(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	before := len(s.Conversation.Messages)

	if err := s.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 2 {
		t.Fatalf("appended %d tool results, want 2", len(added))
	}
	wantIDs := []ToolCallID{"call-a", "call-b"}
	for i, m := range added {
		if m.Role != RoleTool {
			t.Fatalf("appended[%d] role = %q, want tool", i, m.Role)
		}
		if m.ToolResult == nil {
			t.Fatalf("appended[%d] has nil ToolResult", i)
		}
		if m.ToolResult.CallID != wantIDs[i] {
			t.Fatalf("appended[%d] CallID = %q, want %q (in order)", i, m.ToolResult.CallID, wantIDs[i])
		}
		if !m.ToolResult.IsError {
			t.Fatalf("appended[%d] IsError = false, want true (abort sentinel)", i)
		}
		// The synthetic text is durable MODEL-FACING replayed history: the
		// FAILURE seam must attribute the abort to the run failure — never to a
		// user cancellation that did not happen (the childAutoDenyMessage
		// accuracy discipline).
		if m.ToolResult.Content != recoverCloseOutMessage {
			t.Fatalf("appended[%d] content = %q, want %q (failure-accurate wording)", i, m.ToolResult.Content, recoverCloseOutMessage)
		}
		if m.ToolResult.Content == interruptCloseOutMessage {
			t.Fatalf("appended[%d] uses the cancellation wording on the failure path (false user-action attribution)", i)
		}
	}
	// The repaired history is provider-replayable: no dangling tool calls.
	if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatalf("recovered history fails tool pairing: %v", err)
	}
}

func TestRecoverPartialToolResultsCloseOutRemainder(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	// call-a already answered before the failure.
	if err := s.RecordToolResults([]ToolResult{NewToolResult("call-a", "ok")}); err != nil {
		t.Fatalf("RecordToolResults: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	before := len(s.Conversation.Messages)

	if err := s.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 1 {
		t.Fatalf("appended %d tool results, want 1 (only the missing call-b)", len(added))
	}
	if added[0].ToolResult == nil || added[0].ToolResult.CallID != "call-b" {
		t.Fatalf("appended result = %+v, want a synthetic result for call-b", added[0].ToolResult)
	}
	if !added[0].ToolResult.IsError {
		t.Fatalf("appended call-b IsError = false, want true")
	}
	if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatalf("recovered history fails tool pairing: %v", err)
	}
}

func TestRecoverNoOpWhenHistoryClean(t *testing.T) {
	t.Run("assistant with no tool calls", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("just answer", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := s.RecordAssistant(NewAssistantMessage("the answer", "", nil)); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
		if err := s.Fail(); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Recover(); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Recover appended to clean history: len=%d, want %d", len(s.Conversation.Messages), before)
		}
	})

	t.Run("fully answered turn (failure after results)", func(t *testing.T) {
		// Failure after a fully-answered turn: every trailing tool call already
		// has its result, so the repair is a no-op and the history stays valid.
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("one thing", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if err := s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{NewToolCall("call-a", "read_file", nil)})); err != nil {
			t.Fatalf("RecordAssistant: %v", err)
		}
		if err := s.RecordToolResults([]ToolResult{NewToolResult("call-a", "ok")}); err != nil {
			t.Fatalf("RecordToolResults: %v", err)
		}
		if err := s.Fail(); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Recover(); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Recover appended to fully-answered history: len=%d, want %d", len(s.Conversation.Messages), before)
		}
		if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
			t.Fatalf("clean recovered history fails tool pairing: %v", err)
		}
	})

	t.Run("trailing user prompt (failure before first token)", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.RecordUserPrompt("a prompt", nil); err != nil {
			t.Fatalf("RecordUserPrompt: %v", err)
		}
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		// Fail before any assistant message is recorded (a pre-first-chunk failure).
		if err := s.Fail(); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		before := len(s.Conversation.Messages)
		if err := s.Recover(); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if len(s.Conversation.Messages) != before {
			t.Fatalf("Recover appended to dangling-user history: len=%d, want %d", len(s.Conversation.Messages), before)
		}
	})
}

// TestSeedHistoryRejectsUnpaired pins the SeedHistory contract (issue #34): it
// seeds a FRESH (idle) session's history when the slice is tool-pairing-valid,
// rejects an unpaired slice (a dangling tool call) via ValidateToolPairing, and is
// illegal from any non-idle state.
func TestSeedHistoryRejectsUnpaired(t *testing.T) {
	call := func(id ToolCallID) Message {
		return NewAssistantMessage("", "", []ToolCall{NewToolCall(id, "Read", nil)})
	}
	result := func(id ToolCallID) Message { return NewToolMessage(NewToolResult(id, "ok")) }

	// Valid paired history seeds cleanly from idle.
	t.Run("valid pairing seeds from idle", func(t *testing.T) {
		s := newTestSession(Limits{})
		hist := []Message{NewUserMessage("goal"), call("c1"), result("c1")}
		if err := s.SeedHistory(hist); err != nil {
			t.Fatalf("SeedHistory(valid) from idle: %v", err)
		}
		if len(s.Conversation.Messages) != 3 {
			t.Fatalf("seeded history len = %d, want 3", len(s.Conversation.Messages))
		}
	})

	// A dangling tool call is rejected (would draw a provider 400 on replay).
	t.Run("unpaired slice rejected", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.SeedHistory([]Message{NewUserMessage("goal"), call("c1")}); err == nil {
			t.Fatalf("SeedHistory(dangling call) should reject")
		}
		if len(s.Conversation.Messages) != 0 {
			t.Fatalf("rejected seed must not mutate history: len=%d", len(s.Conversation.Messages))
		}
	})

	// An orphaned tool result is rejected too.
	t.Run("orphaned result rejected", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.SeedHistory([]Message{result("c1")}); err == nil {
			t.Fatalf("SeedHistory(orphaned result) should reject")
		}
	})

	// Illegal from a non-idle state.
	t.Run("non-idle rejected", func(t *testing.T) {
		s := newTestSession(Limits{})
		if err := s.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		err := s.SeedHistory([]Message{NewUserMessage("goal")})
		if !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("SeedHistory from running err = %v, want ErrIllegalTransition", err)
		}
	})
}

// TestRecordUsageAccumulates pins that RecordUsage element-wise sums onto the
// aggregate's cumulative Usage across multiple calls, so the aggregate is the
// durable twin of the loop's running total.
func TestRecordUsageAccumulates(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordUsage(Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10}); err != nil {
		t.Fatalf("RecordUsage #1: %v", err)
	}
	if err := s.RecordUsage(Usage{InputTokens: 50, OutputTokens: 5, CacheReadTokens: 30, CacheWriteTokens: 0}); err != nil {
		t.Fatalf("RecordUsage #2: %v", err)
	}
	want := Usage{InputTokens: 150, OutputTokens: 25, CacheReadTokens: 70, CacheWriteTokens: 10}
	if s.UsageFor(UsageKindMain) != want {
		t.Fatalf("accumulated Usage = %+v, want %+v", s.UsageFor(UsageKindMain), want)
	}
}

// TestRecordUsageRunningOnly pins the running-only guard: RecordUsage from any
// non-running state is an illegal transition and leaves Usage untouched (mirrors
// RecordAssistant / RecordToolResults).
func TestRecordUsageRunningOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drive func(*Session)
	}{
		{"idle", func(*Session) {}},
		{"awaiting", func(s *Session) {
			mustOK(t, s.BeginTurn())
			mustOK(t, s.PauseForApproval(PendingAsk{AskID: "a1", Tool: "Edit"}))
		}},
		{"completed", func(s *Session) {
			mustOK(t, s.BeginTurn())
			mustOK(t, s.Complete())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			tc.drive(s)
			err := s.RecordUsage(Usage{InputTokens: 99})
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("RecordUsage from %s err = %v, want ErrIllegalTransition", tc.name, err)
			}
			if s.UsageFor(UsageKindMain) != (Usage{}) {
				t.Fatalf("Usage mutated on rejected RecordUsage: %+v", s.UsageFor(UsageKindMain))
			}
		})
	}
}

// TestResetToIdlePreservesUsage is the CRITICAL divergence guard (cloud-native
// Phase 1): the three terminal-recovery seams (Reopen / Interrupt / Recover) all
// route through resetToIdle, which clears the Counters but DELIBERATELY preserves
// the cumulative Usage so the MaxRunTokens budget brake survives reopen/restart.
// Mutation: adding `s.UsageFor(UsageKindMain) = Usage{}` to resetToIdle must fail this test.
func TestResetToIdlePreservesUsage(t *testing.T) {
	spend := Usage{InputTokens: 5000, OutputTokens: 1200, CacheReadTokens: 100, CacheWriteTokens: 50}

	t.Run("Reopen", func(t *testing.T) {
		s := newTestSession(Limits{})
		mustOK(t, s.BeginTurn())
		mustOK(t, s.RecordUsage(spend))
		mustOK(t, s.Complete())
		mustOK(t, s.Reopen())
		if s.UsageFor(UsageKindMain) != spend {
			t.Fatalf("Reopen cleared Usage: got %+v, want %+v (the budget must survive)", s.UsageFor(UsageKindMain), spend)
		}
		if s.Counters != (Counters{}) {
			t.Fatalf("Reopen did NOT reset Counters: %+v", s.Counters)
		}
	})

	t.Run("Interrupt", func(t *testing.T) {
		s := newTestSession(Limits{})
		mustOK(t, s.BeginTurn())
		mustOK(t, s.RecordUsage(spend))
		mustOK(t, s.Cancel())
		mustOK(t, s.Interrupt())
		if s.UsageFor(UsageKindMain) != spend {
			t.Fatalf("Interrupt cleared Usage: got %+v, want %+v", s.UsageFor(UsageKindMain), spend)
		}
	})

	t.Run("Recover", func(t *testing.T) {
		s := newTestSession(Limits{})
		mustOK(t, s.BeginTurn())
		mustOK(t, s.RecordUsage(spend))
		mustOK(t, s.Fail())
		mustOK(t, s.Recover())
		if s.UsageFor(UsageKindMain) != spend {
			t.Fatalf("Recover cleared Usage: got %+v, want %+v", s.UsageFor(UsageKindMain), spend)
		}
	})
}

func TestAbandonFromRunningClosesOutOrphansAndIdles(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	// The session is left StateRunning here — no Cancel, no Fail — mirroring
	// the crash-orphaned snapshot (issue #475): nothing observed a
	// cancellation or a failure, so neither of those seams applies.
	if s.State != StateRunning {
		t.Fatalf("precondition: state = %q, want running", s.State)
	}
	before := len(s.Conversation.Messages)

	if err := s.Abandon(); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if s.State != StateIdle {
		t.Fatalf("after Abandon state = %q, want idle", s.State)
	}
	added := s.Conversation.Messages[before:]
	if len(added) != 2 {
		t.Fatalf("appended %d tool results, want 2", len(added))
	}
	wantIDs := []ToolCallID{"call-a", "call-b"}
	for i, m := range added {
		if m.Role != RoleTool || m.ToolResult == nil {
			t.Fatalf("appended[%d] = %+v, want a tool-role synthetic result", i, m)
		}
		if m.ToolResult.CallID != wantIDs[i] {
			t.Fatalf("appended[%d] CallID = %q, want %q (in order)", i, m.ToolResult.CallID, wantIDs[i])
		}
		if !m.ToolResult.IsError {
			t.Fatalf("appended[%d] IsError = false, want true (abandonment sentinel)", i)
		}
		if m.ToolResult.Content != abandonCloseOutMessage {
			t.Fatalf("appended[%d] content = %q, want the abandon wording", i, m.ToolResult.Content)
		}
		if m.ToolResult.Content == interruptCloseOutMessage {
			t.Fatalf("appended[%d] content reused interruptCloseOutMessage — must be its own wording", i)
		}
		if m.ToolResult.Content == recoverCloseOutMessage {
			t.Fatalf("appended[%d] content reused recoverCloseOutMessage — must be its own wording", i)
		}
	}
	if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatalf("abandoned history fails tool pairing: %v", err)
	}
}

func TestAbandonRefusedOutsideRunning(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(*Session)
	}{
		{"idle", func(*Session) {}},
		{"awaiting", func(s *Session) { _ = s.BeginTurn(); _ = s.PauseForApproval(PendingAsk{}) }},
		{"completed", func(s *Session) { _ = s.BeginTurn(); _ = s.Complete() }},
		{"failed", func(s *Session) { _ = s.Fail() }},
		{"cancelled", func(s *Session) { _ = s.BeginTurn(); _ = s.Cancel() }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.Abandon(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Abandon from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
		})
	}
}

func TestAbandonPreservesUsage(t *testing.T) {
	spend := Usage{InputTokens: 5000, OutputTokens: 1200, CacheReadTokens: 100, CacheWriteTokens: 50}

	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	mustOK(t, s.RecordUsage(spend))
	if s.State != StateRunning {
		t.Fatalf("precondition: state = %q, want running", s.State)
	}
	mustOK(t, s.Abandon())
	if s.UsageFor(UsageKindMain) != spend {
		t.Fatalf("Abandon cleared Usage: got %+v, want %+v (the budget must survive)", s.UsageFor(UsageKindMain), spend)
	}
	if s.Counters != (Counters{}) {
		t.Fatalf("Abandon did NOT reset Counters: %+v", s.Counters)
	}
}

// TestAbandonIsIdempotentAgainstAlreadyRepairedHistory pins the claim the
// composition-level staleness sweep relies on (issue #475 design §4): a
// second application of the SAME repair mechanism against an already-repaired
// history is a no-op, since two replicas' sweeps can both Load the same stale
// snapshot before either writes back. closeOutInterruptedTurn builds its
// "already answered" set from the messages following the last assistant
// turn, so re-running it after the first Abandon's synthetic results have
// already been appended matches nothing and appends nothing further. Abandon
// itself cannot be called twice in a row on the same object (the first call's
// resetToIdle leaves the session StateIdle, and a second Abandon there
// correctly refuses per TestAbandonRefusedOutsideRunning) — this test instead
// pins the underlying repair helper's idempotency directly.
func TestAbandonIsIdempotentAgainstAlreadyRepairedHistory(t *testing.T) {
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("do two things", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []ToolCall{
		NewToolCall("call-a", "read_file", nil),
		NewToolCall("call-b", "list_dir", nil),
	}
	if err := s.RecordAssistant(NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	s.closeOutInterruptedTurn(abandonCloseOutMessage)
	after1 := len(s.Conversation.Messages)

	// A second replica's sweep re-applies the SAME repair against the SAME
	// (already-repaired) history before either has written back.
	s.closeOutInterruptedTurn(abandonCloseOutMessage)

	if len(s.Conversation.Messages) != after1 {
		t.Fatalf("second closeOutInterruptedTurn changed history length: got %d, want %d (no-op)", len(s.Conversation.Messages), after1)
	}
	if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatalf("twice-repaired history fails tool pairing: %v", err)
	}
}

// TestRecordUsageKeepsLifetimeCompatibilityMirror pins the deprecated Usage
// projection to the canonical main ledger. It never resets across terminal
// lifecycle transitions.
func TestRecordUsageKeepsLifetimeCompatibilityMirror(t *testing.T) {
	s := newTestSession(Limits{})
	first := Usage{InputTokens: 5000, OutputTokens: 1200}
	second := Usage{InputTokens: 300, OutputTokens: 70}
	mustOK(t, s.BeginTurn())
	mustOK(t, s.RecordUsage(first))
	mustOK(t, s.Complete())
	mustOK(t, s.Reopen())
	mustOK(t, s.BeginTurn())
	mustOK(t, s.RecordUsage(second))

	want := first.Add(second)
	if s.UsageFor(UsageKindMain) != want {
		t.Fatalf("Usage = %+v, want lifetime main total %+v", s.UsageFor(UsageKindMain), want)
	}
	if got := s.TokenUsageSnapshot()[UsageKindMain].Total; got != want {
		t.Fatalf("TokenUsageSnapshot()[main].Total = %+v, want %+v", got, want)
	}
}

// TestLimitsWithDefaults covers the per-field merge: an all-zero Limits inherits
// every default, a partially-set Limits keeps its non-zero caps and inherits the
// rest, and a fully-set Limits is returned verbatim.
func TestLimitsWithDefaults(t *testing.T) {
	def := Limits{MaxTurns: 1000, MaxToolCalls: 4000, MaxConsecutiveFailures: 5}

	t.Run("all-zero inherits every default", func(t *testing.T) {
		if got := (Limits{}).WithDefaults(def); got != def {
			t.Fatalf("got %+v, want the full default %+v", got, def)
		}
	})

	t.Run("only MaxTurns pinned keeps the rest", func(t *testing.T) {
		got := Limits{MaxTurns: 7}.WithDefaults(def)
		want := Limits{MaxTurns: 7, MaxToolCalls: 4000, MaxConsecutiveFailures: 5}
		if got != want {
			t.Fatalf("got %+v, want %+v (MaxTurns pinned, the rest inherited — never zeroed)", got, want)
		}
	})
	t.Run("fully-set is returned verbatim", func(t *testing.T) {
		full := Limits{MaxTurns: 1, MaxToolCalls: 2, MaxConsecutiveFailures: 3}
		if got := full.WithDefaults(def); got != full {
			t.Fatalf("got %+v, want the verbatim %+v", got, full)
		}
	})
}

// TestRecordFailureMetadataRejectsNonFailed asserts the state guard:
// retry metadata is legal only from StateFailed.
func TestRecordFailureMetadataRejectsNonFailed(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(s *Session)
	}{
		{"idle", func(*Session) {}},
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) {
			_ = s.BeginTurn()
			_ = s.PauseForApproval(PendingAsk{})
		}},
		{"completed", func(s *Session) {
			_ = s.BeginTurn()
			mustOK(t, s.Complete())
		}},
		{"cancelled", func(s *Session) { mustOK(t, s.Cancel()) }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.RecordFailureMetadata(RetryMetadata{Disposition: RetryDispositionPermanent}); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("RecordFailureMetadata from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
			if got := s.FailureMetadata(); got != (RetryMetadata{}) {
				t.Fatalf("FailureMetadata from %s = %+v, want zero", mk.name, got)
			}
		})
	}
}

// TestRecordFailureMetadataRoundTrip asserts that recovery clears typed metadata.
func TestRecordFailureMetadataRoundTrip(t *testing.T) {
	s := newTestSession(Limits{})
	mustOK(t, s.RecordUserPrompt("prompt", nil))
	mustOK(t, s.BeginTurn())
	mustOK(t, s.Fail())
	want := RetryMetadata{Disposition: RetryDispositionPermanent, Progress: StreamProgressVisible}
	mustOK(t, s.RecordFailureMetadata(want))
	if got := s.FailureMetadata(); got != want {
		t.Fatalf("FailureMetadata after stamp = %+v, want %+v", got, want)
	}
	mustOK(t, s.Recover())
	if got := s.FailureMetadata(); got != (RetryMetadata{}) {
		t.Fatalf("FailureMetadata after Recover = %+v, want zero", got)
	}
	if s.State != StateIdle {
		t.Fatalf("after Recover state = %q, want idle", s.State)
	}
}

// TestRecordLastErrorStateGuardAndClear asserts the state guard (legal ONLY from
// StateFailed, mirroring RecordFailurePermanence), the one-line collapse + 400-rune
// clamp, and that Recover/Reopen clear the cause via resetToIdle so a recovered
// session never keeps a stale cause. Mirrors the permanence test shape.
func TestRecordLastErrorStateGuardAndClear(t *testing.T) {
	for _, mk := range []struct {
		name  string
		setup func(s *Session)
	}{
		{"idle", func(*Session) {}},
		{"running", func(s *Session) { _ = s.BeginTurn() }},
		{"awaiting", func(s *Session) {
			_ = s.BeginTurn()
			_ = s.PauseForApproval(PendingAsk{})
		}},
		{"completed", func(s *Session) {
			_ = s.BeginTurn()
			mustOK(t, s.Complete())
		}},
		{"cancelled", func(s *Session) { mustOK(t, s.Cancel()) }},
	} {
		t.Run(mk.name, func(t *testing.T) {
			s := newTestSession(Limits{})
			mk.setup(s)
			if err := s.RecordLastError("boom"); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("RecordLastError from %s: err = %v, want ErrIllegalTransition", mk.name, err)
			}
			if s.LastError() != "" {
				t.Fatalf("LastError from %s = %q, want empty (not set)", mk.name, s.LastError())
			}
		})
	}

	// Full lifecycle: Fail() → RecordLastError → LastError() → Recover clears it.
	s := newTestSession(Limits{})
	if err := s.RecordUserPrompt("prompt", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	mustOK(t, s.BeginTurn())
	mustOK(t, s.Fail())
	if s.State != StateFailed {
		t.Fatalf("precondition: state = %q, want failed", s.State)
	}
	if s.LastError() != "" {
		t.Fatal("LastError before stamp = non-empty, want empty")
	}
	mustOK(t, s.RecordLastError("upstream 503: model overloaded"))
	if got, want := s.LastError(), "upstream 503: model overloaded"; got != want {
		t.Fatalf("LastError after stamp = %q, want %q", got, want)
	}
	mustOK(t, s.Recover())
	if s.LastError() != "" {
		t.Fatal("LastError after Recover = non-empty, want empty (cleared by resetToIdle)")
	}

	// Reopen from a completed-then-failed cycle also clears it.
	mustOK(t, s.BeginTurn())
	mustOK(t, s.Complete())
	mustOK(t, s.Reopen())
	if s.LastError() != "" {
		t.Fatal("LastError after Reopen = non-empty, want empty (cleared by resetToIdle)")
	}
}

// TestRecordLastErrorNormalisesAndClamps asserts the cause is collapsed to ONE line
// (whitespace → single spaces) and clamped to maxSnapshotErrorRunes (400) + ellipsis,
// mirroring the event-side subagentCausePayload normaliser so the snapshot and the
// event agree byte-for-byte on the persisted cause.
func TestRecordLastErrorNormalisesAndClamps(t *testing.T) {
	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	mustOK(t, s.Fail())

	// One-line collapse: newlines/tabs/extra spaces → single spaces.
	mustOK(t, s.RecordLastError("BOOM-\n  upstream detail\n\tdetail"))
	if got, want := s.LastError(), "BOOM- upstream detail detail"; got != want {
		t.Fatalf("LastError not collapsed to one line: got %q, want %q", got, want)
	}
	if strings.ContainsAny(s.LastError(), "\n\r\t") {
		t.Fatalf("LastError must contain no raw line/control separators, got %q", s.LastError())
	}

	// Rune clamp: 5000 runes → 400 + ellipsis.
	huge := strings.Repeat("z", 5000)
	mustOK(t, s.RecordLastError(huge))
	if n := len([]rune(s.LastError())); n != maxSnapshotErrorRunes+1 {
		t.Fatalf("LastError was not clamped: %d runes, want %d+1 (the ellipsis)", n, maxSnapshotErrorRunes)
	}
	if !strings.HasSuffix(s.LastError(), "…") {
		t.Fatalf("LastError must end with the truncation ellipsis, got %q", s.LastError())
	}
}

// TestSnapshotCauseMirrorsEventCauseContract pins the DOC-COMMENT'S byte-for-byte
// agreement claim between the session-local snapshot normaliser
// (normaliseSnapshotError / clampSnapshotRunes / maxSnapshotErrorRunes) and the
// event-side engine/agent subagentCausePayload / clampRunes /
// maxSubagentCausePreview. The layering rule forbids session importing
// engine/agent, so this session-package test pins its OWN side against the
// hardcoded outputs the agent algorithm produces; a companion test in
// engine/agent (TestSnapshotAndEventCauseAgreement) pins the agent side against
// the same expectations. A change to either side's algorithm or constant breaks
// exactly one of the two, tripping CI on the drift.
func TestSnapshotCauseMirrorsEventCauseContract(t *testing.T) {
	if maxSnapshotErrorRunes != 400 {
		t.Fatalf("maxSnapshotErrorRunes = %d, must stay 400 to agree with engine/agent's maxSubagentCausePreview", maxSnapshotErrorRunes)
	}
	// Each expectation is the output engine/agent's subagentCausePayload produces
	// (strings.Join(strings.Fields(in), " ") then clampRunes(400) with the
	// ellipsis appended on truncation).
	for _, tc := range []struct{ in, want string }{
		{"upstream 503: model overloaded", "upstream 503: model overloaded"},
		{"BOOM-\n  upstream detail\n\tdetail", "BOOM- upstream detail detail"},
		{"  leading and trailing  ", "leading and trailing"},
		{"", ""},
		{strings.Repeat("z", 5000), strings.Repeat("z", 400) + "…"},
	} {
		if got := normaliseSnapshotError(tc.in); got != tc.want {
			t.Errorf("normaliseSnapshotError(%q) = %q, want the event-side output %q", tc.in, got, tc.want)
		}
	}
}

func TestReplaceHistoryAtBoundaryStateAndMetadataContract(t *testing.T) {
	setups := []struct {
		name  string
		legal bool
		setup func(*Session)
	}{
		{"idle", true, func(*Session) {}},
		{"running", false, func(s *Session) { mustOK(t, s.BeginTurn()) }},
		{"awaiting", false, func(s *Session) {
			mustOK(t, s.BeginTurn())
			mustOK(t, s.PauseForApproval(PendingAsk{AskID: "ask", Tool: "Read"}))
		}},
		{"completed", true, func(s *Session) { mustOK(t, s.BeginTurn()); mustOK(t, s.Complete()) }},
		{"cancelled", true, func(s *Session) { mustOK(t, s.Cancel()) }},
		{"failed", true, func(s *Session) { mustOK(t, s.BeginTurn()); mustOK(t, s.Fail()); mustOK(t, s.RecordLastError("boom")) }},
	}
	for _, tc := range setups {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(Limits{MaxTurns: 9})
			mustOK(t, s.RecordUserPrompt("original", nil))
			tc.setup(s)
			s.Counters = Counters{Turns: 7, ToolCalls: 3, ConsecutiveFailures: 2}
			s.RestoreTokenUsage(map[UsageKind]TokenUsage{UsageKindMain: {Models: map[string]Usage{"unknown": {InputTokens: 101, OutputTokens: 17}}}})
			s.ProviderID, s.ModelID, s.Profile = "provider", "model", "no-fs"
			s.EnvironmentRef = EnvironmentRef{Kind: "remote", ID: "env"}
			s.Owner = &Principal{Issuer: "issuer", Subject: "owner", GrantType: GrantTypeUser}
			replacement := []Message{NewUserMessage("compacted")}
			before := *s
			beforeConversation := *s.Conversation
			before.Conversation = &beforeConversation

			err := s.ReplaceHistoryAtBoundary(replacement)
			if !tc.legal {
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("error = %v, want ErrIllegalTransition", err)
				}
				if !reflect.DeepEqual(*s, before) {
					t.Fatal("rejected replacement mutated aggregate")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReplaceHistoryAtBoundary: %v", err)
			}
			before.Conversation.Messages = replacement
			if !reflect.DeepEqual(*s, before) {
				t.Fatal("replacement changed metadata beyond conversation history")
			}
		})
	}

	t.Run("invalid pairing is atomic", func(t *testing.T) {
		s := newTestSession(Limits{})
		mustOK(t, s.RecordUserPrompt("original", nil))
		before := CloneMessages(s.Conversation.Messages)
		orphan := []Message{NewToolMessage(NewToolResult("missing", "bad"))}
		if err := s.ReplaceHistoryAtBoundary(orphan); err == nil {
			t.Fatal("accepted orphaned tool result")
		}
		if !reflect.DeepEqual(s.Conversation.Messages, before) {
			t.Fatal("invalid replacement mutated history")
		}
	})
}
