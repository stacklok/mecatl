package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestTurnEndZeroUsageKeepsStickyContextMeter pins issue-#82 Fix A2: a TurnEndMsg
// reporting zero input tokens (a stalled / usage-less turn) must NOT erase a
// previously-known context occupancy. The meter is sticky, mirroring the sticky
// window denominator.
func TestTurnEndZeroUsageKeepsStickyContextMeter(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{
			SessionID:     "sess-sticky-0001",
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "m", ContextWindow: 200000},
		},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000}},
	)
	if m.contextTokens != 40000 {
		t.Fatalf("after a non-zero turn contextTokens = %d, want 40000", m.contextTokens)
	}
	// A subsequent zero-usage turn.end must leave the last-known value intact.
	m = applyAll(m, client.TurnEndMsg{Turn: 2, Usage: client.Usage{InputTokens: 0, OutputTokens: 0}})
	if m.contextTokens != 40000 {
		t.Errorf("after a zero-usage turn contextTokens = %d, want 40000 (sticky, not erased)", m.contextTokens)
	}
}

// TestCreateSessionCmdCarriesCaps asserts the createSessionCmd builds a
// SessionReadyMsg carrying the capabilities the SessionCreator returned (the
// relayed truth), and that the SessionReadyMsg handler stores them on m.caps.
// This pins the Phase-A plumbing: caps flow create → cmd → msg → model, stored
// (and, this phase, unrendered).
func TestCreateSessionCmdCarriesCaps(t *testing.T) {
	want := client.Capabilities{MCP: true, Memory: true, Teams: true}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, caps: want}
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Ctx:     context.Background(),
	})

	cmd := m.createSessionCmd()
	if cmd == nil {
		t.Fatal("createSessionCmd returned nil")
	}
	msg := cmd()
	ready, ok := msg.(client.SessionReadyMsg)
	if !ok {
		t.Fatalf("createSessionCmd msg = %T, want client.SessionReadyMsg", msg)
	}
	if ready.SessionID != "sess-test-0001" {
		t.Fatalf("SessionID = %q, want sess-test-0001", ready.SessionID)
	}
	if ready.Capabilities != want {
		t.Fatalf("Capabilities = %+v, want %+v", ready.Capabilities, want)
	}

	got := applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, ready)
	if got.caps != want {
		t.Fatalf("model caps = %+v, want %+v", got.caps, want)
	}
}

// TestSessionReadyDefaultCapsAllFalse asserts an older server (no caps field)
// leaves the model at the all-false zero value rather than over-promising.
func TestSessionReadyDefaultCapsAllFalse(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}} // caps left zero
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Ctx:     context.Background(),
	})
	ready := m.createSessionCmd()().(client.SessionReadyMsg)
	if (ready.Capabilities != client.Capabilities{}) {
		t.Fatalf("default caps = %+v, want all-false zero value", ready.Capabilities)
	}
	got := applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, ready)
	if (got.caps != client.Capabilities{}) {
		t.Fatalf("model caps = %+v, want all-false zero value", got.caps)
	}
}

// TestEffectiveModelInHeaderFromTurnZero asserts the effective model the server
// resolved (echoed on SessionReadyMsg) lands in m.resolvedSessionModel AND renders in the
// header from turn zero — and that NO model segment shows while still connecting (no
// create response yet). It also covers the older-server nil-resolved-model graceful
// degrade: zero value ⇒ still no segment, no crash.
func TestEffectiveModelInHeaderFromTurnZero(t *testing.T) {
	resolved := client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5-effective", ContextWindow: 400000}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, resolvedModel: resolved}
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Ctx:     context.Background(),
	})

	// While connecting (before the create response), the header shows NO model
	// segment — the server owns the value and the ui must not guess it.
	connecting := applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if strings.Contains(connecting.renderHeader(), "gpt-5-effective") {
		t.Fatalf("connecting header shows a model segment, want none:\n%s", connecting.renderHeader())
	}

	ready := m.createSessionCmd()().(client.SessionReadyMsg)
	if ready.ResolvedModel != resolved {
		t.Fatalf("SessionReadyMsg.ResolvedModel = %+v, want %+v", ready.ResolvedModel, resolved)
	}
	got := applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30}, ready)
	if got.resolvedSessionModel != resolved {
		t.Fatalf("m.resolvedSessionModel = %+v, want %+v", got.resolvedSessionModel, resolved)
	}
	if !strings.Contains(got.renderHeader(), "gpt-5-effective") {
		t.Fatalf("header missing the effective model id from turn zero:\n%s", got.renderHeader())
	}
}

// TestEffectiveModelDrivesFooterMeter closes the echo→render loop (issue #65): the
// SAME server-echoed ResolvedModel{ContextWindow: 400000} that lands in
// m.resolvedSessionModel becomes the footer meter's denominator (no --context-window
// override), so a 40K occupancy renders the bar + "40K/400K" through the live
// reducer path.
func TestEffectiveModelDrivesFooterMeter(t *testing.T) {
	resolved := client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5-effective", ContextWindow: 400000}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, resolvedModel: resolved}
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Ctx:     context.Background(),
	})
	ready := m.createSessionCmd()().(client.SessionReadyMsg)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		ready,
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000, OutputTokens: 800}},
	)
	if got := m.contextWindow(); got != 400000 {
		t.Fatalf("contextWindow() = %d, want 400000 (server-echoed)", got)
	}
	got := stripANSIstr(m.fitFooter("connected", 160))
	if !strings.Contains(got, "40K/400K") {
		t.Errorf("footer = %q, want it to contain %q (echo→render loop closed)", got, "40K/400K")
	}
}

// TestFooterMeterFollowsModelSwitch covers req 4 ("follows model switch for free")
// directly: a model switch is just a fresh SessionReadyMsg carrying the new model's
// window, so applying one with a 200K window then a SECOND with a 400K window must
// move the footer denominator from /200K to /400K with NO --context-window override.
func TestFooterMeterFollowsModelSwitch(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	// First model: 200K window, 40K occupied.
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{
			SessionID:     "sess-switch-0001",
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "small", ContextWindow: 200000},
		},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000}},
	)
	if got := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(got, "/200K") {
		t.Fatalf("after first model the footer should show /200K, got %q", got)
	}
	// Switch model (a fresh SessionReadyMsg with a larger window) — the denominator
	// must follow with no other plumbing.
	m = applyAll(m, client.SessionReadyMsg{
		SessionID:     "sess-switch-0002",
		ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "large", ContextWindow: 400000},
	})
	if got := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(got, "/400K") {
		t.Errorf("after model switch the footer should show /400K, got %q", got)
	}
}

// TestEffectiveModelOlderServerNoSegment asserts an older server (nil resolved_model
// ⇒ zero value) leaves m.resolvedSessionModel zero and the header shows no model segment —
// graceful degrade, no crash.
func TestEffectiveModelOlderServerNoSegment(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}} // resolvedModel left zero
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Ctx:     context.Background(),
	})
	ready := m.createSessionCmd()().(client.SessionReadyMsg)
	if (ready.ResolvedModel != client.ResolvedModel{}) {
		t.Fatalf("older-server ResolvedModel = %+v, want zero value", ready.ResolvedModel)
	}
	got := applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30}, ready)
	if (got.resolvedSessionModel != client.ResolvedModel{}) {
		t.Fatalf("m.resolvedSessionModel = %+v, want zero value", got.resolvedSessionModel)
	}
	// headerModelLabel returns "" on a zero effective model, so the header has no
	// model segment — just exercising the render path proves no crash.
	if got.headerModelLabel() != "" {
		t.Fatalf("headerModelLabel = %q, want empty (no model segment on older server)", got.headerModelLabel())
	}
	_ = got.renderHeader()
}

// TestHeaderModelLabelUsesInventoryDisplayName asserts the header resolves the human
// display name from the ListModels inventory by (provider_id, model_id), falling
// back to the raw id when the inventory has no match.
func TestHeaderModelLabelUsesInventoryDisplayName(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.phase = phaseIdle // past the connecting gate (the create response has landed)
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	m.modelCatalog.models = []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5 (Friendly)"},
		{ID: "other", ProviderID: "openai", DisplayName: "Other"},
	}
	if got := m.headerModelLabel(); got != "GPT-5 (Friendly)" {
		t.Fatalf("headerModelLabel = %q, want the inventory display name", got)
	}

	// No inventory match ⇒ raw model id.
	m.modelCatalog.models = []client.ModelInfo{{ID: "different", ProviderID: "openai", DisplayName: "X"}}
	if got := m.headerModelLabel(); got != "gpt-5" {
		t.Fatalf("headerModelLabel = %q, want the raw model id fallback", got)
	}
}

// TestHeaderModelLabelFallsBackWhenNoEffectiveModel covers branch 3 (older server,
// post-connect, no effective model): the header falls back to the picker's active
// selection, then to the launch-time --model — the PRE-EXISTING behavior, kept as-is.
func TestHeaderModelLabelFallsBackWhenNoEffectiveModel(t *testing.T) {
	// createModelSelection wins when set (and no effective model).
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.phase = phaseIdle
	m.createModelSelection = client.ModelSelection{ProviderID: "openai", ModelID: "x"}
	if got := m.headerModelLabel(); got != "x" {
		t.Fatalf("headerModelLabel = %q, want the active selection %q", got, "x")
	}

	// With no effective model AND no active selection, fall back to deps.Model.
	m2 := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Model: "y", Ctx: context.Background()})
	m2.phase = phaseIdle
	if got := m2.headerModelLabel(); got != "y" {
		t.Fatalf("headerModelLabel = %q, want the launch-time --model %q", got, "y")
	}
}
