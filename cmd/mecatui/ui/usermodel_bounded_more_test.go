package ui

// Acceptance proofs exercise the root modal lifecycle and its bounded controls.
import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func savedMemoryEntries(n int) []client.UserModelEntry {
	entries := make([]client.UserModelEntry, n)
	for i := range entries {
		entries[i] = client.UserModelEntry{Key: fmt.Sprintf("fact/%03d", i), Description: "a wrapped description of the saved memory"}
	}
	return entries
}
func savedMemoryOpened(t *testing.T, entries []client.UserModelEntry) (Model, *userModelState) {
	t.Helper()
	m, s := openedUserModel(t, newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries}}, client.Capabilities{UserModel: true}))
	return m, s
}
func checkSavedMemoryCard(t *testing.T, m Model, w, h int) {
	t.Helper()
	m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: h})
	rendered := m.View().Content
	if w <= 0 || m.vp.Height() <= 0 {
		if m.renderBody() != "" {
			t.Fatal("rendered with nonpositive offer")
		}
		return
	}
	bounds := m.metrics.outerBounds
	if bounds.x0 < 0 || bounds.x1 > w || bounds.y0 < convTopRow(m) || bounds.y1 > convTopRow(m)+m.vp.Height() {
		t.Fatalf("card outside offer: %+v width=%d height=%d", bounds, w, m.vp.Height())
	}
	if got := lipgloss.Width(m.renderBody()); got > w {
		t.Fatalf("body width=%d offered=%d: %q", got, w, rendered)
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario1_AllStatesFit(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(30))
	for _, state := range []struct {
		name string
		set  func()
	}{
		{"loading", func() { s.loading = true }},
		{"long error", func() { s.loading = false; s.err = errors.New(strings.Repeat("error\x1b[2J 日本語", 20)) }},
		{"enabled empty", func() { s.err = nil; s.setModel(client.UserModel{}) }},
		{"inventory", func() { s.setModel(client.UserModel{Entries: savedMemoryEntries(30)}) }},
		{"long detail", func() {
			s.view = userModelDetail
			s.detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "fact/000", Value: strings.Repeat("日本語 long value ", 60)}, HistoryAvailable: true, History: []client.UserModelRevision{{Version: "v1", Status: "active"}}}
		}},
		{"history unavailable", func() { s.detail.HistoryAvailable = false }},
	} {
		t.Run(state.name, func(t *testing.T) {
			state.set()
			for _, dim := range [][2]int{{16, 16}, {48, 28}, {150, 45}} {
				checkSavedMemoryCard(t, m, dim[0], dim[1])
			}
		})
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario1_CompactFallback(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(4))
	for _, detail := range []bool{false, true} {
		if detail {
			s.view = userModelDetail
			s.detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "fact/000"}}
		}
		for _, dim := range [][2]int{{2, 18}, {6, 20}, {12, 9}, {40, 8}} {
			m = applyAll(m, tea.WindowSizeMsg{Width: dim[0], Height: dim[1]})
			_ = m.View()
			if dim[0] <= m.deps.Theme.Style("askCard").GetHorizontalFrameSize() && m.vp.Height() > 0 && !s.compact {
				t.Fatalf("frame threshold %d must use compact fallback", dim[0])
			}
			if s.compact {
				before := s.list.Offset()
				s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				if s.list.Offset() != before || !strings.Contains(stripANSIstr(m.renderBody()), "close") && dim[0] >= len("esc close") {
					t.Fatal("compact wheel moved list or lost close")
				}
				if _, _, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); closed {
					t.Fatal("compact enter closed panel")
				}
			}
			checkSavedMemoryCard(t, m, dim[0], dim[1])
		}
	}
	for _, dim := range [][2]int{{0, 30}, {70, 0}} {
		checkSavedMemoryCard(t, m, dim[0], dim[1])
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario2_StableSelectionAndReveal(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(40))
	_ = m
	s.Render(30, 10)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	body, _ := s.Render(30, 10)
	if s.list.CursorID() != "fact/001" || !strings.Contains(body, "fact/001") {
		t.Fatalf("logical move did not reveal: %q", body)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	body, _ = s.Render(30, 10)
	if s.list.CursorID() == "fact/001" || !strings.Contains(body, s.list.CursorID()) {
		t.Fatalf("physical page did not select/reveal: %q", body)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	body, _ = s.Render(30, 10)
	if s.list.CursorID() != "fact/039" || !strings.Contains(body, "fact/039") {
		t.Fatal("bottom endpoint not revealed")
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	s.Render(30, 10)
	selected := s.list.CursorID()
	s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	s.Render(30, 10)
	if s.list.CursorID() != selected || s.list.Offset() == 0 {
		t.Fatal("wheel changed selection or failed to scroll")
	}
	rows := s.list.View().Rows
	for _, row := range rows {
		p := presentListRow(row, s.deps.theme.Style("toolName"), s.deps.theme.Style("toolArgs"))
		if ansi.StringWidth(p.Text) > 30 || !strings.HasPrefix(p.Text, " ") && !strings.HasPrefix(p.Text, "▶") {
			t.Fatalf("gutter/fit: %q", p.Text)
		}
	}

	m = newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: savedMemoryEntries(40)}}, client.Capabilities{UserModel: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{
		"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"}, "ScrollTop": {"t"}, "ScrollBottom": {"b"},
	})
	m, s = openedUserModel(t, m)
	s.Render(30, 10)
	m = applyAll(m, tea.KeyPressMsg{Code: 'd'})
	if s.list.CursorID() != "fact/001" {
		t.Fatalf("remapped Down did not select next key: %q", s.list.CursorID())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'n'})
	if s.list.CursorID() == "fact/001" || s.list.Offset() == 0 {
		t.Fatalf("remapped PageDown did not page/reveal: selected=%q offset=%d", s.list.CursorID(), s.list.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'p'}, tea.KeyPressMsg{Code: 'u'}, tea.KeyPressMsg{Code: 'b'})
	if s.list.CursorID() != "fact/039" || s.list.Offset() == 0 {
		t.Fatalf("remapped PageUp/Up/End did not reach bottom: selected=%q offset=%d", s.list.CursorID(), s.list.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't'})
	if s.list.CursorID() != "fact/000" || s.list.Offset() != 0 {
		t.Fatalf("remapped Home did not return to top: selected=%q offset=%d", s.list.CursorID(), s.list.Offset())
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario2_IdentityAndAnchorContinuity(t *testing.T) {
	_, s := savedMemoryOpened(t, savedMemoryEntries(20))
	s.Render(35, 9)
	s.list.SetCursor(8)
	s.Render(35, 9)
	wanted := s.list.CursorID()
	s.list.Scroll(bounded.LineDown)
	top := s.list.View().Rows[0]
	for _, w := range []int{22, 45, 35} {
		s.Render(w, 9)
		visible := s.list.View().Rows
		if s.list.CursorID() != wanted || len(visible) == 0 || visible[0].ID != top.ID || visible[0].ItemLine != top.ItemLine {
			t.Fatalf("resize lost selected key or top physical anchor at %d", w)
		}
	}
	replacement := append([]client.UserModelEntry{{Key: "fact/new"}}, savedMemoryEntries(20)...)
	s.setModel(client.UserModel{Entries: replacement})
	s.Render(35, 9)
	if s.list.CursorID() != wanted {
		t.Fatal("replacement lost retained key")
	}
	s.setModel(client.UserModel{Entries: replacement[:1]})
	s.Render(35, 9)
	if s.list.CursorID() != "fact/new" {
		t.Fatal("removed selection not clamped")
	}
	s.setModel(client.UserModel{})
	s.Render(35, 9)
	if cmd, _, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("empty inventory fetched detail")
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario2_ExactDetailConsistency(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(2))
	s.Render(70, 16)
	cmd, _, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(client.UserModelDetailMsg)
	msg.UserModel = client.UserModel{Entries: savedMemoryEntries(2)[1:], Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: "fact/000", Value: "retained history"}, HistoryAvailable: true, History: []client.UserModelRevision{{Version: "v1"}}}}
	m = applyAll(m, msg)
	body, _ := s.Render(70, 16)
	if !strings.Contains(body, "retained history") || s.view != userModelDetail {
		t.Fatal("matching deleted fact lost detail")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != s || s.view != userModelPanel || s.list.CursorID() != "fact/001" {
		t.Fatalf("escape did not clamp deleted detail selection: view=%d selected=%q", s.view, s.list.CursorID())
	}
	s.setModel(client.UserModel{Entries: savedMemoryEntries(2)})
	s.list.SetCursor(0)
	s.Render(70, 16)
	cmd, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg = cmd().(client.UserModelDetailMsg)
	msg.UserModel.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: "fact/001", Value: "another fact's value"}}
	s.HandleMsg(msg)
	body, _ = s.Render(70, 16)
	if strings.Contains(body, "another fact's value") || !strings.Contains(body, "fact/000") {
		t.Fatal("mismatched detail leaked another fact")
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	cmd, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	missing := cmd().(client.UserModelDetailMsg)
	missing.UserModel = client.UserModel{Entries: savedMemoryEntries(2)}
	s.HandleMsg(missing)
	body, _ = s.Render(70, 16)
	if !strings.Contains(body, "fact/000") || strings.Contains(body, "another fact's value") {
		t.Fatal("missing detail misattributed a value")
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario2_DetailHistoryAndReturn(t *testing.T) {
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: savedMemoryEntries(30)}}, client.Capabilities{UserModel: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{
		"Down": {"d"}, "ScrollD": {"n"}, "ScrollTop": {"t"}, "ScrollBottom": {"b"},
	})
	m, s := openedUserModel(t, m)
	s.Render(60, 12)
	s.list.SetCursor(20)
	s.Render(60, 12)
	selected, offset := s.list.CursorID(), s.list.Offset()
	mm, cmd, _ := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if msg := cmd().(client.UserModelDetailMsg); msg.RequestKey != selected {
		t.Fatalf("fetched %q not %q", msg.RequestKey, selected)
	}
	detail := client.UserModel{Entries: savedMemoryEntries(30), Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: selected, Value: strings.Repeat("value ", 100), Writer: "agent", Origin: "session"}, HistoryAvailable: true, History: []client.UserModelRevision{{Version: "v1", Status: "active"}}}}
	m = applyAll(m, client.UserModelDetailMsg{Generation: s.generation, RequestKey: selected, UserModel: detail})
	s.Render(60, 12)
	for _, step := range []struct {
		code rune
		want string
	}{
		{'d', "down"}, {'n', "page"}, {'t', "top"}, {'b', "bottom"},
	} {
		m = applyAll(m, tea.KeyPressMsg{Code: step.code})
		s.Render(60, 12)
		if step.want == "top" && s.viewport.Offset() != 0 || step.want != "top" && s.viewport.Offset() == 0 {
			t.Fatalf("remapped detail %s offset=%d", step.want, s.viewport.Offset())
		}
	}
	if s.viewport.Offset() == 0 {
		t.Fatal("detail scroll not available")
	}
	end, _ := s.Render(60, 12)
	if !strings.Contains(stripANSIstr(end), "v1 · active") {
		t.Fatalf("history not reachable at end: %q", stripANSIstr(end))
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	s.Render(60, 12)
	if m.modal != s || s.view != userModelPanel || s.list.CursorID() != selected || s.list.Offset() != offset {
		t.Fatalf("return lost inventory selection/offset: %d/%d", s.list.Offset(), offset)
	}
	cmd, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("reopening detail failed")
	}
	s.HandleMsg(client.UserModelDetailMsg{Generation: s.generation, RequestKey: selected, UserModel: detail})
	s.Render(60, 12)
	if s.viewport.Offset() != 0 {
		t.Fatal("reopened detail retained old scroll")
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario3_OpenBackCloseAndFocus(t *testing.T) {
	fum := sampleUserModel()
	for _, tc := range []struct {
		caps  client.Capabilities
		wired client.UserModelLister
	}{
		{client.Capabilities{}, fum},
		{client.Capabilities{UserModel: true}, nil},
	} {
		blocked := newUserModelModel(t, tc.wired, tc.caps)
		if mm, cmd := blocked.runUserModel(); cmd != nil || mm.(Model).modal != nil {
			t.Fatal("opened without server capability and client wiring")
		}
	}
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})
	m.phase = phaseRunning
	if mm, cmd := m.runUserModel(); cmd != nil || mm.(Model).modal != nil {
		t.Fatal("opened while running")
	}
	m.phase = phaseIdle
	mm, cmd := m.runUserModel()
	m = mm.(Model)
	if cmd == nil || m.prompt.Focused() {
		t.Fatal("open did not blur/fetch")
	}
	m = feedCmd(t, m, cmd)
	s := m.modal.(*userModelState)
	m.prompt.Rewrite("/clear")
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal != s || m.clearPending != nil || cmd == nil || m.prompt.Value() != "/clear" {
		t.Fatal("visible panel did not own Enter over hidden prompt")
	}
	m = feedCmd(t, m, cmd)
	if s.view != userModelDetail {
		t.Fatal("detail not opened")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != s || s.view != userModelPanel {
		t.Fatal("detail escape closed modal")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() || fum.calls != 2 {
		t.Fatalf("close did not refocus or fetch once: %d", fum.calls)
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario3_RejectsStaleResults(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(4))
	s.Render(50, 10)

	// The root update seam must consume a same-generation reply for another
	// exact key while the selected key's request remains pending.
	mm, pending := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	matching := pending().(client.UserModelDetailMsg)
	beforeOffset, beforeSelection := s.list.Offset(), s.list.CursorID()
	wrong := matching
	wrong.RequestKey = "fact/001"
	wrong.UserModel.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: wrong.RequestKey, Value: "wrong detail"}}
	mm, _ = m.Update(wrong)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if !s.loading || s.err != nil || s.requestKey != matching.RequestKey || s.view != userModelPanel || s.detail != nil || s.list.CursorID() != beforeSelection || s.list.Offset() != beforeOffset {
		t.Fatalf("wrong-key reply mutated pending detail: loading=%v err=%v request=%q view=%d detail=%+v selection=%q offset=%d", s.loading, s.err, s.requestKey, s.view, s.detail, s.list.CursorID(), s.list.Offset())
	}
	mm, _ = m.Update(matching)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if s.loading || s.err != nil || s.view != userModelDetail || s.detail == nil || s.detail.Current.Key != matching.RequestKey {
		t.Fatalf("matching reply was not installed: loading=%v err=%v view=%d detail=%+v", s.loading, s.err, s.view, s.detail)
	}

	m, s = savedMemoryOpened(t, savedMemoryEntries(4))
	s.Render(50, 10)

	// Drive the public Model.Update seam: abandoning A for B must make A's
	// response stale without leaving the panel in loading state, so B can retry.
	mm, first := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	lateA := first().(client.UserModelDetailMsg)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	mm, _ = m.Update(lateA)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if s.loading || s.err != nil || s.requestKey != "" || s.list.CursorID() != "fact/001" {
		t.Fatalf("late A left B unavailable: loading=%v err=%v request=%q selected=%q", s.loading, s.err, s.requestKey, s.list.CursorID())
	}
	mm, second := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	lateB := second().(client.UserModelDetailMsg)
	if lateB.RequestKey != "fact/001" {
		t.Fatalf("Enter after late A fetched %q, want B", lateB.RequestKey)
	}
	mm, _ = m.Update(lateB)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if s.view != userModelDetail || s.detail == nil || s.detail.Current.Key != "fact/001" {
		t.Fatalf("B detail was not installed after retry: %+v", s)
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	mm, failed := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	failure := failed().(client.UserModelDetailMsg)
	failure.Err = errors.New("detail unavailable")
	mm, _ = m.Update(failure)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if s.err == nil {
		t.Fatal("detail error was not retained for display")
	}
	mm, retry := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if retry == nil {
		t.Fatal("Enter did not retry a visible detail error")
	}
	retried := retry().(client.UserModelDetailMsg)
	mm, _ = m.Update(retried)
	m = mm.(Model)
	s = m.modal.(*userModelState)
	if s.view != userModelDetail || s.err != nil {
		t.Fatalf("retry did not clear detail error: view=%d err=%v", s.view, s.err)
	}

	m, s = savedMemoryOpened(t, savedMemoryEntries(4))
	s.Render(50, 10)
	cmd, _, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	old := cmd().(client.UserModelDetailMsg)
	m = applyAll(m, old)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // back from detail invalidates its request
	for _, bad := range []client.UserModelDetailMsg{old, {Generation: old.Generation, RequestKey: old.RequestKey, Err: errors.New("old")}} {
		m = applyAll(m, bad)
	}
	if s.view != userModelPanel || s.err != nil || s.loading {
		t.Fatalf("late back result mutated state: view=%d err=%v loading=%v", s.view, s.err, s.loading)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, s = openedUserModel(t, m)
	for _, bad := range []client.UserModelMsg{{Generation: old.Generation, UserModel: client.UserModel{Entries: []client.UserModelEntry{{Key: "wrong"}}}}, {Generation: old.Generation, Err: errors.New("old")}} {
		m = applyAll(m, bad)
	}
	if s.list.CursorID() != "fact/000" || s.err != nil || s.loading {
		t.Fatalf("late reopen result mutated state: %+v", s)
	}
}
func TestMecatuiSavedMemoryBoundedBrowser_Scenario3_WheelAndPointerIsolation(t *testing.T) {
	m, s := savedMemoryOpened(t, savedMemoryEntries(60))
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	m.vp.SetYOffset(5)
	before := m.vp.YOffset()
	withoutModal := m
	withoutModal.modal = nil
	mm, _ := withoutModal.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	withoutModal = mm.(Model)
	if got := withoutModal.vp.YOffset(); got <= before {
		t.Fatalf("wheel without modal did not move scrollable conversation: got %d, want > %d", got, before)
	}

	s.Render(45, 9)
	selected := s.list.CursorID()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 1, Y: 1})
	if s.list.Offset() != 0 || m.vp.YOffset() != before {
		t.Fatal("top endpoint wheel escaped saved-memory panel")
	}
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	s.Render(45, 9)
	if s.list.Offset() != 1 || s.list.CursorID() != selected || m.vp.YOffset() != before {
		t.Fatalf("wheel escaped visible viewport: offset=%d", s.list.Offset())
	}
	s.list.Scroll(bounded.End)
	bottomOffset := s.list.Offset()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	if s.list.Offset() != bottomOffset || s.list.CursorID() != selected || m.vp.YOffset() != before {
		t.Fatalf("inventory bottom wheel escaped modal: offset=%d selection=%q conversation=%d", s.list.Offset(), s.list.CursorID(), m.vp.YOffset())
	}
	for _, msg := range []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 10}, tea.MouseMotionMsg{Button: tea.MouseLeft, X: 7, Y: 11}, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 7, Y: 11}} {
		m = applyAll(m, msg)
	}
	if m.sel.active || s.list.CursorID() != selected || s.view != userModelPanel {
		t.Fatal("pointer selected or activated fact")
	}
	s.Render(2, 2)
	offset := s.list.Offset()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.list.Offset() != offset || !s.compact {
		t.Fatal("compact wheel moved list")
	}

	m, s = savedMemoryOpened(t, savedMemoryEntries(2))
	s.Render(45, 9)
	mm, cmd, _ := m.dispatchSurfaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	detail := cmd().(client.UserModelDetailMsg)
	detail.UserModel.Detail = &client.UserModelDetail{Current: client.UserModelRevision{Key: detail.RequestKey, Value: strings.Repeat("detail ", 200)}}
	m = applyAll(m, detail)
	s.Render(45, 9)
	detailBefore := m.vp.YOffset()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if s.viewport.Offset() != 0 || m.vp.YOffset() != detailBefore {
		t.Fatal("detail top endpoint wheel escaped modal")
	}
	s.viewport.Move(bounded.End, len(s.detailLines))
	detailEnd := s.viewport.Offset()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.viewport.Offset() != detailEnd || m.vp.YOffset() != detailBefore {
		t.Fatal("detail bottom endpoint wheel escaped modal")
	}

	// The modal blocks primary paste but does not disable copying an existing
	// prompt selection. Both buttons route through the root mouse dispatcher.
	cb := &fakeClipboard{primary: "must not paste"}
	m.deps.Clipboard = cb
	m.prompt.Rewrite("copy retained selection")
	m.prompt.SelectAll()
	if !m.prompt.HasSelection() {
		t.Fatal("copy gate precondition: no selected text")
	}
	mm, copyCmd := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 1, Y: 1})
	m = mm.(Model)
	if copyCmd == nil {
		t.Fatal("right click dropped existing prompt selection")
	}
	if payload, ok := osc52Payload(collectLeaves(copyCmd)); !ok || payload != "copy retained selection" {
		t.Fatalf("right-click copied %q, want retained selection", payload)
	}
	mm, pasteCmd := m.Update(tea.MouseClickMsg{Button: tea.MouseMiddle, X: 1, Y: 1})
	m = mm.(Model)
	if pasteCmd != nil || cb.primaryCalls != 0 || m.prompt.Value() != "copy retained selection" {
		t.Fatalf("middle-click bypassed modal paste gate: cmd=%v reads=%d draft=%q", pasteCmd != nil, cb.primaryCalls, m.prompt.Value())
	}
}
