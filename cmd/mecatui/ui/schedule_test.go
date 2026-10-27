package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// Offline spy shared by the action, ownership and dispatch scenarios.
type fakeScheduleLister struct {
	schedules                                                                                     []client.Schedule
	sched                                                                                         client.Schedule
	fires                                                                                         []client.ScheduleFire
	fireID                                                                                        string
	err, getErr, firesErr                                                                         error
	listCalls, getCalls, createCalls, deleteCalls, fireCalls, pauseCalls, resumeCalls, firesCalls int
	lastDeleteName, lastPauseName, lastResumeName, lastFireName, lastFiresName, lastGetname       string
	created                                                                                       client.ScheduleSpec
	nextFire                                                                                      time.Time
}

func (f *fakeScheduleLister) ListSchedules(context.Context) ([]client.Schedule, error) {
	f.listCalls++
	return f.schedules, f.err
}
func (f *fakeScheduleLister) GetSchedule(_ context.Context, name string) (client.Schedule, error) {
	f.getCalls++
	f.lastGetname = name
	err := f.getErr
	if err == nil {
		err = f.err
	}
	if f.sched.Spec.Name != "" {
		return f.sched, err
	}
	for _, s := range f.schedules {
		if s.Spec.Name == name {
			return s, err
		}
	}
	return client.Schedule{}, err
}
func (f *fakeScheduleLister) CreateSchedule(_ context.Context, spec client.ScheduleSpec) (client.Schedule, error) {
	f.createCalls++
	f.created = spec
	row := client.Schedule{Spec: spec, State: client.ScheduleState{NextFireAt: f.nextFire}}
	if f.err == nil {
		f.schedules = append(f.schedules, row)
	}
	return row, f.err
}
func (f *fakeScheduleLister) DeleteSchedule(_ context.Context, name string) error {
	f.deleteCalls++
	f.lastDeleteName = name
	return f.err
}
func (f *fakeScheduleLister) FireNow(_ context.Context, name string) (string, string, error) {
	f.fireCalls++
	f.lastFireName = name
	return f.fireID, f.fireID, f.err
}
func (f *fakeScheduleLister) PauseSchedule(_ context.Context, name string) error {
	f.pauseCalls++
	f.lastPauseName = name
	return f.err
}
func (f *fakeScheduleLister) ResumeSchedule(_ context.Context, name string) error {
	f.resumeCalls++
	f.lastResumeName = name
	return f.err
}
func (f *fakeScheduleLister) ListFires(_ context.Context, name string) ([]client.ScheduleFire, error) {
	f.firesCalls++
	f.lastFiresName = name
	if f.firesErr != nil {
		return nil, f.firesErr
	}
	return f.fires, f.err
}
func scheduleCaps() client.Capabilities { return client.Capabilities{Scheduling: true} }
func sampleSchedule(name string) client.Schedule {
	return client.Schedule{Spec: client.ScheduleSpec{Name: name, Prompt: "run the tests", Trigger: client.ScheduleTrigger{Cron: "0 9 * * *"}, Mode: "plan", Profile: "no-fs", Singleton: true, Timezone: "UTC"}, State: client.ScheduleState{Enabled: true}}
}
func newScheduleConv(caps client.Capabilities) *fakeConv {
	return &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: caps}
}
func newScheduleModel(t *testing.T, conv *fakeConv, fs *fakeScheduleLister, caps client.Capabilities) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Session: conv, Conv: conv, Sched: fs, Theme: theme.New("aztec", theme.AztecPalette()), Workspace: "/workspace", Mode: "default", Model: "mock-model", Ctx: context.Background(), NoAltScreen: true})
	return applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "active-chat", Capabilities: caps})
}
func openedSchedule(t *testing.T, fs *fakeScheduleLister) (Model, *scheduleState) {
	t.Helper()
	m := newScheduleModel(t, newScheduleConv(scheduleCaps()), fs, scheduleCaps())
	mm, cmd := m.runSchedule()
	m = feedCmd(t, mm.(Model), cmd)
	s := m.modal.(*scheduleState)
	_ = m.View()
	return m, s
}
func scheduleKey(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	mm, cmd := m.Update(msg)
	m = mm.(Model)
	// Focus/text commands arm an ongoing cursor timer, not fixture RPC work.
	if m.modal == nil {
		return m
	}
	if s, ok := m.modal.(*scheduleState); ok && (s.filter.Focused() || s.view == scheduleCreate && s.pendingAction == "") {
		return m
	}
	return feedCmd(t, m, cmd)
}
func scheduleRune(t *testing.T, m Model, text string) Model {
	t.Helper()
	return scheduleKey(t, m, navigationTestKey(text))
}
func scheduleBody(t *testing.T, s *scheduleState, w, h int) string {
	t.Helper()
	body, hits := s.Render(w, h)
	if len(hits) > 0 || lipgloss.Width(body) > w || (h > 0 && lipgloss.Height(body) > h) {
		t.Fatalf("body exceeds %dx%d: %dx%d\n%s", w, h, lipgloss.Width(body), lipgloss.Height(body), body)
	}
	if w > 0 && h > 0 && lipgloss.Height(body) != h {
		t.Fatalf("body height = %d, want offered height %d", lipgloss.Height(body), h)
	}
	if w <= 0 || h <= 0 {
		if body != "" || !s.compact {
			t.Fatal("zero offer retained body/activation")
		}
	}
	return stripANSIstr(body)
}
func scheduleRows(n int) []client.Schedule {
	rows := make([]client.Schedule, n)
	for i := range rows {
		rows[i] = sampleSchedule(fmt.Sprintf("schedule-%02d", i))
	}
	return rows
}
func scheduleFires(name string, n int) []client.ScheduleFire {
	rows := make([]client.ScheduleFire, n)
	for i := range rows {
		rows[i] = client.ScheduleFire{ID: fmt.Sprintf("fire-%02d", i), ScheduleName: name, SessionID: fmt.Sprintf("session-%02d", i), Stop: "end_turn"}
	}
	return rows
}
func inspectSchedule(t *testing.T, m Model) Model {
	t.Helper()
	return scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestRunScheduleGating(t *testing.T) {
	for _, guard := range []string{"running", "nil", "modal"} {
		t.Run(guard, func(t *testing.T) {
			fs := &fakeScheduleLister{}
			m := newScheduleModel(t, newScheduleConv(scheduleCaps()), fs, scheduleCaps())
			switch guard {
			case "running":
				m.phase = phaseRunning
			case "nil":
				m.deps.Sched = nil
			case "modal":
				m.modal = &bodyOwnerTestSurface{}
			case "capability":
				m.caps.Scheduling = false
			}
			original := m.modal
			mm, cmd := m.runSchedule()
			if mm.(Model).modal != original || cmd != nil || fs.listCalls != 0 {
				t.Fatal("open gate bypassed")
			}
		})
	}
}
func TestScheduleFilterActionsAndClose(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("production"), sampleSchedule("nightly")}, fireID: "fire-now"}
	m, s := openedSchedule(t, fs)
	m.prompt.Rewrite("unsent draft")
	if s.filter.Focused() || m.prompt.Focused() || fs.listCalls != 1 {
		t.Fatal("incorrect open focus/request")
	}
	m = scheduleRune(t, m, "/")
	for _, r := range "prod" {
		m = scheduleRune(t, m, string(r))
	}
	if s.filter.Value() != "prod" || len(s.filtered) != 1 || fs.pauseCalls != 0 || fs.resumeCalls != 0 || fs.deleteCalls != 0 {
		t.Fatal("filter fired an action")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.filter.Focused() || s.filter.Value() != "prod" {
		t.Fatal("first close must blur, retain filter")
	}
	for _, action := range []string{"p", "r", "f"} {
		m = scheduleRune(t, m, action)
	}
	if fs.lastPauseName != "production" || fs.lastResumeName != "production" || fs.lastFireName != "production" || fs.resumeCalls != 1 {
		t.Fatal("bare actions lost selected identity (r must resume)")
	}
	if !strings.Contains(s.notice, "fire-now") {
		t.Fatal("fire success lost feedback")
	}
	m = scheduleRune(t, m, "d")
	if s.view != scheduleConfirm {
		t.Fatal("delete did not confirm")
	}
	if out := scheduleBody(t, s, 100, 30); !strings.Contains(out, "delete production? This cannot be undone.") {
		t.Fatal(out)
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if fs.deleteCalls != 0 || s.view != schedulePanel {
		t.Fatal("backout deleted")
	}
	m = scheduleRune(t, m, "d")
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if fs.lastDeleteName != "production" {
		t.Fatal("wrong delete")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.filter.Value() != "" || m.modal != s {
		t.Fatal("second close must clear retained filter")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil || !m.prompt.Focused() || m.prompt.Value() != "unsent draft" {
		t.Fatal("close leaked into draft")
	}
}
func TestScheduleCreateForm(t *testing.T) {
	for _, trigger := range []string{"0 9 * * *", "every 30 minutes"} {
		t.Run(trigger, func(t *testing.T) {
			fs := &fakeScheduleLister{}
			m, s := openedSchedule(t, fs)
			m = scheduleRune(t, m, "c")
			if s.view != scheduleCreate || !s.form.name.Focused() {
				t.Fatal("form did not own focus")
			}
			focus := func(want int) {
				t.Helper()
				if s.form.focusIdx != want || s.form.name.Focused() != (want == scheduleName) || s.form.prompt.Focused() != (want == schedulePrompt) || s.form.trigger.Focused() != (want == scheduleTiming) {
					t.Fatalf("focus = %d, want %d", s.form.focusIdx, want)
				}
			}
			typeText := func(text string) {
				for _, r := range text {
					m = scheduleRune(t, m, string(r))
				}
			}
			// Only the explicit Create control attempts submission.
			for range scheduleSubmit {
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 0 || !strings.Contains(s.actionErr, "name is required") {
				t.Fatal("empty form submitted")
			}
			for want := scheduleReadOnly; want >= scheduleName; want-- {
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
				focus(want)
			}
			typeText("ny-new")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			focus(schedulePrompt)
			m = scheduleFocus(t, m, s, scheduleSubmit)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 0 || !strings.Contains(s.actionErr, "prompt is required") {
				t.Fatal("missing prompt submitted")
			}
			m = scheduleFocus(t, m, s, schedulePrompt)
			typeText("run nightly tests")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			focus(schedulePrompt)
			typeText("read-only review")
			if s.form.prompt.Value() != "run nightly tests\nread-only review" {
				t.Fatal("Enter did not insert prompt newline")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			focus(scheduleTiming)
			m = scheduleFocus(t, m, s, scheduleSubmit)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 0 || !strings.Contains(scheduleBody(t, s, 100, 30), "Create disabled — timing") {
				t.Fatal("missing timing submitted")
			}
			m = scheduleFocus(t, m, s, scheduleTiming)
			typeText(trigger)
			if s.form.mutating {
				t.Fatal("y/n in fields toggled workspace changes")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			focus(scheduleTimezone)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if s.form.timezone.Open() || s.form.timezone.Value() != "UTC" {
				t.Fatalf("timezone acceptance did not close suggestions: open=%v value=%q query=%q compact=%v", s.form.timezone.Open(), s.form.timezone.Value(), s.form.timezone.Query(), s.compact)
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			focus(scheduleReadOnly)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 0 || s.form.mutating {
				t.Fatal("checkbox Enter submitted or toggled")
			}
			for _, want := range []bool{true, false, true} {
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
				if s.form.mutating != want || fs.createCalls != 0 {
					t.Fatal("Space must only toggle, not submit")
				}
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			focus(scheduleSubmit)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			cron := trigger
			if trigger == "every 30 minutes" {
				cron = "*/30 * * * *"
			}
			if fs.createCalls != 1 || fs.created.Name != "ny-new" || fs.created.Prompt != "run nightly tests\nread-only review" || fs.created.Mode != "default" || !fs.created.Mutating || !fs.created.Singleton || fs.created.Timezone != "UTC" || fs.created.Trigger.Cron != cron || !fs.created.Trigger.OneShot.IsZero() || s.view != schedulePanel || len(s.schedules) != 1 {
				t.Fatalf("create semantics lost: %+v", fs.created)
			}
			m = scheduleRune(t, m, "c")
			m = scheduleRune(t, m, "x")
			if s.form.name.Value() != "x" {
				t.Fatal("form text did not reach focused input")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if s.view != schedulePanel || fs.createCalls != 1 {
				t.Fatal("form backout submitted")
			}
		})
	}
}
func TestScheduleCreateRetainsFormUntilSuccess(t *testing.T) {
	for _, mutating := range []bool{false, true} {
		t.Run(fmt.Sprint(mutating), func(t *testing.T) {
			fs := &fakeScheduleLister{}
			m, s := openedSchedule(t, fs)
			m.prompt.Rewrite("unsent draft")
			m = scheduleRune(t, m, "c")
			s.form.name.SetValue("nightly")
			s.form.prompt.SetValue("review the changes\nreport findings")
			s.form.timezone.SetValue("America/New_York")
			s.form.trigger.SetValue("0 8 * * *")
			s.form.focusIdx = scheduleSubmit
			s.focusField()
			s.form.mutating = mutating
			mode := "plan"
			if mutating {
				mode = "default"
			}
			before := s.form
			if body := scheduleBody(t, s, m.width, m.vp.Height()); !strings.Contains(body, "permission mode: "+mode) {
				t.Fatal("permission mode is not visible: " + body)
			}

			mm, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			if submit == nil || s.view != scheduleCreate || s.pendingAction != "create" {
				t.Fatal("submission left the form before confirmation")
			}
			for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyTab}, navigationTestKey("y"), navigationTestKey("x"), tea.PasteMsg{Content: "hidden edit\n"}} {
				mm, duplicate := m.Update(msg)
				m = mm.(Model)
				if duplicate != nil {
					t.Fatal("pending creation accepted more input or another submission")
				}
			}
			if s.form.name.Value() != before.name.Value() || s.form.prompt.Value() != before.prompt.Value() || s.form.trigger.Value() != before.trigger.Value() || s.form.timezone.Value() != before.timezone.Value() || s.form.timezone.Query() != before.timezone.Query() || s.form.mutating != before.mutating || s.form.focusIdx != before.focusIdx {
				t.Fatal("pending creation changed form values or focus")
			}
			if body := scheduleBody(t, s, m.width, m.vp.Height()); !strings.Contains(body, "operation pending (closing does not undo it)") {
				t.Fatal("pending creation has no visible feedback: " + body)
			}

			fs.err = errors.New("invalid cron expression")
			m = feedCmd(t, m, submit)
			if fs.createCalls != 1 || fs.created.Mode != mode || fs.created.Mutating != mutating {
				t.Fatalf("creation did not request the expected permission mode: %+v", fs.created)
			}
			if s.view != scheduleCreate || s.pendingAction != "" || s.form.name.Value() != before.name.Value() || s.form.prompt.Value() != before.prompt.Value() || s.form.trigger.Value() != before.trigger.Value() || s.form.timezone.Value() != before.timezone.Value() || s.form.timezone.Query() != before.timezone.Query() || s.form.mutating != before.mutating || s.form.focusIdx != before.focusIdx {
				t.Fatal("failed creation discarded form values or focus")
			}
			if body := scheduleBody(t, s, m.width, m.vp.Height()); !strings.Contains(body, "invalid cron expression") {
				t.Fatal("creation error is not visible inline: " + body)
			}

			// Correct the rejected field using the normal input route, then retry.
			for range scheduleSubmit - scheduleTiming {
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
			for _, r := range "0 9 * * *" {
				m = scheduleRune(t, m, string(r))
			}
			for range scheduleSubmit - scheduleTiming {
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			}
			fs.err = nil
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 2 || fs.created.Trigger.Cron != "0 9 * * *" || fs.created.Timezone != "America/New_York" || fs.created.Prompt != "review the changes\nreport findings" || fs.created.Mode != mode || fs.created.Mutating != mutating || s.view != schedulePanel || s.form.name.Value() != "" || len(s.schedules) != 1 || s.actionErr != "" {
				t.Fatalf("corrected creation failed: spec=%+v, view=%d, error=%q", fs.created, s.view, s.actionErr)
			}
			if m.prompt.Value() != "unsent draft" {
				t.Fatal("form editing changed the chat draft")
			}
		})
	}
}

func TestScheduleCreatePendingBackDoesNotCancelRequest(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			fs := &fakeScheduleLister{}
			m, s := openedSchedule(t, fs)
			m.prompt.Rewrite("retained draft")
			m = scheduleRune(t, m, "c")
			s.form.name.SetValue("new")
			s.form.prompt.SetValue("review changes")
			s.form.trigger.SetValue("0 9 * * *")
			s.form.focusIdx = scheduleSubmit
			s.focusField()
			mm, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			m = mm.(Model)
			if s.view != schedulePanel || s.pendingAction != "create" || s.form.initialized {
				t.Fatal("explicit Back failed to abandon the pending form")
			}
			m = scheduleRune(t, m, "c")
			if s.view != schedulePanel || s.form.initialized {
				t.Fatal("pending mutation allowed a new independent form")
			}
			if fail {
				fs.err = errors.New("creation rejected")
			}
			result := submit()
			m = feedCmd(t, m, func() tea.Msg { return result })
			if fs.createCalls != 1 || s.view != schedulePanel || s.pendingAction != "" || m.modal != s || m.prompt.Value() != "retained draft" {
				t.Fatal("late creation result cancelled, reopened form, or changed the draft")
			}
			if fail && !strings.Contains(scheduleBody(t, s, 100, 30), "creation rejected") {
				t.Fatal("late creation error was lost")
			}
			m = scheduleRune(t, m, "c")
			if s.form.name.Value() != "" || s.form.focusIdx != scheduleName || s.form.timezone.Value() != "UTC" {
				t.Fatal("late result resurrected an abandoned form")
			}
			m = schedulePaste(m, "fresh")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			m = schedulePaste(m, "independent draft")
			stale := result.(scheduleResultMsg)
			stale.result = client.ScheduleMsg{Schedule: sampleSchedule("new")}
			m = applyAll(m, stale)
			if s.form.name.Value() != "fresh" || s.form.prompt.Value() != "independent draft" || s.form.focusIdx != schedulePrompt || s.view != scheduleCreate || m.prompt.Value() != "retained draft" || fs.createCalls != 1 {
				t.Fatal("late duplicate success cleared a new draft or changed the root chat")
			}
		})
	}
}

func TestScheduleStableIDsBackAndFallback(t *testing.T) {
	fs := &fakeScheduleLister{schedules: scheduleRows(50), fires: scheduleFires("schedule-25", 40)}
	m, s := openedSchedule(t, fs)
	s.filter.SetValue("schedule-")
	s.syncFilter()
	s.list.SetCursor(25)
	_ = m.View()
	parentID, parentOffset := s.list.CursorID(), s.list.Offset()
	m = inspectSchedule(t, m)
	s.runs.SetCursor(24)
	_ = m.View()
	fireID, fireOffset := s.runs.CursorID(), s.runs.Offset()
	m = scheduleRune(t, m, s.detailsKey())
	if s.view != scheduleDetails {
		t.Fatal("details did not open")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.view != scheduleDetails {
		t.Fatal("Choose in details activated a fire")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.view != scheduleInspect || s.runs.CursorID() != fireID || s.runs.Offset() != fireOffset {
		t.Fatal("details Back lost runs window")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.view != schedulePanel || s.filter.Value() != "schedule-" || s.list.CursorID() != parentID || s.list.Offset() != parentOffset {
		t.Fatal("runs Back lost parent window")
	}
	slices.Reverse(fs.schedules)
	m = feedCmd(t, m, s.listCmd())
	if s.list.CursorID() != parentID {
		t.Fatal("reorder lost raw name identity")
	}
	index := s.list.Cursor()
	fs.schedules = slices.Delete(fs.schedules, index, index+1)
	m = feedCmd(t, m, s.listCmd())
	want := fs.schedules[min(index, len(fs.schedules)-1)].Spec.Name
	if s.list.CursorID() != want {
		t.Fatalf("fallback = %s, want %s", s.list.CursorID(), want)
	}
	m = scheduleRune(t, m, "p")
	if fs.lastPauseName != want {
		t.Fatal("action used obsolete cursor")
	}
	_ = m
}
func TestScheduleGeometryAndLifecycle(t *testing.T) {
	row := sampleSchedule("nightly-" + strings.Repeat("界", 20))
	row.Spec.Prompt = strings.Repeat("prompt ", 40)
	row.Spec.Selector = client.ScheduleSelector{ProviderID: strings.Repeat("provider", 10), ModelID: "model"}
	row.Spec.FireTimeout = time.Minute
	row.State.Enabled = false
	row.State.DeletionPending = true
	row.State.LastFireSessionID = "pending"
	fs := &fakeScheduleLister{schedules: []client.Schedule{row}, fires: scheduleFires(row.Spec.Name, 60)}
	fs.fires[0].Stop = ""
	fs.fires[0].StartedAt = time.Unix(1, 0)
	fs.fires[0].Deadline = time.Unix(2, 0)
	m, s := openedSchedule(t, fs)
	_ = inspectSchedule(t, m)
	for _, view := range []scheduleView{schedulePanel, scheduleInspect, scheduleDetails, scheduleCreate, scheduleConfirm} {
		s.view = view
		if view == scheduleCreate {
			s.openCreate()
		}
		s.confirm = row
		for _, size := range [][2]int{{160, 40}, {100, 20}, {28, 16}, {12, 4}, {1, 1}, {0, 0}} {
			scheduleBody(t, s, size[0], size[1])
		}
	}
	detail := stripANSIstr(scheduleDetailText(s.deps.theme, row))
	for _, want := range []string{"paused", "cleanup pending", "deletion_pending: true", "claimed", "pending admission", "ownership/termination unconfirmed", "elapsed timeout does not prove termination", "run timeout: 1m0s", "workspace access: no workspace files", "permission mode: plan", "run overlap: skip overlapping runs"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("details missing %q: %s", want, detail)
		}
	}
	if strings.Contains(detail, strings.Repeat("prompt ", 30)) || strings.Contains(detail, "fire-00") {
		t.Fatal("details expanded prompt or selected fire")
	}
	fire := renderScheduleFireLine(fs.fires[0])
	for _, want := range []string{"in-flight", "overdue/degraded; termination unconfirmed", "started", "last update", "deadline"} {
		if !strings.Contains(fire, want) {
			t.Fatal(fire)
		}
	}
	s.view = scheduleDetails
	scheduleBody(t, s, 32, 12)
	s.details.Move(bounded.End, s.detailRows)
	if s.details.Offset() == 0 {
		t.Fatal("long metadata not scrollable")
	}
	scheduleBody(t, s, 120, 30)
	offset := s.details.Offset()
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if s.details.Offset() != max(0, offset-1) {
		t.Fatal("resize did not clamp immediately")
	}
}
func TestScheduleRunningLifecycleRendered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline time.Time
		overdue  bool
	}{
		{"running", time.Date(2100, 1, 2, 3, 4, 0, 0, time.UTC), false},
		{"overdue", time.Date(2000, 1, 2, 3, 4, 0, 0, time.UTC), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := sampleSchedule("nightly")
			row.State.Enabled, row.State.DeletionPending = false, true
			row.State.LastFireSessionID = "running-session"
			row.State.LastFireStartedAt = time.Date(1999, 1, 2, 3, 4, 0, 0, time.UTC)
			row.State.LastFireProgressAt = time.Date(1999, 1, 2, 5, 6, 0, 0, time.UTC)
			row.State.FireDeadline = tc.deadline
			m, s := openedSchedule(t, &fakeScheduleLister{schedules: []client.Schedule{row}, fires: scheduleFires("nightly", 1)})
			m = inspectSchedule(t, m)
			for _, view := range []scheduleView{scheduleInspect, scheduleDetails} {
				if view == scheduleDetails {
					m = scheduleRune(t, m, s.detailsKey())
				}
				body := scheduleBody(t, s, 160, 80)
				for _, want := range []string{"paused", "cleanup pending", "d: retry delete from inventory", "in-flight"} {
					if !strings.Contains(body, want) {
						t.Fatalf("view %d missing %q:\n%s", view, want, body)
					}
				}
				if strings.Contains(body, "overdue/degraded; termination unconfirmed") != tc.overdue || strings.Contains(body, "no runs recorded") || strings.Contains(body, "claimed") {
					t.Fatal("running schedule misrepresented: " + body)
				}
				if view == scheduleDetails {
					for _, want := range []string{"deletion_pending: true", "placement cleanup is pending — go back and press d to retry delete", "last run session: running-session", "running: started 1999-01-02 03:04  last update 1999-01-02 05:06  deadline " + tc.deadline.Format("2006-01-02 15:04")} {
						if !strings.Contains(body, want) {
							t.Fatalf("details missing %q:\n%s", want, body)
						}
					}
				}
			}
		})
	}
}

func TestScheduleEmptyClaimedAndErrors(t *testing.T) {
	for _, state := range []string{"empty", "inventory-empty", "no-match", "claimed", "list-error", "action-error", "create-error", "fires-error", "spec-error"} {
		t.Run(state, func(t *testing.T) {
			fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
			if state == "claimed" {
				fs.schedules[0].State.LastFireSessionID = "pending"
			}
			if state == "inventory-empty" {
				fs.schedules = nil
			}
			if state == "list-error" {
				fs.err = errors.New("list failed\x1b[31m")
			}
			m, s := openedSchedule(t, fs)
			want := "no runs recorded"
			switch state {
			case "inventory-empty":
				want = "no schedules yet"
			case "no-match":
				s.filter.SetValue("absent")
				s.syncFilter()
				want = "no matches"
			case "list-error":
				want = "list failed"
			case "action-error":
				fs.err = errors.New("pause rejected")
				m = scheduleRune(t, m, "p")
				want = "pause rejected"
			case "create-error":
				m = scheduleRune(t, m, "c")
				s.form.name.SetValue("new")
				s.form.prompt.SetValue("prompt")
				s.form.trigger.SetValue("0 9 * * *")
				s.form.focusIdx = scheduleSubmit
				fs.err = errors.New("invalid cron expression")
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
				want = "invalid cron expression"
			default:
				if state == "fires-error" {
					fs.firesErr = errors.New("fires rejected")
					fs.sched = sampleSchedule("nightly")
					fs.sched.State.Enabled = false
					want = "could not list: fires rejected"
				}
				if state == "spec-error" {
					fs.getErr = errors.New("spec rejected")
					fs.fires = scheduleFires("nightly", 1)
					want = "could not inspect: spec rejected"
				}
				m = inspectSchedule(t, m)
				if state == "claimed" {
					want = "waiting to start (session pending)"
				}
			}
			out := scheduleBody(t, s, 100, 30)
			if !strings.Contains(out, want) {
				t.Fatalf("missing %s: %s", want, out)
			}
			if state == "fires-error" && (s.inspectErr != nil || s.specPending || s.inspect.State.Enabled || !strings.Contains(out, "paused") || strings.Contains(out, "could not inspect:")) {
				t.Fatal("fires failure hid successful spec: " + out)
			}
			if state == "spec-error" && (s.firesErr != nil || s.firesLoading || s.runs.CursorID() != "fire-00" || !strings.Contains(out, "fire-00") || strings.Contains(out, "could not list:")) {
				t.Fatal("spec failure hid successful fires: " + out)
			}
			if state == "claimed" && strings.Contains(out, "no runs recorded") {
				t.Fatal("claimed mistaken for never fired")
			}
			if state == "claimed" {
				m = scheduleRune(t, m, s.detailsKey())
				detail := scheduleBody(t, s, 160, 60)
				for _, want := range []string{"claimed (session pending; termination unconfirmed)", "last run session: pending", "pending admission: ownership/termination unconfirmed; elapsed timeout does not prove termination"} {
					if !strings.Contains(detail, want) {
						t.Fatalf("claimed details missing %q: %s", want, detail)
					}
				}
			}
			if state == "action-error" {
				m = inspectSchedule(t, m)
			}
			if state == "action-error" || state == "spec-error" {
				m = scheduleRune(t, m, s.detailsKey())
				if !strings.Contains(scheduleBody(t, s, 100, 30), want) {
					t.Fatal("owned error lost across details")
				}
			}
			_ = m
		})
	}
}

// Commands are evaluated without delivering the results, deterministically
// exercising the same ownership envelopes real asynchronous commands return.
func scheduleMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, child := range batch {
			out = append(out, scheduleMessages(child)...)
		}
		return out
	}
	return []tea.Msg{msg}
}
func TestScheduleReadOwnership(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("A"), sampleSchedule("B")}})
	oldList := scheduleMessages(s.listCmd())
	old := oldList[0].(scheduleResultMsg)
	old.result = client.SchedulesMsg{Schedules: []client.Schedule{sampleSchedule("obsolete")}}
	oldList[0] = old
	newerList := scheduleMessages(s.listCmd())
	m = applyAll(m, newerList...)
	before := s.listRequest
	m = applyAll(m, oldList...)
	if s.listRequest != before || s.loading || len(s.schedules) != 2 || s.schedules[0].Spec.Name != "A" {
		t.Fatal("obsolete list accepted")
	}
	s.list.SetCursor(0)
	a := scheduleMessages(s.openInspect())
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	s.list.SetCursor(1)
	b := scheduleMessages(s.openInspect())
	m = applyAll(m, a...)
	if s.inspect.Spec.Name != "B" || !s.firesLoading || !s.specPending {
		t.Fatal("A result changed B")
	}
	m = applyAll(m, b...)
	if s.specPending || s.firesLoading {
		t.Fatal("current B result not applied")
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	s.list.SetCursor(0)
	repeatedA := scheduleMessages(s.openInspect())
	m = applyAll(m, a...)
	if !s.specPending || !s.firesLoading {
		t.Fatal("repeated A accepted old A results")
	}
	s.view = scheduleDetails
	m = applyAll(m, repeatedA...)
	if s.specPending || s.firesLoading {
		t.Fatal("details rejected owned reads")
	}
	// Payload identity is checked independently of envelope identity.
	s.inspectCmd()
	wrong := scheduleResultMsg{owner: s, kind: "spec", request: s.inspectRequest, name: "A", result: client.ScheduleMsg{Schedule: sampleSchedule("B")}}
	m = applyAll(m, wrong)
	if s.inspect.Spec.Name != "A" || !s.specPending {
		t.Fatal("wrong schedule payload applied")
	}
	stale := scheduleMessages(s.listCmd())
	s.Close()
	m.closeModal()
	mm, cmd := m.openSchedule()
	m = feedCmd(t, mm.(Model), cmd)
	next := m.modal.(*scheduleState)
	m = applyAll(m, stale...)
	if m.modal != next || next.loading {
		t.Fatal("closed owner affected reopened surface")
	}
}
func TestScheduleMutationReadOrdering(t *testing.T) {
	for _, action := range []string{"create", "paused", "deleted", "fired", "resumed"} {
		t.Run(action, func(t *testing.T) {
			fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("A")}, fireID: "new-fire"}
			m, s := openedSchedule(t, fs)
			m = inspectSchedule(t, m)
			preList := scheduleMessages(s.listCmd())
			preInspect := scheduleMessages(s.inspectCmd())
			poison := sampleSchedule("A")
			poison.Spec.Prompt = "obsolete snapshot"
			stale := preList[0].(scheduleResultMsg)
			stale.result = client.SchedulesMsg{Schedules: []client.Schedule{poison}}
			preList[0] = stale
			for i, msg := range preInspect {
				stale = msg.(scheduleResultMsg)
				if stale.kind == "spec" {
					stale.result = client.ScheduleMsg{Schedule: poison}
				} else {
					stale.result = client.ScheduleFiresMsg{Fires: scheduleFires("A", 1)}
				}
				preInspect[i] = stale
			}
			var mutation tea.Cmd
			switch action {
			case "create":
				mutation = s.mutate(client.CreateScheduleCmd(s.deps.ctx, fs, sampleSchedule("new").Spec), action, "new")
			case "paused":
				mutation = s.mutate(client.PauseScheduleCmd(s.deps.ctx, fs, "A"), action, "A")
			case "resumed":
				mutation = s.mutate(client.ResumeScheduleCmd(s.deps.ctx, fs, "A"), action, "A")
			case "deleted":
				mutation = s.mutate(client.DeleteScheduleCmd(s.deps.ctx, fs, "A"), action, "A")
			case "fired":
				mutation = s.mutate(client.FireNowCmd(s.deps.ctx, fs, "A"), action, "A")
			}
			// Deliver through Update before evaluating or completing the mutation,
			// and before newer reads could independently obsolete these envelopes.
			for _, msg := range append(preList, preInspect...) {
				m = applyAll(m, msg)
				if s.pendingAction != action || s.schedules[0].Spec.Prompt == poison.Spec.Prompt || s.inspect.Spec.Prompt == poison.Spec.Prompt || len(s.fires) != 0 {
					t.Fatalf("%s snapshot accepted while mutation pending", msg.(scheduleResultMsg).kind)
				}
			}
			if s.mutate(client.PauseScheduleCmd(s.deps.ctx, fs, "A"), "paused", "A") != nil {
				t.Fatal("duplicate pending mutation")
			}
			// A relist and inspect may run while mutation work is outstanding.
			during := scheduleMessages(s.listCmd())
			inspect := scheduleMessages(s.inspectCmd())
			result := scheduleMessages(mutation)
			if len(result) != 1 {
				t.Fatal("mutation wrapper missing")
			}
			forged := result[0].(scheduleResultMsg)
			forged.name = "wrong"
			m = applyAll(m, forged)
			if s.pendingAction == "" {
				t.Fatal("wrong mutation target accepted")
			}
			if action != "create" {
				forged = result[0].(scheduleResultMsg)
				forged.result = client.ScheduleActionMsg{Name: forged.name, Action: "wrong"}
				m = applyAll(m, forged)
				if s.pendingAction == "" {
					t.Fatal("wrong action receipt accepted")
				}
			}
			mm, refresh := m.Update(result[0])
			m = mm.(Model)
			if s.pendingAction != "" || refresh == nil {
				t.Fatal("owned mutation discarded after another request")
			}
			m = applyAll(m, preList...)
			m = applyAll(m, preInspect...)
			m = applyAll(m, during...)
			m = applyAll(m, inspect...)
			if !s.loading || !s.specPending || !s.firesLoading {
				t.Fatal("pre-completion read overwrote authoritative refresh")
			}
			m = feedCmd(t, m, refresh)
			if action == "create" && len(s.schedules) != 2 {
				t.Fatal("list-before-create erased confirmed creation")
			}
			_, duplicate := m.Update(result[0])
			if duplicate != nil {
				t.Fatal("duplicate mutation completion relisted")
			}
		})
	}
}
func TestScheduleMutationCompletionWhileInspectingDifferentSchedule(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("A"), sampleSchedule("B")}, fires: scheduleFires("B", 2)}
	m, s := openedSchedule(t, fs)
	mm, pause := m.Update(navigationTestKey("p"))
	m = mm.(Model)
	s.list.SetCursor(1)
	m = inspectSchedule(t, m)
	if s.inspect.Spec.Name != "B" || s.pendingName != "A" {
		t.Fatal("navigation did not remain available during mutation")
	}
	m = feedCmd(t, m, pause)
	if s.pendingAction != "" || s.inspect.Spec.Name != "B" || fs.lastPauseName != "A" || fs.lastGetname != "B" {
		t.Fatal("mutation completion lost ownership or refreshed wrong schedule")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	mm, pause = m.Update(navigationTestKey("p"))
	m = mm.(Model)
	fs.err = errors.New("mutation rejected")
	result := scheduleMessages(pause)
	fs.err = errors.New("inspect rejected")
	m = inspectSchedule(t, m)
	mm, refresh := m.Update(result[0])
	_ = feedCmd(t, mm.(Model), refresh)
	out := scheduleBody(t, s, 100, 30)
	if !strings.Contains(out, "mutation rejected") || !strings.Contains(out, "inspect rejected") {
		t.Fatal("inspect read error overwrote owned mutation error: " + out)
	}
}

func TestSchedulePendingMutationAllowsNavigationAndKeepsError(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("A")}}
	m, s := openedSchedule(t, fs)
	mm, cmd := m.Update(navigationTestKey("p"))
	m = mm.(Model)
	fs.err = errors.New("pause rejected")
	pending := scheduleMessages(cmd)
	for _, action := range []string{"p", "r", "f", "d", "c"} {
		mm, duplicate := m.Update(navigationTestKey(action))
		m = mm.(Model)
		if duplicate != nil || s.view != schedulePanel || s.pendingAction != "paused" {
			t.Fatal("pending mutation allowed duplicate/overlapping action")
		}
	}
	m = inspectSchedule(t, m)
	m = scheduleRune(t, m, s.detailsKey())
	mm, refresh := m.Update(pending[0])
	m = feedCmd(t, mm.(Model), refresh)
	if s.pendingAction != "" || !strings.Contains(scheduleBody(t, s, 100, 30), "pause rejected") {
		t.Fatal("owned mutation error lost while navigating")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(scheduleBody(t, s, 100, 30), "pause rejected") {
		t.Fatal("error lost on Back")
	}
	// Closing does not claim to roll a mutation back; late completion is ignored.
	s.view = schedulePanel
	fs.err = nil
	m = feedCmd(t, m, s.listCmd())
	mm, cmd = m.Update(navigationTestKey("f"))
	m = mm.(Model)
	late := scheduleMessages(cmd)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = applyAll(m, late...)
	if m.modal != nil || fs.fireCalls != 1 {
		t.Fatal("close cancelled or resurrected completed server action")
	}
	mm, cmd = m.openSchedule()
	m = feedCmd(t, mm.(Model), cmd)
	reopened := m.modal.(*scheduleState)
	m = applyAll(m, late...)
	if m.modal != reopened || reopened.pendingAction != "" || reopened.notice != "" {
		t.Fatal("old mutation receipt affected reopened surface")
	}
}
func TestScheduleTranscriptHandoff(t *testing.T) {
	for _, loadError := range []bool{false, true} {
		t.Run(fmt.Sprint(loadError), func(t *testing.T) {
			fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}, fires: scheduleFires("nightly", 3)}
			m, s := openedSchedule(t, fs)
			loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: "session-01", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "scheduled output"}}}}
			if loadError {
				loader.err = errors.New("load failed")
			}
			inventory := &fakeSessionLister{sessions: []client.SessionListItem{{ID: "chat", Kind: client.SessionKindMain}}}
			m.deps.Transcript, m.deps.Sessions = loader, inventory
			s.transcriptWired = true
			m.prompt.Rewrite("keep my draft")
			m.conv.addUser("active conversation")
			activeID, chat := m.sessionID, m.rend.renderConversation(&m.conv, false)
			m = inspectSchedule(t, m)
			s.runs.SetCursor(1)
			late := scheduleMessages(s.inspectCmd())
			// Resolve the current reads, then activate through Update, not a direct helper.
			m = applyAll(m, late...)
			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			replacement, ok := m.modal.(*sessionsState)
			if !ok || !s.closed || m.phase != phaseReplay {
				t.Fatal("generic close killed replacement Sessions")
			}
			m = feedCmd(t, m, cmd)
			if len(loader.calls) != 1 || loader.calls[0] != "session-01" || (replacement.loadErr != nil) != loadError {
				t.Fatal("wrong transcript target/result")
			}
			m = applyAll(m, late...)
			if m.modal != replacement {
				t.Fatal("late Schedule result killed Sessions")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.modal != replacement || replacement.view != sessionsPanel || replacement.loading || len(replacement.sessions) != 1 {
				t.Fatalf("Back did not populate usable Sessions inventory: %+v", replacement)
			}
			if m.phase != phaseIdle || m.sessionID != activeID || m.prompt.Value() != "keep my draft" || m.rend.renderConversation(&m.conv, false) != chat {
				t.Fatal("inspection rebound/changed active chat or draft")
			}
		})
	}
}
func TestScheduleTranscriptMissingAndCompact(t *testing.T) {
	for _, missing := range []string{"loader", "session", "compact"} {
		t.Run(missing, func(t *testing.T) {
			fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("A")}, fires: scheduleFires("A", 1)}
			m, s := openedSchedule(t, fs)
			if missing != "loader" {
				m.deps.Transcript = &fakeSessionTranscriptLoader{}
				s.transcriptWired = true
			}
			if missing == "session" {
				fs.fires[0].SessionID = ""
			}
			m = inspectSchedule(t, m)
			if missing == "compact" {
				m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0})
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.modal != s || m.phase != phaseIdle {
				t.Fatal("unavailable/hidden transcript replaced Schedule")
			}
			if missing != "compact" && s.notice == "" {
				t.Fatal("missing transcript lacks feedback")
			}
		})
	}
}
func TestScheduleSpecUsesUserFacingLabels(t *testing.T) {
	for _, tc := range []struct {
		spec client.ScheduleSpec
		want []string
	}{
		{client.ScheduleSpec{Trigger: client.ScheduleTrigger{Cron: "*/5 * * * *"}}, []string{"run overlap: allowed", "missed run: run once now", "run limit: unlimited"}},
		{client.ScheduleSpec{Trigger: client.ScheduleTrigger{Cron: "*/5 * * * *"}, Singleton: true, Misfire: "skip", MaxFires: 3}, []string{"run overlap: skip overlapping runs", "missed run: skip and wait for the next one", "run limit: 3"}},
		{client.ScheduleSpec{Trigger: client.ScheduleTrigger{OneShot: time.Unix(1, 0)}}, []string{"run limit: one run"}},
	} {
		var b strings.Builder
		renderScheduleSpecBlock(&b, testTheme().Style("muted"), tc.spec)
		for _, want := range tc.want {
			if !strings.Contains(stripANSIstr(b.String()), want) {
				t.Fatal(b.String())
			}
		}
	}
	rows := []client.Schedule{sampleSchedule("cron"), {Spec: client.ScheduleSpec{Name: "once", Trigger: client.ScheduleTrigger{OneShot: time.Unix(1, 0)}}}}
	if len(filterSchedules(rows, "cron:")) != 1 || !strings.Contains(triggerSummary(rows[1].Spec), "one-shot:") || formatScheduleTime(time.Time{}) != "—" {
		t.Fatal("trigger filter/format lost")
	}
}

// TestScheduleCreateFormModeFollowsMutating pins the mode the create form
// sends. A read-leaning schedule must name plan, which the server requires; an
// empty mode there was refused. A mutating one leaves mode unset so its fires
// use the server's default session mode rather than an explicit default that
// asks with no approver on an allow-all server.
func TestScheduleCreateFormModeFollowsMutating(t *testing.T) {
	for _, tc := range []struct {
		toggle   string
		mutating bool
		wantMode string
	}{
		{toggle: "y", mutating: true, wantMode: ""},
		{toggle: "n", mutating: false, wantMode: "plan"},
	} {
		t.Run("mutating="+tc.toggle, func(t *testing.T) {
			fs := &fakeScheduleLister{sched: sampleSchedule("s")}
			m := newScheduleModel(t, newScheduleConv(scheduleCaps()), fs, scheduleCaps())
			mm, _ := m.openSchedule()
			m = applyAll(mm.(Model), client.SchedulesMsg{Schedules: nil})
			press := func(k tea.KeyPressMsg) tea.Cmd {
				mm, cmd, _ := m.onScheduleKey(k)
				m = mm.(Model)
				return cmd
			}
			typeText := func(s string) {
				for _, r := range s {
					press(tea.KeyPressMsg{Code: r, Text: string(r)})
				}
			}
			tab := tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"}
			press(tea.KeyPressMsg{Code: 'c', Text: "c"})
			typeText("s")
			press(tab)
			typeText("run it")
			press(tab)
			typeText("0 9 * * *")
			press(tab)
			press(tea.KeyPressMsg{Code: rune(tc.toggle[0]), Text: tc.toggle})
			cmd := press(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd == nil {
				t.Fatalf("submit returned no command (actionErr=%q)", m.schedule.actionErr)
			}
			m = feedCmd(t, m, cmd)
			got := fs.lastCreateSpec
			if fs.createCalls != 1 || got.Mutating != tc.mutating || got.Mode != tc.wantMode {
				t.Fatalf("create sent (mutating %t, mode %q) in %d calls, want (mutating %t, mode %q) once",
					got.Mutating, got.Mode, fs.createCalls, tc.mutating, tc.wantMode)
			}
		})
	}
}
