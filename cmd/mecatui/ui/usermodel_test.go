package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Tests for the /usermodel inspection panel: a read-only, idle-only overlay that
// fires GetUserModel and renders the current operator-facts index (key +
// description + aggregate metadata). The agent curates the user model; the panel
// only displays it.

// newUserModelModel builds an idle, sized Model wired to the given fakeUserModel
// and caps. Mirrors newSkillsModel/newAgentsInvModel.
func newUserModelModel(t *testing.T, um client.UserModelLister, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		UserModel:   um,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m
}

func sampleUserModel() *fakeUserModel {
	return &fakeUserModel{model: client.UserModel{
		Entries: []client.UserModelEntry{
			{Key: "name", Description: "the operator's name"},
			{Key: "stack", Description: "prefers Go + hexagonal architecture"},
		},
		SizeBytes: 64,
		SHA256:    "abc123def4567890",
	}}
}

// TestRunUserModelOpensPanel asserts runUserModel opens the panel, blurs the input,
// fires GetUserModel, and renders the entries once the result lands.
func TestRunUserModelOpensPanel(t *testing.T) {
	fum := sampleUserModel()
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})

	mm, cmd := m.runUserModel()
	m = mm.(Model)
	if m.userModel.view != userModelPanel {
		t.Fatalf("view = %v, want userModelPanel", m.userModel.view)
	}
	if !m.userModel.loading {
		t.Error("panel should be loading until the RPC result lands")
	}
	if m.prompt.Focused() {
		t.Error("opening the panel should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runUserModel should fire the GetUserModel RPC command")
	}

	m = feedCmd(t, m, cmd)
	if fum.calls != 1 {
		t.Errorf("GetUserModel calls = %d, want 1", fum.calls)
	}
	if !strings.Contains(m.View().Content, "operator's name") {
		t.Errorf("rendered panel missing entry description:\n%s", m.View().Content)
	}
}

// TestUserModelKeySwallowsNonEsc asserts a non-esc key while the panel is open is
// swallowed (handled=true).
func TestUserModelKeySwallowsNonEsc(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)

	_, _, handled := m.onUserModelKey(tea.KeyPressMsg{Code: 'j'})
	if !handled {
		t.Error("a non-esc key while the panel is open should be swallowed (handled=true)")
	}
}

// TestUserModelError asserts a GetUserModel error renders distinctly.
func TestUserModelError(t *testing.T) {
	fum := &fakeUserModel{err: errors.New("boom")}
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	if m.userModel.err == nil {
		t.Fatal("a GetUserModel error should be recorded on the state")
	}
	if !strings.Contains(m.View().Content, "boom") {
		t.Errorf("error not surfaced in the panel:\n%s", m.View().Content)
	}
}

func TestUserModelDetailHistoryUnavailableAndSanitized(t *testing.T) {
	fum := sampleUserModel()
	fum.model.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "name\x1b]8;;https://evil\a\u202e", Value: "Alice 日本語\x1b[2J\u0085\u2066\u200b\ufeff" + string([]byte{0xff}), Description: "ordinary 日本語", Status: "active\rforged"}}
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	mm, cmd, handled := m.onUserModelKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("enter should fetch selected detail")
	}
	m = feedCmd(t, mm.(Model), cmd)
	view := stripANSIstr(m.View().Content)
	if m.userModel.view != userModelDetail || !strings.Contains(view, "Earlier versions are unavailable") {
		t.Fatalf("detail/history-unavailable not rendered:\n%s", view)
	}
	if strings.ContainsAny(view, "\x1b\a\u0085\u202e\u2066\u200b\ufeff") {
		t.Fatalf("terminal controls escaped detail sanitizer:\n%q", view)
	}
	if !strings.Contains(view, "Alice 日本語") || !strings.Contains(view, "�") {
		t.Fatalf("detail sanitizer lost ordinary Unicode or invalid-UTF8 repair:\n%q", view)
	}
	if !strings.Contains(view, "ask the agent to remove or restore this saved fact") {
		t.Fatal("detail must explain how to change saved memory")
	}
	for _, internalName := range []string{"ForgetUserMemory", "UndoUserMemory"} {
		if strings.Contains(view, internalName) {
			t.Fatalf("detail exposed internal tool name %q:\n%s", internalName, view)
		}
	}
}

func TestUserModelDetailDropsDelayedSelectionResponse(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	m.userModelGen = 8
	m.userModel = userModelState{view: userModelPanel, loading: true, requestKey: "user/b", model: sampleUserModel().model}
	fastB := client.UserModelDetailMsg{Generation: 8, RequestKey: "user/b", UserModel: client.UserModel{Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "user/b", Value: "B"}}}}
	mm, handled := m.updateUserModelMsg(fastB)
	if !handled {
		t.Fatal("current response not handled")
	}
	m = mm.(Model)
	lateA := client.UserModelDetailMsg{Generation: 7, RequestKey: "user/a", UserModel: client.UserModel{Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "user/a", Value: "A"}}}}
	mm, _ = m.updateUserModelMsg(lateA)
	m = mm.(Model)
	if m.userModel.detail == nil || m.userModel.detail.Current.Key != "user/b" || m.userModel.detail.Current.Value != "B" {
		t.Fatalf("late A replaced fast B: %+v", m.userModel.detail)
	}
}

func TestUserModelInventoryDropsResponseAcrossCloseReopen(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	m.userModelGen = 10
	m.userModel = userModelState{view: userModelPanel, loading: true}
	closed, _ := m.closeUserModel()
	m = closed.(Model)
	reopened, _ := m.openUserModel()
	m = reopened.(Model)
	currentGeneration := m.userModelGen
	current := client.UserModel{Entries: []client.UserModelEntry{{Key: "user/current", Description: "current"}}}
	mm, _ := m.updateUserModelMsg(client.UserModelMsg{Generation: currentGeneration, UserModel: current})
	m = mm.(Model)
	stale := client.UserModel{Entries: []client.UserModelEntry{{Key: "user/stale", Description: "stale"}}}
	mm, _ = m.updateUserModelMsg(client.UserModelMsg{Generation: 10, UserModel: stale})
	m = mm.(Model)
	if m.userModelGen != currentGeneration || len(m.userModel.model.Entries) != 1 || m.userModel.model.Entries[0].Key != "user/current" || m.userModel.loading {
		t.Fatalf("stale inventory replaced or cleared reopened panel: gen=%d state=%+v", m.userModelGen, m.userModel)
	}
}

func TestUserModelDetailDropsResponseAcrossCloseReopen(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	m.userModelGen = 3
	m.userModel = userModelState{view: userModelPanel, loading: true, requestKey: "user/a"}
	closed, _ := m.closeUserModel()
	m = closed.(Model)
	reopened, _ := m.openUserModel()
	m = reopened.(Model)
	currentGeneration := m.userModelGen
	stale := client.UserModelDetailMsg{Generation: 3, RequestKey: "user/a", UserModel: client.UserModel{Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "user/a", Value: "stale"}}}}
	mm, _ := m.updateUserModelMsg(stale)
	m = mm.(Model)
	if m.userModelGen != currentGeneration || m.userModel.detail != nil || m.userModel.view != userModelPanel {
		t.Fatalf("closed-generation detail replaced reopened panel: gen=%d state=%+v", m.userModelGen, m.userModel)
	}
}

func TestUserModelOverlayBoundsAndNavigatesOnSmallTerminal(t *testing.T) {
	entries := make([]client.UserModelEntry, 40)
	for i := range entries {
		entries[i] = client.UserModelEntry{Key: fmt.Sprintf("user/%02d", i), Description: "exact description"}
	}
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries}}, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	for range 25 {
		mm, _, _ = m.onUserModelKey(tea.KeyPressMsg{Code: tea.KeyDown})
		m = mm.(Model)
	}
	out := renderUserModelOverlay(m.deps.Theme, m.userModel, m.caps, m.helpKeyMarkings(), 80, 12)
	plain := stripANSIstr(out)
	if got := len(strings.Split(strings.TrimSuffix(plain, "\n"), "\n")); got > 12 {
		t.Fatalf("small-terminal overlay height = %d, want <= 12\n%s", got, plain)
	}
	if !strings.Contains(plain, "user/25") || !strings.Contains(plain, "esc close") {
		t.Fatalf("selection or close affordance not visible after navigation:\n%s", plain)
	}
	if m.userModel.cursor != 25 || len(m.userModel.model.Entries) != 40 {
		t.Fatalf("navigation changed model data: cursor=%d entries=%d", m.userModel.cursor, len(m.userModel.model.Entries))
	}
}

func TestUserModelDetailHistoryScrollKeepsExactData(t *testing.T) {
	history := make([]client.UserModelRevision, 30)
	for i := range history {
		history[i] = client.UserModelRevision{Version: fmt.Sprintf("v%02d", i), Status: "active"}
	}
	st := userModelState{view: userModelDetail, scroll: 25, detail: &client.UserModelDetail{
		Current: client.UserModelRevision{Key: "user/exact", Value: "full exact value"}, HistoryAvailable: true, History: history,
	}}
	out := stripANSIstr(renderUserModelOverlay(theme.New("aztec", theme.AztecPalette()), st, client.Capabilities{UserModel: true}, defaultHelpKeys(), 80, 12))
	if got := len(strings.Split(strings.TrimSuffix(out, "\n"), "\n")); got > 12 {
		t.Fatalf("detail overlay height = %d, want <= 12\n%s", got, out)
	}
	if !strings.Contains(out, "v") || !strings.Contains(out, "scroll") || !strings.Contains(out, "back") {
		t.Fatalf("history navigation affordances missing:\n%s", out)
	}
	if len(st.detail.History) != 30 || st.detail.Current.Value != "full exact value" {
		t.Fatal("rendering changed exact detail data")
	}
}

// --- goldens ---------------------------------------------------------------

// TestUserModelPanelGolden locks the populated inventory panel.
func TestUserModelPanelGolden(t *testing.T) {
	m := newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	if m.userModel.view != userModelPanel {
		t.Fatalf("view = %v, want userModelPanel", m.userModel.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "usermodel.golden", got)
}

// TestUserModelPanelEmptyDisabledGolden locks the "user model not enabled" empty
// state (caps.UserModel false).
func TestUserModelPanelEmptyDisabledGolden(t *testing.T) {
	m := newUserModelModel(t, &fakeUserModel{}, client.Capabilities{UserModel: false})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "usermodel_empty_disabled.golden", got)
}

// TestUserModelPanelEmptyEnabledGolden locks the "enabled but empty" empty state
// (caps.UserModel true, no entries).
func TestUserModelPanelEmptyEnabledGolden(t *testing.T) {
	m := newUserModelModel(t, &fakeUserModel{}, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "usermodel_empty_enabled.golden", got)
}
