package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// fakeLifecycleNotifier records the lifecycle transitions the reducer emits, so
// the UI wiring can be asserted offline (no real notify.sh, no Superset host).
type fakeLifecycleNotifier struct {
	mu    sync.Mutex
	calls []lifecycleCall
}

type lifecycleCall struct {
	kind      string // "start" | "permission" | "stop"
	sessionID string
	failed    bool
	message   string
}

func (f *fakeLifecycleNotifier) Start(_ context.Context, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, lifecycleCall{kind: "start", sessionID: sessionID})
}

func (f *fakeLifecycleNotifier) PermissionRequest(_ context.Context, sessionID, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, lifecycleCall{kind: "permission", sessionID: sessionID, message: message})
}

func (f *fakeLifecycleNotifier) Stop(_ context.Context, sessionID string, failed bool, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, lifecycleCall{kind: "stop", sessionID: sessionID, failed: failed, message: message})
}

func (f *fakeLifecycleNotifier) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.kind
	}
	return out
}

func (f *fakeLifecycleNotifier) snapshot() []lifecycleCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]lifecycleCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func modelWithNotifier(t *testing.T) (Model, *fakeLifecycleNotifier) {
	t.Helper()
	fake := &fakeLifecycleNotifier{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()), func(d *Deps) { d.AgentHook = fake })
	m.sessionID = "sess-super-1"
	return m, fake
}

// TestAgentHookStartFiresOnTurnStart proves the Start transition is wired to the
// real reducer's turn.start handling.
func TestAgentHookStartFiresOnTurnStart(t *testing.T) {
	m, fake := modelWithNotifier(t)
	applyAll(m, client.TurnStartMsg{Turn: 1})
	if got := fake.kinds(); len(got) != 1 || got[0] != "start" {
		t.Fatalf("turn.start should emit exactly one Start, got %v", got)
	}
	if fake.snapshot()[0].sessionID != "sess-super-1" {
		t.Errorf("Start carried wrong session id: %+v", fake.snapshot()[0])
	}
}

// TestAgentHookPermissionRequestFiresOnMainAsk proves a MAIN-session permission
// ask is mirrored as PermissionRequest.
func TestAgentHookPermissionRequestFiresOnMainAsk(t *testing.T) {
	m, fake := modelWithNotifier(t)
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.PermissionAskMsg{AskID: "sess-super-1:1:call-1:r0", Tool: "Shell", Args: `{"command":"ls"}`, Reason: "run ls?"},
	)
	kinds := fake.kinds()
	if len(kinds) != 2 || kinds[0] != "start" || kinds[1] != "permission" {
		t.Fatalf("want [start permission], got %v", kinds)
	}
	perm := fake.snapshot()[1]
	if perm.message != "run ls?" {
		t.Errorf("PermissionRequest should forward the reason, got %q", perm.message)
	}
	_ = m
}

// TestAgentHookPermissionRequestSkippedForChildAsk proves a surfaced SUBAGENT ask
// does NOT drive a terminal-level notification (Superset drives status from the
// main loop only).
func TestAgentHookPermissionRequestSkippedForChildAsk(t *testing.T) {
	m, fake := modelWithNotifier(t)
	// A child ask is prefixed with the CHILD session id, not the live session id.
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.PermissionAskMsg{AskID: "subagent-child-9:1:call-1:r0", Tool: "Shell", Args: `{"command":"ls"}`, Reason: "child wants ls"},
	)
	for _, c := range fake.snapshot() {
		if c.kind == "permission" {
			t.Fatalf("child ask must not emit PermissionRequest: %+v", fake.snapshot())
		}
	}
	_ = m
}

// TestAgentHookStopFiresOnResult proves a clean terminal is mirrored as Stop.
func TestAgentHookStopFiresOnResult(t *testing.T) {
	m, fake := modelWithNotifier(t)
	applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ResultMsg{Stop: "end_turn"},
	)
	kinds := fake.kinds()
	if len(kinds) != 2 || kinds[0] != "start" || kinds[1] != "stop" {
		t.Fatalf("want [start stop], got %v", kinds)
	}
	if fake.snapshot()[1].failed {
		t.Errorf("clean terminal should not be Failed")
	}
}

// TestAgentHookFailedFiresOnErrorResult proves an error terminal is mirrored as a
// Failed Stop with the error preview.
func TestAgentHookFailedFiresOnErrorResult(t *testing.T) {
	m, fake := modelWithNotifier(t)
	applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ResultMsg{Stop: stopError, Error: "provider exploded"},
	)
	stop := lastStop(t, fake)
	if !stop.failed {
		t.Errorf("error terminal should be Failed: %+v", stop)
	}
	if stop.message != "provider exploded" {
		t.Errorf("Failed should forward the error preview, got %q", stop.message)
	}
}

// TestAgentHookFullLifecycleOrder proves the whole Start → PermissionRequest →
// Stop order for a single run through the real reducer.
func TestAgentHookFullLifecycleOrder(t *testing.T) {
	m, fake := modelWithNotifier(t)
	applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.PermissionAskMsg{AskID: "sess-super-1:1:call-1:r0", Tool: "Shell", Args: `{"command":"ls"}`, Reason: "?"},
		client.ResultMsg{Stop: "end_turn"},
	)
	want := []string{"start", "permission", "stop"}
	got := fake.kinds()
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch: want %v, got %v", want, got)
		}
	}
}

// TestAgentHookNilNotifierIsInert proves the default (no Superset terminal) wiring
// makes the reducer emit nothing and never panic.
func TestAgentHookNilNotifierIsInert(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette())) // no host hook dep
	m.sessionID = "sess-x"
	// Drive a full lifecycle; the nil interface must be a no-op.
	applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.PermissionAskMsg{AskID: "sess-x:1:call-1:r0", Tool: "Shell", Args: `{"command":"ls"}`, Reason: "?"},
		client.ResultMsg{Stop: "end_turn"},
	)
}

func lastStop(t *testing.T, f *fakeLifecycleNotifier) lifecycleCall {
	t.Helper()
	calls := f.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].kind == "stop" {
			return calls[i]
		}
	}
	t.Fatalf("no Stop recorded: %+v", calls)
	return lifecycleCall{}
}

// The tests below pin the TRANSPORT terminals — the run-ending paths that carry
// no server ResultMsg. They matter because the notifier closes a busy period
// only on a terminal: a missed one leaves it running and SILENTLY dedupes the
// next run's busy signal, so the host stays stuck busy forever. That dedupe half
// is proven in the agenthook package (TestStartStopStartAcrossRuns); these prove
// the reducer half, that every such path actually reports a terminal.

// runningModelWithNotifier returns a model genuinely in phaseRunning with the
// hook wired. Two of the terminal paths under test (an active stream close and
// auth recovery) only fire for an ACTIVE run, so driving a bare turn.start into
// an idle model would skip exactly the branch being pinned.
func runningModelWithNotifier(t *testing.T) (Model, *fakeLifecycleNotifier) {
	t.Helper()
	fake := &fakeLifecycleNotifier{}
	recv := &fakeRecver{}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, recvers: []*fakeRecver{recv}}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		AgentHook:   fake,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
	)
	m = startRunning(t, m, "do the thing")
	return applyAll(m, client.TurnStartMsg{Turn: 1}), fake
}

// terminalKindsAfter drives an ACTIVE run to a transport terminal, then starts a
// second run, and returns the recorded transitions. The trailing Start is the
// point: it is what a stuck-busy notifier would never reach.
func terminalKindsAfter(t *testing.T, prep func(Model) Model, terminal tea.Msg) ([]string, lifecycleCall) {
	t.Helper()
	m, fake := runningModelWithNotifier(t)
	if prep != nil {
		m = prep(m)
	}
	m = applyAll(m, terminal)
	applyAll(m, client.TurnStartMsg{Turn: 1})
	return fake.kinds(), lastStop(t, fake)
}

func wantStartFailedStopStart(t *testing.T, kinds []string, stop lifecycleCall, what string) {
	t.Helper()
	want := []string{"start", "stop", "start"}
	if len(kinds) != len(want) {
		t.Fatalf("%s: want %v, got %v", what, want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("%s: want %v, got %v", what, want, kinds)
		}
	}
	if !stop.failed {
		t.Errorf("%s: a run that ended without a result is a FAILED terminal, got %+v", what, stop)
	}
	if strings.TrimSpace(stop.message) == "" {
		t.Errorf("%s: terminal should carry a preview for the host notification", what)
	}
}

// TestAgentHookTerminalOnStreamError covers the ordinary transport failure.
func TestAgentHookTerminalOnStreamError(t *testing.T) {
	kinds, stop := terminalKindsAfter(t, nil, client.StreamErrMsg{Err: errors.New("connection reset")})
	wantStartFailedStopStart(t, kinds, stop, "stream error")
}

// TestAgentHookTerminalOnActiveStreamClose covers a clean EOF arriving while the
// run is still active (the server closed before sending a result).
func TestAgentHookTerminalOnActiveStreamClose(t *testing.T) {
	kinds, stop := terminalKindsAfter(t, nil, client.StreamClosedMsg{})
	wantStartFailedStopStart(t, kinds, stop, "active stream close")
}

// TestAgentHookTerminalOnClearHandoff covers the /clear handoff: the source
// stream settles while Clear owns what happens next. Clear suppresses every
// other recovery path, which is exactly why the hook must still settle here.
func TestAgentHookTerminalOnClearHandoff(t *testing.T) {
	prep := func(m Model) Model {
		m.clearPending = &clearHandoff{sourceID: m.sessionID, sourcePhase: m.phase}
		return m
	}
	kinds, stop := terminalKindsAfter(t, prep, client.StreamClosedMsg{})
	wantStartFailedStopStart(t, kinds, stop, "clear handoff")
}

// TestAgentHookTerminalOnAuthRecovery covers authentication recovery, which
// cancels the active run and hands the user to the connect surface.
func TestAgentHookTerminalOnAuthRecovery(t *testing.T) {
	terminal := client.StreamErrMsg{Err: errors.New("unauthenticated"), AuthReason: client.AuthSessionExpired}
	kinds, stop := terminalKindsAfter(t, nil, terminal)
	wantStartFailedStopStart(t, kinds, stop, "auth recovery")
}

// TestAgentHookNoTerminalOnAutomaticRetry pins the deliberate NON-terminal
// exception: a retry-eligible failure starts another run by itself, so the run
// never returned to idle and the host must not be told it finished. The whole
// retry-eligible → retry turn → real terminal sequence must report exactly one
// busy signal and exactly one terminal, at the end.
func TestAgentHookNoTerminalOnAutomaticRetry(t *testing.T) {
	m, fake := modelWithNotifier(t)
	m = applyAll(m, client.TurnStartMsg{Turn: 1}, failedStepRetryableResult())

	if got := fake.kinds(); len(got) != 1 || got[0] != "start" {
		t.Fatalf("an automatic retry is not a terminal: want [start], got %v", got)
	}

	// The retry's own turn.start must not re-announce busy, and the eventual
	// real terminal is the one and only Stop.
	applyAll(m, client.TurnStartMsg{Turn: 2}, client.ResultMsg{Stop: "end_turn"})

	kinds := fake.kinds()
	stops, starts := 0, 0
	for _, k := range kinds {
		switch k {
		case "stop":
			stops++
		case "start":
			starts++
		}
	}
	if stops != 1 || kinds[len(kinds)-1] != "stop" {
		t.Fatalf("want exactly one terminal, last: got %v", kinds)
	}
	if starts != 2 {
		// Two reducer-level Starts are expected (one per turn.start); the
		// notifier dedupes them into ONE host busy signal, which is pinned by
		// the agenthook package's TestStartDedupedWithinRun.
		t.Fatalf("want one Start per turn.start, got %v", kinds)
	}
}

// TestAgentHookPermissionRequestOnQueuedMainAskPromotion proves a MAIN ask that
// arrives behind an already-open background-child card is not lost. It is queued
// silently (the user cannot act on it yet), and the host learns about it at the
// moment it becomes the visible head.
func TestAgentHookPermissionRequestOnQueuedMainAskPromotion(t *testing.T) {
	m, fake := modelWithNotifier(t)
	childAsk := "subagent-child-9:1:call-1:r0"
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		// A detached child's ask takes the visible slot: filtered, no notification.
		client.PermissionAskMsg{AskID: childAsk, Tool: "Shell", Args: `{"command":"ls"}`, Reason: "child wants ls"},
		// The MAIN session then asks; it queues behind the child's card.
		client.PermissionAskMsg{AskID: "sess-super-1:1:call-2:r0", Tool: "Write", Args: `{"path":"x"}`, Reason: "main wants write"},
	)
	for _, c := range fake.snapshot() {
		if c.kind == "permission" {
			t.Fatalf("a queued (invisible) ask must not notify yet: %+v", fake.snapshot())
		}
	}

	// Resolving the child promotes the main ask to the visible head.
	s := approvalSurfaceFor(&m)
	if s == nil {
		t.Fatal("expected an open approval surface")
	}
	if len(s.queue) != 1 {
		t.Fatalf("expected the main ask queued behind the child, got %d queued", len(s.queue))
	}
	mm, _, _ := m.finishApprovalIntent(s.advance(), phaseRunning, nil)
	m = mm.(Model)

	perms := 0
	var got lifecycleCall
	for _, c := range fake.snapshot() {
		if c.kind == "permission" {
			perms++
			got = c
		}
	}
	if perms != 1 {
		t.Fatalf("promotion should emit exactly one PermissionRequest, got %d: %+v", perms, fake.snapshot())
	}
	if got.message != "main wants write" {
		t.Errorf("PermissionRequest should carry the promoted MAIN ask's reason, got %q", got.message)
	}
}

// TestAgentHookNoPermissionRequestPromotingChildAsk proves the child filter
// survives the promotion path too: a child ask promoted to the visible head is
// still not a main-session attention signal.
func TestAgentHookNoPermissionRequestPromotingChildAsk(t *testing.T) {
	m, fake := modelWithNotifier(t)
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.PermissionAskMsg{AskID: "subagent-a:1:call-1:r0", Tool: "Shell", Args: `{"command":"ls"}`, Reason: "child a"},
		client.PermissionAskMsg{AskID: "subagent-b:1:call-1:r0", Tool: "Shell", Args: `{"command":"pwd"}`, Reason: "child b"},
	)
	s := approvalSurfaceFor(&m)
	if s == nil || len(s.queue) != 1 {
		t.Fatalf("expected a second child ask queued behind the first")
	}
	mm, _, _ := m.finishApprovalIntent(s.advance(), phaseRunning, nil)
	m = mm.(Model)
	for _, c := range fake.snapshot() {
		if c.kind == "permission" {
			t.Fatalf("promoting a CHILD ask must not notify: %+v", fake.snapshot())
		}
	}
}
