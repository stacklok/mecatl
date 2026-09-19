package ui

import (
	"context"
	"sync"
	"testing"

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
