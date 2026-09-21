package ui

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Tests for the /models picker: the first SELECTING overlay (cursor + enter-to-
// select + persist), mirroring the mcp.go resource picker. It is idle-only and
// renders purely from client.ModelInfo (no proto in ui).

// newModelsModel builds an idle, sized Model wired to the given fakeModels +
// store + caps, with an optional pre-seeded active selection (the persisted
// last-used). It uses applyAll with SessionReadyMsg (skipping Init's connect
// sequencing) so the picker can be opened directly while idle.
func newModelsModel(t *testing.T, fm *fakeModels, store SelectionStore, caps client.Capabilities, initial client.ModelSelection) Model {
	t.Helper()
	return newModelsModelSized(t, fm, store, caps, initial, 100, 30)
}

// newModelsModelSized is newModelsModel with an explicit terminal size, so the
// window tests can force a small row budget (short height ⇒ the list clips).
func newModelsModelSized(t *testing.T, fm *fakeModels, store SelectionStore, caps client.Capabilities, initial client.ModelSelection, w, h int) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:        conv,
		Conv:           conv,
		Models:         fm,
		Transcript:     modelSwitchTranscriptLoader{},
		SelectionStore: store,
		InitialModel:   initial,
		Theme:          theme.New("aztec", theme.AztecPalette()),
		Server:         "127.0.0.1:8080",
		Workspace:      "/workspace",
		Mode:           "default",
		Model:          "mock-model",
		Ctx:            context.Background(),
		NoAltScreen:    true,
	})
	// Deliver an effective model in the create response so the picker's provenance
	// line + the header model segment render from a KNOWN server-resolved model (the
	// session is FIXED on it). gpt-5/openai matches sampleModels()'s default row.
	m = applyAll(m,
		tea.WindowSizeMsg{Width: w, Height: h},
		client.SessionReadyMsg{
			SessionID:     "sess-test-0001",
			Capabilities:  caps,
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
		},
	)
	return m
}

type modelSwitchTranscriptLoader struct{}

func (modelSwitchTranscriptLoader) GetSessionTranscript(_ context.Context, id string) (client.SessionTranscript, error) {
	return client.SessionTranscript{SessionID: id, Complete: true}, nil
}

type handoffTranscriptLoader struct {
	conv       *fakeConv
	transcript client.SessionTranscript
	err        error
}

func (f *handoffTranscriptLoader) GetSessionTranscript(_ context.Context, _ string) (client.SessionTranscript, error) {
	f.conv.mu.Lock()
	f.conv.operations = append(f.conv.operations, "transcript")
	f.conv.mu.Unlock()
	if f.err != nil {
		return client.SessionTranscript{}, f.err
	}
	return f.transcript, nil
}

// only, neither, and a context-limit-absent row.
func sampleModels() *fakeModels {
	// PromptCached mirrors production for these providers (ADR 0346): the
	// canonical openai and openrouter endpoints both resolve to a cache dialect.
	// The UNCACHED contrast has its own fixture, uncachedModels.
	return &fakeModels{models: []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Image: true, Reasoning: true, ContextLimit: 200000, PromptCached: true},
		{ID: "gpt-5-mini", ProviderID: "openai", DisplayName: "GPT-5 mini", Reasoning: true, ContextLimit: 128000, PromptCached: true},
		{ID: "text-embed", ProviderID: "openai", DisplayName: "text-embed", PromptCached: true}, // neither cap, no ctx
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude", Image: true, Reasoning: true, ContextLimit: 1000000, PromptCached: true},
	}}
}

func modelsCaps() client.Capabilities { return client.Capabilities{ModelSelection: true} }

func modelsSurface(t *testing.T, m Model) *modelsState {
	t.Helper()
	s, ok := m.modal.(*modelsState)
	if !ok {
		t.Fatalf("modal = %T, want *modelsState", m.modal)
	}
	return s
}

// TestRunModelsOpensPicker asserts runModels opens the picker, blurs the input,
// fires ListModels, and renders the rows once the result lands.
func TestRunModelsOpensPicker(t *testing.T) {
	fm := sampleModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})

	mm, cmd := m.runModels()
	m = mm.(Model)
	if modelsSurface(t, m).view != modelsPanel {
		t.Fatalf("view = %v, want modelsPanel", modelsSurface(t, m).view)
	}
	if !modelsSurface(t, m).loading {
		t.Error("picker should be loading until ListModels lands")
	}
	if m.prompt.Focused() {
		t.Error("opening the picker should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runModels should fire the ListModels RPC command")
	}
	m = feedCmd(t, m, cmd)
	if fm.calls != 1 {
		t.Errorf("ListModels calls = %d, want 1", fm.calls)
	}
	if !strings.Contains(m.View().Content, "GPT-5") {
		t.Errorf("picker missing a model label:\n%s", m.View().Content)
	}
	for _, line := range strings.Split(modelSwitchDisclosure, "\n") {
		if !strings.Contains(stripANSIstr(m.View().Content), line) {
			t.Errorf("picker missing model-switch disclosure line %q:\n%s", line, m.View().Content)
		}
	}
}

// TestModelsPanelSanitizesNames locks the terminaltext.Sanitize wrappers in the /models
// picker against deletion: open the picker with a model whose id + display_name +
// provider_id embed ANSI/OSC escapes (the live OpenRouter catalog is an UNTRUSTED
// source now), feed the inventory, render, and assert no raw ESC (0x1b) survives.
// Mirrors skills_test.go's TestSkillsPanelSanitizesNames. Per repo memory the literal
// is an innocuous ANSI escape, never a destructive-looking command.
func TestModelsPanelSanitizesNames(t *testing.T) {
	fm := &fakeModels{models: []client.ModelInfo{
		// id-only row (no display name) ⇒ the row label falls back to the id, so the
		// id's sanitization is exercised in the rendered label.
		{ID: "\x1b]0;pwned\x07evil/model", ProviderID: "open\x1b[31mrouter"},
		// a row with a control/ANSI display name ⇒ the label sanitization is exercised.
		{ID: "openai/safe", ProviderID: "open\x1b[31mrouter", DisplayName: "\x1b[31mRed Model\x1b[0m"},
	}}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	// stripANSIstr removes the LEGITIMATE theme styling escapes; what remains must
	// carry NO raw ESC — if any survives, it came from the server-derived model
	// id/display_name/provider_id and terminaltext.Sanitize was not applied.
	out := stripANSIstr(m.View().Content)
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the rendered picker; terminaltext.Sanitize not applied:\n%q", out)
	}
	// The sanitized fields still render as inert text (ESC stripped, body kept):
	// the id-fallback label, the provider segment, and the display-name label.
	for _, want := range []string{"]0;pwnedevil/model", "[31mrouter", "[31mRed Model[0m"} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitized field %q not rendered as inert text, got:\n%q", want, out)
		}
	}

	// Typing a filter echoes USER text, but the ROWS still carry server strings:
	// re-assert no raw ESC survives once the filter narrows the (sanitized) rows.
	m = typeFilter(t, m, "router")
	out = stripANSIstr(m.View().Content)
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("raw ESC leaked into the FILTERED picker; terminaltext.Sanitize not applied:\n%q", out)
	}
}

// TestRunModelsNilGuard asserts the picker won't open without a model lister wired.
func TestRunModelsNilGuard(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: modelsCaps()}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{Capabilities: modelsCaps()})
	mm, cmd := m.runModels()
	if mm.(Model).modal != nil || cmd != nil {
		t.Error("runModels with no lister wired must be a no-op")
	}
}

// TestModelsCursorNav asserts up/down move across the flat list (every model is a
// row) and clamp at the ends. The cursor now indexes the FILTERED slice; with an
// empty filter filtered == models, so the clamps hold unchanged — assert
// len(filtered)==4 as a guard that the sync ran.
func TestModelsCursorNav(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Fatalf("initial cursor = %d, want 0", modelsSurface(t, m).list.Cursor())
	}
	if len(modelsSurface(t, m).filtered) != 4 {
		t.Fatalf("filtered len = %d, want 4 (empty filter ⇒ filtered == models)", len(modelsSurface(t, m).filtered))
	}
	// Up at the top clamps.
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Errorf("cursor after up at top = %d, want 0 (clamped)", modelsSurface(t, m).list.Cursor())
	}
	// Down moves through all 4 rows then clamps at the last.
	for i := 0; i < 6; i++ {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if modelsSurface(t, m).list.Cursor() != 3 {
		t.Errorf("cursor after many downs = %d, want 3 (clamped at last)", modelsSurface(t, m).list.Cursor())
	}
}

// typeFilter feeds each rune of s into the open picker's focused filter input via
// onModelsKey (the production routing), asserting each key is handled. It mirrors
// how a user types: one KeyPressMsg per rune (Code = the rune).
func typeFilter(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// TestModelsFilterNarrows: typing "claude" into the focused filter narrows to the
// single openrouter/claude row, the cursor clamps to 0, and enter selects it over
// the FILTERED set (proving cursor + active operate over filtered, not models).
func TestModelsFilterNarrows(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	// Move the cursor off row 0 first to prove the filter clamps it back.
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})

	m = typeFilter(t, m, "claude")
	if len(modelsSurface(t, m).filtered) != 1 {
		t.Fatalf("filtered len = %d, want 1 (only claude matches)", len(modelsSurface(t, m).filtered))
	}
	if modelsSurface(t, m).filtered[0].ID != "anthropic/claude" {
		t.Fatalf("filtered[0].ID = %q, want anthropic/claude", modelsSurface(t, m).filtered[0].ID)
	}
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Errorf("cursor after narrowing = %d, want 0 (clamped to filtered bounds)", modelsSurface(t, m).list.Cursor())
	}
	// enter switches IMMEDIATELY (no confirm overlay): a live session exists, so the
	// carryover handoff fires. The picker closes and the phase moves to connecting.
	want := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatalf("enter should switch immediately and close the picker, view = %v", modelsSurface(t, m).view)
	}
	if m.phase != phaseConnecting {
		t.Fatalf("enter should drive phaseConnecting (seamless switch), phase = %v", m.phase)
	}
	if m.createModelSelection != want {
		t.Errorf("createModelSelection = %+v, want the filtered+chosen %+v", m.createModelSelection, want)
	}
	// Running the armed cmd fires CreateSessionWithCarryover with the live session id
	// as the source — the seamless switch's carryover seam.
	// Run only create/carryover and persistence; spinner/focus lifecycle commands
	// do not contribute to these assertions.
	m = feedModelSwitchBusiness(t, m, cmd)
	if got := conv(m).carryoverCalls(); got != 1 {
		t.Errorf("CreateSessionWithCarryover calls = %d, want 1 (seamless carryover switch)", got)
	}
}

// TestModelsCarryoverKeepsVisibleProjection verifies the source projection remains
// visible and intact while the authoritative target transcript is loading.
func TestModelsCarryoverKeepsVisibleProjection(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.conv.addUser("visible user")
	m.conv.startAssistant()
	m.conv.appendAssistant("visible assistant")
	m.refreshView()
	if len(m.rend.blocks.rendered) == 0 || len(m.rend.blocks.markdown) == 0 {
		t.Fatal("precondition: visible conversation should populate renderer caches")
	}

	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}
	mm, cmd, handled := m.chooseModel(sel, "")
	m = mm.(Model)
	if !handled || m.phase != phaseConnecting {
		t.Fatalf("chooseModel = handled:%t phase:%v, want connecting handoff", handled, m.phase)
	}
	for _, want := range []string{"visible user", "visible assistant"} {
		if !strings.Contains(stripANSIstr(m.View().Content), want) {
			t.Fatalf("connecting source projection missing %q:\n%s", want, m.View().Content)
		}
	}

	m = feedCmd(t, m, cmd)
	if m.phase != phaseIdle {
		t.Fatalf("phase after target transcript adoption = %v, want idle", m.phase)
	}
}

// conv extracts the wired *fakeConv from a newModelsModel-built Model (the Session
// and Conv deps are the same *fakeConv). A tiny local helper so the seamless-switch
// assertions can reach the fake's call counters without plumbing.
func conv(m Model) *fakeConv {
	c, _ := m.deps.Session.(*fakeConv)
	return c
}

// feedModelSwitchBusiness executes the source-known model-switch batch: create or
// carryover, then selection persistence. Each business leaf and every meaningful
// reducer follow-up runs through feedCmd. It deliberately skips only lifecycle
// waits: the spinner tick, outer textarea-focus blink, and a failed handoff's
// focus/live-feed waits (focus and feed rearming already mutate the model
// synchronously in that reducer).
func feedModelSwitchBusiness(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	batchMsg := runCmd(cmd)
	batch, ok := batchMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("model switch command = %T, want business batch or outer Batch(business, focus)", batchMsg)
	}
	business := batch
	if len(batch) == 2 {
		businessMsg := runCmd(batch[0])
		business, ok = businessMsg.(tea.BatchMsg)
		if !ok {
			t.Fatalf("outer model switch business command = %T, want Batch(create, save, spinner)", businessMsg)
		}
	}
	if len(business) != 3 {
		t.Fatalf("model switch business command has %d leaves, want create/save/spinner batch", len(business))
	}

	msg := runCmd(business[0])
	switch msg.(type) {
	case modelSwitchReadyMsg, client.SessionReadyMsg:
		mm, followup := m.Update(msg)
		m = feedCmd(t, mm.(Model), followup)
	case modelSwitchFailedMsg:
		mm, followup := m.Update(msg)
		m = mm.(Model)
		if followup == nil {
			t.Fatal("failed model switch returned no focus/rearm follow-up")
		}
		// The reducer synchronously focuses and rearms the source; the returned
		// commands only wait for the textarea blink and live stream.
	case restartFailedMsg:
		mm, followup := m.Update(msg)
		m = mm.(Model)
		if followup != nil {
			t.Fatal("failed plain create returned an unexpected follow-up")
		}
	default:
		t.Fatalf("model switch business command returned unsupported message %T", msg)
	}
	return feedCmd(t, m, business[1])
}

func TestFeedModelSwitchBusinessHandlesPlainCreateFailure(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	fake := conv(m)
	fake.createErr = errors.New("create unavailable")
	m = m.bindSessionID("")
	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}

	mm, cmd, handled := m.chooseModel(sel, "GPT-5 mini")
	if !handled || cmd == nil {
		t.Fatal("plain model switch did not return its create batch")
	}
	m = feedModelSwitchBusiness(t, mm.(Model), cmd)

	if fake.createCount != 1 {
		t.Fatalf("plain CreateSession calls = %d, want 1", fake.createCount)
	}
	if got := fake.closed(); len(got) != 0 {
		t.Fatalf("no-source switch closed sessions = %v, want none", got)
	}
	if m.phase != phaseIdle || m.sessionID != "" || !m.restartFailed || !m.prompt.Focused() {
		t.Fatalf("plain-create failure not recoverable: phase=%v session=%q restartFailed=%t focused=%t", m.phase, m.sessionID, m.restartFailed, m.prompt.Focused())
	}
	if statusText := stripANSIstr(m.statusMsg); !strings.Contains(statusText, "create unavailable") || !strings.Contains(statusText, "retry") {
		t.Fatalf("plain-create failure status = %q, want error and retry affordance", statusText)
	}
	if store.saves != 1 || store.lastSel != sel {
		t.Fatalf("selection persistence = saves:%d selection:%+v, want 1/%+v", store.saves, store.lastSel, sel)
	}
}

func TestFeedModelSwitchBusinessExecutesBusinessFollowupsWithoutLifecycle(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.conv.addUser("source transcript")
	conv(m).getSessionResults = []client.ResolvedModel{{ProviderID: "openrouter", ModelID: "hydrated-model"}}
	m.phase = phaseConnecting
	businessCalls, lifecycleCalls := 0, 0
	var businessOrder []string
	cmd := tea.Batch(tea.Batch(
		func() tea.Msg {
			businessCalls++
			businessOrder = append(businessOrder, "create")
			return modelSwitchReadyMsg{
				token:    m.modelSwitchRequestToken,
				sourceID: m.sessionID,
				ready: client.SessionReadyMsg{
					SessionID:     "sess-new",
					Capabilities:  modelsCaps(),
					ResolvedModel: client.ResolvedModel{ProviderID: "openrouter", ModelID: "anthropic/claude"},
				},
				transcript: client.SessionTranscript{
					SessionID: "sess-new",
					Complete:  true,
					Messages:  []client.ConversationMessage{{Role: "assistant", Text: "target transcript"}},
				},
			}
		},
		func() tea.Msg {
			businessCalls++
			businessOrder = append(businessOrder, "save")
			return selectionSavedMsg{err: errors.New("save failed")}
		},
		func() tea.Msg { lifecycleCalls++; return struct{}{} },
	), func() tea.Msg { lifecycleCalls++; return nil })

	m = feedModelSwitchBusiness(t, m, cmd)
	if businessCalls != 2 {
		t.Fatalf("business commands invoked = %d, want 2", businessCalls)
	}
	if got := strings.Join(businessOrder, ","); got != "create,save" {
		t.Fatalf("business command order = %q, want create,save", got)
	}
	if lifecycleCalls != 0 {
		t.Fatalf("lifecycle commands invoked = %d, want 0", lifecycleCalls)
	}
	if m.sessionID != "sess-new" || m.resolvedSessionModel.ModelID != "hydrated-model" || conv(m).getSessionCalls() != 1 {
		t.Fatalf("model-switch reducer effects missing: sessionID=%q resolved=%+v refreshCalls=%d", m.sessionID, m.resolvedSessionModel, conv(m).getSessionCalls())
	}
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "target transcript") || strings.Contains(view, "source transcript") {
		t.Fatalf("authoritative transcript was not adopted: %q", view)
	}
	if got := conv(m).closed(); len(got) != 1 || got[0] != "sess-test-0001" {
		t.Fatalf("source-close follow-up = %v, want [sess-test-0001]", got)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "could not persist") {
		t.Fatalf("selection-save reducer effect missing: status=%q", stripANSIstr(m.statusMsg))
	}
}

// TestModelsFilterByProvider proves provider_id is a match field: "openai" → the 3
// openai rows; "router" → the single openrouter row (matched by provider_id, not
// id/display_name).
func TestModelsFilterByProvider(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeFilter(t, m, "openai")
	if len(modelsSurface(t, m).filtered) != 3 {
		t.Errorf("filter \"openai\" → %d rows, want 3", len(modelsSurface(t, m).filtered))
	}

	// Reset and filter by a substring of provider_id ONLY.
	modelsSurface(t, m).filter.SetValue("")
	modelsSurface(t, m).syncFilter()
	m = typeFilter(t, m, "router")
	if len(modelsSurface(t, m).filtered) != 1 || modelsSurface(t, m).filtered[0].ProviderID != "openrouter" {
		t.Errorf("filter \"router\" should match the openrouter row by provider_id, got %+v", modelsSurface(t, m).filtered)
	}
}

// TestModelsFilterCaseInsensitive: "CLAUDE" matches "Claude".
func TestModelsFilterCaseInsensitive(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeFilter(t, m, "CLAUDE")
	if len(modelsSurface(t, m).filtered) != 1 || modelsSurface(t, m).filtered[0].ID != "anthropic/claude" {
		t.Errorf("case-insensitive filter \"CLAUDE\" should match Claude, got %+v", modelsSurface(t, m).filtered)
	}
}

// TestModelsFilterUppercaseField proves BOTH sides are lowered: an uppercase FIELD
// (ProviderID "OpenAI") matched by a lowercase query ("openai"). The previous
// case-insensitive test only lowered the query; this lowers the field.
func TestModelsFilterUppercaseField(t *testing.T) {
	in := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "OpenAI", DisplayName: "GPT-5"},
		{ID: "anthropic/claude", ProviderID: "OpenRouter", DisplayName: "Claude"},
	}
	got := filterModels(in, "openai")
	if len(got) != 1 || got[0].ProviderID != "OpenAI" {
		t.Errorf("lowercase query \"openai\" should match uppercase field \"OpenAI\", got %+v", got)
	}
}

// TestModelsFilterPreservesOrder: a query matching >1 row keeps the input
// (provider_id,id-sorted) order — filterModels must not reorder.
func TestModelsFilterPreservesOrder(t *testing.T) {
	in := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"},
		{ID: "gpt-5-mini", ProviderID: "openai", DisplayName: "GPT-5 mini"},
		{ID: "gpt-4", ProviderID: "openai", DisplayName: "GPT-4"},
	}
	got := filterModels(in, "gpt")
	if len(got) != 3 {
		t.Fatalf("filter \"gpt\" → %d rows, want 3", len(got))
	}
	for i := range in {
		if got[i].ID != in[i].ID {
			t.Errorf("filtered[%d].ID = %q, want %q (input order must be preserved)", i, got[i].ID, in[i].ID)
		}
	}
}

// TestModelsFilterCursorClamp: cursor at the last row, then a filter narrowing to 1
// row clamps the cursor to 0 (palette parity).
func TestModelsFilterCursorClamp(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	for i := 0; i < 3; i++ {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if modelsSurface(t, m).list.Cursor() != 3 {
		t.Fatalf("precondition: cursor = %d, want 3", modelsSurface(t, m).list.Cursor())
	}
	m = typeFilter(t, m, "gpt-5-mini")
	if len(modelsSurface(t, m).filtered) != 1 {
		t.Fatalf("filtered len = %d, want 1", len(modelsSurface(t, m).filtered))
	}
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Errorf("cursor after narrowing = %d, want 0 (clamped)", modelsSurface(t, m).list.Cursor())
	}
}

// TestModelsFilterEscClears asserts the two-stage esc: a non-empty filter is
// cleared (picker stays open, filtered restored to the full list); a second esc
// (now-empty filter) closes the picker.
func TestModelsFilterEscClears(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeFilter(t, m, "claude")
	if len(modelsSurface(t, m).filtered) != 1 {
		t.Fatalf("precondition: filtered len = %d, want 1", len(modelsSurface(t, m).filtered))
	}

	// First esc: clears the filter, stays open.
	mm, _, handled := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !handled {
		t.Fatal("esc should be handled by the open picker")
	}
	m = mm.(Model)
	if modelsSurface(t, m).view != modelsPanel {
		t.Error("first esc (non-empty filter) should keep the picker open")
	}
	if modelsSurface(t, m).filter.Value() != "" {
		t.Errorf("first esc should clear the filter, value = %q", modelsSurface(t, m).filter.Value())
	}
	if len(modelsSurface(t, m).filtered) != 4 {
		t.Errorf("filtered should be restored to the full list, len = %d want 4", len(modelsSurface(t, m).filtered))
	}

	// Second esc: empty filter ⇒ closes.
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.modal != nil {
		t.Error("second esc (empty filter) should close the picker")
	}
}

// TestModelsFilterDoesNotInterceptJK guards the arrow-only routing (the j/k-in-
// Up/Down trap): typing "kimi"/"jamba" must accumulate into the filter and NOT move
// the cursor (keys.Up/Down bind k/j, but the picker matches arrows by msg.String()).
func TestModelsFilterDoesNotInterceptJK(t *testing.T) {
	// A list whose names contain j and k so a filter is meaningful.
	fm := &fakeModels{models: []client.ModelInfo{
		{ID: "kimi-k2", ProviderID: "moonshot", DisplayName: "Kimi K2"},
		{ID: "jamba", ProviderID: "ai21", DisplayName: "Jamba"},
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"},
	}}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeFilter(t, m, "kimi")
	if modelsSurface(t, m).filter.Value() != "kimi" {
		t.Errorf("filter value = %q, want \"kimi\" (k/i/m/i must type, not navigate)", modelsSurface(t, m).filter.Value())
	}
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Errorf("cursor moved to %d while typing \"kimi\"; j/k must not be intercepted as nav", modelsSurface(t, m).list.Cursor())
	}
	if len(modelsSurface(t, m).filtered) != 1 || modelsSurface(t, m).filtered[0].ID != "kimi-k2" {
		t.Errorf("filter \"kimi\" should narrow to kimi-k2, got %+v", modelsSurface(t, m).filtered)
	}

	// And "jamba" likewise.
	modelsSurface(t, m).filter.SetValue("")
	modelsSurface(t, m).syncFilter()
	m = typeFilter(t, m, "jamba")
	if modelsSurface(t, m).filter.Value() != "jamba" || modelsSurface(t, m).list.Cursor() != 0 {
		t.Errorf("typing \"jamba\": value=%q cursor=%d, want value \"jamba\" cursor 0", modelsSurface(t, m).filter.Value(), modelsSurface(t, m).list.Cursor())
	}
}

// manyModels builds n flat models for the window tests, labelled model-NNN so an
// in/out-of-window assertion can target a specific row's text.
func manyModels(n int) *fakeModels {
	ms := make([]client.ModelInfo, n)
	for i := 0; i < n; i++ {
		id := "model-" + itoa3(i)
		ms[i] = client.ModelInfo{ID: id, ProviderID: "prov", DisplayName: id}
	}
	return &fakeModels{models: ms}
}

// itoa3 zero-pads a small int to 3 digits so the row labels are distinct
// substrings (model-007 is not a substring of model-070).
func itoa3(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// TestModelsWindowFollowsCursorPastBottom: with a list longer than the row budget
// at a small terminal height, paging the cursor past the window bottom keeps the
// SELECTED row in the rendered window and pushes an early row out of view.
func TestModelsWindowFollowsCursorPastBottom(t *testing.T) {
	fm := manyModels(30)
	m := newModelsModelSized(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{}, 100, 30)
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	// Drive the cursor to the last row.
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if modelsSurface(t, m).list.Cursor() != 29 {
		t.Fatalf("cursor after End = %d, want 29", modelsSurface(t, m).list.Cursor())
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "model-029") {
		t.Errorf("the selected last row (model-029) should be visible in the window:\n%s", out)
	}
	// The cursor's NEIGHBOR must ALSO be in-window, so a degenerate single-row window
	// can't pass this test.
	if !strings.Contains(out, "model-028") {
		t.Errorf("the neighbor row (model-028) should be visible in the window:\n%s", out)
	}
	if strings.Contains(out, "model-000") {
		t.Errorf("an early row (model-000) should be scrolled out of the window:\n%s", out)
	}
}

// TestModelsWindowFollowsCursorPastTop: after jumping to the bottom then back to the
// top, the first row is visible again (the window follows the cursor up too).
func TestModelsWindowFollowsCursorPastTop(t *testing.T) {
	fm := manyModels(30)
	m := newModelsModelSized(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{}, 100, 30)
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if modelsSurface(t, m).list.Cursor() != 0 {
		t.Fatalf("cursor after Home = %d, want 0", modelsSurface(t, m).list.Cursor())
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "model-000") {
		t.Errorf("the first row (model-000) should be visible after Home:\n%s", out)
	}
	// The cursor's NEIGHBOR must ALSO be in-window, so a degenerate single-row window
	// can't pass this test.
	if !strings.Contains(out, "model-001") {
		t.Errorf("the neighbor row (model-001) should be visible after Home:\n%s", out)
	}
	if strings.Contains(out, "model-029") {
		t.Errorf("the last row (model-029) should be out of the window after Home:\n%s", out)
	}
}

// TestModelsNoMatchNote: a filter matching nothing (inventory non-empty) renders the
// distinct "no models match …" note, NOT the server-empty/disabled copy, and enter
// is a no-op (cursor safe).
func TestModelsNoMatchNote(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeFilter(t, m, "zzzzz")
	if len(modelsSurface(t, m).filtered) != 0 {
		t.Fatalf("filtered len = %d, want 0", len(modelsSurface(t, m).filtered))
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, `no models match "zzzzz" — esc to clear`) {
		t.Errorf("the no-match note (with recovery hint) should render, got:\n%s", out)
	}
	if strings.Contains(out, "No selectable models advertised") || strings.Contains(out, "not available on this server") {
		t.Errorf("the no-match state must be distinct from server-empty/disabled copy:\n%s", out)
	}
	// enter is a no-op (cursor past the empty filtered set).
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !mm.(Model).createModelSelection.IsZero() {
		t.Error("enter on a no-match filter must be a no-op")
	}
}

// TestModelsChooseNoSessionUsesPlainCreate asserts that when NO live session exists
// (a failure left no session — the recoverable post-failure state), picking a model
// falls back to the plain create path (restartOnModel): CreateSession — NOT
// CreateSessionWithCarryover (there's no source to carry from). The pick is still
// persisted per-workspace and the phase drives to connecting. This is the honest
// fallback for the seamless switch when there is nothing to carry.
func TestModelsChooseNoSessionUsesPlainCreate(t *testing.T) {
	store := &fakeStore{}
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: modelsCaps()}
	m := newTestModelFromDeps(Deps{
		Session:        conv,
		Conv:           conv,
		Models:         sampleModels(),
		SelectionStore: store,
		Theme:          theme.New("aztec", theme.AztecPalette()),
		Server:         "127.0.0.1:8080",
		Workspace:      "/workspace",
		Mode:           "default",
		Ctx:            context.Background(),
		NoAltScreen:    true,
	})
	// Simulate the recoverable post-failure state: idle, no session, retry-armed. This
	// is the realistic no-session-idle scenario (pre-first-connect is phaseConnecting,
	// where the picker is closed).
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseIdle
	m.sessionID = ""
	m.restartFailed = true
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if m.sessionID != "" {
		t.Fatalf("precondition: no live session should exist, got %q", m.sessionID)
	}

	// Move to the 4th row (openrouter/claude) and enter — seamless switch (no session ⇒
	// plain create, no carryover source).
	for i := 0; i < 3; i++ {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	want := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if m.createModelSelection != want {
		t.Fatalf("createModelSelection = %+v, want %+v", m.createModelSelection, want)
	}
	if m.modal != nil {
		t.Errorf("enter should close the picker, view = %v", modelsSurface(t, m).view)
	}
	if m.phase != phaseConnecting {
		t.Fatalf("phase = %v, want phaseConnecting (seamless switch)", m.phase)
	}
	// Run only the plain create and persistence leaves; spinner/focus are lifecycle-only.
	m = feedModelSwitchBusiness(t, m, cmd)
	if conv.createCount != 1 {
		t.Fatalf("CreateSession calls = %d, want 1 (the plain create path, no source to carry from)", conv.createCount)
	}
	if got := conv.carryoverCalls(); got != 0 {
		t.Fatalf("CreateSessionWithCarryover calls = %d, want 0 (no live session ⇒ no carryover)", got)
	}
	if conv.createdSel != want {
		t.Errorf("CreateSession carried %+v, want %+v", conv.createdSel, want)
	}
	// The pick is persisted per-workspace.
	if store.saves != 1 || store.lastSel != want || store.lastWS != "/workspace" {
		t.Fatalf("store: saves=%d lastSel=%+v lastWS=%q, want 1 / %+v / /workspace",
			store.saves, store.lastSel, store.lastWS, want)
	}
	// With no live session, priorProvider=="" so crossProvider is false — the
	// armed status note surfaces the plain "conversation kept" form on the
	// SessionReadyMsg rebind, NEVER the cross-provider "prior reasoning cache
	// dropped" caveat (there was no prior model to strip). Mirrors
	// TestModelsChooseSwitchArmsStatusNote's stripANSIstr idiom.
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "conversation kept") {
		t.Errorf("status = %q, want it to contain \"conversation kept\" (no live session ⇒ no strip caveat)", st)
	}
	if strings.Contains(st, "prior reasoning cache dropped") {
		t.Errorf("status = %q, must NOT include a second cache warning", st)
	}
	if m.pendingModelSwitchNote != "" {
		t.Errorf("the note should be consumed (one-shot) after the rebind, got %q", m.pendingModelSwitchNote)
	}
}

// TestModelsChooseSwitchArmsStatusNote asserts the seamless switch arms the transient
// "switched to <model> — conversation kept" status note, surfaced on the SessionReadyMsg
// rebind. The picker is the only cache-warning surface.
func TestModelsChooseSwitchArmsStatusNote(t *testing.T) {
	cases := []struct {
		name           string
		live           client.ResolvedModel
		pickDowns      int // cursor downs from row 0 to the picked row
		wantNoteSubstr string
	}{
		{
			name:           "same-provider",
			live:           client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
			pickDowns:      1, // gpt-5-mini, also openai
			wantNoteSubstr: "switched to GPT-5 mini — conversation kept",
		},
		{
			name:           "cross-provider",
			live:           client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
			pickDowns:      3, // anthropic/claude, openrouter
			wantNoteSubstr: "switched to Claude — conversation kept",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			recv := &fakeRecver{gate: make(chan struct{})}
			conv := &fakeConv{
				recv:              recv,
				send:              &fakeSender{},
				caps:              modelsCaps(),
				echoSelAsResolved: true, // the rebind's SessionReadyMsg mirrors the picked selector
			}
			m := newTestModelFromDeps(Deps{
				Session:        conv,
				Conv:           conv,
				Models:         sampleModels(),
				Transcript:     modelSwitchTranscriptLoader{},
				SelectionStore: store,
				Theme:          theme.New("aztec", theme.AztecPalette()),
				Server:         "127.0.0.1:8080",
				Workspace:      "/workspace",
				Mode:           "default",
				Ctx:            context.Background(),
				NoAltScreen:    true,
			})
			m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
				client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: modelsCaps(), ResolvedModel: tc.live})
			mm, cmd := m.runModels()
			m = feedCmd(t, mm.(Model), cmd)

			for i := 0; i < tc.pickDowns; i++ {
				m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			}
			mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			// The note is armed (pendingModelSwitchNote); it is NOT yet the visible status
			// (the rebind has not landed).
			if m.pendingModelSwitchNote == "" {
				t.Fatalf("enter should arm the model-switch note (pendingModelSwitchNote), got empty")
			}
			// Drive only carryover + persistence. SessionReady and its resolved-model
			// follow-up are reduced once; spinner/focus lifecycle commands are skipped.
			m = feedModelSwitchBusiness(t, m, cmd)
			st := stripANSIstr(m.statusMsg)
			if !strings.Contains(st, tc.wantNoteSubstr) {
				t.Fatalf("status = %q, want it to contain %q", st, tc.wantNoteSubstr)
			}
			// The one-shot note was consumed.
			if m.pendingModelSwitchNote != "" {
				t.Errorf("the note should be consumed (one-shot) after the rebind, got %q", m.pendingModelSwitchNote)
			}
		})
	}
}

// TestModelsEscClosesNoChange asserts esc closes the picker without changing the
// active selection.
func TestModelsEscClosesNoChange(t *testing.T) {
	seed := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), seed)
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	// Move the cursor, then esc — active must be unchanged.
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	mm, _, handled := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !handled {
		t.Fatal("esc should be handled by the open picker")
	}
	m = mm.(Model)
	if m.modal != nil {
		t.Error("esc should close the picker")
	}
	if m.createModelSelection != seed {
		t.Errorf("esc must not change createModelSelection: got %+v, want %+v", m.createModelSelection, seed)
	}
}

// TestModelsErrorRenders asserts a ListModels error is recorded + surfaced.
func TestModelsErrorRenders(t *testing.T) {
	fm := &fakeModels{err: errors.New("boom")}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if modelsSurface(t, m).err == nil {
		t.Fatal("a ListModels error should be recorded")
	}
	rendered := m.View().Content
	if !strings.Contains(rendered, "boom") {
		t.Errorf("error not surfaced in the picker:\n%s", rendered)
	}
	// UX finding: a bare error is a dead end — a next-action hint must render
	// alongside it (naming a likely cause + where to look).
	if !strings.Contains(rendered, "mecated is running") {
		t.Errorf("error rendering missing the next-action hint:\n%s", rendered)
	}
}

// TestModelsErrorClearsStaleProviderStatuses is the issue #262 review finding
// 5 pin: a successful ListModels carrying a non-ok status (e.g. toolhive
// unreachable), followed by a LATER failed ListModels (an unrelated transient
// RPC error), must NOT keep rendering the stale status's remediation line
// beneath the new, unrelated error — a failed ListModels carries no
// statuses. Both the state-clear (updateModelsMsg) and the render-time
// defense (modelsState.Render gates on st.err == nil) are exercised by
// asserting the FINAL rendered view.
func TestModelsErrorClearsStaleProviderStatuses(t *testing.T) {
	statuses := []client.ProviderStatus{
		{ProviderID: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}
	m := newModelsModel(t, &fakeModels{models: sampleModels().models, statuses: statuses}, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if len(m.modelCatalog.statuses) != 1 {
		t.Fatalf("precondition: expected the status to be threaded, got %+v", m.modelCatalog.statuses)
	}

	// A later, unrelated ListModels failure.
	mm2, _, handled := m.updateModelsMsg(client.ModelsMsg{RequestToken: m.modelCatalogRequestToken, Err: errors.New("transient rpc error")})
	if !handled {
		t.Fatal("ModelsMsg should be handled")
	}
	m = mm2.(Model)
	if m.modelCatalog.statuses != nil {
		t.Fatalf("stale statuses survived a failed ListModels: %+v", m.modelCatalog.statuses)
	}

	rendered := stripANSIstr(m.View().Content)
	if !strings.Contains(rendered, "✗ list models") {
		t.Errorf("rendered picker missing the error line:\n%s", rendered)
	}
	if strings.Contains(rendered, "thv llm proxy start") {
		t.Errorf("rendered picker still shows the STALE toolhive remediation line beneath an unrelated error:\n%s", rendered)
	}
}

// TestModelsKeyRemovedFallback is the §4 reconcile: when the loaded list does NOT
// contain the persisted active selection, active is cleared to the server default
// and a loud notice fires — but the store is NOT rewritten.
func TestModelsKeyRemovedFallback(t *testing.T) {
	// Persisted selection names openrouter, but the list only has openai now (key
	// removed). The reconcile must clear active to zero with a notice.
	fm := &fakeModels{models: []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
	}}
	store := &fakeStore{}
	gone := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	m := newModelsModel(t, fm, store, modelsCaps(), gone)
	if m.createModelSelection != gone {
		t.Fatalf("precondition: createModelSelection seeded = %+v, want %+v", m.createModelSelection, gone)
	}

	// Drive ListModels through updateModelsMsg directly (idle phase ⇒ no create).
	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{RequestToken: m.modelCatalogRequestToken, Models: fm.models})
	m = mm.(Model)
	if !handled {
		t.Fatal("ModelsMsg should be handled")
	}
	if cmd != nil {
		t.Error("at idle, ModelsMsg must NOT fire a create command")
	}
	if !m.createModelSelection.IsZero() {
		t.Errorf("active should be cleared to the server default, got %+v", m.createModelSelection)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "no longer available") {
		t.Errorf("a loud key-removed notice should fire, got %q", stripANSIstr(m.statusMsg))
	}
	if store.saves != 0 {
		t.Errorf("the state file must NOT be rewritten on fallback, got %d saves", store.saves)
	}
}

// TestModelsReconcileKeepsAvailable asserts an active selection still present in
// the list is preserved (no clear, no notice).
func TestModelsReconcileKeepsAvailable(t *testing.T) {
	fm := sampleModels()
	keep := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), keep)
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models})
	m = mm.(Model)
	if m.createModelSelection != keep {
		t.Errorf("an available selection must be preserved, got %+v want %+v", m.createModelSelection, keep)
	}
}

// TestModelsSaveFailureFailSoft asserts a Save error surfaces as a notice but the
// active selection still holds for the run. The seamless switch persists the pick via
// saveSelectionCmd (batched with the carryover create); a failing Save surfaces as a
// muted notice without aborting the switch.
func TestModelsSaveFailureFailSoft(t *testing.T) {
	store := &fakeStore{err: errors.New("disk full")}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	// Pick gpt-5 (row 0 — the live model) via the seamless switch (enter switches
	// immediately, firing the carryover handoff + the persist).
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	want := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if m.createModelSelection != want {
		t.Fatalf("active should hold for the run despite the save failure, got %+v", m.createModelSelection)
	}
	m = feedModelSwitchBusiness(t, m, cmd) // runs carryover + failing Save, skips spinner/focus lifecycle
	if !strings.Contains(stripANSIstr(m.statusMsg), "could not persist") {
		t.Errorf("a persist failure should surface as a notice, got %q", stripANSIstr(m.statusMsg))
	}
}

// TestModelsConnectErrorDegradesToCreate guards the connecting-phase ListModels
// error branch (models.go ~:157): a ListModels error at connect must NOT strand the
// UI at "connecting…" — it proceeds to CreateSession with the (unreconciled) seeded
// selection so connect completes. (TestModelsErrorRenders covers the idle path; this
// covers the connect path.)
func TestModelsConnectErrorDegradesToCreate(t *testing.T) {
	seed := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: modelsCaps()}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{err: errors.New("list boom")},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Ctx:          context.Background(),
		InitialModel: seed,
	})
	if m.phase != phaseConnecting {
		t.Fatalf("precondition: phase = %v, want phaseConnecting", m.phase)
	}

	// Drive the connect-time ListModels error through the reducer.
	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{RequestToken: m.modelCatalogRequestToken, Err: errors.New("list boom")})
	m = mm.(Model)
	if !handled {
		t.Fatal("connect-time ModelsMsg error should be handled")
	}
	if cmd == nil {
		t.Fatal("a connect-time ListModels error must still fire CreateSession (not strand at connecting)")
	}
	// Running that create cmd must reach SessionReadyMsg carrying the SEEDED
	// (unreconciled) selection — connect completes.
	m = feedCmd(t, applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}), cmd)
	if m.phase != phaseIdle {
		t.Errorf("phase after the degraded create = %v, want phaseIdle (connected)", m.phase)
	}
	if conv.createdSel != seed {
		t.Errorf("CreateSession carried %+v, want the seeded (unreconciled) %+v", conv.createdSel, seed)
	}
}

// TestInitNoListerFiresCreateDirectly guards the no-lister Init branch
// (model.go ~:383): with deps.Models == nil (old server / persistence off) Init
// must fire CreateSession directly with an empty selection — NOT wait on a
// ListModels that never comes.
func TestInitNoListerFiresCreateDirectly(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: client.Capabilities{}}
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		// Models intentionally nil.
		Theme: theme.New("aztec", theme.AztecPalette()),
		Ctx:   context.Background(),
	})

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should return a command")
	}
	// Init returns tea.Batch(sp.Tick, <connect>). Run the leaves and collect their
	// msgs WITHOUT feeding them recursively — the spinner tick re-arms itself, so
	// feedCmd would recurse forever (the bug this test guards must not also trip it).
	// The no-lister branch's connect leaf is createSessionCmd ⇒ a SessionReadyMsg;
	// there must be NO ListModelsMsg (it did not take the lister path).
	msgs := initLeafMsgs(cmd)
	var ready *client.SessionReadyMsg
	for _, msg := range msgs {
		switch v := msg.(type) {
		case client.SessionReadyMsg:
			ready = &v
		case client.ModelsMsg:
			t.Fatal("no-lister Init must NOT fire ListModels (it has no lister wired)")
		}
	}
	if ready == nil {
		t.Fatal("no-lister Init must fire CreateSession directly (a SessionReadyMsg), not stall awaiting ListModels")
		return
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, *ready)
	if m.phase != phaseIdle {
		t.Errorf("phase after no-lister Init = %v, want phaseIdle (connected directly)", m.phase)
	}
	if !conv.createdSel.IsZero() {
		t.Errorf("no-lister create carried %+v, want the empty selection", conv.createdSel)
	}
}

// TestModelsReconcileKeepsModelMissingFromSnapshot is the issue #41 fix at the
// reducer level: the connect-time reconcile is PROVIDER-level, so a persisted
// selection whose provider IS in the inventory but whose exact model is NOT (the
// boot snapshot may be the embedded catalog floor before the live refresh lands)
// must be KEPT and carried into the create verbatim — no clear, no notice.
func TestModelsReconcileKeepsModelMissingFromSnapshot(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	// The openrouter PROVIDER is present, but the exact saved model is absent
	// (an embedded-floor snapshot that predates the model).
	inventory := []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
		{ID: "openai/gpt-5", ProviderID: "openrouter", DisplayName: "GPT-5", ContextLimit: 400000},
	}
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: modelsCaps()}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: inventory},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Ctx:          context.Background(),
		InitialModel: persisted,
	})
	if m.phase != phaseConnecting {
		t.Fatalf("precondition: phase = %v, want phaseConnecting", m.phase)
	}

	// Drive the connect-time ListModels result through the reducer.
	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{RequestToken: m.modelCatalogRequestToken, Models: inventory})
	m = mm.(Model)
	if !handled {
		t.Fatal("connect-time ModelsMsg should be handled")
	}
	if cmd == nil {
		t.Fatal("connect-time ModelsMsg must fire CreateSession")
	}
	if m.createModelSelection != persisted {
		t.Errorf("createModelSelection = %+v, want the KEPT persisted %+v (provider present ⇒ no clear)", m.createModelSelection, persisted)
	}
	if m.modelCatalog.active != persisted {
		t.Errorf("models.active = %+v, want the KEPT persisted %+v", m.modelCatalog.active, persisted)
	}
	if st := stripANSIstr(m.statusMsg); strings.Contains(st, "no longer available") {
		t.Errorf("no key-removed notice must fire when only the MODEL is absent, got %q", st)
	}
	// The create carries the persisted selection verbatim and connect completes.
	m = feedCmd(t, applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}), cmd)
	if m.phase != phaseIdle {
		t.Errorf("phase after the create = %v, want phaseIdle (connected)", m.phase)
	}
	if conv.createdSel != persisted {
		t.Errorf("CreateSession carried %+v, want the persisted %+v", conv.createdSel, persisted)
	}
}

// TestConnectCreateRejectedFallsBackToDefault is the issue #41 fallback leg: the
// kept selection is the SERVER's to validate, so when the connect-time create
// REJECTS it (gRPC InvalidArgument — the code the server maps a bad selector to),
// createSessionCmd retries ONCE with the zero selection (server default). Connect
// must complete (idle), the now-known-bad selection clears for this run (BOTH
// m.createModelSelection and the picker's m.modelCatalog.active — a stale models.active would
// re-seed the picker with the known-bad selection), a LOUD warning names the
// rejected model, and the state file is NOT rewritten. Exactly TWO creates fire:
// the rejected one + the single zero retry — never more.
func TestConnectCreateRejectedFallsBackToDefault(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	// The provider IS in the inventory, so the reconcile keeps the selection — the
	// rejection comes from the server at create time.
	inventory := []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
	}
	recv := &fakeRecver{gate: make(chan struct{})}
	store := &fakeStore{}
	conv := &fakeConv{
		recv:           recv,
		send:           &fakeSender{},
		caps:           modelsCaps(),
		rejectSelector: status.Error(codes.InvalidArgument, "unknown or unavailable provider"),
	}
	m := newTestModelFromDeps(Deps{
		Session:        conv,
		Conv:           conv,
		Models:         &fakeModels{models: inventory},
		SelectionStore: store,
		Theme:          theme.New("aztec", theme.AztecPalette()),
		Ctx:            context.Background(),
		InitialModel:   persisted,
	})

	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{Models: inventory})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("connect-time ModelsMsg must be handled and fire CreateSession")
	}
	// The create cmd runs both legs (reject → zero-selection retry) and its
	// connectFallbackMsg lands in the reducer.
	m = feedCmd(t, applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}), cmd)
	if m.phase != phaseIdle {
		t.Errorf("phase after the fallback create = %v, want phaseIdle (connect completed)", m.phase)
	}
	if !conv.createdSel.IsZero() {
		t.Errorf("the LAST create carried %+v, want the zero selection (the fallback create)", conv.createdSel)
	}
	if conv.createCount != 2 {
		t.Errorf("CreateSession fired %d times, want exactly 2 (the rejected create + ONE zero retry)", conv.createCount)
	}
	if !m.createModelSelection.IsZero() {
		t.Errorf("createModelSelection = %+v, want zero (the rejected selection clears for this run)", m.createModelSelection)
	}
	if !m.modelCatalog.active.IsZero() {
		t.Errorf("models.active = %+v, want zero (a stale picker selection would re-seed the known-bad pick)", m.modelCatalog.active)
	}
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "openai/gpt-5.5") {
		t.Errorf("the fallback warning must name the rejected model, got %q", st)
	}
	if !strings.Contains(st, "server default") {
		t.Errorf("the fallback warning must say the session fell back to the server default, got %q", st)
	}
	if store.saves != 0 {
		t.Errorf("the state file must NOT be rewritten on a server rejection, got %d saves", store.saves)
	}
}

// TestConnectCreateBothFailStaysFatal guards the issue #41 fallback leg's failure
// path: the carried selection is REJECTED (InvalidArgument, so the zero-selection
// retry fires) and the retry ALSO fails — today's fatal path is unchanged:
// ConnectErrMsg → phaseFatal carrying the ORIGINAL (rejection) error, not the
// retry's.
func TestConnectCreateBothFailStaysFatal(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	inventory := []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
	}
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{
		recv: recv,
		send: &fakeSender{},
		caps: modelsCaps(),
		// The selector create is REJECTED; the zero-selection retry then fails with a
		// DISTINCT error, so the original-error assertion below is non-vacuous.
		rejectSelector: status.Error(codes.InvalidArgument, "unknown or unavailable provider"),
		createErr:      errors.New("server unavailable"),
	}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: inventory},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Ctx:          context.Background(),
		InitialModel: persisted,
	})

	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{Models: inventory})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("connect-time ModelsMsg must be handled and fire CreateSession")
	}
	m = feedCmd(t, applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}), cmd)
	if m.phase != phaseFatal {
		t.Errorf("phase after both creates failed = %v, want phaseFatal (unchanged fatal path)", m.phase)
	}
	if conv.createCount != 2 {
		t.Errorf("CreateSession fired %d times, want 2 (the rejected create + the failed zero retry)", conv.createCount)
	}
	if !strings.Contains(m.fatalErr, "unknown or unavailable provider") {
		t.Errorf("fatalErr = %q, want it to carry the ORIGINAL (rejection) error", m.fatalErr)
	}
	if strings.Contains(m.fatalErr, "server unavailable") {
		t.Errorf("fatalErr = %q, must NOT be the retry's error (the original must surface)", m.fatalErr)
	}
}

func TestConnectCreateRetryAuthFailureIsAuthoritative(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "missing"}
	conv := &fakeConv{
		rejectSelector: status.Error(codes.InvalidArgument, "unknown model"),
		createErr:      &client.AuthError{Reason: client.AuthSessionExpired},
	}
	m := New(Deps{Session: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), InitialModel: persisted, BearerBacked: true})
	msg, ok := m.createSessionCmd()().(client.ConnectErrMsg)
	if !ok {
		t.Fatalf("message = %T, want ConnectErrMsg", m.createSessionCmd()())
	}
	if msg.AuthReason != client.AuthSessionExpired || msg.Err != conv.createErr {
		t.Fatalf("retry auth result = %#v, want retry error and expired reason", msg)
	}
}

// TestConnectCreateTransientFailureStaysFatal is the missing cell {saved selection
// valid} × {transient failure}: a connect-time create that fails for a
// NON-rejection reason (deadline, unavailable — anything but gRPC InvalidArgument)
// must NOT trigger the zero-selection fallback, even though that retry WOULD
// succeed — landing the user on the server default with a dishonest "rejected"
// warning over a server blip. It keeps today's fatal path with the original error,
// the selection intact, and exactly ONE create fired.
func TestConnectCreateTransientFailureStaysFatal(t *testing.T) {
	persisted := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	inventory := []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
	}
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{
		recv: recv,
		send: &fakeSender{},
		caps: modelsCaps(),
		// A NON-InvalidArgument failure on the selector create; the fake's
		// zero-selection create would SUCCEED, so an any-error fallback would
		// (wrongly) complete connect on the server default.
		rejectSelector: errors.New("context deadline exceeded"),
	}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: inventory},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Ctx:          context.Background(),
		InitialModel: persisted,
	})

	mm, cmd, handled := m.updateModelsMsg(client.ModelsMsg{Models: inventory})
	m = mm.(Model)
	if !handled || cmd == nil {
		t.Fatal("connect-time ModelsMsg must be handled and fire CreateSession")
	}
	m = feedCmd(t, applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}), cmd)
	if m.phase != phaseFatal {
		t.Errorf("phase after a transient create failure = %v, want phaseFatal (no fallback on a non-rejection)", m.phase)
	}
	if !strings.Contains(m.fatalErr, "context deadline exceeded") {
		t.Errorf("fatalErr = %q, want the original transient error", m.fatalErr)
	}
	if conv.createCount != 1 {
		t.Errorf("CreateSession fired %d times, want exactly 1 (no zero-selection retry on a transient failure)", conv.createCount)
	}
	if m.createModelSelection != persisted {
		t.Errorf("createModelSelection = %+v, want the UNTOUCHED persisted %+v (a transient failure must not clear the pick)", m.createModelSelection, persisted)
	}
	if st := stripANSIstr(m.statusMsg); strings.Contains(st, "rejected") {
		t.Errorf("no dishonest 'rejected' warning may fire on a transient failure, got %q", st)
	}
}

// TestModelsPickerNoActiveMarkerWhenSelectionAbsent: a KEPT-but-absent active
// selection (provider present, exact model missing from the snapshot — the issue
// #41 keep) renders the picker with NO ● marker — no phantom row, no misplaced
// marker; rows otherwise render normally.
func TestModelsPickerNoActiveMarkerWhenSelectionAbsent(t *testing.T) {
	fm := &fakeModels{models: []client.ModelInfo{
		{ID: "openai/gpt-5.1", ProviderID: "openrouter", DisplayName: "GPT-5.1", ContextLimit: 400000},
		{ID: "openai/gpt-5", ProviderID: "openrouter", DisplayName: "GPT-5", ContextLimit: 400000},
	}}
	kept := client.ModelSelection{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), kept)
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	// The reconcile (provider present) keeps the selection.
	if m.modelCatalog.active != kept {
		t.Fatalf("models.active = %+v, want the kept %+v", m.modelCatalog.active, kept)
	}
	panel := m.View().Content
	rows := strings.Split(stripANSIstr(panel), "\n")
	for _, row := range rows {
		if strings.Contains(row, "●") && !strings.Contains(row, "● current") {
			t.Errorf("no row should carry the ● marker for an absent selection, got %q", row)
		}
	}
	if !strings.Contains(stripANSIstr(panel), "GPT-5.1") {
		t.Errorf("rows should still render normally:\n%s", stripANSIstr(panel))
	}
}

// --- provider status (issue #262) -------------------------------------------

// TestModelsEmptyCopy_Default proves the three-way branch: disabled note when
// caps.ModelSelection is false, the gateway-specific note when a status is
// "empty", else the generic "enabled but empty" note.
func TestModelsEmptyCopy_Default(t *testing.T) {
	if got := modelsEmptyCopy(client.Capabilities{ModelSelection: false}, nil); got != modelsDisabledNote {
		t.Errorf("disabled copy = %q, want the disabled note", got)
	}
	if got := modelsEmptyCopy(client.Capabilities{ModelSelection: true}, nil); got != "No selectable models advertised." {
		t.Errorf("generic empty copy = %q", got)
	}
}

// TestModelsEmptyCopy_GatewayEmpty proves an "empty" status REPLACES the
// generic note (issue #262 R6.2) — even when caps.ModelSelection is true.
func TestModelsEmptyCopy_GatewayEmpty(t *testing.T) {
	statuses := []client.ProviderStatus{{ProviderID: "toolhive", State: "empty", Hint: "ask your admin"}}
	got := modelsEmptyCopy(client.Capabilities{ModelSelection: true}, statuses)
	if got != modelsGatewayEmptyNote {
		t.Errorf("gateway-empty copy = %q, want %q", got, modelsGatewayEmptyNote)
	}
}

// TestModelsEmptyCopy_PromotesAnyNonOkStatus is the issue #262 review finding
// 6 fix: a SOLE unreachable/unauthorized provider (not just "empty") must
// promote its own remediation line to the empty-state cause — never the
// generic "No selectable models advertised." (which reads as "nothing is
// configured") nor the disabled note (which reads as "you haven't set a
// key"), both of which contradict a status line naming the real cause.
func TestModelsEmptyCopy_PromotesAnyNonOkStatus(t *testing.T) {
	unreachable := []client.ProviderStatus{
		{ProviderID: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}
	got := modelsEmptyCopy(client.Capabilities{ModelSelection: true}, unreachable)
	want := "toolhive: gateway not reachable — start it with `thv llm proxy start`"
	if got != want {
		t.Errorf("unreachable empty copy = %q, want %q", got, want)
	}
	if got == modelsDisabledNote || got == "No selectable models advertised." {
		t.Errorf("unreachable empty copy fell back to a contradictory generic note: %q", got)
	}

	unauthorized := []client.ProviderStatus{
		{ProviderID: "toolhive", State: "unauthorized", Hint: "re-auth with `thv llm setup`"},
	}
	got = modelsEmptyCopy(client.Capabilities{ModelSelection: true}, unauthorized)
	want = "toolhive: gateway rejected the credential — re-auth with `thv llm setup`"
	if got != want {
		t.Errorf("unauthorized empty copy = %q, want %q", got, want)
	}

	// The SAME promotion applies even when caps.ModelSelection is false (the
	// "reality is slightly worse than the finding" case the review noted): the
	// promoted cause still wins over modelsDisabledNote.
	got = modelsEmptyCopy(client.Capabilities{ModelSelection: false}, unreachable)
	if got == modelsDisabledNote {
		t.Errorf("unreachable + ModelSelection=false empty copy wrongly fell back to the disabled note: %q", got)
	}
}

// TestModelsPickerSoleUnreachableEmptyRenders is the F6 full-render pin: a
// sole toolhive provider that is unreachable, with an EMPTY overall
// inventory, must render EXACTLY ONE line naming the cause — never both the
// generic empty note AND a separate remediation line (the double-statement
// the review flagged is avoided by renderProviderStatusLines suppressing the
// promoted status), and never the disabled note.
func TestModelsPickerSoleUnreachableEmptyRenders(t *testing.T) {
	fm := &fakeModels{statuses: []client.ProviderStatus{
		{ProviderID: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	rendered := stripANSIstr(m.View().Content)

	if got, want := strings.Count(rendered, "toolhive:"), 1; got != want {
		t.Fatalf("rendered picker names the toolhive cause %d times, want exactly %d:\n%s", got, want, rendered)
	}
	if strings.Contains(rendered, "No selectable models advertised.") {
		t.Errorf("rendered picker still shows the generic empty note alongside the named cause:\n%s", rendered)
	}
	if strings.Contains(rendered, "Model selection is not available") {
		t.Errorf("rendered picker shows the disabled note alongside the named cause:\n%s", rendered)
	}
	if !strings.Contains(rendered, "thv llm proxy start") {
		t.Errorf("rendered picker missing the unreachable remediation hint:\n%s", rendered)
	}
}

// TestRenderProviderStatusLines_EmptyIsNoOp proves an empty statuses slice (or
// one containing only ok rows, or an "empty" row when the OVERALL inventory
// is ALSO empty) renders NOTHING — the byte-identical no-op invariant every
// existing golden depends on, and the no-double-state rule with
// modelsEmptyCopy.
func TestRenderProviderStatusLines_EmptyIsNoOp(t *testing.T) {
	if got := renderProviderStatusLines(nil, true); got != nil {
		t.Errorf("nil statuses rendered %v, want nil", got)
	}
	okOnly := []client.ProviderStatus{{ProviderID: "toolhive", State: "ok"}}
	if got := renderProviderStatusLines(okOnly, true); got != nil {
		t.Errorf("ok-only statuses rendered %v, want nil", got)
	}
	emptyOnly := []client.ProviderStatus{{ProviderID: "toolhive", State: "empty", Hint: "x"}}
	if got := renderProviderStatusLines(emptyOnly, true); got != nil {
		t.Errorf("empty-state status rendered %v with an EMPTY overall inventory, want nil (handled by modelsEmptyCopy instead)", got)
	}
}

// TestRenderProviderStatusLines_SuppressesAnyPromotedStatus is the issue #262
// review finding 6 generalization: with an EMPTY overall inventory, an
// unreachable/unauthorized sole status (not just "empty") is ALSO suppressed
// here — modelsEmptyCopy already promoted it to the top-level cause line, so
// duplicating it here would double-state the same remediation.
func TestRenderProviderStatusLines_SuppressesAnyPromotedStatus(t *testing.T) {
	unreachable := []client.ProviderStatus{{ProviderID: "toolhive", State: "unreachable", Hint: "x"}}
	if got := renderProviderStatusLines(unreachable, true); got != nil {
		t.Errorf("unreachable status rendered %v with an EMPTY overall inventory, want nil (promoted to modelsEmptyCopy instead)", got)
	}
	unauthorized := []client.ProviderStatus{{ProviderID: "toolhive", State: "unauthorized", Hint: "x"}}
	if got := renderProviderStatusLines(unauthorized, true); got != nil {
		t.Errorf("unauthorized status rendered %v with an EMPTY overall inventory, want nil (promoted to modelsEmptyCopy instead)", got)
	}
}

// TestRenderProviderStatusLines_MixedDeploymentSurfacesEmpty is the issue
// #262 mixed-deployment gap fix: when the OVERALL inventory is NON-empty
// (other providers have models), a reachable-but-empty toolhive status must
// still surface here — modelsEmptyCopy's replacement note only fires for a
// wholly-empty picker, so without this the state would be invisible.
func TestRenderProviderStatusLines_MixedDeploymentSurfacesEmpty(t *testing.T) {
	statuses := []client.ProviderStatus{{ProviderID: "toolhive", State: "empty", Hint: "ask your platform admin"}}
	lines := renderProviderStatusLines(statuses, false) // inventory NON-empty
	if len(lines) != 1 || lines[0] != "toolhive: credential lists no models — ask your platform admin" {
		t.Fatalf("mixed-deployment empty line = %v", lines)
	}
}

// TestRenderProviderStatusLines_UnreachableAndUnauthorized pins the exact
// remediation copy (issue #262 R6.2), regardless of overall inventory state.
func TestRenderProviderStatusLines_UnreachableAndUnauthorized(t *testing.T) {
	lines := renderProviderStatusLines([]client.ProviderStatus{
		{ProviderID: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}, false)
	if len(lines) != 1 || lines[0] != "toolhive: gateway not reachable — start it with `thv llm proxy start`" {
		t.Fatalf("unreachable line = %v", lines)
	}
	lines = renderProviderStatusLines([]client.ProviderStatus{
		{ProviderID: "toolhive", State: "unauthorized", Hint: "re-auth with `thv llm setup`"},
	}, false)
	if len(lines) != 1 || lines[0] != "toolhive: gateway rejected the credential — re-auth with `thv llm setup`" {
		t.Fatalf("unauthorized line = %v", lines)
	}
}

// TestModelsPickerStatuses_ThreadedFromMsg proves updateModelsMsg captures
// ModelsMsg.Statuses in the root catalog, which is also the render path's source.
func TestModelsPickerStatuses_ThreadedFromMsg(t *testing.T) {
	statuses := []client.ProviderStatus{{ProviderID: "toolhive", State: "unreachable", Hint: "x"}}
	m := newModelsModel(t, &fakeModels{models: sampleModels().models, statuses: statuses}, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if len(m.modelCatalog.statuses) != 1 || m.modelCatalog.statuses[0].ProviderID != "toolhive" {
		t.Fatalf("models.statuses = %+v, want the threaded status", m.modelCatalog.statuses)
	}
	rendered := stripANSI([]byte(m.View().Content))
	if !strings.Contains(string(rendered), "toolhive: gateway not reachable") {
		t.Fatalf("rendered picker missing the status line:\n%s", rendered)
	}
}

// TestModelsPickerCustomProviderStatusRendersAlongsideFloor proves generic picker
// status rendering keeps an operator-defined provider's configured default row
// selectable while showing each safe listing state.
func TestModelsPickerCustomProviderStatusRendersAlongsideFloor(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
	}{
		{name: "unreachable", state: "unreachable", want: "gateway: model service not reachable — check provider configuration"},
		{name: "unauthorized", state: "unauthorized", want: "gateway: model service rejected access — check provider access configuration"},
		{name: "empty", state: "empty", want: "gateway: no selectable models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantSelection := client.ModelSelection{ProviderID: "gateway", ModelID: "gateway-default"}
			fm := &fakeModels{
				models:   []client.ModelInfo{{ProviderID: wantSelection.ProviderID, ID: wantSelection.ModelID, DisplayName: "Gateway Default"}},
				statuses: []client.ProviderStatus{{ProviderID: wantSelection.ProviderID, State: tc.state}},
			}
			m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
			mm, cmd := m.runModels()
			m = feedCmd(t, mm.(Model), cmd)
			rendered := stripANSIstr(m.View().Content)
			for _, want := range []string{"gateway · Gateway Default", tc.want} {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered picker missing %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range []string{"gateway not reachable", "gateway rejected the credential", "credential lists no models", "https://", "listing response body", "gateway-secret"} {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("rendered custom status leaked or used ToolHive copy %q:\n%s", unwanted, rendered)
				}
			}

			mm, cmd, handled := m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			if !handled || m.createModelSelection != wantSelection || m.phase != phaseConnecting || m.modal != nil {
				t.Fatalf("Enter selection = %+v, phase=%v modal=%T handled=%v; want %+v, connecting, closed, handled",
					m.createModelSelection, m.phase, m.modal, handled, wantSelection)
			}
			m = feedModelSwitchBusiness(t, m, cmd)
			if conv(m).createdSel != wantSelection || conv(m).carryoverCalls() != 1 {
				t.Fatalf("restart request = selection %+v, carryover calls %d; want %+v, 1",
					conv(m).createdSel, conv(m).carryoverCalls(), wantSelection)
			}
		})
	}
}

// TestModelsCatalogUpdatesWhilePickerClosed proves catalog results remain root-owned:
// a late update reconciles the next-create selection and feeds gateway/header state
// without reopening the picker.
func TestModelsCatalogUpdatesWhilePickerClosed(t *testing.T) {
	gone := client.ModelSelection{ProviderID: "removed", ModelID: "old-model"}
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), gone)
	if m.modal != nil {
		t.Fatalf("picker view = %v, want closed", modelsSurface(t, m).view)
	}

	statuses := []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", AvailableNotDefault: true, ModelCount: 2}}
	mm, _, handled := m.updateModelsMsg(client.ModelsMsg{
		RequestToken: m.modelCatalogRequestToken,
		Models:       []client.ModelInfo{{ProviderID: "openai", ID: "gpt-5"}},
		Statuses:     statuses,
	})
	if !handled {
		t.Fatal("ModelsMsg should be handled while picker is closed")
	}
	m = mm.(Model)
	if m.modal != nil {
		t.Fatalf("late catalog update opened picker: %v", modelsSurface(t, m).view)
	}
	if len(m.modelCatalog.models) != 1 || m.modelCatalog.models[0].ID != "gpt-5" {
		t.Fatalf("catalog models = %+v, want the late inventory", m.modelCatalog.models)
	}
	if len(m.modelCatalog.statuses) != 1 || !m.modelCatalog.configProvenanceProviderIDs["toolhive"] {
		t.Fatalf("catalog statuses/intent = %+v/%v, want ToolHive status and intent", m.modelCatalog.statuses, m.modelCatalog.configProvenanceProviderIDs)
	}
	if !m.createModelSelection.IsZero() || !m.modelCatalog.active.IsZero() {
		t.Fatalf("reconciled selections = %+v/%+v, want server default", m.createModelSelection, m.modelCatalog.active)
	}
	if m.gatewayNotice == "" {
		t.Fatal("closed-picker catalog update should arm the gateway notice")
	}
}

// TestHeaderToolhiveSegment proves the persistent "via ToolHive gateway"
// header segment (issue #262 R6.3) appears ONLY when the active session's
// provider is toolhive, and sheds under width pressure like any other
// low-priority segment.
func TestHeaderToolhiveSegment(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), Server: "127.0.0.1:8080"})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	if strings.Contains(stripANSIstr(m.renderHeader()), "via ToolHive gateway") {
		t.Fatal("non-toolhive session must NOT show the gateway segment")
	}

	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}
	m = applyAll(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if !strings.Contains(stripANSIstr(m.renderHeader()), "via ToolHive gateway") {
		t.Fatal("a toolhive session must show the gateway segment at a wide width")
	}

	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive-anthropic", ModelID: "claude-sonnet-4-6"}
	m.modelCatalog.statuses = []client.ProviderStatus{
		{ProviderID: "toolhive", State: "ok", AvailableNotDefault: true},
		{ProviderID: "toolhive-anthropic", State: "ok"},
	}
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "via ToolHive gateway") || strings.Contains(header, "gateway available") {
		t.Fatalf("native ToolHive header must show one active-family segment, got:\n%s", header)
	}

	// At a narrow width the segment sheds along with the other low-priority
	// segments; the header must not panic/overflow.
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 30})
	_ = m.renderHeader()
}

// TestHeaderGatewayAvailableSegment (N1): a muted "<provider-id> gateway
// available" segment renders when an AvailableNotDefault status exists and the
// active provider is NOT the gateway. It is the mutually-exclusive sibling of
// the "via ToolHive gateway" segment (active case): when the gateway IS the
// active default, availableNotDefaultStatus returns false so ONLY the "via"
// segment renders — never both.
func TestHeaderGatewayAvailableSegment(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), Server: "127.0.0.1:8080"})
	m = applyAll(m, tea.WindowSizeMsg{Width: 160, Height: 30})

	// Active provider is openai (key-driven); toolhive is available-but-not-default.
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	m.modelCatalog.statuses = gatewayStatuses()
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "toolhive gateway available") {
		t.Errorf("header should show the 'gateway available' segment when an AvailableNotDefault status exists and the active provider is not the gateway, got:\n%s", header)
	}
	// The active-case "via ToolHive gateway" segment must NOT also render.
	if strings.Contains(header, "via ToolHive gateway") {
		t.Errorf("the 'via ToolHive gateway' segment must NOT render alongside the 'available' segment, got:\n%s", header)
	}

	// Now make the gateway the ACTIVE default: AvailableNotDefault flips false, so
	// the 'available' segment disappears and the 'via' segment renders instead.
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", ModelCount: 5, AvailableNotDefault: false}}
	header = stripANSIstr(m.renderHeader())
	if strings.Contains(header, "gateway available") {
		t.Errorf("the 'available' segment must NOT render when the gateway IS the active default, got:\n%s", header)
	}
	if !strings.Contains(header, "via ToolHive gateway") {
		t.Errorf("the 'via ToolHive gateway' segment should render when the gateway is active, got:\n%s", header)
	}

	// No statuses (byte-identical pre-feature path): neither segment renders.
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	m.modelCatalog.statuses = nil
	header = stripANSIstr(m.renderHeader())
	if strings.Contains(header, "gateway available") || strings.Contains(header, "via ToolHive gateway") {
		t.Errorf("neither gateway segment should render with no statuses, got:\n%s", header)
	}
}

// TestHeaderProviderRouteSuffix proves the routed downstream provider appears as a
// "/ <name>" suffix on the header model segment (issue #480) ONLY once a route has
// been reported this session, and is absent before any routed turn (no stale or
// fabricated suffix).
func TestHeaderProviderRouteSuffix(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), Server: "127.0.0.1:8080"})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "moonshotai/kimi-k3"}
	m.phase = phaseIdle // a bound session, so the model segment renders
	m = applyAll(m, tea.WindowSizeMsg{Width: 160, Height: 30})

	// Before any provider.route event: the bare model segment, no "/" suffix.
	header := stripANSIstr(m.renderHeader())
	if strings.Contains(header, "kimi-k3/") {
		t.Fatalf("no route yet — header must NOT show a downstream suffix, got:\n%s", header)
	}

	// A provider.route event arrives (the openrouter entry routed to Google).
	m = applyAll(m, client.ProviderRouteMsg{Text: "Google"})
	header = stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "kimi-k3/Google") {
		t.Errorf("header should show the model + routed downstream as 'kimi-k3/Google', got:\n%s", header)
	}
	if statusText := stripANSIstr(m.statusMsg); !strings.Contains(statusText, "via Google") {
		t.Errorf("route arrival should show transient footer status, got %q", statusText)
	}

	// A subsequent route updates the suffix (e.g. a fallback kicked in).
	m = applyAll(m, client.ProviderRouteMsg{Text: "Amazon Bedrock"})
	header = stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "kimi-k3/Amazon Bedrock") {
		t.Errorf("header should track the latest routed downstream, got:\n%s", header)
	}

	// The next turn clears the per-turn route before any metadata arrives. This is
	// the cache-hit/metadata-miss path: absence must render absence, never stale data.
	m = applyAll(m, client.TurnStartMsg{Turn: 2})
	header = stripANSIstr(m.renderHeader())
	if strings.Contains(header, "kimi-k3/") {
		t.Errorf("a new turn with no route must clear the stale suffix, got:\n%s", header)
	}

	// Session reset is the other stale-state boundary.
	m.providerRoute = "Google"
	m = m.resetSession()
	if m.providerRoute != "" {
		t.Errorf("resetSession must clear providerRoute, got %q", m.providerRoute)
	}
}

// --- goldens ---------------------------------------------------------------

// TestModelsPickerGolden locks the populated, grouped picker with an active marker
// on the persisted row and the glyph matrix.
func TestModelsPickerGolden(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(),
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	if modelsSurface(t, m).view != modelsPanel {
		t.Fatalf("view = %v, want modelsPanel", modelsSurface(t, m).view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models.golden", got)
}

// TestModelsPickerDisabledGolden locks the "model selection not available" empty
// state (caps.ModelSelection false, empty list).
func TestModelsPickerDisabledGolden(t *testing.T) {
	m := newModelsModel(t, &fakeModels{}, &fakeStore{}, client.Capabilities{ModelSelection: false}, client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_disabled.golden", got)
}

// TestOpenAICodexCommandRootSurfaces pins AC8.4 at the TUI boundary: a
// rejected manual token is promoted as the empty inventory's cause with the
// auth.yaml/restart remedy supplied by composition. The golden makes this an
// operator-visible contract rather than a server-only status assertion.
func TestOpenAICodexCommandRootSurfaces(t *testing.T) {
	fm := &fakeModels{statuses: []client.ProviderStatus{{
		ProviderID: "openai-codex",
		State:      "unauthorized",
		Hint:       "replace the manual token in auth.yaml and restart mecatl",
	}}}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	rendered := stripANSI([]byte(m.View().Content))
	for _, want := range []string{"openai-codex", "manual token rejected", "auth.yaml", "restart"} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Errorf("Codex unauthorized surface missing %q:\n%s", want, rendered)
		}
	}
	compareGolden(t, "models_codex_unauthorized.golden", rendered)
}

// TestOpenAICodexHealthyStatusDoesNotImplyOrgGolden proves the broader
// provider_status wire is not confused with ToolHive's config-intent tier: an
// entitled Codex model is selectable, but it never gains the "org" label.
func TestOpenAICodexHealthyStatusDoesNotImplyOrgGolden(t *testing.T) {
	fm := &fakeModels{
		models:   []client.ModelInfo{{ID: "gpt-5", ProviderID: "openai-codex", DisplayName: "GPT-5"}},
		statuses: []client.ProviderStatus{{ProviderID: "openai-codex", State: "ok", ModelCount: 1}},
	}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	rendered := stripANSI([]byte(m.View().Content))
	if !bytes.Contains(rendered, []byte("openai-codex · GPT-5")) {
		t.Fatalf("healthy Codex model missing from picker:\n%s", rendered)
	}
	if bytes.Contains(rendered, []byte("org")) {
		t.Fatalf("manually configured Codex status must not imply org intent:\n%s", rendered)
	}
	compareGolden(t, "models_codex_healthy.golden", rendered)
}

// TestModelsPickerEmptyGolden locks the "enabled but empty" state.
func TestModelsPickerEmptyGolden(t *testing.T) {
	m := newModelsModel(t, &fakeModels{}, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_empty.golden", got)
}

// TestModelsPickerFilteredGolden locks the narrowed list (filter "gpt" applied)
// with the flat provider-tagged rows + the active marker.
func TestModelsPickerFilteredGolden(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(),
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeFilter(t, m, "gpt")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_filtered.golden", got)
}

// TestModelsPickerScrolledGolden locks a mid-list physical window: a 30-row list at
// a small height with a wrapped final row and the cursor at the bottom, proving the
// list clips physical lines (not logical rows) and follows the cursor.
func TestModelsPickerScrolledGolden(t *testing.T) {
	models := manyModels(30)
	models.models[len(models.models)-1].DisplayName = strings.Repeat("wrapped model label ", 8)
	m := newModelsModelSized(t, models, &fakeStore{}, modelsCaps(), client.ModelSelection{}, 100, 30)
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	got := stripANSI([]byte(m.View().Content))
	if !bytes.Contains(got, []byte("↑ 26 lines")) {
		t.Fatalf("scrolled picker must count wrapped physical lines in its overflow indicator:\n%s", got)
	}
	compareGolden(t, "models_scrolled.golden", got)
}

// TestModelsPickerNoMatchGolden locks the filter-matched-nothing note (distinct
// from the server-empty/disabled states).
func TestModelsPickerNoMatchGolden(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeFilter(t, m, "zzzzz")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_nomatch.golden", got)
}

// TestModelsPickerToolhiveUnreachableGolden locks the picker rendering a
// non-ok provider_status remediation line under a non-empty list (issue #262
// R6.2).
func TestModelsPickerToolhiveUnreachableGolden(t *testing.T) {
	fm := sampleModels()
	fm.statuses = []client.ProviderStatus{
		{ProviderID: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
	}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_toolhive_unreachable.golden", got)
}

// TestModelsPickerGatewayEmptyGolden locks the gateway-specific empty-state
// copy REPLACING the generic "No selectable models advertised." note (issue
// #262 R6.2) when the model list is empty AND a status reports "empty".
func TestModelsPickerGatewayEmptyGolden(t *testing.T) {
	fm := &fakeModels{statuses: []client.ProviderStatus{
		{ProviderID: "toolhive", State: "empty", Hint: "ask your platform admin or re-run `thv llm setup`"},
	}}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_gateway_empty.golden", got)
}

// TestModelsPickerMixedDeploymentEmptyGolden locks the mixed-deployment fix:
// OTHER providers have models (a non-empty overall inventory), so
// modelsEmptyCopy's replacement note does NOT fire, yet a reachable-but-empty
// toolhive status must STILL surface as a remediation line — otherwise it
// would be invisible in a picker that otherwise looks healthy.
func TestModelsPickerMixedDeploymentEmptyGolden(t *testing.T) {
	fm := sampleModels()
	fm.statuses = []client.ProviderStatus{
		{ProviderID: "toolhive", State: "empty", Hint: "ask your platform admin or re-run `thv llm setup`"},
	}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_mixed_deployment_empty.golden", got)
}

// TestModelsPickerGlobalDefaultGolden locks a picker where a DIFFERENT row carries
// the ★ global-default marker (and the active ● is on another row), so the two-marker
// column + the provenance line render together.
func TestModelsPickerGlobalDefaultGolden(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(),
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	// Set claude as the global default (the ★ row), distinct from the ● active gpt-5.
	m.modelCatalog.globalDefault = client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "models_global_default.golden", got)
}

// TestModelsChooseCrossProviderStillCarries asserts the redesign's core promise: a
// CROSS-PROVIDER pick STILL carries the conversation (no client-side gate). The live
// session is openai/gpt-5; claude is openrouter. Choosing it fires
// CreateSessionWithCarryover (the server strips the prior provider's reasoning cache).
// This replaces the old same-provider gate the confirm overlay enforced.
func TestModelsChooseCrossProviderStillCarries(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	// claude is 3 down — cross-provider (openrouter vs live openai).
	for i := 0; i < 3; i++ {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatalf("enter should switch immediately and close the picker, view = %v", modelsSurface(t, m).view)
	}
	if m.phase != phaseConnecting {
		t.Fatalf("phase = %v, want phaseConnecting (seamless cross-provider switch)", m.phase)
	}
	// Run the armed cmd: the carryover method fires (NOT the plain create) — no gate.
	// Run only create/carryover and persistence; spinner/focus lifecycle commands
	// do not contribute to these assertions.
	m = feedModelSwitchBusiness(t, m, cmd)
	if got := conv(m).carryoverCalls(); got != 1 {
		t.Fatalf("CreateSessionWithCarryover calls = %d, want 1 (cross-provider still carries, no gate)", got)
	}
	if srcs := conv(m).carryoverSources(); len(srcs) != 1 || srcs[0] != "sess-test-0001" {
		t.Fatalf("carryover source ids = %v, want [sess-test-0001] (the old session)", srcs)
	}
	// The normal receipt is the only post-switch status; the picker already showed
	// the one cache warning before selection.
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "switched to Claude — conversation kept") || strings.Contains(st, "prior reasoning cache dropped") {
		t.Fatalf("cross-provider switch should surface only the normal success receipt, got %q", st)
	}
}

// pressModelsKey routes a key through onModelsKey, asserting it was handled.
func pressModelsKey(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	mm, _, handled := m.onOverlayKey(msg)
	if !handled {
		t.Fatalf("key %v should be handled by the open picker", msg)
	}
	return mm.(Model)
}

// initLeafMsgs runs cmd's batch leaves ONCE and collects their result msgs WITHOUT
// re-feeding them (unlike feedCmd). This is the safe way to inspect what Init()
// dispatched: Init batches the spinner tick, whose handler re-arms itself, so a
// recursive feed would loop forever — initLeafMsgs runs each leaf exactly once and
// returns the raw msgs for the test to assert on. Nested batches are flattened.
func initLeafMsgs(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	var walk func(c tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, leaf := range batch {
				walk(leaf)
			}
			return
		}
		out = append(out, msg)
	}
	walk(cmd)
	return out
}

// --- Wave 3: gateway-available TUI surfaces (Proposals 1–3) -------------------
//
// These tests consume the two new client.ProviderStatus fields (ModelCount,
// AvailableNotDefault) in three TUI surfaces: the idle footer notice (M1), the
// picker "org" tag (S1), and the provenance hint (S2). All are client-only.

// gatewayStatuses is a 2-provider status list for the gateway-available tests:
// openai is the (keyed) default; toolhive is reachable, has 5 models, and is NOT
// the default (AvailableNotDefault==true) — the trigger for all three surfaces.
func gatewayStatuses() []client.ProviderStatus {
	return []client.ProviderStatus{
		{ProviderID: "toolhive", State: "ok", ModelCount: 5, AvailableNotDefault: true},
	}
}

// gatewayModels is sampleModels plus a toolhive model row so the "org" tag and
// the provenance hint have a toolhive MODEL row to render against.
func gatewayModels() *fakeModels {
	return &fakeModels{
		models: []client.ModelInfo{
			{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Image: true, Reasoning: true, ContextLimit: 200000},
			{ID: "claude-sonnet-4-6", ProviderID: "toolhive", DisplayName: "Claude Sonnet 4.6", Image: true, Reasoning: true, ContextLimit: 200000},
		},
		statuses: gatewayStatuses(),
	}
}

// TestGatewayNoticeFiresOnce: a ModelsMsg carrying an AvailableNotDefault status
// sets gatewayNotice; a second ModelsMsg does NOT re-fire (the latch holds).
func TestGatewayNoticeFiresOnce(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	// newModelsModel is idle + already has the effective model; deliver a ModelsMsg
	// with the gateway status (mirrors a post-connect live refresh / re-open).
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	if m.gatewayNotice == "" {
		t.Fatalf("gatewayNotice should be set after an AvailableNotDefault ModelsMsg, got empty")
	}
	if !m.gatewayNoticeShown {
		t.Fatalf("gatewayNoticeShown should latch true after firing")
	}
	if !strings.Contains(m.gatewayNotice, "5 models") {
		t.Errorf("gatewayNotice should name the model count, got %q", m.gatewayNotice)
	}
	first := m.gatewayNotice

	// A second ModelsMsg must NOT re-fire (the latch holds; the text is unchanged).
	mm, _, _ = m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	if m.gatewayNotice != first {
		t.Errorf("second ModelsMsg should not re-fire the notice, got %q want %q", m.gatewayNotice, first)
	}
}

// TestGatewayNoticeNotFiredWhenNoAvailableNotDefault: a ModelsMsg with NO
// available_not_default row (gateway is the default, or unreachable, or absent)
// leaves gatewayNotice empty and the latch unset.
func TestGatewayNoticeNotFiredWhenNoAvailableNotDefault(t *testing.T) {
	fm := sampleModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models})
	m = mm.(Model)
	if m.gatewayNotice != "" {
		t.Errorf("gatewayNotice should be empty with no available_not_default row, got %q", m.gatewayNotice)
	}
	if m.gatewayNoticeShown {
		t.Errorf("gatewayNoticeShown should be false when the notice never fired")
	}
}

func TestGatewayNoticeNotFiredForActiveToolhiveFamily(t *testing.T) {
	fm := gatewayModels()
	statuses := []client.ProviderStatus{{
		ProviderID:          "toolhive-anthropic",
		State:               "ok",
		ModelCount:          1,
		AvailableNotDefault: true,
	}}
	for _, providerID := range []string{"toolhive", "toolhive-anthropic"} {
		m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
		m.resolvedSessionModel = client.ResolvedModel{ProviderID: providerID, ModelID: "claude-sonnet-4-6"}
		mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: statuses})
		m = mm.(Model)
		if m.gatewayNotice != "" || m.gatewayNoticeShown {
			t.Errorf("active provider %q armed same-family gateway notice %q (shown=%t)",
				providerID, m.gatewayNotice, m.gatewayNoticeShown)
		}
	}
}

// TestGatewayNoticeClearedOnKeypress: any keypress at idle clears the notice text
// (the latch stays true so it never re-fires).
func TestGatewayNoticeClearedOnKeypress(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	if m.gatewayNotice == "" {
		t.Fatalf("precondition: notice should be set")
	}
	// Drive a real idle key through the top-level Update (onKey → onIdleKey).
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated.(Model)
	if m.gatewayNotice != "" {
		t.Errorf("a keypress at idle should clear gatewayNotice, got %q", m.gatewayNotice)
	}
	if !m.gatewayNoticeShown {
		t.Errorf("gatewayNoticeShown should stay latched after dismissal")
	}
}

// TestGatewayNoticeRenderedAtIdle: the footer renders the notice at idle; at running
// the running arm owns the footer-left and the notice is NOT shown (even if it were
// still set, which it is here for the assertion).
func TestGatewayNoticeRenderedAtIdle(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	m.sel = selection{} // no active selection: notice arm wins over statusMsg/"ready"
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "toolhive gateway available") {
		t.Errorf("idle footer should render the notice, got:\n%s", got)
	}
	// At running the spinner arm owns the footer-left; the notice must NOT appear.
	m.phase = phaseRunning
	got = stripANSIstr(m.renderFooter())
	if strings.Contains(got, "toolhive gateway available") {
		t.Errorf("running footer must NOT render the notice (the running arm owns the slot), got:\n%s", got)
	}
}

// TestGatewayNoticeClearedOnOpenModels: opening the /models picker dismisses the
// notice (the operator is acting on it).
func TestGatewayNoticeClearedOnOpenModels(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	if m.gatewayNotice == "" {
		t.Fatalf("precondition: notice should be set")
	}
	mm, _ = m.openModels()
	m = mm.(Model)
	if m.gatewayNotice != "" {
		t.Errorf("openModels should clear gatewayNotice, got %q", m.gatewayNotice)
	}
	if !m.gatewayNoticeShown {
		t.Errorf("gatewayNoticeShown should stay latched after openModels")
	}
}

// --- Proposal 2: picker "org" tag (S1) ----------------------------------------

// TestModelRowOrgTagForConfigIntentProvider: a model row whose provider is in
// configProvenanceProviderIDs carries a "org" segment; a key-driven provider's row does not.
func TestModelRowOrgTagForConfigIntentProvider(t *testing.T) {
	configProvenanceProviderIDs := map[string]bool{"toolhive": true}
	toolhive := client.ModelInfo{ID: "claude-sonnet-4-6", ProviderID: "toolhive", DisplayName: "Claude Sonnet 4.6"}
	openrouter := client.ModelInfo{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude"}

	got := modelRowText(client.ModelSelection{}, client.ModelSelection{}, configProvenanceProviderIDs, toolhive)
	if !strings.Contains(got, "org") {
		t.Errorf("toolhive row should carry the org tag, got %q", got)
	}
	// "org" must appear BEFORE the cap segments (it's prepended).
	if !strings.Contains(got, "toolhive · Claude Sonnet 4.6  org") {
		t.Errorf("org tag should lead the segment list, got %q", got)
	}

	got = modelRowText(client.ModelSelection{}, client.ModelSelection{}, configProvenanceProviderIDs, openrouter)
	if strings.Contains(got, "org") {
		t.Errorf("openrouter row must NOT carry the org tag, got %q", got)
	}
}

func TestConfigIntentProviderSetExcludesOpenAICodexStatus(t *testing.T) {
	got := configProvenanceProviderSet([]client.ProviderStatus{
		{ProviderID: "toolhive", State: "ok"},
		{ProviderID: "toolhive-anthropic", State: "ok"},
		{ProviderID: "openai-codex", State: "ok"},
	})
	if !got["toolhive"] {
		t.Fatal("ToolHive status lost its config-intent classification")
	}
	if got["openai-codex"] {
		t.Fatal("Codex entitlement status was misclassified as config intent")
	}
	if !got["toolhive-anthropic"] {
		t.Fatal("native ToolHive status lost its config-intent classification")
	}
}

func TestToolhiveNativeAnthropic_Scenario3_StatusAndPresentation(t *testing.T) {
	got := providerStatusLine(client.ProviderStatus{
		ProviderID: "toolhive-anthropic",
		State:      "unreachable",
		Hint:       "check gateway connectivity or use `--toolhive-llm-mode proxy`",
	})
	want := "toolhive-anthropic: gateway not reachable — check gateway connectivity or use `--toolhive-llm-mode proxy`"
	if got != want {
		t.Fatalf("providerStatusLine = %q, want %q", got, want)
	}
}

// TestModelRowOrgTagNilConfigIntentProviderIDs: a nil configProvenanceProviderIDs map (no gateway)
// means no row carries the "org" tag — the byte-identical pre-feature path.
func TestModelRowOrgTagNilConfigIntentProviderIDs(t *testing.T) {
	toolhive := client.ModelInfo{ID: "claude-sonnet-4-6", ProviderID: "toolhive", DisplayName: "Claude Sonnet 4.6"}
	got := modelRowText(client.ModelSelection{}, client.ModelSelection{}, nil, toolhive)
	if strings.Contains(got, "org") {
		t.Errorf("nil configProvenanceProviderIDs must not produce an org tag, got %q", got)
	}
}

// TestModelOrgTagRenderedInPicker: a picker fed a toolhive model + a toolhive
// status renders the "org" tag on the toolhive row, and NOT on the openai row.
func TestModelOrgTagRenderedInPicker(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "toolhive · Claude Sonnet 4.6  org") {
		t.Errorf("picker should render the org tag on the toolhive row, got:\n%s", out)
	}
	// The openai row must NOT carry "org".
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "openai · GPT-5") && strings.Contains(ln, "org") {
			t.Errorf("openai row must NOT carry the org tag, got %q", ln)
		}
	}
}

// --- Proposal 3: provenance hint (S2) ------------------------------------------

// TestProvenanceHintAppendedWhenGatewayAvailable: an openrouter (default) session
// with a toolhive AvailableNotDefault status appends the muted hint naming the
// gateway outranked by the default provider key. The default (openrouter) is
// key-driven (NOT in configProvenanceProviderIDs), so the key-driven guard holds and the hint
// fires.
func TestProvenanceHintAppendedWhenGatewayAvailable(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	m.modelCatalog.statuses = gatewayStatuses()
	// toolhive is intent-driven; openrouter is NOT (key-driven) — the guard's premise.
	m.modelCatalog.configProvenanceProviderIDs = map[string]bool{"toolhive": true}
	got := m.modelProvenanceLine()
	if !strings.Contains(got, "current:") {
		t.Fatalf("provenance line missing the base current: line, got %q", got)
	}
	if !strings.Contains(got, "toolhive gateway also available") {
		t.Errorf("provenance line missing the gateway hint, got %q", got)
	}
	if !strings.Contains(got, "outranked by your openrouter key") {
		t.Errorf("provenance hint missing the outranking clause, got %q", got)
	}
}

// TestProvenanceHintSuppressedWhenGatewayIsDefault: a toolhive-default session
// (the gateway IS the default) shows NO hint — the outranking condition does not hold.
func TestProvenanceHintSuppressedWhenGatewayIsDefault(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}
	// toolhive is the default here, so AvailableNotDefault is false on its row.
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", ModelCount: 5, AvailableNotDefault: false}}
	got := m.modelProvenanceLine()
	if strings.Contains(got, "gateway also available") {
		t.Errorf("provenance hint should be suppressed when the gateway IS the default, got %q", got)
	}
}

// TestProvenanceHintSuppressedWhenDefaultIsIntentDriven: when the default provider is
// ITSELF intent-driven (in configProvenanceProviderIDs), the "outranked by your … key" wording would
// mislead (an intent-driven default has no key), so the hint is suppressed even though a
// different gateway row is AvailableNotDefault.
func TestProvenanceHintSuppressedWhenDefaultIsIntentDriven(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	// A second intent-driven provider is the default; toolhive is AvailableNotDefault.
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "other-gateway", ModelID: "some-model"}
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", ModelCount: 5, AvailableNotDefault: true}}
	// The default (other-gateway) IS intent-driven — the key-driven guard must suppress.
	m.modelCatalog.configProvenanceProviderIDs = map[string]bool{"toolhive": true, "other-gateway": true}
	got := m.modelProvenanceLine()
	if strings.Contains(got, "gateway also available") {
		t.Errorf("provenance hint must be suppressed when the default is intent-driven (no key), got %q", got)
	}
	if strings.Contains(got, "outranked by your") {
		t.Errorf("provenance hint must not claim a key for an intent-driven default, got %q", got)
	}
}

// TestProvenanceHintSuppressedWhenNoStatus: no statuses ⇒ no hint (byte-identical
// to the pre-feature line).
func TestProvenanceHintSuppressedWhenNoStatus(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	got := m.modelProvenanceLine()
	if strings.Contains(got, "gateway also available") {
		t.Errorf("provenance hint should be suppressed with no statuses, got %q", got)
	}
}

// --- golden: idle footer gateway notice ---------------------------------------

// TestFooterGatewayNoticeGolden locks the idle footer-left rendering of the
// once-per-process gateway notice (Proposal 1): an AvailableNotDefault status fires
// the notice, and at idle (no selection, no statusMsg) the footer-left renders it
// muted. The snapshot is the stripped full renderFooter at a wide width.
func TestFooterGatewayNoticeGolden(t *testing.T) {
	fm := gatewayModels()
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models, Statuses: fm.statuses})
	m = mm.(Model)
	m.sel = selection{} // no selection: the notice arm wins over statusMsg/"ready"
	got := stripANSIstr(m.renderFooter())
	compareGolden(t, "footer_gateway_notice.golden", []byte(got+"\n"))
}

// uncachedModels is the ADR 0346 contrast fixture: every row reports
// PromptCached=false, which is the ONE shape composition can actually produce
// now that decision 1 arms the breakpoint on every Responses endpoint. The flag
// behind it (--no-prompt-cache) is harness-wide, so a fixture marking a single
// non-caching provider would encode a state the server cannot emit.
func uncachedModels() *fakeModels {
	return &fakeModels{models: []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", Reasoning: true, ContextLimit: 200000},
		{ID: "anthropic/claude-opus-4.8", ProviderID: "openrouter-anthropic", DisplayName: "Claude Opus 4.8", Reasoning: true, ContextLimit: 1000000},
		{ID: "anthropic/claude-opus-4.8", ProviderID: "toolhive", DisplayName: "Claude Opus 4.8", Reasoning: true, ContextLimit: 1000000},
	}}
}

// markedRowsFor returns the rendered model rows (never the legend) that carry
// the no-cache marker, keyed by the substring identifying each row. The legend
// line contains "no-cache" too, so a whole-view Contains cannot distinguish a
// marked row from the explanation of one.
func markedRowsFor(view, rowSubstring string) (found, marked bool) {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "no-cache = ") {
			continue // the legend, not a row
		}
		if !strings.Contains(line, rowSubstring) {
			continue
		}
		found = true
		if strings.Contains(line, "no-cache") {
			marked = true
		}
	}
	return found, marked
}

// TestUnifiedPromptCache_Scenario2_PickerMarksUncachedRow pins AC3.5: the row
// mecatl sends no cache breakpoint for is MARKED and stays SELECTABLE. Marking
// rather than hiding was the explicit decision, so this asserts both halves,
// plus that the legend only appears when there is a marker to explain.
//
// This is a RENDERER contract: it feeds PromptCached=false in directly, because
// the renderer's job is to mark whatever false it is handed. Composition can
// only produce false under --no-prompt-cache (a harness-wide switch), which the
// sibling TestADR_0346_PromptCachedTrueWithoutDialect covers. The test name
// keeps its Scenario2 prefix because the approved acceptance plan cites it
// verbatim in AC3.5's verify line.
func TestUnifiedPromptCache_Scenario2_PickerMarksUncachedRow(t *testing.T) {
	m := newModelsModel(t, uncachedModels(), &fakeStore{}, modelsCaps(),
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	view := string(stripANSI([]byte(m.View().Content)))

	if found, marked := markedRowsFor(view, "toolhive"); !found || !marked {
		t.Errorf("the toolhive Claude row must be marked (found=%v marked=%v), got:\n%s", found, marked, view)
	}
	// Scoping, the half that was previously unasserted: the SAME disabled
	// posture leaves a non-Anthropic row unmarked, because false there only
	// means mecatl sends no hint and an implicit cacher may cache anyway.
	if found, marked := markedRowsFor(view, "GPT-5"); !found || marked {
		t.Errorf("the gpt-5 row must NOT be marked (found=%v marked=%v), got:\n%s", found, marked, view)
	}
	if !strings.Contains(view, "no-cache = no prompt-cache breakpoint sent") {
		t.Errorf("the legend must appear when a row is marked, got:\n%s", view)
	}
	// Selectable: the row is still present and reachable in the filtered list.
	surface := modelsSurface(t, m)
	var found bool
	for _, mi := range surface.filtered {
		if mi.ProviderID == "toolhive" && !mi.PromptCached {
			found = true
		}
	}
	if !found {
		t.Error("the uncached row must remain selectable, not be hidden")
	}

	// Contrast: an all-cached catalog shows neither marker nor legend.
	clean := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(),
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	cm, ccmd := clean.runModels()
	clean = feedCmd(t, cm.(Model), ccmd)
	if cleanView := string(stripANSI([]byte(clean.View().Content))); strings.Contains(cleanView, "no-cache") {
		t.Errorf("an all-cached catalog must show no marker and no legend, got:\n%s", cleanView)
	}
}
