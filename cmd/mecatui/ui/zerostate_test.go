package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// zeroStateModel builds a connected, sized, idle model with an EMPTY conversation
// and the given caps — the first-run state the welcome card renders in.
func zeroStateModel(t *testing.T, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Theme:        aztec(),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Model:        "mock-model",
		Ctx:          context.Background(),
		NoAltScreen:  true,
		kittyCapable: func() bool { return false },
	})
	// A tall viewport so the FULL welcome card shows (mascot + wordmark + cwd +
	// model + tagline + affordances + memory note) — the splash now fits its body to
	// height-cardChrome and trades the low-priority info away on short terminals, so a
	// short test window would trim the memory note and make the caps-tailoring
	// assertions vacuous. 100x60 leaves room for everything.
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 80},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m
}

// TestZeroStateEmbeddedGolden locks the welcome card under embedded defaults: it
// offers "?", "/" (built-ins always exist), "f6" (teams on) and "ctrl+t",
// and notes that memory is on.
func TestZeroStateEmbeddedGolden(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	if !m.conv.isEmpty() {
		t.Fatal("conversation should be empty for the zero-state")
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "zerostate_embedded.golden", got)
}

// TestZeroStateAllOnGolden locks the welcome card under an all-on server: it adds
// the "/" line (commands on).
func TestZeroStateAllOnGolden(t *testing.T) {
	m := zeroStateModel(t, allOnCaps())
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "zerostate_all_on.golden", got)
}

// TestZeroStateVanishesAfterPrompt asserts the welcome card disappears once the
// first block is appended (the conversation is no longer empty).
func TestZeroStateVanishesAfterPrompt(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	before := stripANSIstr(m.View().Content)
	if !strings.Contains(before, "Welcome to mecatui") {
		t.Fatalf("zero-state card missing before any prompt:\n%s", before)
	}
	m.conv.addUser("do something")
	m.refreshView()
	after := stripANSIstr(m.View().Content)
	if strings.Contains(after, "Welcome to mecatui") {
		t.Fatalf("zero-state card should vanish after the first block:\n%s", after)
	}
}

// TestZeroStateCapsTailoring asserts the affordance list tracks caps WITHOUT
// pinning layout: "/" ALWAYS appears (built-in slash commands always exist),
// "f6" ALWAYS appears (the unified agents overlay — subagents are always
// available via Subagent, so it is no longer gated on the teams cap), and notes only
// when their cap is on.
func TestZeroStateCapsTailoring(t *testing.T) {
	embedded := stripANSIstr(zeroStateModel(t, embeddedCaps()).renderZeroState())
	allOn := stripANSIstr(zeroStateModel(t, allOnCaps()).renderZeroState())

	// "/" is always advertised now — built-ins (/clear, /help) exist regardless of
	// server slash-command support.
	if !strings.Contains(embedded, "slash commands") {
		t.Errorf("embedded zero-state should advertise / slash commands (built-ins always exist):\n%s", embedded)
	}
	if !strings.Contains(allOn, "slash commands") {
		t.Errorf("all-on zero-state should advertise / slash commands:\n%s", allOn)
	}
	// f6 is the unified agents overlay (subagents + teams) — always advertised.
	if !strings.Contains(embedded, "agents") {
		t.Errorf("embedded zero-state should advertise f6 agents:\n%s", embedded)
	}
	// memory note gated on caps.Memory (on in both).
	if !strings.Contains(embedded, "memory is on") {
		t.Errorf("embedded zero-state should note memory is on:\n%s", embedded)
	}

	// A bare-bones server (everything off) still shows "/" (built-ins) + "?" +
	// "f6" (agents — always available) + "ctrl+t", but no memory affordance.
	bare := stripANSIstr(zeroStateModel(t, client.Capabilities{}).renderZeroState())
	if !strings.Contains(bare, "slash commands") {
		t.Errorf("bare zero-state should still advertise / (built-ins always exist):\n%s", bare)
	}
	if !strings.Contains(bare, "agents") {
		t.Errorf("bare zero-state should still advertise f6 agents (subagents always available):\n%s", bare)
	}
	if strings.Contains(bare, "memory is on") {
		t.Errorf("bare zero-state should not advertise memory:\n%s", bare)
	}
}

// TestZeroStateGatewayNote (N2): the welcome splash renders a
// "<provider-id> gateway detected (no API key needed) — /models" line when an
// intent-driven provider is available-but-not-default. The splash renders at
// phaseIdle (post-connect), by which point the first ModelsMsg has landed and
// m.modelCatalog.statuses is populated — so the line catches a new operator at the
// moment they're most attentive. Suppressed when the gateway is the default
// (availableNotDefaultStatus returns false) or when no statuses are present.
func TestZeroStateGatewayNote(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	// Deliver a ModelsMsg carrying an AvailableNotDefault status (mirrors a
	// post-connect live refresh / the connect-time ListModels landing).
	fm := gatewayModels()
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	if !m.conv.isEmpty() {
		t.Fatalf("precondition: conversation should still be empty for the zero-state")
	}
	plain := stripANSIstr(m.renderZeroState())
	if !strings.Contains(plain, "toolhive gateway detected (no API key needed) — /models") {
		t.Errorf("splash should render the gateway-detected line when an AvailableNotDefault status exists, got:\n%s", plain)
	}

	// Suppressed when the gateway IS the default (AvailableNotDefault false).
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", ModelCount: 5, AvailableNotDefault: false}}
	plain = stripANSIstr(m.renderZeroState())
	if strings.Contains(plain, "gateway detected") {
		t.Errorf("splash should NOT render the gateway line when the gateway is the default, got:\n%s", plain)
	}

	// Suppressed with no statuses (byte-identical pre-feature path).
	m.modelCatalog.statuses = nil
	plain = stripANSIstr(m.renderZeroState())
	if strings.Contains(plain, "gateway detected") {
		t.Errorf("splash should NOT render the gateway line with no statuses, got:\n%s", plain)
	}
}
