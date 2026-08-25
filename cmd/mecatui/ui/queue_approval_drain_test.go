package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestResolveAskArmsNoExtraStreamReader is the deterministic regression guard for
// the queued-message-after-approval bug.
//
// The fan-in invariant is "exactly one WaitForMsg(streamCh) in flight per run": each
// stream-event handler consumes one message and re-arms exactly one reader. The
// permission.ask handler (afterEvent) arms that one reader and the run then PAUSES,
// so the reader is still in flight when the user approves — nothing has arrived to
// consume it. resolveAsk must therefore NOT arm a second reader; if it does, that
// extra reader is permanent and bound to THIS run's channel. It outlives the run: at
// a clean end the queue-drain re-points streamCh to the next run, and the stale
// reader then either delivers this run's trailing StreamClosed into the freshly
// drained follow-up (canceling it — "stream error: EOF" + "context canceled") or
// re-arms on the wrong channel and strands it.
//
// This test pins the invariant directly and deterministically: with a reader already
// "in flight" (a pending message sitting on streamCh), resolveAsk's returned command
// must NOT consume it. The buggy `tea.Batch(send, m.waitCmd())` spawns a second
// WaitForMsg that eats the pending message; the fixed `send`-only command leaves it
// untouched.
func TestResolveAskArmsNoExtraStreamReader(t *testing.T) {
	// Both the allow and deny paths are reader-identical (resolveAsk returns the same
	// send-only command either way; allow/deny only changes the frame payload), so
	// both must leave the afterEvent reader as the sole reader.
	for _, tc := range []struct {
		name    string
		verdict client.Verdict
	}{
		{"allow", client.VerdictAllowOnce},
		{"deny", client.VerdictDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := theme.New("aztec", theme.AztecPalette())
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
			m := New(Deps{
				Session:     conv,
				Conv:        conv,
				Theme:       th,
				Ctx:         context.Background(),
				NoAltScreen: true,
			})
			m = applyAll(m,
				tea.WindowSizeMsg{Width: 100, Height: 30},
				client.SessionReadyMsg{SessionID: "sess-test-0001"},
			)

			// Put the model where the permission.ask handler leaves it: awaiting approval
			// on a live stream, with the single reader that afterEvent armed represented by
			// a not-yet-consumed message parked on the channel.
			ch := make(chan tea.Msg, 1)
			m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
			m.streamCh = ch
			m.phase = phaseRunning
			m = applyAll(m, client.PermissionAskMsg{AskID: "ask-1", Tool: "Write"})
			ch <- client.StreamClosedMsg{} // the message the in-flight reader will eventually take

			key := tea.KeyPressMsg{Code: 'a', Text: "a"}
			if tc.verdict == client.VerdictDeny {
				key = tea.KeyPressMsg{Code: 'd', Text: "d"}
			}
			_, cmd := pressKey(m, key)
			runBatchLeaves(cmd) // execute the send + any (wrongly) batched reader

			// The parked message must still be on the channel. If resolveAsk armed a second
			// reader, runBatchLeaves ran it and it consumed the message — the stale reader
			// that tears down the next queued run.
			select {
			case <-ch:
				// good: message untouched (we drained our own marker just now to assert it).
			default:
				t.Fatal("resolveAsk armed an EXTRA stream reader (it consumed the pending " +
					"channel message): the permission.ask handler already armed one, so this " +
					"duplicate survives the run and tears down the next queued follow-up run")
			}
		})
	}
}

// TestStaleStreamGenerationDropped pins the structural backstop: a stream message
// tagged with a generation that no longer matches the model's current run is dropped
// and does NOT drive a state transition — so a reader leaked onto an abandoned
// channel (across a drain, or via any future double-arm) can never tear down the
// current run, regardless of how many readers leaked. A current-generation message
// of the same kind, by contrast, is processed normally.
func TestStaleStreamGenerationDropped(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := New(Deps{Session: conv, Conv: conv, Theme: th, Ctx: context.Background(), NoAltScreen: true})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})

	// Simulate an in-flight run on generation 5.
	m.phase = phaseRunning
	m.streamGen = 5
	m.streamCh = make(chan tea.Msg, 1)

	// A StreamClosed from an OLDER generation (a reader bound to an abandoned channel)
	// must be inert: the run keeps streaming.
	mm, _ := m.Update(streamMsg{gen: 4, msg: client.StreamClosedMsg{}})
	stale := mm.(Model)
	if stale.phase != phaseRunning {
		t.Fatalf("stale-generation StreamClosed tore the run down: phase = %d, want phaseRunning", stale.phase)
	}

	// The SAME message at the CURRENT generation is processed (the run ends).
	mm, _ = m.Update(streamMsg{gen: 5, msg: client.StreamClosedMsg{}})
	current := mm.(Model)
	if current.phase != phaseIdle {
		t.Fatalf("current-generation StreamClosed was not processed: phase = %d, want phaseIdle", current.phase)
	}
}

// TestQueueDrainAfterApprovalCompletesRunB is the end-to-end happy-path companion:
// a run that paused for a permission approval and then completed cleanly drains a
// staged follow-up (run B), and run B streams to its own clean result. With the
// single-reader fan-in restored this is deterministic; the stale-reader bug
// (TestResolveAskArmsNoExtraStreamReader guards its root cause) would corrupt run B
// here.
func TestQueueDrainAfterApprovalCompletesRunB(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())

	// Run 1: streams to a permission.ask (gated there), then — after approval and the
	// manual release — a tail delta and a clean result.
	run1 := &fakeRecver{
		script: []*mecatlv1.ConverseResponse{
			ev(&mecatlv1.Event{Type: "session.init", Seq: 1}),
			ev(&mecatlv1.Event{Type: "turn.start", Seq: 2, Turn: 1}),
			ev(&mecatlv1.Event{Type: "permission.ask", Seq: 3, Turn: 1, Ask: &mecatlv1.PermissionAsk{
				AskId: "ask-1", Tool: "Write", Args: `{"path":"a.txt","content":"x"}`, Reason: "Write requires approval",
			}}),
			ev(&mecatlv1.Event{Type: "message.delta", Seq: 4, Turn: 1, Text: "alpha first"}),
			ev(&mecatlv1.Event{Type: "result", Seq: 5, Turn: 1, Result: &mecatlv1.Result{
				Stop: "end_turn", Text: "alpha first", Usage: &mecatlv1.Usage{},
			}}),
		},
		gateType: "permission.ask", gate: make(chan struct{}), reachedGate: make(chan struct{}),
	}
	// Run 2 (the drained follow-up): GATED at its delta so it stays in phaseRunning,
	// giving any stale reader a window to (wrongly) tear it down before it can finish.
	run2 := &fakeRecver{
		script: []*mecatlv1.ConverseResponse{
			ev(&mecatlv1.Event{Type: "session.init", Seq: 1}),
			ev(&mecatlv1.Event{Type: "turn.start", Seq: 2, Turn: 1}),
			ev(&mecatlv1.Event{Type: "message.delta", Seq: 3, Turn: 1, Text: "beta second"}),
			ev(&mecatlv1.Event{Type: "result", Seq: 4, Turn: 1, Result: &mecatlv1.Result{
				Stop: "end_turn", Text: "beta second", Usage: &mecatlv1.Usage{},
			}}),
		},
		gateType: "message.delta", gate: make(chan struct{}), reachedGate: make(chan struct{}),
	}

	send := &fakeSender{}
	conv := &fakeConv{
		recv:         run1,
		send:         send,
		recvers:      []*fakeRecver{run1, run2},
		sessionReady: make(chan struct{}),
	}
	prog := newProgress()
	model := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       th,
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
		onPhase:     prog.record,
	})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))

	prog.wait(t, phaseIdle, 3*time.Second)

	// Run 1: prompt "first" → streams to the gated permission.ask.
	tm.Type("first")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitClosed(t, "run 1 reached its permission.ask", run1.reachedGate, 5*time.Second)
	prog.wait(t, phaseAwaitingApproval, 5*time.Second)

	// Approve (enter). resolveAsk returns to phaseRunning; run 1 stays gate-held (the
	// sender does NOT auto-release), so the model is provably running with no result
	// yet — the deterministic window to enqueue.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Enqueue "second" while phaseRunning. Keys are FIFO after the approve and run 1 is
	// held, so enter enqueues (it cannot race run 1's result).
	tm.Type("second")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Release run 1 → its tail (delta + result) streams → clean end_turn → the queue
	// drains run 2 (which opens and holds at its delta).
	run1.release()
	waitClosed(t, "run 2 streamed its delta (held)", run2.reachedGate, 5*time.Second)

	// Release run 2's tail. The chain settles to idle (runDone==1 — run 1's completion
	// coalesced into run 2's submit in one reducer step, so only run 2's end surfaces
	// idle).
	run2.release()
	prog.waitRunComplete(t, 1, 5*time.Second)

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if len(fm.queued) != 0 {
		t.Errorf("queue should be empty after the drain, got %v", fm.queued)
	}
	if fm.phase != phaseIdle {
		t.Errorf("final phase = %d, want phaseIdle (both runs settled)", fm.phase)
	}
	var prompts []string
	for _, fr := range send.frames() {
		if p := fr.GetPrompt(); p != nil {
			prompts = append(prompts, p.GetText())
		}
	}
	if len(prompts) != 2 || prompts[0] != "first" || prompts[1] != "second" {
		t.Fatalf("prompt frames = %v, want [first second] (run B's prompt must have been sent)", prompts)
	}
	frame := stripANSIstr(fm.View().Content)
	if !strings.Contains(frame, "beta second") {
		t.Errorf("final frame missing run B's streamed output 'beta second':\n%s", frame)
	}
}
