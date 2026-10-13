package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func worktreeInventory(n int) []client.Worktree {
	rows := make([]client.Worktree, n)
	for i := range rows {
		rows[i] = client.Worktree{Selector: testWorktreeSelector(fmt.Sprintf("opaque-%d", i)), Label: fmt.Sprintf("worktree-%02d", i), Branch: "topic"}
	}
	return rows
}

func selectedWorktree(t *testing.T, s *worktreesState) client.Worktree {
	t.Helper()
	cursor := s.list.Cursor()
	if cursor < 0 || cursor >= len(s.filtered) {
		t.Fatalf("cursor %d outside %d filtered worktrees", cursor, len(s.filtered))
	}
	return s.filtered[cursor]
}

func openedWorktrees(t *testing.T, rows []client.Worktree) (Model, *worktreesState) {
	t.Helper()
	m := newWorktreesModel(t, &fakeConv{}, &fakeWorktreeLister{wts: rows})
	mm, cmd := m.openWorktrees()
	m = feedWorktreesList(t, mm.(Model), cmd)
	s := m.modal.(*worktreesState)
	_ = m.View()
	return m, s
}

func worktreesBodyFits(t *testing.T, s *worktreesState, width, height int) string {
	t.Helper()
	body, hits := s.Render(width, height)
	if width <= 0 || height <= 0 {
		if body != "" || s.list.Valid() {
			t.Fatal("zero offer retained body or usable list")
		}
		return body
	}
	if lipgloss.Width(body) > width || lipgloss.Height(body) > height || !utf8.ValidString(body) || len(hits) != 0 {
		t.Fatalf("body does not fit %dx%d: %dx%d\n%s", width, height, lipgloss.Width(body), lipgloss.Height(body), body)
	}
	return stripANSIstr(body)
}

func TestWorktreesSurface_Scenario1_ManyRowsAndShortOffers(t *testing.T) {
	rows := worktreeInventory(80)
	for _, width := range []int{1, 2, 3, 12, 32, 80, 124} {
		for _, height := range []int{1, 2, 4, 5, 6, 9, 20} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				_, s := openedWorktrees(t, rows)
				out := worktreesBodyFits(t, s, width, height)
				if s.compact {
					cursor := s.list.CursorID()
					for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyDown}, {Code: 'j', Text: "j"}} {
						s.HandleKey(msg)
					}
					if s.confirm != nil || s.filter.Value() != "" || s.list.CursorID() != cursor {
						t.Fatal("compact fallback accepted typing/navigation/activation")
					}
					if _, handled, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !handled || !closed {
						t.Fatal("compact fallback cannot close")
					}
					return
				}
				if !strings.Contains(out, "more") || strings.Contains(out, rows[79].Label) {
					t.Fatalf("unbounded inventory or missing overflow:\n%s", out)
				}
				for _, navigation := range []struct {
					msg  tea.KeyPressMsg
					want client.WorktreeSelector
				}{
					{tea.KeyPressMsg{Code: tea.KeyEnd}, rows[79].Selector},
					{tea.KeyPressMsg{Code: tea.KeyUp}, rows[78].Selector},
					{tea.KeyPressMsg{Code: tea.KeyHome}, rows[0].Selector},
					{tea.KeyPressMsg{Code: tea.KeyDown}, rows[1].Selector},
				} {
					s.HandleKey(navigation.msg)
					out = worktreesBodyFits(t, s, width, height)
					if got := selectedWorktree(t, s).Selector; got != navigation.want {
						t.Fatalf("%s selected selector %q, want %q", navigation.msg.String(), got, navigation.want)
					}
					if !strings.Contains(out, "▶") {
						t.Fatalf("navigation did not reveal selection:\n%s", out)
					}
				}
			})
		}
	}
}

func TestWorktreesSurface_Scenario2_WrappingAndStableCard(t *testing.T) {
	rows := worktreeInventory(30)
	rows[4].Label = strings.Repeat("wide label 界 ❤️ ", 12)
	m, s := openedWorktrees(t, rows)
	m = effortOffer(m, 32, 16)
	_ = m.renderBody()
	bounds := m.metrics.outerBounds
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyEnd}, tea.KeyPressMsg{Code: tea.KeyHome},
		tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown},
		tea.KeyPressMsg{Code: tea.KeyDown},
	} {
		m = applyAll(m, msg)
		body := m.renderBody()
		got := m.metrics.outerBounds
		if got.x1-got.x0 != bounds.x1-bounds.x0 || got.y1-got.y0 != bounds.y1-bounds.y0 || lipgloss.Height(body) > m.vp.Height() || lipgloss.Width(body) > m.width {
			t.Fatalf("card geometry changed during %T: %+v -> %+v", msg, bounds, got)
		}
	}
	for range len(rows) {
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
		body := m.renderBody()
		got := m.metrics.outerBounds
		if got.x1-got.x0 != bounds.x1-bounds.x0 || got.y1-got.y0 != bounds.y1-bounds.y0 || lipgloss.Height(body) > m.vp.Height() {
			t.Fatalf("wrapped-row navigation changed fixed card geometry: %+v -> %+v", bounds, got)
		}
	}
	s.list.SetCursor(4)
	out := worktreesBodyFits(t, s, 32, 30)
	view := s.list.View()
	wrapped, markers := 0, 0
	for _, row := range view.Rows {
		if row.Selected {
			wrapped++
			if row.CursorMarker {
				markers++
			}
		}
	}
	if wrapped < 2 || markers != 1 || strings.Count(out, "▶") != 1 {
		t.Fatalf("wrapped selection presentation: rows=%d markers=%d\n%s", wrapped, markers, out)
	}
}

func TestWorktreesSurface_Scenario3_FilterReorderAndRefreshIdentity(t *testing.T) {
	rows := worktreeInventory(30)
	_, s := openedWorktrees(t, rows)
	worktreesBodyFits(t, s, 80, 10)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	selected := s.list.CursorID()
	for range 8 {
		s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	worktreesBodyFits(t, s, 80, 10)
	anchor := s.list.View().Rows[0].ID
	refresh := s.listCmd()
	s.lister.(*fakeWorktreeLister).wts = append([]client.Worktree{rows[29]}, rows[:29]...)
	s.HandleMsg(refresh())
	worktreesBodyFits(t, s, 80, 10)
	if s.list.CursorID() != selected || s.list.View().Rows[0].ID != anchor || selectedWorktree(t, s).Selector != rows[1].Selector {
		t.Fatal("refresh/reorder lost the selected worktree or window identity")
	}
	s.filter.SetValue("worktree")
	s.syncFilter()
	worktreesBodyFits(t, s, 80, 10)
	if s.list.CursorID() != selected || s.list.View().Rows[0].ID != anchor || selectedWorktree(t, s).Selector != rows[1].Selector {
		t.Fatal("compatible filter lost the selected worktree or window identity")
	}
	s.filter.SetValue(rows[1].Label)
	s.syncFilter()
	if s.list.CursorID() != selected || len(s.filtered) != 1 || selectedWorktree(t, s).Selector != rows[1].Selector {
		t.Fatal("filter selected by filtered index instead of the selected worktree identity")
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.confirm == nil || s.confirm.Selector != rows[1].Selector {
		t.Fatalf("filtered confirmation selector = %+v, want %q", s.confirm, rows[1].Selector)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.filter.Value() != "" || s.list.CursorID() != selected {
		t.Fatal("clear filter moved the selected worktree")
	}
	worktreesBodyFits(t, s, 80, 10)
	for _, text := range []string{"j", "k", "g", "G"} {
		s.HandleKey(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	}
	if s.filter.Value() != "jkgG" {
		t.Fatal("printable navigation aliases did not reach filter first")
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if _, _, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !closed {
		t.Fatal("second Escape did not close")
	}
}

func TestWorktreesSurface_Scenario4_WheelResizeAndLiveConversation(t *testing.T) {
	rows := worktreeInventory(50)
	m, s := openedWorktrees(t, rows)
	m.conv.addUser("source")
	m.conv.startAssistant()
	m.conv.appendAssistant(strings.Repeat("underlying conversation\n\n", 100))
	m.refreshView()
	m = effortOffer(m, 80, 10)
	m.vp.SetYOffset(9)
	m.conversationView.observe(m.vp)
	_ = m.View()
	before, selected := m.vp.YOffset(), s.list.CursorID()
	for range 8 {
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	out := stripANSIstr(m.renderBody())
	offset := s.list.Offset()
	if offset == 0 || strings.Contains(out, "▶") || s.list.CursorID() != selected || m.vp.YOffset() != before {
		t.Fatal("wheel did not independently scroll list, or leaked into selection/conversation")
	}
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "live update while picker open"})
	_ = m.renderBody()
	if m.modal != s || s.list.Offset() != offset || s.list.CursorID() != selected || m.vp.YOffset() != before || !strings.Contains(m.conv.scrollback.SnapshotAt(1).Payload.(scrollback.AssistantCardSnapshot).Text, "live update") {
		t.Fatal("live event interrupted modal or did not update conversation")
	}
	m = effortOffer(m, 70, 9)
	out = stripANSIstr(m.renderBody())
	if !strings.Contains(out, "▶") || s.list.CursorID() != selected {
		t.Fatal("resize did not reveal the selected worktree")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.confirm == nil || s.confirm.Selector != rows[0].Selector {
		t.Fatalf("wheel/resize confirmation selector = %+v, want %q", s.confirm, rows[0].Selector)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	for _, geometry := range [][2]int{{0, 10}, {80, 0}, {1, 1}} {
		m = applyAll(m, tea.WindowSizeMsg{Width: geometry[0], Height: geometry[1]})
		before := m.vp.YOffset()
		m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.modal != s || s.confirm != nil || m.vp.YOffset() != before || !s.compact {
			t.Fatal("compact/zero geometry leaked input or activated stale rows")
		}
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("compact close did not restore prompt focus")
	}
}

func TestWorktreesSurface_Scenario5_LoadingErrorEmptyAndUnsafeMetadata(t *testing.T) {
	m := newWorktreesModel(t, &fakeConv{}, &fakeWorktreeLister{})
	mm, _ := m.openWorktrees()
	s := mm.(Model).modal.(*worktreesState)
	if out := worktreesBodyFits(t, s, 80, 20); !strings.Contains(out, "loading") || strings.Contains(out, ": select") {
		t.Fatalf("loading state advertised selection:\n%s", out)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.confirm != nil {
		t.Fatal("loading state activated")
	}
	for _, result := range []client.WorktreesMsg{{Err: errors.New("offline\x1b\u202e")}, {}} {
		s.loading = true
		s.HandleMsg(worktreesResultMsg{owner: s, request: s.request, result: result})
		out := worktreesBodyFits(t, s, 32, 20)
		if result.Err != nil && !strings.Contains(out, "could not list") || result.Err == nil && !strings.Contains(out, "no worktrees found") || strings.Contains(out, ": select") || strings.Contains(out, "\u202e") {
			t.Fatalf("incorrect error/empty state:\n%s", out)
		}
	}
	rows := worktreeInventory(2)
	rows[0].Label = "safe\x1b\x07\u202ename\n\tlabel"
	rows[0].Branch = "topic\u202e\r\n\t"
	rows[1].Branch = ""
	rows[1].Revision = "界界界界界界界界界"
	_, s = openedWorktrees(t, rows)
	out := worktreesBodyFits(t, s, 80, 20)
	if !strings.Contains(out, "safenamelabel  (topic)") || strings.Contains(out, "\u202e") || strings.Contains(out, "opaque-") || !strings.Contains(out, "界界界界界界界界") {
		t.Fatalf("unsafe or damaged metadata:\n%s", out)
	}
	s.filter.SetValue("missing")
	s.syncFilter()
	if out := worktreesBodyFits(t, s, 32, 20); !strings.Contains(out, "no matches") {
		t.Fatalf("filtered-empty state missing:\n%s", out)
	}
}

func TestWorktreesSurface_Scenario6_StaleResultsAndCapturedDependencies(t *testing.T) {
	m, old := openedWorktrees(t, worktreeInventory(2))
	late := old.listCmd()()
	requestCtx := old.lister.(*fakeWorktreeLister).ctx
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if requestCtx.Err() == nil {
		t.Fatal("close did not cancel discovery")
	}
	m = applyAll(m, late)
	if m.modal != nil {
		t.Fatal("late result reopened closed surface")
	}
	mm, _ := m.openWorktrees()
	m = mm.(Model)
	current := m.modal.(*worktreesState)
	m = applyAll(m, late)
	if !current.loading || len(current.worktrees) != 0 {
		t.Fatal("reopened picker accepted previous lifetime's response")
	}
	first := current.listCmd()()
	secondCmd := current.listCmd()
	m = applyAll(m, first)
	if !current.loading {
		t.Fatal("picker accepted previous request's response")
	}
	m.deps.Worktrees = &fakeWorktreeLister{err: errors.New("wrong dependency")}
	m.sessionID = "other-session"
	m = applyAll(m, secondCmd())
	if current.err != nil || current.sourceID != "source-session" || current.loading {
		t.Fatal("open picker did not capture its lister/source dependency")
	}
	before := slices.Clone(current.worktrees)
	m = applyAll(m, first, client.WorktreesMsg{Worktrees: worktreeInventory(9)})
	if !slices.Equal(current.worktrees, before) {
		t.Fatal("uncorrelated result mutated surface inventory")
	}
}

func TestWorktreesSurface_Scenario7_ConfirmationAndExplicitRootHandoff(t *testing.T) {
	rows := worktreeInventory(3)
	m, s := openedWorktrees(t, rows)
	conv := m.deps.Session.(*fakeConv)
	m.conv.addUser("keep source until success")
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.confirm == nil || s.confirm.Selector != rows[1].Selector || m.phase != phaseIdle || conv.createCount != 0 {
		t.Fatal("selection performed root effects instead of confirmation")
	}
	s.filter.SetValue("worktree")
	s.syncFilter()
	selected, offset := s.list.CursorID(), s.list.Offset()
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.confirm != nil || s.filter.Value() != "worktree" || s.list.CursorID() != selected || s.list.Offset() != offset {
		t.Fatal("confirmation Escape lost picker state")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal != nil || m.phase != phaseConnecting || m.sessionID != "source-session" || m.conv.isEmpty() || len(conv.closed()) != 0 || cmd == nil {
		t.Fatal("explicit confirmation failed create-first root handoff")
	}
	m = feedCmd(t, m, cmd)
	if calls := conv.clearCalls(); len(calls) != 1 || calls[0].sourceID != "source-session" || calls[0].selector == nil || *calls[0].selector != rows[1].Selector {
		t.Fatalf("successor authority = %+v, want source-session and %q", calls, rows[1].Selector)
	}
	if m.sessionID == "source-session" || !m.conv.isEmpty() {
		t.Fatal("server-confirmed successor was not adopted")
	}
	m, _ = openedWorktrees(t, rows)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.sessionID = "changed-source"
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal != nil || m.phase != phaseIdle || m.sessionID != "changed-source" || !strings.Contains(m.statusMsg, "stale") {
		t.Fatal("root accepted a handoff for a different source binding")
	}
}

func TestWorktreesSurface_Scenario8_ConfirmationFitsAndResizeInvalidatesActivation(t *testing.T) {
	rows := worktreeInventory(2)
	rows[0].Label = strings.Repeat("界🧪 label\x1b\a\u202e ", 12)
	rows[0].Branch = strings.Repeat("topic/é\r\n\t\u202e", 12)
	m, s := openedWorktrees(t, rows)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, offer := range [][2]int{{80, 80}, {80, 8}, {32, 12}, {12, 25}, {3, 3}, {1, 1}, {0, 20}, {80, 0}} {
		out := worktreesBodyFits(t, s, offer[0], offer[1])
		if strings.ContainsAny(out, "\x1b\a\r\t\u202e") {
			t.Fatalf("confirmation leaked unsafe metadata at %dx%d:\n%s", offer[0], offer[1], out)
		}
		if offer == [2]int{80, 80} && (s.compact || !strings.Contains(out, "界") || !strings.Contains(out, "branch:")) {
			t.Fatalf("large confirmation did not render sanitized Unicode metadata:\n%s", out)
		}
		if !s.compact && (!strings.Contains(out, "start") || !strings.Contains(out, "back")) {
			t.Fatalf("fitted confirmation lost actions:\n%s", out)
		}
		if s.compact {
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if s.intent != nil {
				t.Fatal("compact confirmation emitted a handoff")
			}
		}
	}
	m = effortOffer(m, 80, 20)
	_ = m.View()
	m = effortOffer(m, 80, 1)
	// Deliberately no View between resize and Enter.
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal != s || m.phase != phaseIdle || !s.compact || s.intent != nil {
		t.Fatal("resized confirmation activated stale usable geometry")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("compact confirmation cannot close")
	}
	m, s = openedWorktrees(t, []client.Worktree{{Label: "stale"}})
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal != s || s.confirm != nil || s.err == nil || m.phase != phaseIdle {
		t.Fatal("missing selector escaped surface validation")
	}
}
