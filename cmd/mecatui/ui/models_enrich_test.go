package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Commit B — /models picker UX enrichment: header next: badge, picker provenance
// line, restart-now confirm overlay, ctrl+g global default, ●/★ markers, reconcile
// notice naming the fallback. Client-only (no proto/server change).

// --- header next: badge ----------------------------------------------------

// idleModelWith builds an idle, sized model with the given effective + pending-next
// selections wired (no overlays open), so the header next-badge logic is exercised
// directly. effective is delivered as the create response's resolved model; next is
// set as the pending createModelSelection.
func idleModelWith(t *testing.T, effective client.ResolvedModel, next client.ModelSelection, inv []client.ModelInfo) Model {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Server:  "127.0.0.1:8080",
		Mode:    "default",
		Ctx:     context.Background(),
	})
	m.modelCatalog.models = inv
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", ResolvedModel: effective},
	)
	m.createModelSelection = next
	return m
}

// TestHeaderNextBadgeShownWhenDiffers: a pending-next model that differs from the
// effective model renders a "next: <name>" header segment (display name resolved
// from the inventory).
func TestHeaderNextBadgeShownWhenDiffers(t *testing.T) {
	inv := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"},
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude"},
	}
	m := idleModelWith(t,
		client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
		client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"},
		inv)
	if got := m.headerNextBadge(); got != "next: Claude" {
		t.Fatalf("headerNextBadge = %q, want %q", got, "next: Claude")
	}
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "next: Claude") {
		t.Fatalf("header missing the next: badge:\n%s", header)
	}
	// The effective model (GPT-5) is still the primary model segment.
	if !strings.Contains(header, "GPT-5") {
		t.Fatalf("header missing the effective model segment:\n%s", header)
	}
}

// TestHeaderNextBadgeHiddenWhenSame: a pending-next equal to the effective model
// shows NO badge (nothing to preview).
func TestHeaderNextBadgeHiddenWhenSame(t *testing.T) {
	m := idleModelWith(t,
		client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"},
		nil)
	if got := m.headerNextBadge(); got != "" {
		t.Fatalf("headerNextBadge = %q, want empty (same model ⇒ no badge)", got)
	}
	if strings.Contains(stripANSIstr(m.renderHeader()), "next:") {
		t.Fatalf("header should carry no next: badge when next==effective")
	}
}

// TestHeaderNextBadgeHiddenWhenNoEffective: with no known effective model (older
// server / connecting), the model SEGMENT already shows the pending-next, so a next:
// badge would duplicate — it is suppressed.
func TestHeaderNextBadgeHiddenWhenNoEffective(t *testing.T) {
	m := idleModelWith(t,
		client.ResolvedModel{}, // no effective model known
		client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"},
		nil)
	if got := m.headerNextBadge(); got != "" {
		t.Fatalf("headerNextBadge = %q, want empty (no effective ⇒ no badge)", got)
	}
}

// TestHeaderNextBadgeDroppedUnderWidthPressure: at a narrow width the next: badge is
// the FIRST segment shed — the socket/mode survive, the badge does not.
func TestHeaderNextBadgeDroppedUnderWidthPressure(t *testing.T) {
	inv := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"},
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude"},
	}
	m := idleModelWith(t,
		client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
		client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"},
		inv)
	// Wide: the badge fits.
	if !strings.Contains(stripANSIstr(m.renderHeader()), "next: Claude") {
		t.Fatalf("at 160 cols the next: badge should fit")
	}
	// Narrow: shed the badge first; the socket (the LAST identity segment) survives.
	m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	header := stripANSIstr(m.renderHeader())
	if strings.Contains(header, "next:") {
		t.Fatalf("at 60 cols the next: badge must be dropped first, got:\n%s", header)
	}
	if !strings.Contains(header, "127.0.0.1:8080") {
		t.Fatalf("the socket must survive when the next: badge is dropped, got:\n%s", header)
	}
}

// --- provenance line -------------------------------------------------------

// TestModelProvenanceServerDefault: an effective model matching none of the
// client-held sources reads "server default".
func TestModelProvenanceServerDefault(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	if got := m.modelProvenance(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}); got != "server default" {
		t.Fatalf("provenance = %q, want server default", got)
	}
}

// TestModelProvenanceWorkspaceDefault: an effective model equal to the per-workspace
// state entry loaded at launch reads "workspace default".
func TestModelProvenanceWorkspaceDefault(t *testing.T) {
	ws := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		WorkspaceDefault: ws, WorkspaceDefaultSet: true,
	})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: ws.ProviderID, ModelID: ws.ModelID}
	if got := m.modelProvenance(ws); got != "workspace default" {
		t.Fatalf("provenance = %q, want workspace default", got)
	}
}

// TestModelProvenanceGlobalDefault: an effective model equal to the global default
// (and NOT a workspace entry) reads "global default".
func TestModelProvenanceGlobalDefault(t *testing.T) {
	gd := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		GlobalDefault: gd,
	})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: gd.ProviderID, ModelID: gd.ModelID}
	if got := m.modelProvenance(gd); got != "global default" {
		t.Fatalf("provenance = %q, want global default", got)
	}
}

// TestModelProvenancePickedThisSession: after an explicit restart-now pick, the
// effective model reads "picked this session" (highest precedence).
func TestModelProvenancePickedThisSession(t *testing.T) {
	picked := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		// Even though it ALSO matches a workspace default, picked-this-session wins.
		WorkspaceDefault: picked, WorkspaceDefaultSet: true,
	})
	m.pickedThisSession = picked
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: picked.ProviderID, ModelID: picked.ModelID}
	if got := m.modelProvenance(picked); got != "picked this session" {
		t.Fatalf("provenance = %q, want picked this session", got)
	}
}

// TestModelProvenanceFlag: an effective model equal to the launch-time --model flag
// reads "--model flag".
func TestModelProvenanceFlag(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		Model: "gpt-5",
	})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}
	if got := m.modelProvenance(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}); got != "--model flag" {
		t.Fatalf("provenance = %q, want --model flag", got)
	}
}

// TestModelProvenanceToolhiveAutoSelected: issue #262 review finding 7 — a
// toolhive effective model whose provider_status row carries the
// server-side AutoSelected bit (no key, no --default-model chose it; it was
// resolved from the FIRST model the gateway credential listed) reads
// "auto-selected", NOT "server default" (which would imply a deliberate
// operator choice). The label is now vendor-neutral: it is gated on the WIRE
// bit (statusAutoSelected over m.modelCatalog.statuses), never a bare
// ProviderID=="toolhive" check.
func TestModelProvenanceToolhiveAutoSelected(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", DefaultModelAutoSelected: true}}
	if got := m.modelProvenance(client.ModelSelection{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}); got != "auto-selected" {
		t.Fatalf("provenance = %q, want auto-selected", got)
	}
}

// TestModelProvenanceToolhiveOperatorConfigured_NotAutoSelected pins the fix
// itself (issue #262 review finding 7): a toolhive default the OPERATOR
// configured (--default-model) carries NO AutoSelected bit on the wire, so
// the vendor-neutral label must fall through to "server default" — the bare
// vendor-name check this replaces would have wrongly claimed "auto-selected"
// for every toolhive session, including this deliberately-configured one.
func TestModelProvenanceToolhiveOperatorConfigured_NotAutoSelected(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "gpt-5"}
	m.modelCatalog.statuses = []client.ProviderStatus{{ProviderID: "toolhive", State: "ok", DefaultModelAutoSelected: false}}
	if got := m.modelProvenance(client.ModelSelection{ProviderID: "toolhive", ModelID: "gpt-5"}); got != "server default" {
		t.Fatalf("provenance = %q, want server default (operator-configured, no AutoSelected bit)", got)
	}
}

// TestModelProvenanceNoStatusRow_NotAutoSelected: a toolhive effective model
// with NO matching status row (e.g. a deployment that never wired
// provider_status) must not spuriously claim "auto-selected" either —
// statusAutoSelected's miss branch returns false, falling through to "server
// default".
func TestModelProvenanceNoStatusRow_NotAutoSelected(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()})
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}
	if got := m.modelProvenance(client.ModelSelection{ProviderID: "toolhive", ModelID: "claude-sonnet-4-6"}); got != "server default" {
		t.Fatalf("provenance = %q, want server default (no status row)", got)
	}
}

// --- ●/★ markers -----------------------------------------------------------

// TestModelRowMarkers asserts the fixed-width 2-marker column: ● on the pending
// (active) row, ★ on the global-default row, "●★" when a row is both, and two
// spaces when neither — layout-stable for goldens.
func TestModelRowMarkers(t *testing.T) {
	active := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	gd := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	gpt := client.ModelInfo{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"}
	claude := client.ModelInfo{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude"}
	mini := client.ModelInfo{ID: "gpt-5-mini", ProviderID: "openai", DisplayName: "GPT-5 mini"}

	if got := modelRowText(active, gd, nil, gpt); !strings.HasPrefix(got, "●  ") {
		t.Errorf("active row should start with the ● marker (+ space pad), got %q", got)
	}
	if got := modelRowText(active, gd, nil, claude); !strings.HasPrefix(got, " ★ ") {
		t.Errorf("global-default row should carry the ★ marker, got %q", got)
	}
	if got := modelRowText(active, gd, nil, mini); !strings.HasPrefix(got, "   ") {
		t.Errorf("a plain row should have a blank 2-cell marker column, got %q", got)
	}
	// A row that is BOTH active AND the global default shows "●★".
	if got := modelRowText(active, active, nil, gpt); !strings.HasPrefix(got, "●★ ") {
		t.Errorf("a row that is both active and global default should show ●★, got %q", got)
	}
}

// --- footer hint -----------------------------------------------------------

// TestModelsFooterMentionsGlobalDefault: the picker footer mentions ctrl+g and the
// ★ legend.
func TestModelsFooterMentionsGlobalDefault(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "ctrl+g set global default") {
		t.Errorf("picker footer should mention ctrl+g, got:\n%s", out)
	}
	if !strings.Contains(out, "★ global default") {
		t.Errorf("picker footer should carry the ★ legend, got:\n%s", out)
	}
}

// --- ctrl+g global default -------------------------------------------------

// TestModelsSetGlobalDefaultPersists: ctrl+g on the cursor row persists the global
// default (NOT the per-workspace Save), marks the ★ row, and shows a toast — without
// changing the active selection or restarting.
func TestModelsSetGlobalDefaultPersists(t *testing.T) {
	store := &fakeStore{}
	m := newModelsModel(t, sampleModels(), store, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	// Move to the 4th row (openrouter/claude) and ctrl+g it.
	for i := 0; i < 3; i++ {
		m = pressModelsKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	mm, cmd, _ = m.onOverlayKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = mm.(Model)
	want := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if m.modelCatalog.globalDefault != want {
		t.Fatalf("globalDefault = %+v, want %+v (★ marker tracks immediately)", m.modelCatalog.globalDefault, want)
	}
	// ctrl+g must NOT change the active/pending selection or open the confirm.
	if !m.createModelSelection.IsZero() {
		t.Errorf("ctrl+g must not change the active selection, got %+v", m.createModelSelection)
	}
	if modelsSurface(t, m).view != modelsPanel {
		t.Errorf("ctrl+g must keep the picker open, view = %v", modelsSurface(t, m).view)
	}
	if got := modelsSurface(t, m).catalog.globalDefault; got != want {
		t.Fatalf("surface globalDefault = %+v, want %+v (open picker marker must update)", got, want)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "★") {
		t.Fatal("open picker should render the updated global-default marker")
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "global default set") {
		t.Errorf("status should read 'global default set', got %q", stripANSIstr(m.statusMsg))
	}
	// The persist Cmd writes the GLOBAL default, not the per-workspace entry.
	m = feedCmd(t, m, cmd)
	if store.globalSaves != 1 || store.lastGlobalSel != want {
		t.Fatalf("store: globalSaves=%d lastGlobalSel=%+v, want 1 / %+v", store.globalSaves, store.lastGlobalSel, want)
	}
	if store.saves != 0 {
		t.Errorf("ctrl+g must NOT write the per-workspace entry, got %d Save calls", store.saves)
	}
}

// --- reconcile notice names the fallback model ------------------------------

// TestReconcileNoticeNamesFallbackModel: when the persisted model is gone AND the
// effective model is populated (e.g. picker reopened post-connect), the reconcile
// notice appends "now running <effective model id>".
func TestReconcileNoticeNamesFallbackModel(t *testing.T) {
	fm := &fakeModels{models: []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
	}}
	gone := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), gone)
	// newModelsModel delivered an effective model (openai/gpt-5) on the create
	// response, so the reconcile notice can name it.
	mm, _, _ := m.updateModelsMsg(client.ModelsMsg{Models: fm.models})
	m = mm.(Model)
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "no longer available") {
		t.Fatalf("reconcile notice missing the unavailable warning, got %q", st)
	}
	if !strings.Contains(st, "now running gpt-5") {
		t.Fatalf("reconcile notice should name the fallback model, got %q", st)
	}
}

// --- seamless switch handoff (the redesigned pick→switch flow) --------------

// TestCarryoverHandoff is the seamless /models switch e2e (same-provider): pick
// gpt-5-mini (openai — the live session is openai/gpt-5), press enter, and assert the
// carryover path fires with NO confirm overlay: CreateSessionWithCarryover was called
// with the OLD session id as the source, the old session was closed AFTER the new one
// is ready, a NEW session id is bound, the footer/effective-model heal path rebinds via
// SessionReadyMsg, and the transient "switched to <model> — conversation kept" status
// note surfaces on the rebind. (Server-side history seeding is owned by
// create_carryover_test.go; this asserts the CLIENT wiring end to end through the real
// fake-backed harness.)
func TestCarryoverHandoff(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
		{ID: "gpt-5-mini", ProviderID: "openai", DisplayName: "GPT-5 mini", ContextLimit: 128000},
	}
	run := &fakeRecver{script: simpleRunScript("first")}
	conv := &fakeConv{
		recv:              run,
		send:              &fakeSender{},
		recvers:           []*fakeRecver{run},
		caps:              client.Capabilities{ModelSelection: true},
		sessionReady:      make(chan struct{}),
		created:           make(chan struct{}),
		recreated:         make(chan struct{}),
		echoSelAsResolved: true,
		// The live session resolves to gpt-5/openai; gpt-5-mini is SAME provider.
		resolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
	}
	prog := newProgress()
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Models:      &fakeModels{models: models},
		Transcript:  modelSwitchTranscriptLoader{},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Ctx:         context.Background(),
		NoAltScreen: true,
		onPhase:     prog.record,
	})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))

	// Connect + run a prompt to completion (a real transcript exists to carry over).
	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)
	tm.Type("hello there")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	prog.waitRunComplete(t, 1, 5*time.Second)

	// Open /models, filter to mini, enter — seamless switch (carryover, same provider).
	for _, r := range "/models" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "mini" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // seamless switch — carries the conversation

	// The carryover create fired (recreate signal shared with restart-now).
	waitClosed(t, "carryover re-create", conv.recreated, 5*time.Second)

	// Graceful double-ctrl+c quit, then assert on the final model.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
	fm := tm.FinalModel(t).(Model)

	// (a) CreateSessionWithCarryover was called exactly once, with the OLD session id.
	if got := conv.carryoverCalls(); got != 1 {
		t.Fatalf("CreateSessionWithCarryover calls = %d, want 1", got)
	}
	if srcs := conv.carryoverSources(); len(srcs) != 1 || srcs[0] != "sess-test-0001" {
		t.Fatalf("carryover source ids = %v, want [sess-test-0001] (the old session)", srcs)
	}
	// (b) the OLD session was closed (best-effort, after the new one was ready).
	closed := conv.closed()
	if len(closed) != 1 || closed[0] != "sess-test-0001" {
		t.Fatalf("CloseSession calls = %v, want [sess-test-0001] (the old session, closed after the new one was ready)", closed)
	}
	// (c) a NEW session id is bound.
	if fm.sessionID != "sess-test-0002" {
		t.Fatalf("final sessionID = %q, want sess-test-0002 (rebound to the carryover session)", fm.sessionID)
	}
	// (d) the header shows the NEW effective model (gpt-5-mini), set from the new
	// SessionReadyMsg — the shared reducer path.
	want := client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5-mini"}
	if fm.resolvedSessionModel != want {
		t.Fatalf("final resolvedSessionModel = %+v, want %+v (rebound from the carryover session)", fm.resolvedSessionModel, want)
	}
	// (e) the adopted projection comes from the authoritative target snapshot,
	// not the source's locally accumulated transcript.
	visible := stripANSIstr(fm.View().Content)
	if strings.Contains(visible, "hello there") || strings.Contains(visible, "first") {
		t.Fatalf("target adoption must not retain source projection:\n%s", visible)
	}
	if !fm.restartedThisRun {
		t.Fatalf("restartedThisRun should be set after a carryover handoff")
	}
	// The transient switch note is unit-tested in TestModelsChooseSwitchArmsStatusNote
	// (the double-ctrl+c quit overwrites statusMsg here, so it can't be asserted at
	// FinalModel without output-flush sequencing).
}

// TestCarryoverHandoffFailure asserts a failed CreateSessionWithCarryover leaves
// the source session bound and recoverable; the handoff must not reconstruct it.
func TestCarryoverHandoffFailure(t *testing.T) {
	conv := &fakeConv{
		recv:      &fakeRecver{},
		send:      &fakeSender{},
		caps:      client.Capabilities{ModelSelection: true},
		createErr: errors.New("carryover create rejected"),
	}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: sampleModels().models},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Ctx:          context.Background(),
		NoAltScreen:  true,
		InitialModel: client.ModelSelection{},
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: modelsCaps(),
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}})

	// Pick gpt-5-mini (same provider) via the seamless switch: chooseModel reads the
	// cursor row, arms the status note, and fires restartOnModelWithCarryover (the
	// carryover create). Prime the picker so the cursor is on gpt-5-mini.
	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}
	m.modelCatalog.models = sampleModels().models
	mm, _, handled := m.chooseModel(sel, "")
	m = mm.(Model)
	if !handled {
		t.Fatal("chooseModel should be handled for a same-provider candidate")
	}
	if m.phase != phaseConnecting {
		t.Fatalf("phase mid-handoff = %v, want phaseConnecting", m.phase)
	}

	// Drive the carryover cmd → the failing create → source-preserving failure.
	failMsg := m.carryoverCmd("sess-test-0001", sel, m.modelSwitchRequestToken)()
	if _, ok := failMsg.(modelSwitchFailedMsg); !ok {
		t.Fatalf("a failed carryover create must preserve its source, got %T", failMsg)
	}
	mm2, _ := m.Update(failMsg)
	m = mm2.(Model)

	// The source session was NOT closed on the failure path (the carryover cmd closes
	// the source only AFTER a successful create).
	if closed := conv.closed(); len(closed) != 0 {
		t.Fatalf("CloseSession calls = %v, want [] (source not closed on carryover failure)", closed)
	}
	// RECOVERABLE: NOT the terminal fatal screen.
	if m.phase == phaseFatal {
		t.Fatalf("a failed carryover create must NOT drive the fatal screen (phase=%v)", m.phase)
	}
	if m.phase != phaseIdle {
		t.Fatalf("phase after a failed carryover create = %v, want phaseIdle (recoverable)", m.phase)
	}
	if m.sessionID != "sess-test-0001" {
		t.Fatalf("sessionID after a failed carryover create = %q, want source session", m.sessionID)
	}
	if m.restartFailed {
		t.Fatal("restartFailed must remain false when the source session survives")
	}
	// A visible error status names the model that failed.
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "could not switch") || !strings.Contains(st, "gpt-5-mini") {
		t.Fatalf("status should name the failed carryover model, got %q", st)
	}
	// The carryover method WAS called (the attempt fired), but the source survived.
	if got := conv.carryoverCalls(); got != 1 {
		t.Fatalf("CreateSessionWithCarryover calls = %d, want 1 (the attempt fired)", got)
	}
}

// TestSeamlessSwitchCrossProviderCarriesProgram asserts via the real program that a
// CROSS-PROVIDER pick STILL carries the conversation (the redesign removed the
// same-provider gate): the live session is openai/gpt-5, claude is openrouter, and
// picking it fires CreateSessionWithCarryover (the server strips the prior provider's
// reasoning cache). Complements the unit-level TestModelsChooseCrossProviderStillCarries.
func TestSeamlessSwitchCrossProviderCarriesProgram(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5", ContextLimit: 200000},
		{ID: "anthropic/claude", ProviderID: "openrouter", DisplayName: "Claude", ContextLimit: 1000000},
	}
	run := &fakeRecver{script: simpleRunScript("first")}
	conv := &fakeConv{
		recv:              run,
		send:              &fakeSender{},
		recvers:           []*fakeRecver{run},
		caps:              client.Capabilities{ModelSelection: true},
		sessionReady:      make(chan struct{}),
		created:           make(chan struct{}),
		recreated:         make(chan struct{}),
		echoSelAsResolved: true, // the rebind's SessionReadyMsg mirrors the picked selector
		// Live session openai/gpt-5; claude is cross-provider.
		resolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
	}
	prog := newProgress()
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Models:      &fakeModels{models: models},
		Transcript:  modelSwitchTranscriptLoader{},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Ctx:         context.Background(),
		NoAltScreen: true,
		onPhase:     prog.record,
	})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))

	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)
	tm.Type("cross-provider visible user")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	prog.waitRunComplete(t, 1, 5*time.Second)

	// Open /models, filter to claude (cross-provider), enter — seamless switch.
	for _, r := range "/models" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "claude" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // seamless switch — carries (cross-provider)

	// The carryover create fired (no gate swallowed it).
	waitClosed(t, "cross-provider carryover re-create", conv.recreated, 5*time.Second)

	// Graceful double-ctrl+c quit, then assert on the final model.
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
	fm := tm.FinalModel(t).(Model)

	// The carryover method WAS called (cross-provider still carries, no gate).
	if got := conv.carryoverCalls(); got != 1 {
		t.Fatalf("CreateSessionWithCarryover calls = %d, want 1 (cross-provider still carries)", got)
	}
	if srcs := conv.carryoverSources(); len(srcs) != 1 || srcs[0] != "sess-test-0001" {
		t.Fatalf("carryover source ids = %v, want [sess-test-0001] (the old session)", srcs)
	}
	// A NEW session id is bound (the handoff happened).
	if fm.sessionID != "sess-test-0002" {
		t.Fatalf("sessionID = %q, want sess-test-0002 (rebound to the carryover session)", fm.sessionID)
	}
	// The header shows the NEW effective model (claude).
	want := client.ResolvedModel{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if fm.resolvedSessionModel != want {
		t.Fatalf("final resolvedSessionModel = %+v, want %+v (rebound from the carryover session)", fm.resolvedSessionModel, want)
	}
	visible := stripANSIstr(fm.View().Content)
	if strings.Contains(visible, "cross-provider visible user") || strings.Contains(visible, "first") {
		t.Fatalf("cross-provider target must not retain local source projection:\n%s", visible)
	}
	// The cross-provider strip caveat is unit-tested in TestModelsChooseSwitchArmsStatusNote
	// (the double-ctrl+c quit overwrites statusMsg here, so it can't be asserted at
	// FinalModel without output-flush sequencing).
}

// TestRestartOnModelTearsDownLiveRun is the LOAD-BEARING teardown proof: it drives a
// real streaming run (a gated recver holds the stream open, so m.stream != nil,
// cancelRun is set, and a reader goroutine is subscribed), then invokes
// restartOnModel DIRECTLY (the picker is idle-only and cannot coexist with a live
// run, so this exercises restartOnModel's defensive endRun at the unit level). It
// asserts the run's context was CANCELLED (runCancelled closes), the stream is nilled,
// and streamGen advanced — i.e. no goroutine stays subscribed to the old session. If
// endRun were removed from restartOnModel this test would hang on runCancelled / fail
// the stream-nil + streamGen assertions.
func TestRestartOnModelTearsDownLiveRun(t *testing.T) {
	// A gated recver: it yields the delta then BLOCKS before the result, so the stream
	// stays open (phaseRunning) until the run ctx is cancelled.
	run := &fakeRecver{script: simpleRunScript("live"), gateType: "message.delta", gate: make(chan struct{}), reachedGate: make(chan struct{})}
	conv := &fakeConv{
		recv:         run,
		send:         &fakeSender{},
		recvers:      []*fakeRecver{run},
		runCancelled: make(chan struct{}),
	}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Models:      &fakeModels{models: []client.ModelInfo{{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"}}},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"})

	// Start a real run: submitPrompt opens the stream, sets cancelRun, bumps streamGen,
	// and launches the reader goroutine.
	m.prompt.Rewrite("run something")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.stream == nil || m.cancelRun == nil {
		t.Fatalf("precondition: a live run should have a stream + cancelRun")
	}
	genBefore := m.streamGen
	// Drive the reader goroutine (the cmd batch starts WaitForMsg/ReadLoop). Run the
	// leaves so the goroutine actually launches and the OpenConverse ctx-watch arms.
	runBatchLeaves(cmd)
	waitClosed(t, "run streamed its delta (gated)", run.reachedGate, 5*time.Second)

	// Now restart-now directly. Its endRun must cancel the live run.
	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	mm, _, handled := m.restartOnModel(sel)
	m = mm.(Model)
	if !handled {
		t.Fatal("restartOnModel should report handled")
	}

	// The decisive proof: the per-run context was cancelled (the reader goroutine is
	// no longer subscribed to the old session). Output-flush-independent.
	waitClosed(t, "live run cancelled by the restart handoff", conv.runCancelled, 5*time.Second)
	if m.stream != nil {
		t.Fatalf("stream should be nil after restartOnModel tore the run down")
	}
	if m.cancelRun != nil {
		t.Fatalf("cancelRun should be nil after the teardown")
	}
	if m.streamGen <= genBefore {
		t.Fatalf("streamGen should advance past %d after the teardown, got %d (stale readers must go inert)", genBefore, m.streamGen)
	}
	if m.phase != phaseConnecting {
		t.Fatalf("phase after restartOnModel = %v, want phaseConnecting", m.phase)
	}
}

// TestRestartNowCreateFailureRecovers is the #1 failure-mode guard: when the
// restart-now RE-CREATE fails, the app must stay RECOVERABLE (NOT the terminal fatal
// screen). It drives the handoff at the unit level (so the recovery status can be
// inspected without a quit overwriting it) with a fake whose SECOND CreateSession
// errors, then asserts the old session was still closed, the model is idle with no
// session (NOT phaseFatal), a visible error status names the failed model, and the
// failure message is a restartFailedMsg (not the fatal ConnectErrMsg).
func TestRestartNowCreateFailureRecovers(t *testing.T) {
	conv := &fakeConv{
		recv:            &fakeRecver{gate: make(chan struct{})},
		send:            &fakeSender{},
		caps:            client.Capabilities{ModelSelection: true},
		resolvedModel:   client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
		secondCreateErr: errors.New("provider unavailable"),
		// The connect here is delivered via applyAll(SessionReadyMsg) — it does NOT call
		// CreateSession — so seed createCount=1 to make the restart re-create the SECOND
		// (failing) call, matching production where the startup connect was the first.
		createCount: 1,
	}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Models:       &fakeModels{models: sampleModels().models},
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Ctx:          context.Background(),
		NoAltScreen:  true,
		InitialModel: client.ModelSelection{},
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: modelsCaps(),
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"}})

	// Pick claude (cross-provider) via the seamless switch: with a live session the
	// carryover path fires, but this test exercises the PLAIN-create failure recovery
	// (restartOnModelCmd — the no-session fallback + the retry path share it), so drive
	// restartOnModel directly to set phaseConnecting, then run its failing cmd.
	sel := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	mm, _, handled := m.restartOnModel(sel)
	m = mm.(Model)
	if !handled {
		t.Fatal("restartOnModel should be handled")
	}
	if m.phase != phaseConnecting {
		t.Fatalf("phase mid-handoff = %v, want phaseConnecting", m.phase)
	}

	// Drive the restart cmd → the failing re-create → restartFailedMsg, then reduce it.
	failMsg := m.restartOnModelCmd("sess-test-0001", sel)()
	if _, ok := failMsg.(restartFailedMsg); !ok {
		t.Fatalf("a failed re-create must produce restartFailedMsg, got %T (NOT ConnectErrMsg)", failMsg)
	}
	mm2, _ := m.Update(failMsg)
	m = mm2.(Model)

	// The old session was closed (best-effort teardown ran in the cmd).
	if closed := conv.closed(); len(closed) != 1 || closed[0] != "sess-test-0001" {
		t.Fatalf("CloseSession calls = %v, want [sess-test-0001]", closed)
	}
	// RECOVERABLE: NOT the terminal fatal screen.
	if m.phase == phaseFatal {
		t.Fatalf("a failed restart re-create must NOT drive the fatal screen (phase=%v)", m.phase)
	}
	if m.phase != phaseIdle {
		t.Fatalf("phase after a failed restart re-create = %v, want phaseIdle (recoverable)", m.phase)
	}
	if m.sessionID != "" {
		t.Fatalf("sessionID after a failed re-create = %q, want empty (no session)", m.sessionID)
	}
	if !m.restartFailed {
		t.Fatalf("restartFailed should be set after a failed re-create (arms enter-to-retry)")
	}
	// A visible error status names the model that failed.
	st := stripANSIstr(m.statusMsg)
	if !strings.Contains(st, "could not switch") || !strings.Contains(st, "anthropic/claude") {
		t.Fatalf("status should name the failed model, got %q", st)
	}
	// And the app is still interactive: a key is handled (not swallowed by a fatal
	// screen). onIdleKey on empty-enter fires the retry.
	mm3, cmd := m.onIdleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the recoverable state must accept enter-to-retry")
	}
	_ = mm3
}

// TestRestartFailedEnterRetries asserts the enter-to-retry affordance on a
// SUCCEEDING retry: in the recoverable post-failure state (restartFailed + idle +
// empty session), enter on an empty prompt fires the RECOVERABLE create path carrying
// the still-pending selection; its SessionReadyMsg rebinds the session and clears the
// flag. The flag is NOT cleared eagerly — the resolving msg owns its lifecycle.
func TestRestartFailedEnterRetries(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	// Simulate the recoverable post-failure state.
	m.phase = phaseIdle
	m.sessionID = ""
	m.restartFailed = true
	m.createModelSelection = client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}

	mm, cmd := m.onIdleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("enter in the recoverable state should fire a retry create")
	}
	if m.phase != phaseConnecting {
		t.Fatalf("retry should drive phaseConnecting, got %v", m.phase)
	}
	// The flag is NOT cleared eagerly: the resolving msg owns its lifecycle (so a
	// re-failure can re-set it without an eager-clear race). It stays set until the
	// SessionReadyMsg/restartFailedMsg lands.
	if !m.restartFailed {
		t.Fatalf("restartFailed must NOT be cleared eagerly on fire; the resolving msg owns it")
	}
	// The retry create carries the still-pending selection and (on success) yields a
	// SessionReadyMsg whose reducer clears the flag and rebinds the session. The retry
	// cmd is a batch (the create + the spinner re-arm tick), so locate the ready msg
	// among the resolved leaves.
	var ready client.SessionReadyMsg
	found := false
	for _, msg := range flattenLeafMsgs(cmd, scaleWait(3*time.Second)) {
		if r, ok := msg.(client.SessionReadyMsg); ok {
			ready, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("retry cmd did not yield a SessionReadyMsg leaf")
	}
	want := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if conv.createdSel != want {
		t.Fatalf("retry create carried %+v, want the pending %+v", conv.createdSel, want)
	}
	// The retry must NOT close any session (empty oldID → no CloseSession).
	if closed := conv.closed(); len(closed) != 0 {
		t.Fatalf("a retry must not call CloseSession (empty oldID), got %v", closed)
	}
	// Reducing the success msg clears the recovery flag.
	mm2, _ := m.Update(ready)
	if mm2.(Model).restartFailed {
		t.Fatalf("SessionReadyMsg must clear restartFailed")
	}
}

// TestRestartRetryReFailureStaysRecoverable is the missing sibling: a retry whose
// re-create ALSO fails must STAY recoverable (NOT fatal), looping back to the same
// recoverable state with retry still armed. It drives the recoverable state, fires
// enter-to-retry against a fake whose CreateSession persistently errors, and asserts
// the result msg is restartFailedMsg (NOT client.ConnectErrMsg) and the reduced model
// is phaseIdle (recoverable, retry re-armed) — never phaseFatal. Fails before the
// retry-routing fix (the old retry used createSessionCmd → ConnectErrMsg → fatal).
func TestRestartRetryReFailureStaysRecoverable(t *testing.T) {
	conv := &fakeConv{
		recv:      &fakeRecver{},
		send:      &fakeSender{},
		createErr: errors.New("still rate limited"), // EVERY create fails (persistent transient)
	}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	// Recoverable post-failure state (as left by the first failed restart re-create).
	m.phase = phaseIdle
	m.sessionID = ""
	m.restartFailed = true
	m.createModelSelection = client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}

	// Enter-to-retry fires the recoverable create path.
	mm, cmd := m.onIdleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("enter in the recoverable state should fire a retry")
	}
	if m.phase != phaseConnecting {
		t.Fatalf("retry should drive phaseConnecting, got %v", m.phase)
	}

	// The retry re-create FAILS again: it must produce restartFailedMsg, NOT the fatal
	// ConnectErrMsg. The retry cmd is a batch (the create + the spinner re-arm tick),
	// so locate the failure msg among the resolved leaves.
	var failMsg tea.Msg
	for _, leaf := range flattenLeafMsgs(cmd, scaleWait(3*time.Second)) {
		if _, isFatal := leaf.(client.ConnectErrMsg); isFatal {
			t.Fatalf("a re-failed retry must NOT produce the fatal ConnectErrMsg")
		}
		if _, ok := leaf.(restartFailedMsg); ok {
			failMsg = leaf
		}
	}
	if failMsg == nil {
		t.Fatalf("a re-failed retry must produce a restartFailedMsg leaf")
	}
	// Reducing it loops back to the SAME recoverable state — never fatal.
	mm2, _ := m.Update(failMsg)
	m = mm2.(Model)
	if m.phase == phaseFatal {
		t.Fatalf("a re-failed retry must NOT strand on the fatal screen")
	}
	if m.phase != phaseIdle {
		t.Fatalf("a re-failed retry should stay recoverable (phaseIdle), got %v", m.phase)
	}
	if !m.restartFailed {
		t.Fatalf("a re-failed retry should keep the retry armed (restartFailed set)")
	}
	if m.sessionID != "" {
		t.Fatalf("a re-failed retry should remain session-less, got %q", m.sessionID)
	}
	// And it is STILL interactive: enter re-fires the retry once more (no wedge).
	_, cmd2 := m.onIdleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("the app must still accept enter-to-retry after a re-failure")
	}
}

// TestRestartStaleStreamEventDropped ties the streamGen guard to the restart seam: a
// streamMsg tagged with the PRE-restart generation, fed AFTER restartOnModel, is
// dropped (its stale reader can't mutate the new session).
func TestRestartStaleStreamEventDropped(t *testing.T) {
	run := &fakeRecver{script: simpleRunScript("live"), gateType: "message.delta", gate: make(chan struct{}), reachedGate: make(chan struct{})}
	conv := &fakeConv{
		recv: run, send: &fakeSender{}, recvers: []*fakeRecver{run},
		runCancelled: make(chan struct{}),
	}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv,
		Models:      &fakeModels{models: []client.ModelInfo{{ID: "gpt-5", ProviderID: "openai", DisplayName: "GPT-5"}}},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
	m.prompt.Rewrite("run something")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	staleGen := m.streamGen // the generation the live reader is tagged with
	runBatchLeaves(cmd)
	waitClosed(t, "delta gated", run.reachedGate, 5*time.Second)

	// Restart — endRun bumps streamGen, so staleGen is now behind.
	mm, _, _ = m.restartOnModel(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
	m = mm.(Model)
	waitClosed(t, "live run cancelled", conv.runCancelled, 5*time.Second)
	if m.streamGen == staleGen {
		t.Fatalf("streamGen should have advanced past the stale %d", staleGen)
	}

	// Feed a stream event tagged with the PRE-restart generation: it must be dropped
	// (the reducer's gen guard), leaving the conversation untouched (still empty after
	// the reset — the stale delta must not append a ghost block).
	mm2, _ := m.Update(streamMsg{gen: staleGen, msg: client.AssistantDeltaMsg{Turn: 1, Text: "stale ghost"}})
	m = mm2.(Model)
	m.refreshView()
	if !m.conv.isEmpty() {
		t.Fatalf("a stale-generation stream event must be dropped (conversation must stay empty)")
	}
	if strings.Contains(stripANSIstr(m.View().Content), "stale ghost") {
		t.Fatalf("the stale delta text must not appear after a restart")
	}
}

// TestWelcomeSplashSuppressedAfterRestart locks the first-run-only welcome splash
// gate: a genuine first session (idle + empty conversation) shows the splash; once
// restartedThisRun is set (a model switch happened), the same idle+empty frame shows
// NO splash (it is a first-run affordance, not a per-switch one).
func TestWelcomeSplashSuppressedAfterRestart(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()),
		Workspace: "/workspace", Mode: "default", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 60},
		client.SessionReadyMsg{SessionID: "sess-test-0001"})
	// First run: idle + empty → the welcome splash shows.
	if !strings.Contains(stripANSIstr(m.View().Content), "Welcome to mecatui") {
		t.Fatalf("first-run idle+empty frame should show the welcome splash:\n%s", stripANSIstr(m.View().Content))
	}
	// After a restart (restartedThisRun set), the same idle+empty frame shows NO splash.
	m.restartedThisRun = true
	m.refreshView()
	if strings.Contains(stripANSIstr(m.View().Content), "Welcome to mecatui") {
		t.Fatalf("the welcome splash must be suppressed after a restart (restartedThisRun)")
	}
}

// TestRestartOnModelCmdNoCloseWhenNoOldID asserts the defensive oldID=="" branch:
// restartOnModelCmd must NOT call CloseSession when there is no prior session id.
func TestRestartOnModelCmdNoCloseWhenNoOldID(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
	})
	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	cmd := m.restartOnModelCmd("", sel) // no old session id
	msg := cmd()
	if _, ok := msg.(client.SessionReadyMsg); !ok {
		t.Fatalf("cmd msg = %T, want SessionReadyMsg", msg)
	}
	if closed := conv.closed(); len(closed) != 0 {
		t.Fatalf("CloseSession must NOT be called when oldID is empty, got %v", closed)
	}
}
