package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func scenarioMentionMatches(n int) []string {
	matches := make([]string, n)
	for i := range matches {
		matches[i] = fmt.Sprintf("path-%02d.txt", i)
	}
	return matches
}

func scenarioMentionState(matches []string) mentionState {
	st := mentionState{open: true, matches: matches}
	st.syncList()
	return st
}

func TestMecatuiMentionBoundedList_Scenario1_GeometryAndIndicators(t *testing.T) {
	for _, width := range []int{12, 48, 160} {
		st := scenarioMentionState(scenarioMentionMatches(maxMentionCandidates))
		got := renderMentionSized(testTheme(), st, width, maxMentionRows)
		if got == "" {
			t.Fatalf("width %d suppressed usable mention geometry", width)
		}
		if gotWidth := ansi.StringWidth(strings.Split(got, "\n")[0]); gotWidth != min(128, width) {
			t.Fatalf("outer width = %d, want %d", gotWidth, min(128, width))
		}
		for _, line := range strings.Split(got, "\n") {
			if cells := ansi.StringWidth(line); cells > width {
				t.Fatalf("line width = %d, offered %d: %q", cells, width, ansi.Strip(line))
			}
		}
		view := st.list.ViewWithIndicators(maxMentionRows, false)
		if len(view.Rows) > maxMentionRows || view.Below == 0 || !strings.Contains(ansi.Strip(got), "↓") {
			t.Fatalf("body did not retain bounded rows and overflow: view=%+v\n%s", view, ansi.Strip(got))
		}
	}

	short := scenarioMentionState([]string{"a", "b"})
	got := renderMentionSized(testTheme(), short, 40, maxMentionRows)
	if rows := len(short.list.View().Rows); rows != 2 {
		t.Fatalf("small result body rows = %d, want 2", rows)
	}
	if got == "" {
		t.Fatal("small result set was suppressed")
	}
	if got := renderMentionSized(testTheme(), short, 40, 0); got != "" || short.list.Valid() {
		t.Fatalf("degenerate geometry rendered or retained key ownership: %q valid=%t", ansi.Strip(got), short.list.Valid())
	}
}

func TestMecatuiMentionBoundedList_Scenario1_BoundedDiscoveryAndReachability(t *testing.T) {
	if maxMentionWalk != 4000 {
		t.Fatalf("walk bound = %d, want 4000", maxMentionWalk)
	}
	root := t.TempDir()
	for i := range maxMentionCandidates + 12 {
		name := filepath.Join(root, fmt.Sprintf("visible-%03d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "regular-after-specials.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("visible-000.txt", filepath.Join(root, "symlink-before-regulars")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo-before-regulars"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	matches := matchMentionFilesWithHome(root, "", os.UserHomeDir)
	if len(matches) != maxMentionCandidates {
		t.Fatalf("candidate count = %d, want %d", len(matches), maxMentionCandidates)
	}
	if !slices.IsSorted(matches) {
		t.Fatalf("matches are not sorted: %v", matches)
	}
	for _, match := range matches {
		if strings.Contains(match, ".hidden") {
			t.Fatalf("hidden path admitted: %q", match)
		}
		if strings.Contains(match, "fifo-before-regulars") || strings.Contains(match, "symlink-before-regulars") {
			t.Fatalf("non-regular candidate consumed the cap: %q", match)
		}
	}
	if !slices.Contains(matches, "visible-062.txt") {
		t.Fatalf("regular candidate excluded after special files consumed the cap: %v", matches)
	}
	for i := range maxMentionWalk {
		name := filepath.Join(root, fmt.Sprintf("walk-%04d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "zz-walk-stop.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := matchFiles(root, "walk-stop", "", maxMentionCandidates); len(got) != 0 {
		t.Fatalf("walk exceeded %d entries: %v", maxMentionWalk, got)
	}
	st := scenarioMentionState(matches)
	st.list.SetGeometry(40, 4, 1, bounded.Clip)
	for i, want := range matches {
		st.list.SetCursor(i)
		if st.list.CursorID() != want {
			t.Fatalf("candidate %d unreachable: got %q, want %q", i, st.list.CursorID(), want)
		}
	}
}

func TestMecatuiMentionBoundedList_Scenario1_SelectionPresentationAndPaging(t *testing.T) {
	st := scenarioMentionState(scenarioMentionMatches(12))
	got := renderMentionSized(testTheme(), st, 32, 4)
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "▶ @path-00.txt") || !strings.Contains(plain, "  @path-01.txt") {
		t.Fatalf("mention rows lack standard equal-width gutters:\n%s", plain)
	}
	st.list.Move(bounded.LineDown)
	if st.list.Cursor() != 1 {
		t.Fatalf("Down selected %d, want 1", st.list.Cursor())
	}
	st.list.Move(bounded.PageDown)
	if st.list.Cursor() <= 1 {
		t.Fatalf("page down did not select first path after physical window: %d", st.list.Cursor())
	}
	paged := st.list.Cursor()
	st.list.Move(bounded.PageUp)
	if st.list.Cursor() >= paged {
		t.Fatalf("page up did not move symmetrically: %d -> %d", paged, st.list.Cursor())
	}
	m := newMentionModel(t, seedWorkspace(t))
	m.mention = scenarioMentionState(scenarioMentionMatches(12))
	_ = m.View()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(Model)
	if m.mention.list.Cursor() == 0 {
		t.Fatal("real pgdown key did not page the mention list")
	}
	before := m.mention.list.Cursor()
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := updated.(Model).mention.list.Cursor(); got <= before {
		t.Fatalf("real Down key did not route to mention selection: %d", got)
	}
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyHome}, {Code: tea.KeyEnd}} {
		if _, handled := m.onMentionKey(msg); handled {
			t.Fatalf("mention unexpectedly claimed %q", msg.String())
		}
	}
	cursor := m.mention.list.Cursor()
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 1})
	if got := updated.(Model).mention.list.Cursor(); got != cursor {
		t.Fatalf("mouse moved mention cursor: %d -> %d", cursor, got)
	}
}

func TestMecatuiMentionBoundedList_Scenario1_SanitizesFilesystemPaths(t *testing.T) {
	displayRaw := "hostile\tline\nnext\u2028split\u202e-name.txt"
	st := scenarioMentionState([]string{displayRaw})
	got := renderMentionSized(testTheme(), st, 80, 4)
	plain := ansi.Strip(got)
	if strings.ContainsAny(plain, "\t\u2028\u202e") || !strings.Contains(plain, "▶ @hostilelinenextsplit-name.txt") {
		t.Fatalf("hostile terminal control was not sanitized into one display line: %q", got)
	}
	if got := st.list.CursorID(); got != displayRaw {
		t.Fatalf("sanitization changed stable ID: got %q, want %q", got, displayRaw)
	}
	raw := "hostile\u202e-name.txt"
	m := newMentionModel(t, seedWorkspace(t))
	m.prompt.Rewrite("attach @hostile")
	m.mention = scenarioMentionState([]string{raw})
	m.mention.open = true
	m = m.mentionComplete()
	if got := m.prompt.Value(); got != "attach @"+raw+" " {
		t.Fatalf("completion used sanitized value %q, want raw %q", got, raw)
	}
}

func TestMecatuiMentionBoundedList_Scenario1_StableSelectionAndAnchorsAcrossRefresh(t *testing.T) {
	st := scenarioMentionState([]string{"a", "b", "c", "d", "e"})
	st.list.SetGeometry(20, 2, 1, bounded.Clip)
	st.list.SetCursor(3)
	st.list.Scroll(bounded.LineDown)
	selected, offset := st.list.CursorID(), st.list.Offset()
	st.matches = []string{"x", "a", "b", "c", "d", "e"}
	st.syncList()
	if st.list.CursorID() != selected || st.list.Offset() != offset+1 {
		t.Fatalf("refresh lost stable selection/anchor: id=%q offset=%d", st.list.CursorID(), st.list.Offset())
	}
	st.matches = []string{"a", "b", "c", "e"}
	st.syncList()
	replacement := st.list.CursorID()
	anchorAfterRemoval := st.list.View().Rows[0].ID
	st.matches = []string{"a", "b", "c", "d", "e"}
	st.syncList()
	if st.list.CursorID() != replacement {
		t.Fatalf("removed selection snapped back: got %q, replacement %q", st.list.CursorID(), replacement)
	}
	if got := st.list.View().Rows[0].ID; got != anchorAfterRemoval {
		t.Fatalf("removed top anchor snapped back: got %q, replacement %q", got, anchorAfterRemoval)
	}
	_ = renderMentionSized(testTheme(), st, 12, 2)
	if st.list.CursorID() != replacement {
		t.Fatalf("resize lost stable selection: %q", st.list.CursorID())
	}
}

func TestMecatuiMentionBoundedList_Scenario1_PhaseSpecificKeyOwnership(t *testing.T) {
	for _, phase := range []phase{phaseIdle, phaseRunning} {
		for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}} {
			m := newMentionModel(t, seedWorkspace(t))
			m.prompt.Rewrite("@")
			m = m.syncMention()
			m.phase = phase
			_ = m.View()
			updated, _ := m.Update(msg)
			if got := updated.(Model); !got.mentionVisible() || got.prompt.Value() != "@" {
				t.Fatalf("phase %d did not reserve %q", phase, msg.String())
			}
		}
		m := newMentionModel(t, seedWorkspace(t))
		m.prompt.Rewrite("@main")
		m = m.syncMention()
		m.phase = phase
		_ = m.View()
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		completed := updated.(Model)
		if completed.prompt.Value() != "@main.go " || completed.mention.open {
			t.Fatalf("phase %d Enter did not complete mention: prompt=%q", phase, completed.prompt.Value())
		}
		m.prompt.Rewrite("@main")
		m = m.syncMention()
		m.phase = phase
		_ = m.View()
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		dismissed := updated.(Model)
		if dismissed.mention.open || (phase == phaseRunning && dismissed.statusMsg == "cancelling…") {
			t.Fatalf("phase %d Escape did not dismiss mention", phase)
		}
	}

	m := newMentionModel(t, seedWorkspace(t))
	m.prompt.Rewrite("@")
	m = m.syncMention()
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 5})
	_ = m.View()
	if m.mentionVisible() {
		t.Fatal("precondition: short frame should suppress mention")
	}
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}, {Code: tea.KeyTab}, {Code: tea.KeyEnter}, {Code: tea.KeyEsc}} {
		if _, handled := m.onMentionKey(msg); handled {
			t.Fatalf("suppressed mention claimed %q", msg.String())
		}
	}
	m.phase = phaseRunning
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := updated.(Model).statusMsg; got != "cancelling…" {
		t.Fatalf("suppressed mention did not relinquish Escape to running cancellation: %q", got)
	}

	m = newMentionModel(t, seedWorkspace(t))
	m.prompt.Rewrite("@main")
	m = m.syncMention()
	m.phase = phaseRunning
	_ = m.View()
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	dismissed := updated.(Model)
	if dismissed.mention.open || dismissed.statusMsg == "cancelling…" {
		t.Fatal("visible running mention Escape did not dismiss before cancellation")
	}
	updated, _ = dismissed.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := updated.(Model).statusMsg; got != "cancelling…" {
		t.Fatalf("Escape did not cancel after mention dismissal: %q", got)
	}
}

func TestMecatuiMentionBoundedList_Scenario1_PreservesCompletionAndAttachmentContract(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(workspace, "local.txt"), filepath.Join(base, "parent.txt")} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ token, want string }{{"./loc", "./local.txt"}, {"../par", "../parent.txt"}} {
		m := newMentionModel(t, workspace)
		m.prompt.Rewrite("attach @" + tc.token)
		m = m.syncMention().mentionComplete()
		if got := m.prompt.Value(); got != "attach @"+tc.want+" " {
			t.Fatalf("completion = %q, want spelling %q", got, tc.want)
		}
	}
	if got := attachableMentions(workspace, "literal @missing and @somedir", os.UserHomeDir); len(got) != 0 {
		t.Fatalf("unresolved/non-regular mentions became attachments: %+v", got)
	}
	if got := attachableMentions(workspace, "attach @local.txt", os.UserHomeDir); len(got) != 1 || got[0].Label != "local.txt" {
		t.Fatalf("regular-file attachment contract changed: %+v", got)
	}
}
