package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// fakeScheduleLister is a spy client.ScheduleLister for the /schedule overlay
// tests (issue #234): it records calls and returns canned results.
type fakeScheduleLister struct {
	schedules []client.Schedule
	sched     client.Schedule
	fires     []client.ScheduleFire
	fireID    string
	err       error

	listCalls   int
	getCalls    int
	createCalls int
	deleteCalls int
	fireCalls   int
	pauseCalls  int
	resumeCalls int
	firesCalls  int

	lastDeleteName string
	lastPauseName  string
	lastResumeName string
	lastFireName   string
	lastFiresName  string
	lastGetname    string
}

func (f *fakeScheduleLister) ListSchedules(_ context.Context) ([]client.Schedule, error) {
	f.listCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.schedules, nil
}

func (f *fakeScheduleLister) GetSchedule(_ context.Context, name string) (client.Schedule, error) {
	f.getCalls++
	f.lastGetname = name
	if f.err != nil {
		return client.Schedule{}, f.err
	}
	return f.sched, nil
}

func (f *fakeScheduleLister) CreateSchedule(_ context.Context, _ client.ScheduleSpec) (client.Schedule, error) {
	f.createCalls++
	if f.err != nil {
		return client.Schedule{}, f.err
	}
	return f.sched, nil
}

func (f *fakeScheduleLister) DeleteSchedule(_ context.Context, name string) error {
	f.deleteCalls++
	f.lastDeleteName = name
	return f.err
}

func (f *fakeScheduleLister) FireNow(_ context.Context, name string) (string, string, error) {
	f.fireCalls++
	f.lastFireName = name
	if f.err != nil {
		return "", "", f.err
	}
	return f.fireID, f.fireID, nil
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
	if f.err != nil {
		return nil, f.err
	}
	return f.fires, nil
}

func scheduleCaps() client.Capabilities { return client.Capabilities{Scheduling: true} }

func sampleSchedule(name string) client.Schedule {
	oneShot := time.Date(2026, 7, 6, 14, 0, 0, 0, time.UTC)
	return client.Schedule{
		Spec: client.ScheduleSpec{
			Name:     name,
			Prompt:   "run the tests",
			Trigger:  client.ScheduleTrigger{OneShot: oneShot},
			Mode:     "default",
			Misfire:  "fire_once_now",
			MaxFires: 0,
			Selector: client.ScheduleSelector{ProviderID: "openai", ModelID: "gpt-5"},
		},
		State: client.ScheduleState{
			NextFireAt: oneShot,
			FireCount:  1,
			Enabled:    true,
		},
	}
}

func newScheduleModel(t *testing.T, conv *fakeConv, fs *fakeScheduleLister, caps client.Capabilities) Model {
	t.Helper()
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Sched:       fs,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(
		m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{
			SessionID:    "sess-test-0001",
			Capabilities: caps,
		},
	)
	return m
}

func newScheduleConv(caps client.Capabilities) *fakeConv {
	return &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: caps}
}

// TestRunScheduleOpensOverlay asserts runSchedule opens the panel, blurs the
// input, fires ListSchedules, and renders the rows once the result lands.
func TestRunScheduleOpensOverlay(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{
		sampleSchedule("nightly"),
		sampleSchedule("hourly"),
	}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())

	mm, cmd := m.runSchedule()
	m = mm.(Model)
	if m.schedule.view != schedulePanel {
		t.Fatalf("view = %v, want schedulePanel", m.schedule.view)
	}
	if !m.schedule.loading {
		t.Error("overlay should be loading until ListSchedules lands")
	}
	if m.ta.Focused() {
		t.Error("opening the overlay should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runSchedule should fire the ListSchedules RPC command")
	}
	m = feedCmd(t, m, cmd)
	if fs.listCalls != 1 {
		t.Errorf("ListSchedules calls = %d, want 1", fs.listCalls)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "nightly") {
		t.Errorf("overlay missing a schedule name:\n%s", stripANSIstr(m.View().Content))
	}
}

// TestRunScheduleNilGuard: with no lister wired, openSchedule is a no-op.
func TestRunScheduleNilGuard(t *testing.T) {
	conv := newScheduleConv(client.Capabilities{})
	m := New(Deps{
		Session: conv, Conv: conv, Theme: theme.New("aztec", theme.AztecPalette()),
		Workspace: "/ws", Ctx: context.Background(), NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40}, client.SessionReadyMsg{SessionID: "s1"})
	mm, cmd := m.openSchedule()
	m = mm.(Model)
	if m.schedule.view != scheduleNone {
		t.Fatalf("openSchedule with nil lister should be a no-op, view = %v", m.schedule.view)
	}
	if cmd != nil {
		t.Errorf("openSchedule with nil lister should fire no command, got %v", cmd)
	}
}

// TestScheduleFilterNarrows: the filter input narrows the list by name.
func TestScheduleFilterNarrows(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{
		sampleSchedule("nightly"),
		sampleSchedule("hourly"),
	}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	if len(m.schedule.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 before filter", len(m.schedule.filtered))
	}
	m.schedule.filter.SetValue("night")
	m = m.syncScheduleFilter()
	if len(m.schedule.filtered) != 1 {
		t.Fatalf("filtered = %d, want 1 after 'night'", len(m.schedule.filtered))
	}
	if m.schedule.filtered[0].Spec.Name != "nightly" {
		t.Errorf("filtered[0] = %+v, want nightly", m.schedule.filtered[0])
	}
}

// TestScheduleFilterMatchesTriggerSummary: the filter also matches on the
// trigger summary (cron expression), so filtering by "cron" finds cron schedules.
func TestScheduleFilterMatchesTriggerSummary(t *testing.T) {
	scheds := []client.Schedule{
		{Spec: client.ScheduleSpec{Name: "every5", Trigger: client.ScheduleTrigger{Cron: "*/5 * * * *"}}},
		{Spec: client.ScheduleSpec{Name: "oneshot", Trigger: client.ScheduleTrigger{OneShot: time.Now()}}},
	}
	got := filterSchedules(scheds, "cron")
	if len(got) != 1 || got[0].Spec.Name != "every5" {
		t.Fatalf("filter by trigger summary = %+v, want every5", got)
	}
}

// TestSchedulePauseResumeFireNowDelete asserts each action key fires the right
// RPC and refreshes the list on success.
func TestSchedulePauseResumeFireNowDelete(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		fireID:    "fire-1",
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0

	// p — pause. feedCmd runs PauseScheduleCmd → ScheduleActionMsg → re-list.
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if fs.pauseCalls != 1 || fs.lastPauseName != "nightly" {
		t.Fatalf("pause calls = %d name=%q, want 1/nightly", fs.pauseCalls, fs.lastPauseName)
	}
	if fs.listCalls != 1 {
		t.Errorf("after pause, list calls = %d, want 1 (the re-list)", fs.listCalls)
	}

	// r — resume
	mm, cmd, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if fs.resumeCalls != 1 || fs.lastResumeName != "nightly" {
		t.Fatalf("resume calls = %d name=%q, want 1/nightly", fs.resumeCalls, fs.lastResumeName)
	}
	if fs.listCalls != 2 {
		t.Errorf("after resume, list calls = %d, want 2", fs.listCalls)
	}

	// f — fire-now
	mm, cmd, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if fs.fireCalls != 1 || fs.lastFireName != "nightly" {
		t.Fatalf("fire calls = %d name=%q, want 1/nightly", fs.fireCalls, fs.lastFireName)
	}
	if fs.listCalls != 3 {
		t.Errorf("after fire-now, list calls = %d, want 3", fs.listCalls)
	}

	// d — open confirm, then enter — delete
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	if m.schedule.view != scheduleConfirm {
		t.Fatalf("after d: view = %v, want scheduleConfirm", m.schedule.view)
	}
	mm, cmd, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if fs.deleteCalls != 1 || fs.lastDeleteName != "nightly" {
		t.Fatalf("delete calls = %d name=%q, want 1/nightly", fs.deleteCalls, fs.lastDeleteName)
	}
	if fs.listCalls != 4 {
		t.Errorf("after delete, list calls = %d, want 4", fs.listCalls)
	}
}

// TestScheduleFilterModeDoesNotFireActions asserts the "/"-to-filter fix
// (collision between the single-letter action keys and the filter input): the
// overlay opens in ACTION mode (filter not focused), "/" enters filter mode,
// letters that double as action keys (p/r) are fed to the filter instead of
// firing pause/resume while filtering, esc exits filter mode keeping the
// value, and the SAME letter fires its action once back in action mode.
func TestScheduleFilterModeDoesNotFireActions(t *testing.T) {
	// Named "production" (not just "prod") so filtering down to "prod" still
	// matches the row — the point being tested is the key-routing, not the
	// filter's substring semantics.
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("production")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0

	if m.schedule.filter.Focused() {
		t.Fatal("openSchedule should NOT focus the filter (action mode by default)")
	}

	// "/" enters filter mode.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = mm.(Model)
	if !m.schedule.filter.Focused() {
		t.Fatal("'/' should focus the filter")
	}

	// p, r, o, d while filtering must feed the input, not fire actions.
	for _, r := range "prod" {
		mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
		if cmd != nil {
			m = feedCmd(t, m, cmd)
		}
	}
	if m.schedule.filter.Value() != "prod" {
		t.Fatalf("filter value = %q, want %q", m.schedule.filter.Value(), "prod")
	}
	if fs.pauseCalls != 0 || fs.resumeCalls != 0 {
		t.Fatalf("typing p/r while filtering must not fire actions: pause=%d resume=%d",
			fs.pauseCalls, fs.resumeCalls)
	}

	// esc blurs the filter but KEEPS the value.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.schedule.filter.Focused() {
		t.Fatal("esc in filter mode should blur, not close")
	}
	if m.schedule.filter.Value() != "prod" {
		t.Fatalf("esc should keep the filter value, got %q", m.schedule.filter.Value())
	}

	// Now back in action mode, 'p' fires pause.
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("'p' in action mode should return the pause command")
	}
	m = feedCmd(t, m, cmd)
	if fs.pauseCalls != 1 {
		t.Fatalf("pause calls = %d, want 1", fs.pauseCalls)
	}
}

// TestScheduleInspectLoadsFires asserts enter opens the inspect sub-view, fires
// GetSchedule + ListFires, and renders the fires.
func TestScheduleInspectLoadsFires(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-1", Stop: "end_turn"},
		},
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0

	// enter — open inspect
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.schedule.view != scheduleInspect {
		t.Fatalf("after enter: view = %v, want scheduleInspect", m.schedule.view)
	}
	if !m.schedule.firesLoading {
		t.Error("fires should be loading until ListFires lands")
	}
	m = feedCmd(t, m, cmd) // runs GetSchedule + ListFires
	if fs.getCalls != 1 || fs.lastGetname != "nightly" {
		t.Errorf("get calls = %d name=%q, want 1/nightly", fs.getCalls, fs.lastGetname)
	}
	if fs.firesCalls != 1 || fs.lastFiresName != "nightly" {
		t.Errorf("fires calls = %d name=%q, want 1/nightly", fs.firesCalls, fs.lastFiresName)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "fire-1") {
		t.Errorf("inspect missing a fire id:\n%s", out)
	}
	if !strings.Contains(out, "end_turn") {
		t.Errorf("inspect missing a fire stop:\n%s", out)
	}
}

// TestScheduleConfirmDeleteBackout asserts esc backs out of the confirm sub-view
// without deleting.
func TestScheduleConfirmDeleteBackout(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0

	// d — open confirm
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = mm.(Model)
	if m.schedule.view != scheduleConfirm {
		t.Fatalf("after d: view = %v, want scheduleConfirm", m.schedule.view)
	}
	// esc — back to panel (no delete)
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.schedule.view != schedulePanel {
		t.Fatalf("after esc: view = %v, want schedulePanel", m.schedule.view)
	}
	if fs.deleteCalls != 0 {
		t.Errorf("esc should NOT delete, deleteCalls = %d", fs.deleteCalls)
	}
}

// TestScheduleErrorRender asserts a ListSchedules error renders an error line.
func TestScheduleErrorRender(t *testing.T) {
	fs := &fakeScheduleLister{err: errors.New("store unreachable")}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, cmd := m.openSchedule()
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "could not list schedules") {
		t.Errorf("overlay should render the error line:\n%s", out)
	}
	if !strings.Contains(out, "store unreachable") {
		t.Errorf("overlay should render the error text:\n%s", out)
	}
}

// TestScheduleEscCloses: esc closes the overlay and refocuses the prompt.
func TestScheduleEscCloses(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.schedule.view != scheduleNone {
		t.Fatalf("esc should close the overlay, view = %v", m.schedule.view)
	}
}

// TestScheduleEmptyRender asserts an empty list renders an honest hint pointing
// at the author paths (CLI / settings.yaml), not an opaque "no schedules".
func TestScheduleEmptyRender(t *testing.T) {
	fs := &fakeScheduleLister{schedules: nil}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, cmd := m.openSchedule()
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "no schedules found") {
		t.Errorf("overlay should render the empty hint:\n%s", out)
	}
}

// TestTriggerSummary asserts the trigger summary renders cron vs one-shot
// distinctly.
func TestTriggerSummary(t *testing.T) {
	cron := triggerSummary(client.ScheduleSpec{Trigger: client.ScheduleTrigger{Cron: "*/5 * * * *"}})
	if cron != "cron: */5 * * * *" {
		t.Errorf("cron summary = %q", cron)
	}
	oneShot := triggerSummary(client.ScheduleSpec{Trigger: client.ScheduleTrigger{OneShot: time.Date(2026, 7, 6, 14, 0, 0, 0, time.UTC)}})
	if !strings.HasPrefix(oneShot, "one-shot: ") {
		t.Errorf("one-shot summary = %q", oneShot)
	}
}

// TestScheduleActionErrorRender asserts a failed action RPC surfaces the error
// line in the panel render. Mirrors TestScheduleErrorRender: drive openSchedule →
// SchedulesMsg → the action key → ScheduleActionMsg{Err: ...}, then assert the
// renderSchedulePanel output contains the error text.
func TestScheduleActionErrorRender(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0

	// p — pause. The lister returns an error from PauseSchedule (the err field
	// is shared across methods, so set it AFTER ListSchedules already landed).
	fs.err = errors.New("schedule is paused; nothing to resume")
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = mm.(Model)
	m = feedCmd(t, m, cmd) // runs PauseScheduleCmd → ScheduleActionMsg{Err: ...}
	if fs.pauseCalls != 1 {
		t.Fatalf("pause calls = %d, want 1", fs.pauseCalls)
	}
	if m.schedule.actionErr == "" {
		t.Fatal("actionErr should be set after a failed action")
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "action failed") {
		t.Errorf("overlay should render the action-failed line:\n%s", out)
	}
	if !strings.Contains(out, "schedule is paused") {
		t.Errorf("overlay should render the action error text:\n%s", out)
	}
}

// TestRunScheduleNotIdle: opening mid-run is a no-op — the phaseIdle guard at the
// top of openSchedule keeps the overlay closed and fires no RPC. Mirrors
// TestRunWorktreesNotIdle.
func TestRunScheduleNotIdle(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	m.phase = phaseRunning // mid-run
	mm, cmd := m.openSchedule()
	m = mm.(Model)
	if m.schedule.view != scheduleNone {
		t.Fatalf("openSchedule mid-run should be a no-op, view = %v", m.schedule.view)
	}
	if cmd != nil {
		t.Errorf("openSchedule mid-run should fire no command, got %v", cmd)
	}
	if fs.listCalls != 0 {
		t.Errorf("openSchedule mid-run should not call ListSchedules, got %d", fs.listCalls)
	}
}

// newScheduleModelWithReplayer builds a schedule model that ALSO wires a
// session lister + replayer (so the jump-to-fire shortcut's switchToSession
// handoff has a replayer to call). Mirrors newScheduleModel + newSessionsModel.
func newScheduleModelWithReplayer(t *testing.T, conv *fakeConv, fs *fakeScheduleLister, caps client.Capabilities, fr *fakeSessionReplayer) Model {
	t.Helper()
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		Sched:       fs,
		Sessions:    &fakeSessionLister{},
		Replayer:    fr,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	}
	if fr == nil {
		deps.Replayer = nil
	}
	m := New(deps)
	m = applyAll(
		m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m
}

// openInspectWithFires drives openSchedule → SchedulesMsg → enter (inspect) →
// GetSchedule+ListFires, returning the model in the inspect sub-view with the
// fires loaded. Shared by the jump-to-fire tests.
func openInspectWithFires(t *testing.T, m Model, fs *fakeScheduleLister) Model {
	t.Helper()
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})
	m.schedule.cursor = 0
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedCmd(t, m, cmd) // GetSchedule + ListFires
	if m.schedule.view != scheduleInspect {
		t.Fatalf("setup: view = %v, want scheduleInspect", m.schedule.view)
	}
	return m
}

// TestScheduleInspectJumpToFireTranscript asserts enter on a fire cursor row
// jumps to the fire's session: since a fire session id is top-level, the
// continue-by-default handoff opens the replay stream (loading the prior
// conversation) with continueOnLoad set, and on stream close transitions to
// phaseIdle (live/interactive). The schedule overlay is cleared (scheduleNone).
func TestScheduleInspectJumpToFireTranscript(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-fire-1", Stop: "end_turn"},
			{ID: "fire-2", ScheduleName: "nightly", SessionID: "sess-fire-2", Stop: "end_turn"},
		},
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithReplayer(t, conv, fs, scheduleCaps(), fr)
	m = openInspectWithFires(t, m, fs)

	// Move cursor down to fire-2, then enter → jump.
	mm, _, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.schedule.fireCursor != 1 {
		t.Fatalf("fireCursor = %d, want 1", m.schedule.fireCursor)
	}
	mm, _, _ = m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	// A fire session id is top-level → continue-by-default → opens the replay
	// stream (loading) with continueOnLoad set.
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay (loading history)", m.phase)
	}
	if !m.sessions.continueOnLoad {
		t.Error("continueOnLoad should be true (top-level fire session)")
	}
	if m.sessionID != "sess-fire-2" {
		t.Fatalf("sessionID = %q, want sess-fire-2", m.sessionID)
	}
	if m.schedule.view != scheduleNone {
		t.Fatalf("schedule view = %v, want scheduleNone (cleared on jump)", m.schedule.view)
	}
	if fr.calls != 1 || fr.lastID != "sess-fire-2" {
		t.Fatalf("replayer calls=%d lastID=%q, want 1/sess-fire-2", fr.calls, fr.lastID)
	}

	// Stream closes → carry transcript → phaseIdle (continue).
	mm, _ = m.updateReplayMsg(replayMsg{gen: m.sessions.replayGen, msg: client.StreamClosedMsg{}})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("after StreamClosed: phase = %v, want phaseIdle (continue-by-default)", m.phase)
	}
	if m.sessionID != "sess-fire-2" {
		t.Fatalf("sessionID = %q, want sess-fire-2 (kept)", m.sessionID)
	}
}

// TestScheduleInspectJumpToFireNoSessionID asserts a fire with an empty
// SessionID sets a statusMsg about "no session id" and stays in the inspect
// sub-view (no jump).
func TestScheduleInspectJumpToFireNoSessionID(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "", Stop: "end_turn"},
		},
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithReplayer(t, conv, fs, scheduleCaps(), fr)
	m = openInspectWithFires(t, m, fs)

	mm, _, _ := m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle (no jump)", m.phase)
	}
	if m.schedule.view != scheduleInspect {
		t.Fatalf("view = %v, want scheduleInspect (stayed)", m.schedule.view)
	}
	if fr.calls != 0 {
		t.Errorf("replayer should NOT be called, got %d calls", fr.calls)
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "no session id") {
		t.Errorf("statusMsg = %q, want it to mention 'no session id'", got)
	}
}

// TestScheduleInspectJumpToFireNoReplayer asserts that with no Replayer wired,
// enter/t is a no-op (no jump, no statusMsg) and the footer hint does NOT
// advertise the transcript action.
func TestScheduleInspectJumpToFireNoReplayer(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-fire-1", Stop: "end_turn"},
		},
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithReplayer(t, conv, fs, scheduleCaps(), nil) // no replayer
	m = openInspectWithFires(t, m, fs)

	mm, _, _ := m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle (no-op)", m.phase)
	}
	if m.schedule.view != scheduleInspect {
		t.Fatalf("view = %v, want scheduleInspect (stayed)", m.schedule.view)
	}
	// 't' is also a no-op.
	mm, _, _ = m.onScheduleInspectKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.schedule.view != scheduleInspect {
		t.Fatalf("view = %v, want scheduleInspect (stayed after 't')", m.schedule.view)
	}
	out := stripANSIstr(m.View().Content)
	if strings.Contains(out, "open transcript") {
		t.Errorf("footer hint should NOT advertise the transcript action without a replayer:\n%s", out)
	}
	if !strings.Contains(out, "esc: back") {
		t.Errorf("footer hint should still show 'esc: back':\n%s", out)
	}
}

// TestScheduleInspectFireCursorNavigation asserts ↑/↓ move the fireCursor,
// clamped at the bounds, and the render reflects the cursor highlight (the ▶
// marker on the cursor row).
func TestScheduleInspectFireCursorNavigation(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-1", Stop: "end_turn"},
			{ID: "fire-2", ScheduleName: "nightly", SessionID: "sess-2", Stop: "end_turn"},
			{ID: "fire-3", ScheduleName: "nightly", SessionID: "sess-3", Stop: "end_turn"},
		},
	}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithReplayer(t, conv, fs, scheduleCaps(), fr)
	m = openInspectWithFires(t, m, fs)
	if m.schedule.fireCursor != 0 {
		t.Fatalf("initial fireCursor = %d, want 0", m.schedule.fireCursor)
	}

	// Down twice → cursor 2.
	m = applyAll(
		m,
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyDown},
	)
	if m.schedule.fireCursor != 2 {
		t.Fatalf("fireCursor = %d, want 2", m.schedule.fireCursor)
	}
	// Down once more → clamped at 2 (len-1).
	mm, _, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.schedule.fireCursor != 2 {
		t.Fatalf("fireCursor = %d, want 2 (clamped)", m.schedule.fireCursor)
	}
	// Up once → cursor 1.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	if m.schedule.fireCursor != 1 {
		t.Fatalf("fireCursor = %d, want 1", m.schedule.fireCursor)
	}
	// Up to 0, then up once more → clamped at 0.
	m = applyAll(
		m,
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.KeyPressMsg{Code: tea.KeyUp},
	)
	if m.schedule.fireCursor != 0 {
		t.Fatalf("fireCursor = %d, want 0 (clamped)", m.schedule.fireCursor)
	}
	// Render reflects the cursor: the ▶ marker is on fire-1 (cursor 0), the
	// other rows have the blank marker.
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "▶ fire-1") {
		t.Errorf("render should highlight fire-1 with ▶:\n%s", out)
	}
	if strings.Contains(out, "▶ fire-2") || strings.Contains(out, "▶ fire-3") {
		t.Errorf("only the cursor row should be highlighted:\n%s", out)
	}
}
