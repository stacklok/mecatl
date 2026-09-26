package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Tests for the /effort picker (ADR 0055): a tiny SELECTING enum overlay that
// FORK-RESUMES the session onto a peer at the chosen reasoning-effort tier (ADR
// 0068) — enter applies DIRECTLY (no confirm step) and the transcript SURVIVES.
// Renders purely from client state (no proto in ui).

// pressEffortKey routes a key through onEffortKey, asserting it was handled.
func pressEffortKey(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	mm, _, handled := m.onEffortKey(msg)
	if !handled {
		t.Fatalf("key %v should be handled by the open picker", msg)
	}
	return mm.(Model)
}

// TestF7OpensEffortPicker proves the replacement shortcut reaches the effort
// operation through the normal idle-key reducer.
func TestF7OpensEffortPicker(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	m = mm.(Model)
	if m.effort.view != effortPanel {
		t.Fatalf("f7 left effort view = %v, want effortPanel", m.effort.view)
	}
}

// TestRunEffortOpensPicker asserts runEffort opens the picker, blurs the input, and
// renders the enum rows (no RPC — the enum is fixed and client-owned).
func TestRunEffortOpensPicker(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runEffort()
	m = mm.(Model)
	if m.effort.view != effortPanel {
		t.Fatalf("view = %v, want effortPanel", m.effort.view)
	}
	if m.prompt.Focused() {
		t.Error("opening the picker should blur the textarea")
	}
	_ = cmd
	out := stripANSIstr(m.View().Content)
	for _, tier := range effortTiers {
		if !strings.Contains(out, tier) {
			t.Errorf("picker missing the %q tier row:\n%s", tier, out)
		}
	}
	if !strings.Contains(out, "Reasoning effort") {
		t.Errorf("picker missing the title:\n%s", out)
	}
}

// TestEffortPickerWarnsOnNoReasoningModel (ADR 0055 UX): when the CURRENT effective
// model is KNOWN (in the loaded inventory) to lack reasoning support, the picker shows
// a degrade warning — so a user who restarts for an unattainable tier is acknowledged
// in the UI, not only the server log. A reasoning-capable model shows NO warning, and
// an UNKNOWN model (not in the inventory) is silent (fail-open).
func TestEffortPickerWarnsOnNoReasoningModel(t *testing.T) {
	const warn = "no reasoning support"

	// (1) Known no-reasoning model → warning.
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.modelCatalog.models = []client.ModelInfo{
		{ID: "no-reason", ProviderID: "openai", DisplayName: "No Reason", Reasoning: false},
	}
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "no-reason"}
	mm, _ := m.runEffort()
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, warn) {
		t.Errorf("a known no-reasoning model must warn in the picker:\n%s", out)
	}

	// (2) Reasoning-capable model → no warning.
	m2 := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m2.modelCatalog.models = []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Reasoning: true},
	}
	m2.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	mm2, _ := m2.runEffort()
	if got := stripANSIstr(mm2.(Model).View().Content); strings.Contains(got, warn) {
		t.Errorf("a reasoning-capable model must NOT warn:\n%s", got)
	}

	// (3) Unknown model (not in inventory) → silent (fail-open).
	m3 := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m3.modelCatalog.models = nil
	m3.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "mystery"}
	mm3, _ := m3.runEffort()
	if got := stripANSIstr(mm3.(Model).View().Content); strings.Contains(got, warn) {
		t.Errorf("an unknown model must be silent (fail-open):\n%s", got)
	}
}

// TestRunEffortGatedWithoutModelSelection asserts the picker is a no-op when model
// selection is unavailable (the effort only matters with a selectable model).
func TestRunEffortGatedWithoutModelSelection(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, client.Capabilities{ModelSelection: false}, client.ModelSelection{})
	mm, _ := m.runEffort()
	m = mm.(Model)
	if m.effort.view != effortNone {
		t.Fatalf("without ModelSelection the picker must stay closed, view = %v", m.effort.view)
	}
}

// TestEffortCursorStartsOnCurrent asserts the cursor opens on the CURRENT effective
// effort row (so the active tier is pre-selected), not always at the top.
func TestEffortCursorStartsOnCurrent(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = "high"
	mm, _ := m.runEffort()
	m = mm.(Model)
	// effortTiers = [auto low medium high xhigh max] → "high" is index 3.
	if m.effort.cursor != 3 {
		t.Fatalf("cursor = %d, want 3 (the current 'high' row)", m.effort.cursor)
	}
}

// TestEffortNavBounds asserts arrow nav is clamped to [0, len-1].
func TestEffortNavBounds(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.runEffort()
	m = mm.(Model)
	// Up at the top stays at 0.
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.effort.cursor != 0 {
		t.Fatalf("up at top = %d, want 0", m.effort.cursor)
	}
	// Down past the bottom clamps at len-1.
	for i := 0; i < len(effortTiers)+3; i++ {
		m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.effort.cursor != len(effortTiers)-1 {
		t.Fatalf("down past bottom = %d, want %d", m.effort.cursor, len(effortTiers)-1)
	}
}

// TestEffortEscCloses asserts esc closes the picker.
func TestEffortEscCloses(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.runEffort()
	m = mm.(Model)
	mm, _, handled := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !handled {
		t.Fatal("esc should be handled by the open picker")
	}
	if mm.(Model).effort.view != effortNone {
		t.Error("esc should close the picker")
	}
}

// TestEffortPickForksDirectly is the load-bearing behavioural test (ADR 0068): it
// opens the picker, moves to a real tier, presses enter ONCE, and asserts the
// switchEffort fork-resume handoff fired DIRECTLY — no confirm step — with the
// selection synchronously applied (model preserved, effort changed, recorded as
// the explicit this-session pick), the overlay dismissed, and the phase driven to
// connecting. The fork carries ONLY the effort delta (the source id + effort); the
// model is preserved on the fork (never sent).
func TestEffortPickForksDirectly(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	// newModelsModel delivered openai/gpt-5 as the effective model — that is what the
	// effort change must PRESERVE.
	conv := m.deps.Session.(*fakeConv)

	mm, _ := m.runEffort()
	m = mm.(Model)
	// Move to "high" (index 3) and press enter — this applies DIRECTLY (no confirm).
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	mm, cmd, handled := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if !handled {
		t.Fatal("enter should be handled by the open picker")
	}

	// The selection applied synchronously: model PRESERVED, effort changed, recorded
	// as the explicit this-session pick.
	want := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"}
	if m.createModelSelection != want {
		t.Fatalf("createModelSelection = %+v, want %+v (model preserved, effort changed)", m.createModelSelection, want)
	}
	if m.pickedThisSession != want {
		t.Fatalf("pickedThisSession = %+v, want %+v", m.pickedThisSession, want)
	}
	// The overlay is dismissed (no confirm step) and the session is mid-switch.
	if m.effort.view != effortNone {
		t.Errorf("the switch should dismiss the effort overlay, view = %v", m.effort.view)
	}
	if m.phase != phaseConnecting {
		t.Errorf("the switch should drive phaseConnecting, got %v", m.phase)
	}
	// Run the fork cmd batch: ForkSession carries the SOURCE id + the effort ONLY.
	m = feedCmd(t, m, cmd)
	if conv.forkedFrom != "sess-test-0001" {
		t.Fatalf("forkedFrom = %q, want the source session sess-test-0001", conv.forkedFrom)
	}
	if conv.forkedEffort != "high" {
		t.Fatalf("forkedEffort = %q, want high", conv.forkedEffort)
	}
	// The source session is closed once (best-effort, after a successful fork).
	if got := conv.closed(); len(got) != 1 || got[0] != "sess-test-0001" {
		t.Fatalf("closed = %v, want [sess-test-0001] (source closed once after the fork)", got)
	}
	// The pick is persisted per-workspace (the effort rides the selection).
	if store.saves < 1 || store.lastSel != want {
		t.Fatalf("store: saves=%d lastSel=%+v, want >=1 / %+v", store.saves, store.lastSel, want)
	}
}

// TestEffortPickPreservesTranscript is the HEADLINE regression guard against
// re-introducing resetSession (ADR 0068): a fork-resume must leave m.conv (the
// conversation transcript) UNCHANGED across the switch + the SessionReadyMsg
// rebind — the fork carries the conversation server-side, so the client must NOT
// wipe it. Asserts the transcript, the session-id rebind to the fork id, and the
// effective-model refresh from the refetch.
func TestEffortPickPreservesTranscript(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	// The fork's GetSession refetch echoes the resolved model at the new effort.
	conv.resolvedModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"}
	// Stage a transcript: a user block + an assistant block (the fork must keep both).
	m.conv.addUser("what is the plan?")
	m.conv.startAssistant()
	m.conv.appendAssistant("the plan is …")
	before := m.conv

	mm, _ := m.runEffort()
	m = mm.(Model)
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // low (index 1)
	mm, cmd, _ := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	// The transcript is UNTOUCHED by the handoff itself.
	if len(m.conv.testBlocks()) != len(before.testBlocks()) {
		t.Fatalf("transcript blocks = %d after the handoff, want %d (fork must not wipe it)", len(m.conv.testBlocks()), len(before.testBlocks()))
	}
	m = feedCmd(t, m, cmd)
	// After the fork + SessionReadyMsg: the transcript is STILL unchanged …
	if len(m.conv.testBlocks()) != len(before.testBlocks()) {
		t.Fatalf("transcript blocks = %d after the fork, want %d (regression: resetSession re-introduced?)", len(m.conv.testBlocks()), len(before.testBlocks()))
	}
	if !reflect.DeepEqual(m.conv.testBlocks(), before.testBlocks()) {
		t.Errorf("typed scrollback snapshots changed across the fork")
	}
	// … the session id rebinds to the fork id …
	if m.sessionID != "sess-fork-1" {
		t.Errorf("sessionID = %q, want the fork id sess-fork-1", m.sessionID)
	}
	// … the source is closed exactly once …
	if got := conv.closed(); len(got) != 1 || got[0] != "sess-test-0001" {
		t.Errorf("closed = %v, want [sess-test-0001]", got)
	}
	// … and the effective model updates from the refetch (the new effort echo drives
	// the header suffix + the /effort cursor ●).
	if m.resolvedSessionModel.ReasoningEffort != "high" {
		t.Errorf("resolvedSessionModel.ReasoningEffort = %q, want high (from the fork's GetSession refetch)", m.resolvedSessionModel.ReasoningEffort)
	}
	if m.phase != phaseIdle {
		t.Errorf("phase = %v, want phaseIdle (the fork's SessionReadyMsg rebinds idle)", m.phase)
	}
}

// TestEffortPickFailureLeavesSourceOpen asserts the recoverable failure path (ADR
// 0068): a failed fork does NOT close the source session — the old session is still
// the user's live one — and the recoverable reducer leaves the app idle with
// enter-to-retry armed.
func TestEffortPickFailureLeavesSourceOpen(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.forkErr = context.DeadlineExceeded

	mm, _ := m.runEffort()
	m = mm.(Model)
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // low
	mm, cmd, _ := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	// The recoverable reducer fired (NOT the terminal fatal path).
	if m.restartFailed {
		t.Error("restartFailed = true, want false (the source remains bound after a failed fork)")
	}
	if m.phase != phaseIdle {
		t.Errorf("phase = %v, want phaseIdle (recoverable, not fatal)", m.phase)
	}
	// The source session was NOT closed — a failed fork leaves the live session alone.
	if got := conv.closed(); len(got) != 0 {
		t.Errorf("closed = %v, want empty (the source session is NOT closed on a failed fork)", got)
	}
}

func TestEffortPickFailureRetryDoesNotDiscardSource(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.forkErr = context.DeadlineExceeded

	m, cmd := effortHandoffPick(t, m)
	m = feedCmd(t, m, cmd)
	if m.sessionID != "sess-test-0001" || m.phase != phaseIdle || m.restartFailed {
		t.Fatalf("failed effort handoff did not restore source: id=%q phase=%v restartFailed=%v", m.sessionID, m.phase, m.restartFailed)
	}
	if conv.forkCount != 1 || len(conv.closed()) != 0 {
		t.Fatalf("fork failure changed source lifecycle: forks=%d closed=%v", conv.forkCount, conv.closed())
	}

	mm, retryCmd := m.onIdleSubmit()
	m = mm.(Model)
	if retryCmd != nil {
		t.Fatal("idle retry unexpectedly started a fresh restart after effort failure")
	}
	if conv.forkCount != 1 || m.sessionID != "sess-test-0001" {
		t.Fatalf("idle retry changed effort source: forks=%d id=%q", conv.forkCount, m.sessionID)
	}
}

// TestEffortPickRefetchFailureKeepsFork covers the post-fork GetSession-refetch
// failure leg (ADR 0068): the fork SUCCEEDS (the peer session exists) but the
// resolved-model refetch fails. The app must DEGRADE gracefully — recoverable
// (restartFailed armed, the fork origin recorded for a re-fork retry), NOT stuck in
// phaseConnecting and NOT fatal — and the source session IS closed (the fork itself
// succeeded). m.resolvedSessionModel may stay stale (the footer heal re-derives it), but
// the fork happened. (The transcript is the fork's guarantee: it rides the
// server-side copy regardless of the refetch.)
func TestEffortPickRefetchFailureKeepsFork(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.getSessionErr = context.DeadlineExceeded // the fork succeeds; the refetch fails

	mm, _ := m.runEffort()
	m = mm.(Model)
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // low
	mm, cmd, _ := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)

	// The fork FIRED and succeeded (the peer session was created server-side) …
	if conv.forkCount != 1 || conv.forkedFrom != "sess-test-0001" {
		t.Fatalf("fork: count=%d from=%q, want 1 / sess-test-0001 (the fork succeeded)", conv.forkCount, conv.forkedFrom)
	}
	// … so the source session remains open and only the exact failed target is cleaned up.
	if got := conv.closed(); len(got) != 1 || got[0] != "sess-fork-1" {
		t.Fatalf("closed = %v, want [sess-fork-1] (hydration failure cleans the target only)", got)
	}
	// … and NOTHING was created fresh (the fork path never calls CreateSession).
	if conv.createCount != 0 {
		t.Errorf("createCount = %d, want 0 (the fork path never re-creates)", conv.createCount)
	}
	if m.restartFailed {
		t.Error("restartFailed = true, want false (the source remains usable)")
	}
	if m.sessionID != "sess-test-0001" {
		t.Errorf("sessionID = %q, want source session after hydration failure", m.sessionID)
	}
	if m.phase != phaseIdle {
		t.Errorf("phase = %v, want phaseIdle (recoverable — NOT stuck connecting, NOT fatal)", m.phase)
	}
	if strings.Contains(stripANSIstr(m.statusMsg), "press enter to retry") {
		t.Errorf("statusMsg = %q, must not arm obsolete effort retry", m.statusMsg)
	}
}

// TestEffortPickAutoSendsEmpty asserts the auto sentinel maps to the EMPTY effort on
// the fork (and in the persisted selection) — the clean-state convention. The pick
// applies DIRECTLY (one enter, no confirm).
func TestEffortPickAutoSendsEmpty(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = "high" // start from a non-auto state
	conv := m.deps.Session.(*fakeConv)

	mm, _ := m.runEffort()
	m = mm.(Model)
	// The cursor opens on "high" (index 3); move up to "auto" (index 0).
	for i := 0; i < 5; i++ {
		m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.effort.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (auto)", m.effort.cursor)
	}
	mm, cmd, _ := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply directly
	m = mm.(Model)
	want := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: ""}
	if m.createModelSelection != want {
		t.Fatalf("createModelSelection = %+v, want %+v (auto ⇒ empty effort)", m.createModelSelection, want)
	}
	m = feedCmd(t, m, cmd)
	if conv.forkedEffort != "" {
		t.Fatalf("auto pick forked effort %q, want empty (the auto sentinel maps to unset)", conv.forkedEffort)
	}
}

// TestEffortValueLabelRoundTrip locks the auto↔"" sentinel mapping in both
// directions, and that real tiers are pass-through.
func TestEffortValueLabelRoundTrip(t *testing.T) {
	if effortValue("auto") != "" {
		t.Errorf("effortValue(auto) = %q, want empty", effortValue("auto"))
	}
	if effortValue("high") != "high" {
		t.Errorf("effortValue(high) = %q, want high", effortValue("high"))
	}
	if effortLabel("") != "auto" {
		t.Errorf("effortLabel(\"\") = %q, want auto", effortLabel(""))
	}
	if effortLabel("medium") != "medium" {
		t.Errorf("effortLabel(medium) = %q, want medium", effortLabel("medium"))
	}
}

// TestEffortHeaderSuffix asserts the header effort suffix renders the resolved effort
// and is ABSENT for the unset/auto states.
func TestEffortHeaderSuffix(t *testing.T) {
	if got := effortHeaderSuffix("high"); got != "high" {
		t.Errorf("effortHeaderSuffix(high) = %q, want high", got)
	}
	if got := effortHeaderSuffix(""); got != "" {
		t.Errorf("effortHeaderSuffix(\"\") = %q, want empty", got)
	}
	if got := effortHeaderSuffix("auto"); got != "" {
		t.Errorf("effortHeaderSuffix(auto) = %q, want empty (defensive)", got)
	}
}

// TestEffortPickerGolden locks the populated enum picker: the ● marks the current
// (effective) tier and the cursor highlights a different row, so both affordances
// render together.
func TestEffortPickerGolden(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = "medium" // the ● row
	mm, _ := m.runEffort()
	m = mm.(Model)
	// Move the cursor OFF the current row so the ● and the highlight are distinct.
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.effort.view != effortPanel {
		t.Fatalf("view = %v, want effortPanel", m.effort.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "effort_picker.golden", got)
}

// TestEffortPickCurrentTierForksToo pins that enter on the ALREADY-CURRENT tier
// still applies directly (no no-op short-circuit): the fork fires with the current
// tier's effort. Re-forking at the same tier is cheap + honest (the server may
// normalise/clamp it), and a silent no-op would read as a dead key.
func TestEffortPickCurrentTierForksToo(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	// The cursor opens on the CURRENT tier (unset ⇒ "auto", index 0) — enter WITHOUT
	// moving picks the already-current tier and forks directly.
	mm, _ := m.runEffort()
	m = mm.(Model)
	mm, cmd, handled := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if !handled {
		t.Fatal("enter on the current tier should be handled")
	}
	if m.effort.view != effortNone {
		t.Errorf("the switch should dismiss the effort overlay, view = %v", m.effort.view)
	}
	if m.phase != phaseConnecting {
		t.Errorf("phase = %v, want phaseConnecting (the fork fires even on the current tier)", m.phase)
	}
	m = feedCmd(t, m, cmd)
	if conv.forkCount != 1 {
		t.Fatalf("forkCount = %d, want 1 (the fork fires even on the current tier)", conv.forkCount)
	}
	if conv.forkedEffort != "" {
		t.Errorf("forkedEffort = %q, want empty (the current tier is unset/auto)", conv.forkedEffort)
	}
}

// TestEffortPickCurrentTierNonAutoForksToo extends the fork-fires-even-on-current-
// tier invariant to a NON-AUTO tier: with the source at ReasoningEffort "high" the
// cursor opens on "high", and enter WITHOUT moving still forks — carrying "high"
// verbatim. A silent no-op would read as a dead key whatever the tier, not just auto.
func TestEffortPickCurrentTierNonAutoForksToo(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	m.resolvedSessionModel.ReasoningEffort = "high" // the CURRENT tier is non-auto
	conv := m.deps.Session.(*fakeConv)

	mm, _ := m.runEffort()
	m = mm.(Model)
	// The cursor opens on the current "high" row (index 3) — enter WITHOUT moving.
	if m.effort.cursor != 3 {
		t.Fatalf("cursor = %d, want 3 (the current 'high' row)", m.effort.cursor)
	}
	mm, cmd, handled := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if !handled {
		t.Fatal("enter on the current non-auto tier should be handled")
	}
	if m.phase != phaseConnecting {
		t.Errorf("phase = %v, want phaseConnecting (the fork fires even on the current tier)", m.phase)
	}
	m = feedCmd(t, m, cmd)
	if conv.forkCount != 1 {
		t.Fatalf("forkCount = %d, want 1 (the fork fires even on the current tier)", conv.forkCount)
	}
	if conv.forkedEffort != "high" {
		t.Errorf("forkedEffort = %q, want high (the current non-auto tier rides the fork)", conv.forkedEffort)
	}
}

// TestEffortPickerSwallowsOtherKeys pins the picker's keyboard ownership: an
// arbitrary rune key while the picker is open is swallowed (handled=true) and
// neither dismisses the overlay nor leaks to the prompt — only enter/esc/arrows
// act.
func TestEffortPickerSwallowsOtherKeys(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.runEffort()
	m = mm.(Model)
	// A stray rune key is swallowed; the overlay stays on the panel.
	mm, _, handled := m.onEffortKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !handled {
		t.Fatal("a stray rune key should be handled (swallowed) by the picker overlay")
	}
	m = mm.(Model)
	if m.effort.view != effortPanel {
		t.Fatalf("view = %v, want effortPanel (the key must not leak or dismiss)", m.effort.view)
	}
}

// TestEffortRendersInHeader asserts the resolved effort appears beside the model in
// the header when set, and is absent when unset (the footer/header live display).
func TestEffortRendersInHeader(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	// Unset effort: no suffix beside the model. (The inventory is not loaded here, so
	// the header shows the raw effective model id "gpt-5", not the display name.)
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "gpt-5") {
		t.Fatalf("header should show the model:\n%s", header)
	}
	if strings.Contains(header, "· high") {
		t.Fatalf("unset effort must not render a suffix:\n%s", header)
	}
	// Set effort: the suffix renders beside the model.
	m.resolvedSessionModel.ReasoningEffort = "high"
	m.refreshView()
	header = stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "· high") {
		t.Fatalf("a set effort should render a ` · high` suffix beside the model:\n%s", header)
	}
}

// TestEffortForkReturnsCapsFromSnapshot asserts that the /effort fork's
// switchEffortCmd populates SessionReadyMsg.Capabilities from the GetSession
// snapshot's Capabilities field rather than leaving it zero (issue #348).
func TestEffortForkReturnsCapsFromSnapshot(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)

	// Simulate a server that returns non-zero caps on GetSession (the fork's
	// refetch after ForkSession succeeds).
	wantCaps := client.Capabilities{MCP: true, Teams: true, Agents: true}
	conv.getSessionCaps = wantCaps

	// Drive the /effort pick: open, pick "high", press enter.
	mm, _ := m.runEffort()
	m = mm.(Model)
	// Move to "high" (index 3).
	for i := 0; i < 3; i++ {
		m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	mm, cmd, handled := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if !handled {
		t.Fatal("enter should be handled by the open picker")
	}

	// Run the fork cmd batch: it should return a SessionReadyMsg with non-zero caps.
	m = feedCmd(t, m, cmd)
	// The fork succeeded — m.caps should now be the adopted value (applied
	// by applySessionReady when it processes SessionReadyMsg).
	if m.caps != wantCaps {
		t.Fatalf("after fork: caps = %+v, want %+v (adopted from the GetSession snapshot)", m.caps, wantCaps)
	}
}
