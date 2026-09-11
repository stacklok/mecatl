package server_test

// Scenario 3 of docs/acceptance/steer-while-running.md: the LOST TERMINAL RACE —
// a steer arrives for a session whose run is already terminal (the user's "still
// running" belief lagged the real state). The Service-level routing decision
// (Service.Steer) either enqueues the text to the LIVE run's steer inbox or, on a
// terminal race (no live run / the engine reports steer-too-late), promotes it
// into a fresh follow-up run through the EXISTING hardened run-entry funnel
// (StartRunContent → loadAndReopen → the lease + recover-if-terminal) — it never
// drops the text silently and never starts a run on an unrepaired terminal
// session.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// blockingTextTool blocks holding the run mid-dispatch (genuinely LIVE) until
// its release channel is closed, then returns a fixed result. Unlike
// blockingTool it does NOT wait on ctx — the test releases it after the steer is
// enqueued so the steer drains at the next turn boundary on a still-live run.
type blockingTextTool struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingTextTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "Read: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*blockingTextTool) ReadOnly() bool { return true }
func (b *blockingTextTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	close(b.started)
	select {
	case <-b.release:
		return session.NewToolResult(in.ID, "read ok"), nil
	case <-ctx.Done(): // run cancelled while parked mid-dispatch (the cancelled case)
		return session.ToolResult{}, ctx.Err()
	}
}

var _ tool.Tool = (*blockingTextTool)(nil)

// newSteerService builds a Service whose engine arms the steer inbox
// (Deps.EnableSteer) and persists in-loop via Deps.Store, over the given mockllm
// script + tools + policy rules. The engine is interactive because these are
// wire-facing controls and may need to keep an approval ask parked. memstore is
// shared between the Service and the engine so an in-loop
// Cancelled/Failed/Fail lands in the same store the run-entry funnel reads.
func newSteerService(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, tools ...tool.Tool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range tools {
		cat.MustRegister(tl)
	}
	if rules == nil {
		rules = permpolicy.AllowAllFloorRules()
	}
	store := memstore.New()
	engine := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     cat,
		Policy:      permpolicy.NewPolicy(rules, nil),
		Model:       "test-model",
		Interactive: true,
		Store:       store,
		EnableSteer: true,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now: func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new steer service: %v", err)
	}
	return svc
}

// steerUserTexts returns the recorded genuine user-message texts, in order.
func steerUserTexts(sess *session.Session) []string {
	var out []string
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser {
			out = append(out, m.Text)
		}
	}
	return out
}

// drainForResult consumes a run's events and returns its terminal EvResult stop.
func drainForResult(t *testing.T, run *agent.Run) session.StopReason {
	t.Helper()
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	return stop
}

func assertSteerState(t *testing.T, svc *server.Service, id session.SessionID, want session.State) {
	t.Helper()
	got, err := svc.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.State != want {
		t.Fatalf("precondition: session state = %q, want %q", got.State, want)
	}
}

// TestSteer_LiveRunEnqueues (AC3.1): a steer arriving on a genuinely LIVE run is
// enqueued to that run's inbox (the engine accepts/supersedes it) and the SAME
// run drains it at the next boundary — it is NOT promoted to a new run.
func TestSteer_LiveRunEnqueues(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	// Turn 1: call the blocking tool (the run parks mid-dispatch, still live).
	// Turn 2: the post-steer turn answers with text. A THIRD model call would only
	// happen if a spurious promoted run also started (mockllm has no third scripted
	// turn → an exhausted cursor drives the no-progress path, which would NOT end
	// the run StopEndTurn — catching an accidental promotion as a wrong stop).
	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "look at a.go", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	<-block.started // the run is genuinely mid-dispatch → live.

	image, err := session.NewImageContent("image/png", []byte("pixels"))
	if err != nil {
		t.Fatal(err)
	}
	outc, promoted, _, err := svc.Steer(ctx, sess.ID, "steer mid-flight", []session.Content{image}, "", "")
	if err != nil {
		t.Fatalf("Steer on a live run: %v", err)
	}
	if promoted {
		t.Fatalf("steer on a live run must NOT be promoted; got promoted=true")
	}
	if outc != agent.SteerAccepted && outc != agent.SteerAppended {
		t.Fatalf("steer on a live run outcome = %q, want accepted/appended", outc)
	}

	// Let the tool finish; the run continues and drains the steer at the next
	// boundary (the Step 2a drain fires BEFORE BeginTurn of turn 2).
	close(block.release)

	var sawSteerEcho bool
	stop := session.StopReason("")
	for ev := range run.Events() {
		if ev.Type == session.EvSteer && ev.Steer != nil && ev.Steer.Text == "steer mid-flight" {
			if len(ev.Steer.Parts) != 1 || string(ev.Steer.Parts[0].Data) != "pixels" {
				t.Fatalf("steer echo parts = %#v", ev.Steer.Parts)
			}
			sawSteerEcho = true
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, run)

	if stop != session.StopEndTurn {
		t.Fatalf("live run stop = %q, want end_turn (a spurious extra model call would change this)", stop)
	}
	if !sawSteerEcho {
		t.Fatalf("live run never emitted EvSteer carrying the steer text (steer not drained in-run)")
	}
	got, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	texts := steerUserTexts(got)
	if len(texts) != 2 || texts[0] != "look at a.go" || texts[1] != "steer mid-flight" {
		t.Fatalf("user messages = %v, want [look at a.go, steer mid-flight] (steer recorded into the SAME live run)", texts)
	}
	messages := got.Conversation.Messages
	if len(messages) < 3 || len(messages[len(messages)-2].Parts) != 1 || string(messages[len(messages)-2].Parts[0].Data) != "pixels" {
		t.Fatalf("recorded steer media missing: %#v", messages)
	}
}

// TestSteer_TerminalRacePromotes (AC3.2): a steer arriving AFTER the run went
// terminal is promoted to a fresh follow-up run that drives through the
// run-entry funnel — the text is NOT silently dropped and the promoted run
// records the steer as its user prompt.
func TestSteer_TerminalRacePromotes(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	svc := newSteerService(t, llm, nil)
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "hello", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(sess.ID, run)
	assertSteerState(t, svc, sess.ID, session.StateCompleted)

	// The user's "still running" belief lagged: no run is live now.
	image, err := session.NewImageContent("image/png", []byte("late-pixels"))
	if err != nil {
		t.Fatal(err)
	}
	outc, promoted, run2, err := svc.Steer(ctx, sess.ID, "", []session.Content{image}, "", "")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if !promoted {
		t.Fatalf("terminal steer must be promoted; got promoted=false (outcome=%q)", outc)
	}
	if outc != agent.SteerTooLate {
		t.Fatalf("terminal-race steer outcome = %q, want too_late", outc)
	}
	// The promotion must actually register + drive a follow-up run, not drop.
	if run2 == nil {
		t.Fatalf("promoted steer returned a nil run (silently dropped)")
	}
	if got, ok := svc.LookupRun(sess.ID); !ok || got != run2 {
		t.Fatalf("promoted run not the registered live run for the session")
	}
	if stop := drainForResult(t, run2); stop != session.StopEndTurn {
		t.Fatalf("promoted run stop = %q, want end_turn", stop)
	}
	svc.FinishRun(sess.ID, run2)

	got, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	texts := steerUserTexts(got)
	if len(texts) != 2 || texts[0] != "hello" || texts[1] != "" {
		t.Fatalf("user messages = %v, want [hello, media-only] (steer promoted, not dropped)", texts)
	}
	messages := got.Conversation.Messages
	if len(messages) < 2 || len(messages[len(messages)-2].Parts) != 1 || string(messages[len(messages)-2].Parts[0].Data) != "late-pixels" {
		t.Fatalf("promoted media missing: %#v", messages)
	}
}

// TestSteer_PromotionUsesRunEntryFunnel (AC3.3): promotion reuses the run-entry
// funnel — reopen-if-completed / interrupt-if-cancelled / recover-if-failed — so
// a follow-up run NEVER starts on an unrepaired terminal session. Each terminal
// state is reached through the real Service/engine surface, then a steer is
// promoted; the promoted run must reach a clean terminal EvResult and record the
// steer (proving the terminal state was repaired to idle first — the wedge
// "RecordUserPrompt from <terminal>" would fail the promotion instead).
func TestSteer_PromotionUsesRunEntryFunnel(t *testing.T) {
	type fixture struct {
		svc  *server.Service
		llm  *mockllm.Provider
		sess *session.Session
	}
	build := func(t *testing.T, turns []mockllm.Turn, tools ...tool.Tool) fixture {
		t.Helper()
		llm := mockllm.New(turns...)
		svc := newSteerService(t, llm, nil, tools...)
		sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		return fixture{svc: svc, llm: llm, sess: sess}
	}
	// promote asserts the steer is promoted through the funnel: too_late +
	// promoted, the follow-up run reaches a clean stop, and the steer text is
	// recorded (never a run on an unrepaired terminal session, never a drop).
	promote := func(t *testing.T, f fixture) {
		t.Helper()
		ctx := context.Background()
		outc, promoted, run, err := f.svc.Steer(ctx, f.sess.ID, "promoted follow-up", nil, "", "")
		if err != nil {
			t.Fatalf("Steer: %v", err)
		}
		if !promoted {
			t.Fatalf("steer must be promoted (no live run); got promoted=false (outcome=%q)", outc)
		}
		if outc != agent.SteerTooLate {
			t.Fatalf("outcome = %q, want too_late", outc)
		}
		if run == nil {
			t.Fatalf("promotion returned no follow-up run")
		}
		if stop := drainForResult(t, run); stop != session.StopEndTurn {
			t.Fatalf("promoted run stop = %q, want end_turn (an unrepaired terminal session would wedge before any model turn)", stop)
		}
		f.svc.FinishRun(f.sess.ID, run)
		if f.llm.Calls() < 2 {
			t.Fatalf("provider calls = %d, want >= 2 (the promoted run must drive a model turn; a run on an unrepaired terminal session never reaches the model)", f.llm.Calls())
		}
		got, err := f.svc.GetSession(ctx, f.sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		var recorded bool
		for _, txt := range steerUserTexts(got) {
			if txt == "promoted follow-up" {
				recorded = true
			}
		}
		if !recorded {
			t.Fatalf("promoted steer text not recorded; users=%v", steerUserTexts(got))
		}
	}

	t.Run("completed", func(t *testing.T) {
		f := build(t, []mockllm.Turn{mockllm.TextTurn("first"), mockllm.TextTurn("second")})
		run, err := f.svc.StartRunContent(context.Background(), f.sess.ID, "prompt", nil)
		if err != nil {
			t.Fatalf("drive completed: %v", err)
		}
		drainRun(t, run)
		f.svc.FinishRun(f.sess.ID, run)
		assertSteerState(t, f.svc, f.sess.ID, session.StateCompleted)
		promote(t, f) // funnel must Reopen the completed session before driving.
	})

	t.Run("cancelled", func(t *testing.T) {
		block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
		f := build(t, []mockllm.Turn{
			mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
			mockllm.TextTurn("recovered"),
		}, block)
		run, err := f.svc.StartRunContent(context.Background(), f.sess.ID, "prompt", nil)
		if err != nil {
			t.Fatalf("drive cancelled: %v", err)
		}
		<-block.started
		run.Cancel()
		drainRun(t, run)
		f.svc.FinishRun(f.sess.ID, run)
		assertSteerState(t, f.svc, f.sess.ID, session.StateCancelled)
		promote(t, f) // funnel must Interrupt (repair) the cancelled session.
	})

	t.Run("failed", func(t *testing.T) {
		f := build(t, []mockllm.Turn{
			// A genuine mid-stream provider failure → session.Failed.
			mockllm.ErrorTurn(errors.New("upstream 502: bad gateway")),
			mockllm.TextTurn("recovered"),
		})
		run, err := f.svc.StartRunContent(context.Background(), f.sess.ID, "prompt", nil)
		if err != nil {
			t.Fatalf("drive failed: %v", err)
		}
		if stop := drainForResult(t, run); stop != session.StopError {
			t.Fatalf("drive failed: stop = %q, want error", stop)
		}
		f.svc.FinishRun(f.sess.ID, run)
		assertSteerState(t, f.svc, f.sess.ID, session.StateFailed)
		promote(t, f) // funnel must Recover the failed session before driving.
	})
}
