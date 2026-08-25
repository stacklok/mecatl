package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// These tests pin the spinner tick phase-gate: the bubbles spinner's tick chain is
// self-perpetuating (every sp.Update returns the next tick cmd), so an idle TUI
// would otherwise run a full 10fps Update→View loop forever. The reducer drops
// spinner.TickMsg outside the spinner-visible phases (spinnerVisible:
// phaseRunning/phaseConnecting), and every transition INTO a visible phase re-arms
// m.sp.Tick.

// resolveLeafMsg runs cmd on a goroutine and races it against a short bound,
// the same timeout-race convention as isQuitCmd (queue_test.go): a synchronous
// command resolves at once; a slow/blocking one (the run reader waitCmd, a
// tea.Tick timer) is treated as "no message" rather than blocking the test.
func resolveLeafMsg(cmd tea.Cmd, d time.Duration) (tea.Msg, bool) {
	if cmd == nil {
		return nil, false
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		return msg, true
	case <-time.After(d):
		return nil, false
	}
}

// containsSpinnerTick reports whether cmd — possibly a (nested) tea.Batch —
// contains a leaf that yields a spinner.TickMsg. Leaves are resolved with the
// timeout race above, so a batch that also carries the BLOCKING waitCmd leaf
// (afterEvent, submitPrompt) cannot hang the assertion. The bound is load-scaled
// (scaleWait): unlike isQuitCmd this race is in the MUST-resolve direction — a
// CPU-starved leaf under parallel -race load would otherwise read as a spurious
// "did not re-arm" FAILURE (and the tick leaf can sit LAST in a batch behind
// several blocking leaves, stacking the per-leaf waits ahead of it).
func containsSpinnerTick(cmd tea.Cmd) bool {
	msg, ok := resolveLeafMsg(cmd, scaleWait(50*time.Millisecond))
	if !ok {
		return false
	}
	if batch, isBatch := msg.(tea.BatchMsg); isBatch {
		for _, c := range batch {
			if containsSpinnerTick(c) {
				return true
			}
		}
		return false
	}
	_, isTick := msg.(spinner.TickMsg)
	return isTick
}

// flattenLeafMsgs resolves cmd — flattening nested batches — and returns every
// leaf message that resolves within the bound; blocking leaves (e.g. a parked
// stream reader) are skipped. For tests that must locate a specific message
// inside a batch that also carries the spinner re-arm tick.
func flattenLeafMsgs(cmd tea.Cmd, d time.Duration) []tea.Msg {
	msg, ok := resolveLeafMsg(cmd, d)
	if !ok {
		return nil
	}
	if batch, isBatch := msg.(tea.BatchMsg); isBatch {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, flattenLeafMsgs(c, d)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// resolveWithin resolves cmd to its message or fails the test. Used where the
// message itself is needed (the spinner's chained tea.Tick — a real ~100ms timer,
// so the bound is generous and load-scaled).
func resolveWithin(t *testing.T, cmd tea.Cmd, d time.Duration) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil cmd: expected a command to resolve")
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		return msg
	case <-time.After(d):
		t.Fatalf("command did not resolve within %v", d)
		return nil
	}
}

// TestIdleSpinnerTickDropped pins the gate itself: in every phase where the footer
// does NOT render the spinner, a delivered spinner.TickMsg is dropped — nil cmd
// (the self-perpetuating chain terminates) and the spinner frame untouched
// (m.sp.Update was never called).
func TestIdleSpinnerTickDropped(t *testing.T) {
	for _, tc := range []struct {
		name string
		ph   phase
	}{
		{"idle", phaseIdle},
		{"awaitingApproval", phaseAwaitingApproval},
		{"fatal", phaseFatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.phase = tc.ph
			frame := m.sp.View()

			mm, cmd := m.Update(m.sp.Tick()) // a real matching-id tick
			got := mm.(Model)

			if cmd != nil {
				t.Errorf("phase %d: tick returned a non-nil cmd — the chain must terminate (nil) in a spinner-hidden phase", tc.ph)
			}
			if got.sp.View() != frame {
				t.Errorf("phase %d: spinner frame advanced (%q → %q) — the dropped tick must not reach sp.Update", tc.ph, frame, got.sp.View())
			}
		})
	}
}

// TestVisiblePhaseSpinnerTickPropagates pins the complement: while the spinner IS
// rendered (running/connecting) a tick advances the frame and chains the next tick,
// and that chained tick advances the frame again — the animation genuinely runs.
func TestVisiblePhaseSpinnerTickPropagates(t *testing.T) {
	for _, tc := range []struct {
		name string
		ph   phase
	}{
		{"running", phaseRunning},
		{"connecting", phaseConnecting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.phase = tc.ph
			f0 := m.sp.View()

			mm, cmd := m.Update(m.sp.Tick())
			m1 := mm.(Model)
			if cmd == nil {
				t.Fatal("visible-phase tick returned nil cmd — the chain must continue")
			}
			f1 := m1.sp.View()
			if f1 == f0 {
				t.Fatalf("visible-phase tick did not advance the frame (still %q)", f0)
			}

			// Feed the chained tick back once: a second, distinct frame.
			msg := resolveWithin(t, cmd, scaleWait(3*time.Second))
			tick, ok := msg.(spinner.TickMsg)
			if !ok {
				t.Fatalf("chained cmd yielded %T, want spinner.TickMsg", msg)
			}
			mm2, cmd2 := m1.Update(tick)
			m2 := mm2.(Model)
			if cmd2 == nil {
				t.Fatal("second visible-phase tick returned nil cmd — the chain must continue")
			}
			if m2.sp.View() == f1 {
				t.Fatalf("second tick did not advance the frame (still %q)", f1)
			}
		})
	}
}

// TestSpinnerRearmedOnEveryVisibleTransition pins the re-arm half of the contract:
// because the gate terminates the chain whenever the spinner leaves the screen,
// EVERY transition into a visible phase must restart it (m.sp.Tick in the returned
// batch) or the spinner freezes on its first frame.
func TestSpinnerRearmedOnEveryVisibleTransition(t *testing.T) {
	t.Run("resolveAskAllowKeypress", func(t *testing.T) {
		// awaitingApproval → running via the approval enter keypress (resolveAsk).
		m, _ := newQueueModel(t)
		m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
		m.streamCh = make(chan tea.Msg, 1)
		m.phase = phaseRunning
		m = applyAll(m, client.PermissionAskMsg{AskID: "ask-1", Tool: "Write"})

		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		got := mm.(Model)
		if got.phase != phaseRunning {
			t.Fatalf("phase = %d, want phaseRunning", got.phase)
		}
		if !containsSpinnerTick(cmd) {
			t.Error("resolveAsk did not re-arm m.sp.Tick on the awaitingApproval→running transition")
		}
	})

	t.Run("permissionRetractMatched", func(t *testing.T) {
		// awaitingApproval → running via a harness-withdrawn ask (PermissionRetractMsg).
		m, _ := newQueueModel(t)
		m.streamCh = make(chan tea.Msg, 1)
		m.streamCh <- client.StreamClosedMsg{} // park a msg so afterEvent's reader leaf resolves
		m.phase = phaseRunning
		m = applyAll(m, client.PermissionAskMsg{AskID: "ask-1", Tool: "Bash"})

		mm, cmd := m.Update(client.PermissionRetractMsg{AskID: "ask-1"})
		got := mm.(Model)
		if got.phase != phaseRunning {
			t.Fatalf("phase = %d, want phaseRunning", got.phase)
		}
		if !containsSpinnerTick(cmd) {
			t.Error("PermissionRetractMsg did not re-arm m.sp.Tick on the awaitingApproval→running transition")
		}
	})

	t.Run("restartRetry", func(t *testing.T) {
		// idle → connecting via the failed-restart retry (enter on an empty line).
		m, _ := newQueueModel(t)
		m.sessionID = ""
		m.restartFailed = true

		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		got := mm.(Model)
		if got.phase != phaseConnecting {
			t.Fatalf("phase = %d, want phaseConnecting", got.phase)
		}
		if !containsSpinnerTick(cmd) {
			t.Error("restart-retry did not re-arm m.sp.Tick on the idle→connecting transition")
		}
	})

	t.Run("restartOnModelPick", func(t *testing.T) {
		// any → connecting via the /models picker restart-now handoff.
		m, _ := newQueueModel(t)
		sel := client.ModelSelection{ProviderID: "prov", ModelID: "model-x"}

		mm, cmd, handled := m.restartOnModel(sel)
		if !handled {
			t.Fatal("restartOnModel reported handled=false")
		}
		got := mm.(Model)
		if got.phase != phaseConnecting {
			t.Fatalf("phase = %d, want phaseConnecting", got.phase)
		}
		if !containsSpinnerTick(cmd) {
			t.Error("restartOnModel did not re-arm m.sp.Tick on the transition into phaseConnecting")
		}
	})

	t.Run("submitPrompt", func(t *testing.T) {
		// idle → running via a normal prompt submit (regression pin: this site already
		// armed the spinner before the gate existed; it must keep doing so).
		m, _ := newQueueModel(t)
		m = typeText(t, m, "hello")

		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		got := mm.(Model)
		if got.phase != phaseRunning {
			t.Fatalf("phase = %d, want phaseRunning", got.phase)
		}
		if !containsSpinnerTick(cmd) {
			t.Error("submitPrompt did not arm m.sp.Tick on the idle→running transition")
		}
	})

	t.Run("initConnectingWithLister", func(t *testing.T) {
		// Launch → connecting, Models-lister-wired Init branch (ListModels-first
		// sequencing). With the gate landed, Init is the SOLE starter of the
		// connect-phase animation: dropping its m.sp.Tick freezes the spinner from
		// launch until the first submit.
		conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
		m := New(Deps{
			Session:     conv,
			Conv:        conv,
			Theme:       theme.New("aztec", theme.AztecPalette()),
			Ctx:         context.Background(),
			NoAltScreen: true,
			Models:      &fakeModels{models: []client.ModelInfo{{ID: "m-1", ProviderID: "prov"}}},
		})
		if m.phase != phaseConnecting {
			t.Fatalf("a fresh model must start in phaseConnecting, got %d", m.phase)
		}
		if !containsSpinnerTick(m.Init()) {
			t.Error("Init (lister-wired branch) did not arm m.sp.Tick for the initial phaseConnecting")
		}
	})

	t.Run("initConnectingNoLister", func(t *testing.T) {
		// Launch → connecting, no-lister / old-server Init branch (direct
		// CreateSession). Same sole-starter argument as the lister-wired branch.
		conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
		m := New(Deps{
			Session:     conv,
			Conv:        conv,
			Theme:       theme.New("aztec", theme.AztecPalette()),
			Ctx:         context.Background(),
			NoAltScreen: true,
		})
		if m.phase != phaseConnecting {
			t.Fatalf("a fresh model must start in phaseConnecting, got %d", m.phase)
		}
		if !containsSpinnerTick(m.Init()) {
			t.Error("Init (no-lister branch) did not arm m.sp.Tick for the initial phaseConnecting")
		}
	})
}

// TestSpinnerVisibleMatchesFooterRender sweeps every phase and ties
// spinnerVisible() to renderFooter's ACTUAL render set, so the gate predicate and
// the footer's spinner arms cannot drift silently: a phase whose footer renders
// the spinner glyph must be spinner-visible (or its animation would be gated off
// while on screen), and a phase whose footer doesn't must not be (or a dead chain
// would keep ticking off screen).
func TestSpinnerVisibleMatchesFooterRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		ph   phase
	}{
		{"connecting", phaseConnecting},
		{"idle", phaseIdle},
		{"running", phaseRunning},
		{"awaitingApproval", phaseAwaitingApproval},
		{"fatal", phaseFatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.phase = tc.ph
			rendered := strings.Contains(m.renderFooter(), m.sp.View())
			if rendered != m.spinnerVisible() {
				t.Errorf("phase %s: footer renders the spinner = %v but spinnerVisible() = %v — keep spinnerVisible in sync with renderFooter's spinner arms",
					tc.name, rendered, m.spinnerVisible())
			}
		})
	}
}

// TestBubblesSpinnerDedupesDuplicateChains is the sentinel for the blanket-re-arm
// safety assumption: the bubbles spinner dedupes by id+tag, so a duplicate chain's
// stale tick (a tag older than the spinner's current one) is a no-op — which is why
// every visible transition may re-arm unconditionally without a generation counter.
// If a bubbles upgrade ever drops the tag dedup, this fails and the re-arm strategy
// needs revisiting.
func TestBubblesSpinnerDedupesDuplicateChains(t *testing.T) {
	m, _ := newQueueModel(t)
	m.phase = phaseRunning

	// Accept one tick: the spinner bumps its internal tag and chains the next tick.
	mm, cmd := m.Update(m.sp.Tick())
	m1 := mm.(Model)
	msg := resolveWithin(t, cmd, scaleWait(3*time.Second))
	tick, ok := msg.(spinner.TickMsg)
	if !ok {
		t.Fatalf("chained cmd yielded %T, want spinner.TickMsg", msg)
	}

	// Accept the chained (post-bump) tick: tag advances again.
	mm2, _ := m1.Update(tick)
	m2 := mm2.(Model)
	frame := m2.sp.View()

	// Redeliver the now-PRE-bump tick — what a duplicate chain would feed. The
	// spinner must drop it: unchanged frame, no chained cmd.
	mm3, cmd3 := m2.Update(tick)
	m3 := mm3.(Model)
	if cmd3 != nil {
		t.Error("stale (pre-bump) tick chained a new cmd — bubbles' tag dedup no longer protects the blanket re-arm")
	}
	if m3.sp.View() != frame {
		t.Errorf("stale (pre-bump) tick advanced the frame (%q → %q) — bubbles' tag dedup no longer protects the blanket re-arm", frame, m3.sp.View())
	}
}
