package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// The user-reachable E2E for the /models slice, at the teatest level (the real
// program loop). It proves the headline behaviour end-to-end: a PERSISTED model
// selection threads through the connect sequence (ListModels → reconcile →
// CreateSession) and the create carries the picked (provider, model); and the
// key-removed fallback proves a now-unavailable persisted provider does NOT reach
// the create (it would hard-fail with InvalidArgument) — the create carries the
// empty selection and connect still succeeds.
//
// teatest DISCIPLINE (the documented -race flush-starvation flake): these cases
// sequence on the fake's goroutine signals (conv.created) + the onPhase observer
// (prog.wait/FinalModel) — NEVER WaitFor(tm.Output()). conv.created closes when
// CreateSession returns, conv.createdSel records what it carried, and onPhase
// surfaces the reducer reaching phaseIdle.

// newModelsProgram wires a real program with a Models lister + a pre-seeded
// persisted selection + the deterministic create/phase signals. The fake stream is
// idle-able: no prompt is sent (the slice's create-time behaviour is what we test).
func newModelsProgram(t *testing.T, models []client.ModelInfo, initial client.ModelSelection) (Model, *fakeConv, *progress) {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{
		recv:         recv,
		send:         &fakeSender{},
		caps:         client.Capabilities{ModelSelection: true},
		sessionReady: make(chan struct{}),
		created:      make(chan struct{}),
	}
	prog := newProgress()
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: models},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Ctx:          context.Background(),
		NoAltScreen:  true,
		InitialModel: initial,
		onPhase:      prog.record,
	})
	return m, conv, prog
}

// TestModelsE2EPersistedSelectionCarried drives the program through connect with a
// persisted, still-AVAILABLE selection and asserts the startup CreateSession
// carried exactly that (provider, model) — the §4 reconcile-then-create sequence.
func TestModelsE2EPersistedSelectionCarried(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude", ContextLimit: 1000000},
	}
	m, conv, prog := newModelsProgram(t, models, persisted)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	// Sequence on the create-happened signal (goroutine signal), not output.
	waitClosed(t, "CreateSession after ListModels reconcile", conv.created, 5*time.Second)
	if conv.createdSel != persisted {
		t.Fatalf("CreateSession carried %+v, want the persisted %+v", conv.createdSel, persisted)
	}
	// The reducer settles to idle (connected).
	prog.wait(t, phaseIdle, 5*time.Second)

	// Graceful double-ctrl+c quit.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if fm.createModelSelection != persisted {
		t.Errorf("final createModelSelection = %+v, want %+v", fm.createModelSelection, persisted)
	}
}

// TestModelsE2EKeyRemovedFallback drives connect with a persisted selection whose
// provider is NO LONGER in the inventory (key removed). The reconcile must clear
// it, so the create carries the EMPTY selection (server default) and connect still
// succeeds — the regression guard against the InvalidArgument hard-fail.
func TestModelsE2EKeyRemovedFallback(t *testing.T) {
	gone := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	// Only openai is available now — openrouter's key was removed.
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
	}
	m, conv, prog := newModelsProgram(t, models, gone)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	waitClosed(t, "CreateSession after key-removed reconcile", conv.created, 5*time.Second)
	if !conv.createdSel.IsZero() {
		t.Fatalf("CreateSession carried %+v, want the EMPTY selection (server default) after fallback", conv.createdSel)
	}
	prog.wait(t, phaseIdle, 5*time.Second)

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if !fm.createModelSelection.IsZero() {
		t.Errorf("final createModelSelection = %+v, want zero (fell back to server default)", fm.createModelSelection)
	}
}

// TestModelsE2EStaleSnapshotCarriesSavedSelection is the issue #41 headline e2e:
// connect with a persisted selection whose PROVIDER is in the inventory but whose
// exact MODEL is not (the boot ListModels snapshot is the embedded catalog floor
// until the async live refresh lands, which the connect race always wins). The
// provider-level reconcile must KEEP the selection, so the startup create carries
// it verbatim — the server, not the stale snapshot, validates the model string.
func TestModelsE2EStaleSnapshotCarriesSavedSelection(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	// The openrouter provider is present; the exact saved model is absent (the
	// embedded floor predates it).
	models := []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
		{ID: "openai/gpt-5", ProviderID: "openrouter", DisplayName: "GPT-5", ContextLimit: 400000},
	}
	m, conv, prog := newModelsProgram(t, models, persisted)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	// Sequence on the create-happened signal (goroutine signal), not output.
	waitClosed(t, "CreateSession after provider-level reconcile", conv.created, 5*time.Second)
	if conv.createdSel != persisted {
		t.Fatalf("CreateSession carried %+v, want the KEPT persisted %+v (issue #41)", conv.createdSel, persisted)
	}
	prog.wait(t, phaseIdle, 5*time.Second)

	// Graceful double-ctrl+c quit.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if fm.createModelSelection != persisted {
		t.Errorf("final createModelSelection = %+v, want the kept %+v", fm.createModelSelection, persisted)
	}
}

// TestModelsE2EFilterAndSelect drives the headline scroll+filter flow end-to-end:
// after connect, open /models (via the slash-command submit path), type a filter
// that uniquely narrows the list, press enter, and assert the FILTERED+chosen model
// is carried into createModelSelection. Sequenced on the create signal + FinalModel only,
// never on tm.Output() (the documented flush-starvation flake discipline).
func TestModelsE2EFilterAndSelect(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude", ContextLimit: 1000000},
	}
	// No persisted selection: the startup create carries the empty (default).
	m, conv, prog := newModelsProgram(t, models, client.ModelSelection{})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)

	// Open the picker by typing "/models" and pressing enter (the slash-command
	// submit path → runModels → openModels).
	for _, r := range "/models" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Type a filter that uniquely narrows to the openrouter/claude row, then press
	// enter — the seamless switch (no confirm overlay): a live session exists, so the
	// carryover handoff fires and createModelSelection is set to the filtered+chosen model.
	for _, r := range "claude" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Graceful double-ctrl+c quit.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	want := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if fm.createModelSelection != want {
		t.Errorf("final createModelSelection = %+v, want the filtered+chosen %+v (seamless switch)", fm.createModelSelection, want)
	}
}
