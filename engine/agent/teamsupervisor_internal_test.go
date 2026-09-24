package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// noopHookRunner is a no-op port.HookRunner: a runner is PRESENT (so the hook
// dispatch path executes) but no hook is configured (the in-package twin of the
// agent_test package's noopHooks, unreachable from this internal test file).
type noopHookRunner struct{}

func (noopHookRunner) Run(context.Context, governance.HookEvent) (port.HookResult, error) {
	return port.HookResult{}, nil
}

// recordedWarn captures one Diagnostics record for the WI-9 helper test.
type recordedWarn struct {
	level port.Level
	msg   string
	args  []any
}

// capturingDiag is a minimal in-package recording Diagnostics stub (the external
// recordingDiag lives in the agent_test package and is unreachable from this internal
// test file).
type capturingDiag struct {
	lines []recordedWarn
}

func (d *capturingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.lines = append(d.lines, recordedWarn{level: level, msg: msg, args: args})
}

func (d *capturingDiag) With(...any) port.Diagnostics { return d }

func warnArgValue(line recordedWarn, key string) any {
	for i := 0; i+1 < len(line.args); i += 2 {
		if k, ok := line.args[i].(string); ok && k == key {
			return line.args[i+1]
		}
	}
	return nil
}

// TestWarnUnexpectedRecovery exercises the WI-9 helper directly: it emits exactly one
// WARN only when a member could not be returned to idle for a reason OTHER than
// cancellation, rides the supplied diag, and is a no-op (no panic) for the
// nil-diag/expected cases. Both recovery seams (Reopen and, for a StopError member,
// Recover — issue #318) report through it.
func TestWarnUnexpectedRecovery(t *testing.T) {
	t.Run("nil reopen error → no line", func(t *testing.T) {
		d := &capturingDiag{}
		warnUnexpectedRecovery(context.Background(), d, "worker", session.StopMaxTurns, nil)
		if len(d.lines) != 0 {
			t.Fatalf("expected no diagnostic for a nil reopen error; got %+v", d.lines)
		}
	})
	t.Run("cancelled stop with error → no line", func(t *testing.T) {
		d := &capturingDiag{}
		warnUnexpectedRecovery(context.Background(), d, "worker", session.StopCancelled, errors.New("reopen: not completed"))
		if len(d.lines) != 0 {
			t.Fatalf("a cancelled member's expected reopen failure must not warn; got %+v", d.lines)
		}
	})
	t.Run("unexpected reopen failure → one warn", func(t *testing.T) {
		d := &capturingDiag{}
		warnUnexpectedRecovery(context.Background(), d, "worker", session.StopMaxTurns, errors.New("boom"))
		if len(d.lines) != 1 {
			t.Fatalf("expected exactly one WARN line; got %+v", d.lines)
		}
		line := d.lines[0]
		if line.level != port.LevelWarn {
			t.Fatalf("level = %v, want LevelWarn", line.level)
		}
		if got := warnArgValue(line, "member"); got != "worker" {
			t.Fatalf("member attr = %v, want %q", got, "worker")
		}
		if got := warnArgValue(line, "error"); got != "boom" {
			t.Fatalf("error attr = %v, want %q", got, "boom")
		}
	})
	t.Run("nil diag → no panic", func(_ *testing.T) {
		warnUnexpectedRecovery(context.Background(), nil, "worker", session.StopMaxTurns, errors.New("boom"))
	})
}

// TestRunTurnCancelledMemberCapturesTurnsUsed replicates the cancelled-member scenario
// (a request observer cancels the ctx as the worker's turn reaches the provider, so the
// member ends StopCancelled) and asserts the internal memberRT state: the cancelled
// member's turn spend is captured and counted (turnsUsed == 1) despite its failed
// Reopen (Reopen is completed-only, so it always fails for a cancelled member), and
// the member is classified StopReasonCancelled.
func TestRunTurnCancelledMemberCapturesTurnsUsed(t *testing.T) {
	tm := team.New("t")
	ctx, cancel := context.WithCancel(context.Background())
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		prov := mockllm.NewWith(
			[]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { cancel() })},
			mockllm.TextTurn("never reached cleanly"),
		)
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHookRunner{}, Model: "mock",
		})}
	}
	sup := NewSupervisor(tm, memEnv("/ws"), factory, WithMaxRounds(5))
	if err := sup.AddMember(context.Background(), MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	out := sup.Run(ctx, nil)

	// The outcome must report the single member as stopped/cancelled (the scenario
	// actually fired), so the internal-state assertions below mean something.
	if len(out.Members) != 1 {
		t.Fatalf("outcome members = %d, want 1: %+v", len(out.Members), out.Members)
	}
	if mo := out.Members[0]; mo.Disposition != DispositionStopped || mo.Reason != StopReasonCancelled {
		t.Fatalf("outcome disposition = %q/%q, want stopped/cancelled", mo.Disposition, mo.Reason)
	}

	m, ok := sup.members["worker"]
	if !ok {
		t.Fatalf("supervisor has no member %q; members=%v", "worker", sup.members)
	}
	if m.turnsUsed != 1 {
		t.Fatalf("turnsUsed = %d, want 1 (the cancelled member's turn spend must be captured and counted)", m.turnsUsed)
	}
	if m.stopReason != StopReasonCancelled {
		t.Fatalf("stopReason = %q, want %q", m.stopReason, StopReasonCancelled)
	}
}

// TestCleanupAllAttributesIdleClientCancel pins the cleanupAll stop attribution
// (I2 panel fix): a member whose per-member cancel fired while it idled in the
// FINAL round (so planRound never ran again to observe it) reaches cleanupAll
// un-stopped — its registry entry must land StopCancelled, attributed from the
// member ctx BEFORE cleanupAll's own release-cancel fires — while an untouched
// member lands StopEndTurn.
func TestCleanupAllAttributesIdleClientCancel(t *testing.T) {
	tm := team.New("demo")
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(spec MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: cat,
			Policy:  allow,
			Hooks:   noopHookRunner{},
			Model:   "mock",
		})}
	}
	reg := newChildRunRegistry()
	sup := NewSupervisor(tm, memEnv("/ws"), factory,
		withParentCaps(parentCaps{children: reg}))
	ctx := context.Background()
	if err := sup.AddMember(ctx, MemberSpec{Name: "lead", Lead: true}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, MemberSpec{Name: "worker"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}

	// The client cancel lands while the worker idles and NO further round plans
	// (the final-round case): cleanupAll is the only path left to mark it done.
	if !sup.CancelMember("worker") {
		t.Fatalf("CancelMember must return true for an enrolled member")
	}
	sup.cleanupAll()

	stopOf := func(id string) session.StopReason {
		t.Helper()
		reg.mu.Lock()
		defer reg.mu.Unlock()
		e := reg.entries[id]
		if e == nil || e.state != childDone {
			t.Fatalf("registry entry %q missing or not done", id)
		}
		return e.stop
	}
	if got := stopOf(string(sup.members["worker"].sess.ID)); got != session.StopCancelled {
		t.Fatalf("idle-cancelled member's registry stop = %q, want %q (attribution must precede the release-cancel)",
			got, session.StopCancelled)
	}
	// The lead NEVER ran a drive and was never cancelled: under the A5 state
	// vocabulary cleanupAll REMOVES its entry (a pre-start abort) rather than
	// fabricating done-with-StopEndTurn — the ghost this team had before.
	reg.mu.Lock()
	_, leadPresent := reg.entries[string(sup.members["lead"].sess.ID)]
	reg.mu.Unlock()
	if leadPresent {
		t.Fatalf("a never-driven, never-cancelled member must be REMOVED at cleanupAll (A5), not left as a done entry")
	}
}

// fakeRunner is a minimal tool.CommandRunner for supervisor tests that need a
// non-nil runner on the base Environment.
type fakeRunner struct{}

func (fakeRunner) Run(context.Context, string) (tool.CommandResult, error) {
	return tool.CommandResult{}, nil
}

func (fakeRunner) RunWithEnvironment(context.Context, string, tool.CommandEnvironmentOverlay) (tool.CommandResult, error) {
	return tool.CommandResult{}, nil
}

// TestBaseSharingReadOnlyMemberIsShellless proves the issue-#462 review fix: a
// base-sharing (default read-only) member's Environment carries a NIL runner as
// defense-in-depth, even when the parent (base) Environment has a bound runner.
// It MUST NOT reuse the parent runner — a base-sharing member has no shell, and
// a future mis-wire must not hand it the parent's shell via the base Environment.
func TestBaseSharingReadOnlyMemberIsShellless(t *testing.T) {
	tm := team.New("t")
	// Build a base Environment WITH a runner — the parent has a shell.
	base := memEnvRunner("/ws", fakeRunner{})
	if base.CommandRunner() == nil {
		t.Fatal("precondition: base Environment must carry a runner")
	}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(_ MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, "ro", nil) {
			cat.MustRegister(tl)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	// No forker wired → the member base-shares (no IsolateReadOnly, no Mutating).
	sup := NewSupervisor(tm, base, factory)
	if err := sup.AddMember(context.Background(), MemberSpec{Name: "ro"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	m, ok := sup.members["ro"]
	if !ok {
		t.Fatalf("member %q not found", "ro")
	}
	if m.env.CommandRunner() != nil {
		t.Errorf("base-sharing read-only member env has a non-nil runner %T — "+
			"it must carry nil as defense-in-depth (must not reuse the parent runner)", m.env.CommandRunner())
	}
	// The base Environment is untouched — the parent runner survives.
	if base.CommandRunner() == nil {
		t.Error("the base Environment's runner was lost — the member must not mutate the base")
	}
}

// TestSynthesisBudgetBaselineSurvivesNudge proves the lead's fresh allowance is
// immutable for the whole synthesis Run, including its no-progress re-drive, while
// the session's lifetime main accounting stays intact.
func TestSynthesisBudgetBaselineSurvivesNudge(t *testing.T) {
	tm := team.New("baseline")
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	provider := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("working complete"),
			mockllm.UsageChunk(session.Usage{InputTokens: 100}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.UsageChunk(session.Usage{InputTokens: 40}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("synthesis after nudge"),
			mockllm.UsageChunk(session.Usage{InputTokens: 20}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	factory := func(_ MemberSpec, _ string) MemberBuild {
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: provider, Catalog: tool.NewCatalog(), Policy: allow, Hooks: noopHookRunner{},
			Model: "mock", MaxRunTokens: 50,
		})}
	}
	sup := NewSupervisor(tm, memEnv("/ws"), factory, WithMaxRounds(1))
	if err := sup.AddMember(context.Background(), MemberSpec{Name: "lead", Lead: true, InitialPrompt: "work then report"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	out := sup.Run(context.Background(), nil)
	if out.Report != "synthesis after nudge" {
		t.Fatalf("report = %q, want synthesis after no-progress nudge", out.Report)
	}
	lead := sup.members["lead"].sess
	want := session.Usage{InputTokens: 160}
	if got := lead.UsageFor(session.UsageKindMain); got != want || lead.TokenUsageSnapshot()[session.UsageKindMain].Total != want {
		t.Fatalf("lifetime main usage = %+v / %+v, want %+v", got, lead.TokenUsageSnapshot()[session.UsageKindMain].Total, want)
	}
}
