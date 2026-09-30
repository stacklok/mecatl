package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// newUserModelModel builds an idle, sized model with a scripted saved-memory client.
func newUserModelModel(t *testing.T, um client.UserModelLister, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, UserModel: um,
		Theme: theme.New("aztec", theme.AztecPalette()), Server: "127.0.0.1:8080",
		Workspace: "/workspace", Mode: "default", Model: "mock-model",
		Ctx: context.Background(), NoAltScreen: true,
	})
	return applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps})
}

func sampleUserModel() *fakeUserModel {
	return &fakeUserModel{model: client.UserModel{
		Entries:   []client.UserModelEntry{{Key: "name", Description: "the operator's name"}, {Key: "stack", Description: "prefers Go + hexagonal architecture"}},
		SizeBytes: 64, SHA256: "abc123def4567890",
	}}
}

func openedUserModel(t *testing.T, m Model) (Model, *userModelState) {
	t.Helper()
	mm, cmd := m.runUserModel()
	if cmd == nil {
		t.Fatal("expected live index request")
	}
	m = feedCmd(t, mm.(Model), cmd)
	s, ok := m.modal.(*userModelState)
	if !ok {
		t.Fatalf("expected saved-memory surface, got %T", m.modal)
	}
	_ = m.View()
	return m, s
}

func TestRunUserModelOpensPanel(t *testing.T) {
	fum := sampleUserModel()
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})
	mm, cmd := m.runUserModel()
	m = mm.(Model)
	s, ok := m.modal.(*userModelState)
	if !ok || s.view != userModelPanel || !s.loading || m.prompt.Focused() || cmd == nil {
		t.Fatalf("open state=%T cmd=%v focus=%v", m.modal, cmd, m.prompt.Focused())
	}
	m = feedCmd(t, m, cmd)
	if fum.calls != 1 || !strings.Contains(m.View().Content, "operator's name") {
		t.Fatalf("index not fetched or rendered: %d %q", fum.calls, m.View().Content)
	}
}

func TestUserModelKeySwallowsNonEsc(t *testing.T) {
	m, _ := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	_, _, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: 'j'})
	if !handled {
		t.Fatal("non-navigation key escaped modal")
	}
}

func TestUserModelError(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, &fakeUserModel{err: errors.New("boom")}, client.Capabilities{UserModel: true}))
	if s.err == nil || !strings.Contains(m.View().Content, "boom") {
		t.Fatal("index error not shown")
	}
}

func TestUserModelDetailHistoryUnavailableAndSanitized(t *testing.T) {
	fum := sampleUserModel()
	fum.model.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "name", Value: "Alice 日本語\x1b[2J\u0085\u2066\u200b\ufeff" + string([]byte{0xff}), Status: "active\rforged"}}
	m, s := openedUserModel(t, newUserModelModel(t, fum, client.Capabilities{UserModel: true}))
	mm, cmd, handled := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("enter did not fetch detail")
	}
	m = feedCmd(t, mm.(Model), cmd)
	view := stripANSIstr(m.View().Content)
	if s.view != userModelDetail || !strings.Contains(view, "Earlier versions are unavailable") {
		t.Fatalf("detail missing: %s", view)
	}
	if strings.ContainsAny(view, "\x1b\a\u0085\u202e\u2066\u200b\ufeff") || !strings.Contains(view, "Alice 日本語") || !strings.Contains(view, "�") {
		t.Fatalf("unsafe or lost content: %q", view)
	}
	if !strings.Contains(view, "ask the agent to remove or restore this saved fact") || strings.Contains(view, "ForgetUserMemory") {
		t.Fatalf("incorrect edit affordance: %q", view)
	}
}

func TestUserModelDetailDropsDelayedSelectionResponse(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	mm, cmd, _ := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	good := cmd().(client.UserModelDetailMsg)
	m = applyAll(m, good)
	m = applyAll(m, client.UserModelDetailMsg{Generation: good.Generation - 1, RequestKey: "stack", UserModel: client.UserModel{Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "stack", Value: "wrong"}}}})
	if m.modal != s || s.view != userModelDetail || s.detail.Current.Key != "name" {
		t.Fatalf("late detail replaced current: %+v", s.detail)
	}
}

func TestUserModelInventoryDropsResponseAcrossCloseReopen(t *testing.T) {
	m, s := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	old := s.generation
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil {
		t.Fatal("close retained surface")
	}
	m, s = openedUserModel(t, m)
	m = applyAll(m, client.UserModelMsg{Generation: old, UserModel: client.UserModel{Entries: []client.UserModelEntry{{Key: "stale"}}}})
	if s.list.CursorID() != "name" || s.generation <= old {
		t.Fatal("stale index replaced current")
	}
}

func TestUserModelDetailDropsResponseAcrossCloseReopen(t *testing.T) {
	m, _ := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	mm, cmd, _ := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	old := cmd().(client.UserModelDetailMsg)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, s := openedUserModel(t, m)
	m = applyAll(m, old)
	if m.modal != s || s.view != userModelPanel || s.detail != nil || s.loading {
		t.Fatalf("old detail changed reopened panel: %+v", s)
	}
}

func TestUserModelOverlayBoundsAndNavigatesOnSmallTerminal(t *testing.T) {
	entries := make([]client.UserModelEntry, 40)
	for i := range entries {
		entries[i] = client.UserModelEntry{Key: fmt.Sprintf("user/%02d", i), Description: "exact description"}
	}
	m, s := openedUserModel(t, newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries}}, client.Capabilities{UserModel: true}))
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 22})
	_ = m.View()
	for range 25 {
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
		_ = m.View()
	}
	out, _ := s.Render(70, 12)
	plain := stripANSIstr(out)
	if len(strings.Split(plain, "\n")) > 12 || !strings.Contains(plain, "user/25") || s.list.CursorID() != "user/25" || len(s.model.Entries) != 40 {
		t.Fatalf("bounded navigation lost fact: %q", plain)
	}
}

func TestUserModelDetailHistoryScrollKeepsExactData(t *testing.T) {
	history := make([]client.UserModelRevision, 30)
	for i := range history {
		history[i] = client.UserModelRevision{Version: fmt.Sprintf("v%02d", i), Status: "active"}
	}
	s := &userModelState{view: userModelDetail, viewport: &bounded.Viewport{}, deps: surfaceDeps{theme: theme.New("aztec", theme.AztecPalette()), marks: defaultHelpKeys()}, detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "user/exact", Value: "full exact value"}, HistoryAvailable: true, History: history}}
	s.Render(70, 12)
	s.viewport.Move(bounded.End, len(s.detailLines))
	out, _ := s.Render(70, 12)
	plain := stripANSIstr(out)
	if len(strings.Split(plain, "\n")) > 12 || !strings.Contains(plain, "v") || !strings.Contains(plain, "back") || s.detail.Current.Value != "full exact value" {
		t.Fatalf("history not reachable: %q", plain)
	}
}

func TestUserModelPanelGolden(t *testing.T) {
	m, _ := openedUserModel(t, newUserModelModel(t, sampleUserModel(), client.Capabilities{UserModel: true}))
	compareGolden(t, "usermodel.golden", stripANSI([]byte(m.View().Content)))
}
func TestUserModelPanelEmptyDisabledGolden(t *testing.T) {
	m := newUserModelModel(t, &fakeUserModel{}, client.Capabilities{})
	s := &userModelState{deps: (&m).surfaceDeps(), list: &bounded.List{}, viewport: &bounded.Viewport{}}
	m.modal = s
	compareGolden(t, "usermodel_empty_disabled.golden", stripANSI([]byte(m.View().Content)))
}
func TestUserModelPanelEmptyEnabledGolden(t *testing.T) {
	m, _ := openedUserModel(t, newUserModelModel(t, &fakeUserModel{}, client.Capabilities{UserModel: true}))
	compareGolden(t, "usermodel_empty_enabled.golden", stripANSI([]byte(m.View().Content)))
}

func TestUserModelDescriptionRailWrapsAndResizesWithoutChangingKeyRows(t *testing.T) {
	entries := make([]client.UserModelEntry, 8)
	for i := range entries {
		entries[i] = client.UserModelEntry{
			Key:         fmt.Sprintf("fact/%d-%s", i, strings.Repeat("key ", 8)),
			Description: strings.Repeat("a wrapped saved-memory description ", 4),
		}
	}
	_, s := savedMemoryOpened(t, entries)
	s.Render(20, 14)
	rows := s.list.View().Rows
	railAt := -1
	for i, row := range rows {
		if row.ID == entries[0].Key && strings.HasPrefix(row.Text, userModelDescriptionMarker+userModelDescriptionRail) {
			railAt = i
			break
		}
	}
	if railAt < 2 {
		t.Fatalf("narrow list did not wrap the key before its description: %#v", rows)
	}
	for _, row := range rows[:railAt] {
		if strings.HasPrefix(row.Text, userModelDescriptionMarker+userModelDescriptionRail) {
			t.Fatalf("key continuation received a description rail: %#v", row)
		}
	}
	for _, width := range []int{20, 70, 20} {
		s.Render(width, 14)
		for _, row := range s.list.View().Rows {
			if ansi.StringWidth(s.renderListRow(row)) > width {
				t.Fatalf("width=%d row overflows: %q", width, row.Text)
			}
		}
	}

	s.list.SetCursor(5)
	s.Render(20, 14)
	s.list.Scroll(bounded.LineDown)
	top := s.list.View().Rows[0]
	selected := s.list.CursorID()
	for _, width := range []int{70, 20} {
		s.Render(width, 14)
		got := s.list.View().Rows[0]
		if s.list.CursorID() != selected || got.ID != top.ID {
			t.Fatalf("width=%d lost selection or top anchor: selected=%q top=%#v want=%#v", width, s.list.CursorID(), got, top)
		}
	}
}

func TestUserModelDescriptionRailRepairsInvalidUTF8(t *testing.T) {
	_, s := savedMemoryOpened(t, []client.UserModelEntry{{Key: "fact/\x9b2J", Description: "\x9dtitle \xc3"}})
	out, _ := s.Render(32, 14)
	if !utf8.ValidString(out) || strings.Contains(out, "\x9b") || strings.Contains(out, "\x9d") || strings.Contains(out, "\xc3") || strings.Contains(out, userModelDescriptionMarker) {
		t.Fatalf("unsafe bytes or hidden marker in rendered memory: %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "│ �title �") {
		t.Fatalf("invalid description bytes did not become visible replacement runes: %q", out)
	}
}

func TestUserModelDescriptionRailWideGraphemesFitOrCompact(t *testing.T) {
	entries := []client.UserModelEntry{
		{Key: "fact/界", Description: "界🙂"},
		{Key: "fact/emoji", Description: "🙂界"},
	}
	_, s := savedMemoryOpened(t, entries)

	out, _ := s.Render(5, 80)
	if !s.compact || strings.Contains(out, userModelDescriptionMarker) {
		t.Fatalf("width 5 must use compact output without the rail sentinel: compact=%t output=%q", s.compact, out)
	}
	if s.list.CursorID() != entries[0].Key {
		t.Fatalf("compact fallback changed selected key: %q", s.list.CursorID())
	}

	out, _ = s.Render(6, 80)
	if s.compact || strings.Contains(out, userModelDescriptionMarker) {
		t.Fatalf("width 6 must render rails without the sentinel: compact=%t output=%q", s.compact, out)
	}
	rows := s.list.View().Rows
	haveRail := false
	for _, row := range rows {
		if strings.HasPrefix(row.Text, userModelDescriptionMarker+userModelDescriptionRail) {
			haveRail = true
		}
		if got := ansi.StringWidth(s.renderListRow(row)); got > 6 {
			t.Fatalf("width 6 row overflows (%d): %#v", got, row)
		}
	}
	if !haveRail || s.list.CursorID() != entries[0].Key {
		t.Fatalf("width 6 lost rail or selected key: rail=%t selected=%q rows=%#v", haveRail, s.list.CursorID(), rows)
	}
}
