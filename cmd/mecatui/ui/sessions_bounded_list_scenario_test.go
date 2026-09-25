package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func sessionsScenarioRows(n int) []client.SessionListItem {
	rows := make([]client.SessionListItem, n)
	for i := range rows {
		rows[i] = client.SessionListItem{
			ID: fmt.Sprintf("opaque-%02d", i), Title: fmt.Sprintf("session %02d with enough words to wrap", i),
			Kind: client.SessionKindMain, State: "completed", ModifiedAt: 1, Turns: int32(i),
			Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true, CopyID: true, Fork: true, Rename: true, Delete: true, ViewTranscript: true},
		}
	}
	return rows
}

func sessionsScenarioState(rows []client.SessionListItem) *sessionsState {
	st := newSessionsPanelState()
	st.deps.theme, st.deps.marks, st.deps.keys = testTheme(), defaultHelpKeys(), defaultKeys()
	st.loading, st.loadState, st.sessions = false, sessionsComplete, rows
	st.syncFilter()
	return &st
}

func sessionsScenarioKey(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(text[0]), Text: text}
}

func TestMecatuiSessionsBoundedList_Scenario1_GeometryPresentationAndIndicators(t *testing.T) {
	for _, tc := range []struct{ width, height int }{{0, 10}, {20, 0}, {2, 8}, {8, 3}, {24, 8}, {56, 20}} {
		st := sessionsScenarioState(sessionsScenarioRows(20))
		st.loadState = sessionsLoadingMore
		got, _ := st.Render(tc.width, tc.height)
		if tc.width <= 0 || tc.height <= 0 {
			if got != "" || st.list != nil {
				t.Fatalf("%dx%d rendered or built a list: %q list=%p", tc.width, tc.height, ansi.Strip(got), st.list)
			}
			continue
		}
		lines := strings.Split(got, "\n")
		if len(lines) > tc.height {
			t.Fatalf("%dx%d rendered %d lines:\n%s", tc.width, tc.height, len(lines), ansi.Strip(got))
		}
		for _, line := range lines {
			if width := ansi.StringWidth(line); width > tc.width {
				t.Fatalf("%dx%d rendered %d cells: %q", tc.width, tc.height, width, ansi.Strip(line))
			}
		}
		if tc.height < 6 || tc.width < 4 {
			if st.list != nil || len(lines) != 1 || tc.width >= 4 && !strings.Contains(ansi.Strip(got), "clo") {
				t.Fatalf("%dx%d did not use Close-only compact fallback: %q list=%p", tc.width, tc.height, ansi.Strip(got), st.list)
			}
			continue
		}
		view := st.list.ViewWithIndicators(st.rowBudget, false)
		if st.rowBudget > 12 || len(view.Rows)+(boolInt(view.Above > 0)+boolInt(view.Below > 0)) > 12 {
			t.Fatalf("body exceeded twelve physical rows: budget=%d view=%+v", st.rowBudget, view)
		}
		if view.Below == 0 || !strings.Contains(ansi.Strip(got), "↓") {
			t.Fatalf("missing bounded-list overflow indicator: %+v\n%s", view, ansi.Strip(got))
		}
		if !strings.Contains(ansi.Strip(got), "▶✓ ") {
			t.Fatalf("selected row lacks standard selection/state gutter:\n%s", ansi.Strip(got))
		}
	}

	gutter := sessionsScenarioState(sessionsScenarioRows(2))
	gutter.sessions[0].Title = strings.Repeat("wrapped ", 5)
	if _, _ = gutter.Render(40, 12); gutter.list == nil {
		t.Fatal("gutter scenario did not construct its list")
	}
	view := gutter.list.ViewWithIndicators(gutter.rowBudget, false)
	var selected, continuation, unselected *bounded.ListRow
	for i := range view.Rows {
		row := &view.Rows[i]
		switch {
		case row.Selected && row.CursorMarker:
			selected = row
		case row.Selected && row.ItemLine > 0:
			continuation = row
		case !row.Selected && row.ItemLine == 0:
			unselected = row
		}
	}
	if selected == nil || continuation == nil || unselected == nil {
		t.Fatalf("gutter scenario lacks selected/continuation/unselected rows: %+v", view)
	}
	selectedPresentation := presentListRow(*selected, gutter.deps.theme.Style("accent"), gutter.deps.theme.Style("muted"))
	continuationPresentation := presentListRow(*continuation, gutter.deps.theme.Style("accent"), gutter.deps.theme.Style("muted"))
	unselectedPresentation := presentListRow(*unselected, gutter.deps.theme.Style("accent"), gutter.deps.theme.Style("muted"))
	if !strings.HasPrefix(selectedPresentation.Text, "▶✓ ") || !strings.HasPrefix(continuationPresentation.Text, "   ") || !strings.HasPrefix(unselectedPresentation.Text, " ✓ ") {
		t.Fatalf("selection, continuation, or unselected gutter drifted: selected=%q continuation=%q unselected=%q", selectedPresentation.Text, continuationPresentation.Text, unselectedPresentation.Text)
	}
	if selectedPresentation.Style.Render("same row") == unselectedPresentation.Style.Render("same row") {
		t.Fatal("selected and unselected session rows use the same style")
	}

	st := sessionsScenarioState([]client.SessionListItem{{ID: "raw-id", Title: "unsafe\tname\nnext\u202e", Kind: client.SessionKindMain, State: "running"}})
	got, _ := st.Render(40, 8)
	if plain := ansi.Strip(got); strings.ContainsAny(plain, "\t\u202e") || !strings.Contains(plain, "unsafenamenext") || st.list.CursorID() != "raw-id" {
		t.Fatalf("terminal-safe fields or opaque ID changed: %q id=%q", plain, st.list.CursorID())
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestMecatuiSessionsBoundedList_Scenario1_NavigationPagingAndWheelOwnership(t *testing.T) {
	st := sessionsScenarioState(sessionsScenarioRows(10))
	st.sessions[0].Title = strings.Repeat("oversized ", 20)
	st.syncFilter()
	st.deps.keys.Up = key.NewBinding(key.WithKeys("u"))
	st.deps.keys.Down = key.NewBinding(key.WithKeys("j"))
	st.deps.keys.ScrollU = key.NewBinding(key.WithKeys("p"))
	st.deps.keys.ScrollD = key.NewBinding(key.WithKeys("n"))
	st.deps.keys.ScrollTop = key.NewBinding(key.WithKeys("t"))
	st.deps.keys.ScrollBottom = key.NewBinding(key.WithKeys("b"))
	_, _ = st.Render(24, 8)

	st.HandleKey(sessionsScenarioKey("j"))
	if st.list.Cursor() != 1 {
		t.Fatalf("rebound Down selected %d", st.list.Cursor())
	}
	st.HandleKey(sessionsScenarioKey("u"))
	if st.list.Cursor() != 0 {
		t.Fatalf("rebound Up selected %d", st.list.Cursor())
	}
	oversizedID := st.list.CursorID()
	seenLines := map[int]bool{}
	offsets := []int{}
	for st.list.CursorID() == oversizedID {
		view := st.list.ViewWithIndicators(st.rowBudget, false)
		offsets = append(offsets, st.list.Offset())
		for _, row := range view.Rows {
			if row.ID == oversizedID {
				seenLines[row.ItemLine] = true
			}
		}
		st.HandleKey(sessionsScenarioKey("n"))
	}
	if len(offsets) < 2 {
		t.Fatalf("oversized row did not expose multiple physical windows: offsets=%v", offsets)
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] <= offsets[i-1] {
			t.Fatalf("oversized row did not advance through successive physical offsets: %v", offsets)
		}
	}
	maxLine := 0
	for line := range seenLines {
		maxLine = max(maxLine, line)
	}
	for line := 0; line <= maxLine; line++ {
		if !seenLines[line] {
			t.Fatalf("oversized row skipped physical segment %d before cursor changed: seen=%v offsets=%v", line, seenLines, offsets)
		}
	}
	if st.list.CursorID() == oversizedID {
		t.Fatalf("oversized row never paged to its successor: offsets=%v", offsets)
	}
	st.HandleKey(sessionsScenarioKey("b"))
	if st.list.Cursor() != len(st.filtered)-1 {
		t.Fatalf("rebound bottom selected %d", st.list.Cursor())
	}
	st.HandleKey(sessionsScenarioKey("t"))
	if st.list.Cursor() != 0 {
		t.Fatalf("rebound top selected %d", st.list.Cursor())
	}
	st.HandleKey(sessionsScenarioKey("n"))
	st.HandleKey(sessionsScenarioKey("p"))
	if st.list.Cursor() != 0 || st.list.Offset() != 0 {
		t.Fatalf("rebound page up did not return oversized row to top: cursor=%d offset=%d", st.list.Cursor(), st.list.Offset())
	}

	cursor, offset := st.list.Cursor(), st.list.Offset()
	if _, handled := st.HandleWheel(tea.MouseWheelMsg{}); !handled || st.list.Cursor() != cursor || st.list.Offset() != offset {
		t.Fatalf("wheel ownership changed list: handled=%t cursor=%d offset=%d", handled, st.list.Cursor(), st.list.Offset())
	}
}

func TestMecatuiSessionsBoundedList_Scenario1_StableIdentityAcrossSurfaceRefreshes(t *testing.T) {
	rows := sessionsScenarioRows(8)
	for i := range rows {
		rows[i].Title = fmt.Sprintf("keep row %d", i)
	}
	st := sessionsScenarioState(rows)
	_, _ = st.Render(24, 8)
	st.list.SetCursor(4)
	st.list.Scroll(bounded.LineDown)
	selected := st.list.CursorID()
	top := st.list.View().Rows[0]

	st.filter.SetValue("keep")
	st.syncFilter()
	if cmd := st.applyPage(client.SessionInventoryPageMsg{Cursor: "page-2", Page: client.SessionInventoryPage{
		Sessions: []client.SessionListItem{{ID: "page-append", Title: "keep appended", Kind: client.SessionKindMain}}, NextCursor: "page-3",
	}}); cmd != nil {
		t.Fatal("unwired page append unexpectedly scheduled a command")
	}
	if st.list.CursorID() != selected {
		t.Fatalf("real page append lost selected ID: %q", st.list.CursorID())
	}
	gotTop := st.list.View().Rows[0]
	if gotTop.ID != top.ID || gotTop.ItemLine != top.ItemLine {
		t.Fatalf("page append lost top semantic anchor: got=%+v want=%+v", gotTop, top)
	}

	replacementPage := append([]client.SessionListItem(nil), st.sessions...)
	st.loadState = sessionsInitialLoading
	if cmd := st.applyPage(client.SessionInventoryPageMsg{Page: client.SessionInventoryPage{Sessions: replacementPage}}); cmd != nil {
		t.Fatal("unwired page replacement unexpectedly scheduled a command")
	}
	if st.list.CursorID() != selected {
		t.Fatalf("real page replacement lost selected ID: %q", st.list.CursorID())
	}
	gotTop = st.list.View().Rows[0]
	if gotTop.ID != top.ID || gotTop.ItemLine != top.ItemLine {
		t.Fatalf("page replacement lost top semantic anchor: got=%+v want=%+v", gotTop, top)
	}

	for i := range st.sessions {
		if st.sessions[i].ID == selected {
			st.sessions[i].Title = "keep renamed"
		}
	}
	st.syncFilter()
	resizeTop := st.list.View().Rows[0]
	_, _ = st.Render(18, 8)
	if st.list.CursorID() != selected {
		t.Fatalf("rename/resize lost selected ID: %q", st.list.CursorID())
	}
	resizedTop := st.list.View().Rows[0]
	if resizedTop.ID != resizeTop.ID || resizedTop.ItemLine != resizeTop.ItemLine {
		t.Fatalf("resize lost top semantic anchor: got=%+v want=%+v", resizedTop, resizeTop)
	}

	removedIndex := st.list.Cursor()
	st.sessions = append(st.sessions[:removedIndex], st.sessions[removedIndex+1:]...)
	st.syncFilter()
	replacement := st.list.CursorID()
	st.sessions = append(st.sessions, client.SessionListItem{ID: selected, Title: "keep returned", Kind: client.SessionKindMain})
	st.syncFilter()
	if replacement == "" || st.list.CursorID() != replacement {
		t.Fatalf("removed selection resurrected: replacement=%q selected=%q", replacement, st.list.CursorID())
	}

	st.sessions = append(st.sessions, client.SessionListItem{ID: "scheduled", Title: "keep schedule", Kind: client.SessionKindScheduled})
	st.nextTab()
	st.syncFilter()
	if st.list.Cursor() != 0 || st.list.CursorID() != "scheduled" {
		t.Fatalf("tab did not reset to first row: cursor=%d id=%q", st.list.Cursor(), st.list.CursorID())
	}
}

func TestMecatuiSessionsBoundedList_Scenario1_PreservesActionsAndNonInventoryStates(t *testing.T) {
	for _, action := range []string{"y", "v", "f", "r", "d", "enter"} {
		st := sessionsScenarioState(sessionsScenarioRows(2))
		_, _ = st.Render(60, 10)
		st.list.SetCursor(1)
		var msg tea.KeyPressMsg
		if action == "enter" {
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		} else {
			msg = sessionsScenarioKey(action)
		}
		cmd, handled, _ := st.HandleKey(msg)
		if !handled {
			t.Fatalf("action %q was not handled", action)
		}
		switch action {
		case "y":
			if got := cmd().(inventorySessionIDCopiedMsg).id; got != "opaque-01" {
				t.Fatalf("copy targeted %q", got)
			}
		case "r", "d", "f":
			if st.actionID != "opaque-01" {
				t.Fatalf("%s targeted %q", action, st.actionID)
			}
		case "v", "enter":
			if st.selected.ID != "opaque-01" || cmd == nil {
				t.Fatalf("%s targeted %q cmd=%v", action, st.selected.ID, cmd != nil)
			}
		}
	}

	for _, setup := range []func(*sessionsState){
		func(st *sessionsState) { st.loading, st.loadState = true, sessionsInitialLoading },
		func(st *sessionsState) { st.sessions = nil; st.syncFilter() },
		func(st *sessionsState) {
			st.sessions = nil
			st.syncFilter()
			st.err, st.loadState = errors.New("initial page failed"), sessionsInitialPageError
		},
		func(st *sessionsState) { st.tab = tabStorageHealth },
	} {
		st := sessionsScenarioState(sessionsScenarioRows(2))
		setup(st)
		_, _ = st.Render(60, 10)
		if st.list != nil {
			t.Fatalf("non-inventory state constructed list: state=%+v", st)
		}
		st.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
		st.HandleKey(sessionsScenarioKey("d"))
		if st.list != nil || st.confirmDelete || st.actionID != "" {
			t.Fatal("non-inventory state routed list navigation or an inventory action")
		}
	}

	compact := sessionsScenarioState(sessionsScenarioRows(2))
	got, _ := compact.Render(12, 3)
	if compact.list != nil || !strings.Contains(ansi.Strip(got), "close") {
		t.Fatalf("compact state=%q list=%p", ansi.Strip(got), compact.list)
	}
	if cmd, _, _ := compact.HandleKey(sessionsScenarioKey("d")); cmd != nil || compact.confirmDelete || compact.actionID != "" {
		t.Fatal("compact fallback routed a normal action")
	}
	if _, _, closed := compact.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !closed {
		t.Fatal("compact fallback did not route Close")
	}
}
