package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestScheduleFillPlacementRetainsRootChrome(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: scheduleRows(1), fires: scheduleFires("schedule-00", 1)})
	m.prompt.Rewrite("retained draft")
	m = inspectSchedule(t, m)
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails, scheduleCreate, scheduleConfirm} {
		s.view = view
		if view == scheduleCreate {
			s.openCreate()
		}
		s.confirm = s.schedules[0]
		body, _ := s.Render(m.width, m.vp.Height())
		if got := m.renderBody(); got != body {
			t.Fatalf("view %d does not use parent fill placement", view)
		}
		if m.metrics.outerBounds != m.metrics.contentBounds || s.offers[view] != [2]int{m.width, m.vp.Height()} {
			t.Fatal("parent offered a card instead of the conversation")
		}
		above, below := m.chrome()
		full := stripANSIstr(m.View().Content)
		for _, chunk := range append(above, below...) {
			if !strings.Contains(full, stripANSIstr(chunk.content)) {
				t.Fatal("schedule displaced root chrome")
			}
		}
		if !strings.Contains(full, "retained draft") {
			t.Fatal("schedule replaced the prompt")
		}
	}
}

func TestScheduleNavigationAndDetailsKeyPrecedence(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: scheduleRows(60), fires: scheduleFires("schedule-00", 60)})
	m.prompt.Rewrite("draft")
	// Bindings captured at open, not live Model keys, own all browsing levels.
	s.deps.keys = applyKeyOverrides(s.deps.keys, map[string][]string{
		"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n", "t"},
		"ScrollTop": {"x"}, "ScrollBottom": {"b"}, "Choose": {"x", "q", "v"}, "Close": {"q"},
	})
	s.deps.marks = keyMarkings(s.deps.keys)
	s.transcriptWired = true
	body := scheduleBody(t, s, 100, 30)
	for _, absent := range []string{"p: pause", "d: delete"} {
		if strings.Contains(body, absent) {
			t.Fatalf("reserved action hint %q is advertised: %s", absent, body)
		}
	}
	for _, present := range []string{"v: runs", "c: create", "r: resume", "f: fire-now", "/: filter", "q: close"} {
		if !strings.Contains(body, present) {
			t.Fatalf("available action hint %q is missing: %s", present, body)
		}
	}
	m = scheduleRune(t, m, "/")
	body = scheduleBody(t, s, 100, 30)
	for _, absent := range []string{": runs", ": create", ": pause", ": resume", ": fire-now", ": delete", ": filter"} {
		if strings.Contains(body, absent) {
			t.Fatalf("filter footer advertises inactive action %q: %s", absent, body)
		}
	}
	if !strings.Contains(body, "x: done  q: done") {
		t.Fatalf("filter footer does not advertise its exits: %s", body)
	}
	m = scheduleRune(t, m, "q")
	if s.filter.Focused() {
		t.Fatal("Close did not exit filter")
	}
	m = scheduleRune(t, m, "d")
	if s.list.Cursor() != 1 || s.view != schedulePanel {
		t.Fatal("configured Down lost to bare delete")
	}
	m = scheduleRune(t, m, "b")
	if s.list.Cursor() != 59 {
		t.Fatal("configured End lost")
	}
	m = scheduleRune(t, m, "x")
	if s.list.Cursor() != 0 || s.view != schedulePanel {
		t.Fatal("navigation must precede Choose")
	}
	m = scheduleRune(t, m, "n")
	if s.list.Cursor() == 0 {
		t.Fatal("configured page down did not move")
	}
	m = scheduleRune(t, m, "p")
	if s.pendingAction != "" {
		t.Fatal("configured page up fired pause")
	}
	s.list.SetCursor(0)
	m = scheduleRune(t, m, "v")
	if s.view != scheduleInspect {
		t.Fatal("configured Choose did not open runs")
	}
	body = scheduleBody(t, s, 100, 30)
	if strings.Contains(body, "/t: open transcript") || strings.Contains(body, "t: open transcript") || !strings.Contains(body, "v: open transcript") {
		t.Fatalf("transcript footer does not reflect available activation keys: %s", body)
	}
	chord := s.detailsKey()
	if chord == "" || chord == "d" || chord == "t" {
		t.Fatalf("Details chord collides: %q", chord)
	}
	if !strings.Contains(scheduleBody(t, s, 100, 30), chord+": details") {
		t.Fatal("actual Details chord not discoverable")
	}
	m = scheduleRune(t, m, "d")
	if s.runs.Cursor() != 1 || s.view != scheduleInspect {
		t.Fatal("Details stole configured Down")
	}
	m = scheduleRune(t, m, "x")
	if s.runs.Cursor() != 0 || s.intent != nil {
		t.Fatal("runs Top lost to Choose")
	}
	m = scheduleRune(t, m, chord)
	if s.view != scheduleDetails {
		t.Fatal("discovered Details chord not effective")
	}
	m = scheduleRune(t, m, "v")
	if s.view != scheduleDetails || s.intent != nil {
		t.Fatal("details Choose activated transcript")
	}
	m = scheduleRune(t, m, "q")
	if s.view != scheduleInspect {
		t.Fatal("Close lost to Choose on details")
	}
	m = scheduleRune(t, m, "q")
	if s.view != schedulePanel || m.modal != s {
		t.Fatal("Close lost to Choose on runs")
	}
	m = scheduleRune(t, m, "q")
	if m.modal != nil || m.prompt.Value() != "draft" {
		t.Fatal("Close lost to Choose on inventory")
	}
}

func TestScheduleWheelOwnsOnlyActiveWindow(t *testing.T) {
	fs := &fakeScheduleLister{schedules: scheduleRows(80), fires: scheduleFires("schedule-00", 80)}
	m, s := openedSchedule(t, fs)
	m.prompt.Rewrite("draft")
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m.vp.SetContent(strings.Repeat("conversation\n", 200))
	m.vp.GotoTop()
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails} {
		if view == scheduleInspect {
			m = inspectSchedule(t, m)
		}
		if view == scheduleDetails {
			m = scheduleRune(t, m, s.detailsKey())
		}
		_ = m.View()
		selected, fire := s.list.CursorID(), s.runs.CursorID()
		parentOffset, runsOffset, detailOffset := s.list.Offset(), s.runs.Offset(), s.details.Offset()
		for range 4 {
			m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		}
		_ = m.View()
		if s.list.CursorID() != selected || s.runs.CursorID() != fire || m.vp.YOffset() != 0 || m.prompt.Value() != "draft" {
			t.Fatal("wheel moved cursor or leaked into conversation/prompt")
		}
		switch view {
		case schedulePanel:
			if s.list.Offset() <= parentOffset || s.runs.Offset() != runsOffset {
				t.Fatal("inventory wheel did not scroll only inventory")
			}
		case scheduleInspect:
			if s.runs.Offset() <= runsOffset || s.list.Offset() != parentOffset {
				t.Fatal("runs wheel did not scroll only runs")
			}
		case scheduleDetails:
			if s.details.Offset() <= detailOffset || s.runs.Offset() != runsOffset || s.list.Offset() != parentOffset {
				t.Fatal("details wheel did not scroll only details")
			}
		}
	}
}

func TestScheduleCompactDisablesEveryHiddenAction(t *testing.T) {
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails, scheduleCreate, scheduleConfirm} {
		for _, size := range [][2]int{{5, 2}, {100, 1}, {0, 0}} {
			t.Run(fmt.Sprintf("%d/%v", view, size), func(t *testing.T) {
				fs := &fakeScheduleLister{schedules: scheduleRows(2), fires: scheduleFires("schedule-00", 1)}
				m, s := openedSchedule(t, fs)
				m.prompt.Rewrite("draft")
				m = inspectSchedule(t, m)
				s.view = view
				s.confirm = fs.schedules[0]
				if view == scheduleCreate {
					s.openCreate()
					s.form.name.SetValue("new")
					s.form.prompt.SetValue("prompt")
					s.form.trigger.SetValue("0 9 * * *")
					s.form.focusIdx = 3
				}
				scheduleBody(t, s, size[0], size[1])
				for _, msg := range []tea.KeyPressMsg{navigationTestKey("c"), navigationTestKey("p"), navigationTestKey("r"), navigationTestKey("f"), navigationTestKey("d"), navigationTestKey("t"), {Code: tea.KeyEnter}, {Code: tea.KeyDown}} {
					cmd, handled, closed := s.HandleKey(msg)
					if cmd != nil || !handled || closed || s.view != view || s.intent != nil {
						t.Fatal("compact activated invisible action")
					}
				}
				if cmd, handled, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || !handled || !closed {
					t.Fatal("compact close must exit whole surface")
				}
				if m.prompt.Value() != "draft" || fs.createCalls+fs.pauseCalls+fs.resumeCalls+fs.fireCalls+fs.deleteCalls != 0 {
					t.Fatal("compact mutated or leaked")
				}
			})
		}
	}
}

func TestScheduleResizeHiddenParentRevealsSelection(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: scheduleRows(100), fires: scheduleFires("schedule-90", 100)})
	s.list.SetCursor(90)
	_ = m.View()
	m = inspectSchedule(t, m)
	s.runs.SetCursor(90)
	_ = m.View()
	m = scheduleRune(t, m, s.detailsKey())
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 20})
	_ = m.View()
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	body := stripANSIstr(m.renderBody())
	if !strings.Contains(body, "fire-90") {
		t.Fatal("resized hidden runs failed to reveal selected ID")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	body = stripANSIstr(m.renderBody())
	if !strings.Contains(body, "schedule-90") {
		t.Fatal("resized hidden inventory failed to reveal selected ID")
	}
}

func TestScheduleRawIdentityIsNotDisplayText(t *testing.T) {
	raw := "task\x1b[31m\nname"
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("task"), sampleSchedule(raw)}}
	m, s := openedSchedule(t, fs)
	s.list.SetCursor(1)
	_ = scheduleRune(t, m, "p")
	if s.list.CursorID() != raw || fs.lastPauseName != raw {
		t.Fatal("sanitized label replaced raw Spec.Name identity")
	}
	body := scheduleBody(t, s, 100, 30)
	if strings.Contains(body, "\x1b[31m") {
		t.Fatal("server name leaked terminal control sequence")
	}
}

func TestScheduleFireIdentityRefreshAndTranscriptFallback(t *testing.T) {
	fs := &fakeScheduleLister{schedules: scheduleRows(2), fires: scheduleFires("schedule-00", 40)}
	m, s := openedSchedule(t, fs)
	m.deps.Transcript = &fakeSessionTranscriptLoader{}
	s.transcriptWired = true
	m = inspectSchedule(t, m)
	s.runs.SetCursor(25)
	_ = m.View()
	id := s.runs.CursorID()
	slices.Reverse(fs.fires)
	m = feedCmd(t, m, s.inspectCmd())
	if s.runs.CursorID() != id {
		t.Fatal("fire reorder lost Fire.ID identity")
	}
	index := s.runs.Cursor()
	fs.fires = slices.Delete(fs.fires, index, index+1)
	m = feedCmd(t, m, s.inspectCmd())
	selected := fs.fires[min(index, len(fs.fires)-1)]
	if s.runs.CursorID() != selected.ID {
		t.Fatal("fire fallback differs from bounded fallback")
	}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal.(*sessionsState).selected.ID != selected.SessionID {
		t.Fatal("transcript used obsolete fire cursor or Fire.ID instead of SessionID")
	}
}

func TestSchedulePagesEndpointsAndDetailReset(t *testing.T) {
	fs := &fakeScheduleLister{schedules: scheduleRows(80), fires: scheduleFires("schedule-00", 80)}
	m, s := openedSchedule(t, fs)
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails} {
		if view == scheduleInspect {
			m = inspectSchedule(t, m)
		}
		if view == scheduleDetails {
			m = scheduleRune(t, m, s.detailsKey())
		}
		_ = m.View()
		for _, move := range []bounded.Move{bounded.End, bounded.Top, bounded.PageDown, bounded.PageUp} {
			list := &s.list
			if view == scheduleInspect {
				list = &s.runs
			}
			want := 0
			if view == scheduleDetails {
				switch move {
				case bounded.End:
					want = max(0, s.detailRows-s.details.Height())
				case bounded.PageDown:
					want = min(s.details.Offset()+s.details.Height(), max(0, s.detailRows-s.details.Height()))
				case bounded.PageUp:
					want = max(0, s.details.Offset()-s.details.Height())
				}
			} else {
				// These short fixtures occupy exactly one physical row each.
				switch move {
				case bounded.End:
					want = 79
				case bounded.PageDown:
					want = list.Offset() + list.Height()
				case bounded.PageUp:
					want = max(0, list.Offset()-list.Height())
				}
			}
			msg := map[bounded.Move]tea.KeyPressMsg{
				bounded.End: {Code: tea.KeyEnd}, bounded.Top: {Code: tea.KeyHome},
				bounded.PageDown: {Code: tea.KeyPgDown}, bounded.PageUp: {Code: tea.KeyPgUp},
			}[move]
			// Use deterministic captured page bindings, independent of platform chords.
			s.deps.keys = applyKeyOverrides(s.deps.keys, map[string][]string{"ScrollD": {"pgdown"}, "ScrollU": {"pgup"}})
			m = scheduleKey(t, m, msg)
			body := scheduleBody(t, s, m.width, m.vp.Height())
			position := list.Cursor()
			if view == scheduleDetails {
				position = s.details.Offset()
				indicator := fmt.Sprintf("↑ %d · ↓ %d more", position, max(0, s.detailRows-position-s.details.Height()))
				if !strings.Contains(body, indicator) {
					t.Fatal("wrong details overflow: " + body)
				}
			} else {
				if !strings.Contains(body, "▶ "+list.CursorID()) {
					t.Fatal("selected row not revealed: " + body)
				}
				for _, indicator := range []struct {
					arrow string
					count int
				}{{"↑", list.Offset()}, {"↓", 80 - list.Offset() - list.Height()}} {
					if indicator.count > 0 && !strings.Contains(body, fmt.Sprintf("%s %d more", indicator.arrow, indicator.count)) || indicator.count == 0 && strings.Contains(body, indicator.arrow+" ") {
						t.Fatal("wrong list overflow: " + body)
					}
				}
			}
			if position != want {
				t.Fatalf("view %d move %d position = %d, want %d", view, move, position, want)
			}
		}
		if view == schedulePanel {
			s.list.SetCursor(0)
		}
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	s.list.SetCursor(1)
	fs.fires = scheduleFires("schedule-01", 3)
	m = inspectSchedule(t, m)
	m = scheduleRune(t, m, s.detailsKey())
	_ = m.View()
	if s.details.Offset() != 0 || s.runs.CursorID() != "fire-00" {
		t.Fatal("different inspected schedule retained old detail/fire window")
	}
}

func TestScheduleWrappedContentReachable(t *testing.T) {
	row := sampleSchedule("nightly")
	row.Spec.Trigger.Cron = strings.Repeat("界cron", 100)
	row.Spec.Selector = client.ScheduleSelector{ProviderID: strings.Repeat("界provider", 40), ModelID: "model-tail"}
	row.State.LastFireSessionID = "detail-tail"
	fires := scheduleFires(row.Spec.Name, 1)
	fires[0].Err = strings.Repeat("界failure", 100) + "run-tail"
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: []client.Schedule{row}, fires: fires})
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	s.deps.keys = applyKeyOverrides(s.deps.keys, map[string][]string{"ScrollD": {"pgdown"}})
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails} {
		if view == scheduleInspect {
			m = inspectSchedule(t, m)
		}
		if view == scheduleDetails {
			m = scheduleRune(t, m, s.detailsKey())
		}
		text, width := scheduleRowText(row), m.width-2
		if view == scheduleInspect {
			text = renderScheduleFireLine(fires[0])
		}
		if view == scheduleDetails {
			text, width = stripANSIstr(scheduleDetailText(s.deps.theme, row)), m.width
		}
		physical := strings.Split(ansi.Hardwrap(text, width, true), "\n")
		body := scheduleBody(t, s, m.width, m.vp.Height())
		if s.compact || len(physical) <= m.vp.Height() {
			t.Fatalf("view %d fixture does not exercise physical overflow", view)
		}
		var seen strings.Builder
		for range len(physical) + 1 {
			seen.WriteString(body + "\n")
			key := tea.KeyPressMsg{Code: tea.KeyPgDown}
			if view == scheduleDetails {
				key.Code = tea.KeyDown
			}
			m = scheduleKey(t, m, key)
			body = scheduleBody(t, s, m.width, m.vp.Height())
		}
		for i, line := range physical {
			if line = strings.TrimSpace(line); line != "" && !strings.Contains(seen.String(), line) {
				t.Fatalf("view %d physical row %d/%d unreachable: %q", view, i, len(physical), line)
			}
		}
		if view == scheduleDetails {
			if s.details.Offset() != len(physical)-s.details.Height() || !strings.Contains(body, "detail-tail") || !strings.Contains(body, "↓ 0 more") {
				t.Fatal("details physical end/tail not reached: " + body)
			}
		} else if strings.Contains(body, "more") {
			t.Fatal("partial single-row segments counted as hidden logical items: " + body)
		}
	}
}

func TestScheduleCompactTranscriptCloseExitsSessions(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: scheduleRows(1), fires: scheduleFires("schedule-00", 1)})
	m.deps.Transcript = &fakeSessionTranscriptLoader{}
	m.deps.Sessions = &fakeSessionLister{}
	s.transcriptWired = true
	m.prompt.Rewrite("draft")
	active := m.sessionID
	m = inspectSchedule(t, m)
	m = inspectSchedule(t, m)
	if _, ok := m.modal.(*sessionsState); !ok {
		t.Fatal("handoff missing")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil || m.phase != phaseIdle || m.sessionID != active || m.prompt.Value() != "draft" {
		t.Fatal("compact transcript Close did not exit non-destructively")
	}
}

func TestScheduleSurfaceOwnsBoundedConversation(t *testing.T) {
	fs := &fakeScheduleLister{}
	for i := range 80 {
		fs.schedules = append(fs.schedules, sampleSchedule(fmt.Sprintf("schedule-%02d", i)))
	}
	m := newScheduleModel(t, newScheduleConv(scheduleCaps()), fs, scheduleCaps())
	mm, cmd := m.runSchedule()
	m = feedCmd(t, mm.(Model), cmd)
	if m.modal == nil {
		t.Fatal("/schedule must own the one active modal, not a legacy body route")
	}
	for _, size := range [][2]int{{100, 20}, {160, 30}, {24, 12}, {8, 2}, {0, 0}} {
		body, hits := m.modal.Render(size[0], size[1])
		if len(hits) != 0 {
			t.Fatal("schedule must not introduce click selection")
		}
		if size[0] == 0 || size[1] == 0 {
			if body != "" {
				t.Fatal("zero offer rendered content")
			}
			continue
		}
		if lipgloss.Width(body) > size[0] || lipgloss.Height(body) > size[1] {
			t.Fatalf("offer %v exceeded: %dx%d", size, lipgloss.Width(body), lipgloss.Height(body))
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0}, tea.KeyPressMsg{Code: 'c', Text: "c"}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if fs.createCalls != 0 {
		t.Fatal("hidden create")
	}
}
