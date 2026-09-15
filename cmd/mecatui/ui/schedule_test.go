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
	m := newTestModelFromDeps(Deps{
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
	if m.prompt.Focused() {
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
	m := newTestModelFromDeps(Deps{
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
		// textinput mutates synchronously; cmd is only its delayed cursor blink.
		_ = cmd
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

func TestScheduleDeletionPendingRenderIncludesRetryGuidance(t *testing.T) {
	sched := sampleSchedule("cleanup")
	sched.State.Enabled = false
	sched.State.DeletionPending = true
	fs := &fakeScheduleLister{schedules: []client.Schedule{sched}, sched: sched}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})

	panel := stripANSIstr(m.View().Content)
	for _, want := range []string{"cleanup pending", "retry delete"} {
		if !strings.Contains(panel, want) {
			t.Errorf("schedule panel missing %q:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, "  paused") {
		t.Errorf("schedule panel mislabeled pending cleanup as paused:\n%s", panel)
	}

	m.schedule.cursor = 0
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	inspect := stripANSIstr(m.View().Content)
	for _, want := range []string{"deletion_pending: true", "cleanup is pending", "retry delete"} {
		if !strings.Contains(inspect, want) {
			t.Errorf("schedule inspect missing %q:\n%s", want, inspect)
		}
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

// TestScheduleCreateErrorRender: a Create that fails server-side (bad cron,
// duplicate name, validation) surfaces the error in the panel instead of
// silently closing the form. The form has already closed to schedulePanel on
// submit, so the ScheduleMsg error path must set actionErr for the user to see
// any feedback. Mirrors TestScheduleActionErrorRender.
func TestScheduleCreateErrorRender(t *testing.T) {
	fs := &fakeScheduleLister{}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: nil})

	// Simulate the CreateScheduleCmd result arriving as a ScheduleMsg carrying a
	// server rejection.
	m = applyAll(m, client.ScheduleMsg{Err: errors.New("invalid cron expression")})

	if m.schedule.actionErr == "" {
		t.Fatal("actionErr should be set after a failed Create")
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "action failed") {
		t.Errorf("panel should render the action-failed line:\n%s", out)
	}
	if !strings.Contains(out, "invalid cron expression") {
		t.Errorf("panel should render the create error text:\n%s", out)
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

// newScheduleModelWithTranscript builds a schedule model that also wires the
// authoritative transcript loader used by the jump-to-fire inspection path.
func newScheduleModelWithTranscript(t *testing.T, conv *fakeConv, fs *fakeScheduleLister, caps client.Capabilities, loader *fakeSessionTranscriptLoader) Model {
	t.Helper()
	deps := Deps{
		Session:     conv,
		Conv:        conv,
		Sched:       fs,
		Sessions:    &fakeSessionLister{},
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	}
	if loader != nil {
		deps.Transcript = loader
	}
	m := newTestModelFromDeps(deps)
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

// TestScheduleInspectJumpToFireTranscript asserts Enter on a fire cursor row
// loads the authoritative transcript for read-only inspection without rebinding
// the active chat. The schedule overlay is cleared.
func TestScheduleInspectJumpToFireTranscript(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-fire-1", Stop: "end_turn"},
			{ID: "fire-2", ScheduleName: "nightly", SessionID: "sess-fire-2", Stop: "end_turn"},
		},
	}
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{
		SessionID: "sess-fire-2", Complete: true,
		Messages: []client.ConversationMessage{{Role: "assistant", Text: "scheduled output"}},
	}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithTranscript(t, conv, fs, scheduleCaps(), loader)
	activeSessionID := m.sessionID
	m = openInspectWithFires(t, m, fs)

	// Move cursor down to fire-2, then enter → inspect.
	mm, _, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.schedule.fireCursor != 1 {
		t.Fatalf("fireCursor = %d, want 1", m.schedule.fireCursor)
	}
	mm, cmd, _ := m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseReplay {
		t.Fatalf("phase = %v, want phaseReplay", m.phase)
	}
	if m.sessionID != activeSessionID {
		t.Fatalf("active sessionID = %q, want preserved %q", m.sessionID, activeSessionID)
	}
	if m.schedule.view != scheduleNone {
		t.Fatalf("schedule view = %v, want scheduleNone", m.schedule.view)
	}
	if cmd == nil {
		t.Fatal("jump did not start transcript load")
	}
	m = applyAll(m, cmd())
	if len(loader.calls) != 1 || loader.calls[0] != "sess-fire-2" {
		t.Fatalf("transcript calls=%q, want [sess-fire-2]", loader.calls)
	}
	if m.phase != phaseReplay || ensureActiveSessions(&m).loading || ensureActiveSessions(&m).loadErr != nil {
		t.Fatalf("loaded inspection state: phase=%v loading=%v err=%v", m.phase, ensureActiveSessions(&m).loading, ensureActiveSessions(&m).loadErr)
	}
	if m.sessionID != activeSessionID {
		t.Fatalf("loaded inspection rebound sessionID = %q, want %q", m.sessionID, activeSessionID)
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
	loader := &fakeSessionTranscriptLoader{}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithTranscript(t, conv, fs, scheduleCaps(), loader)
	m = openInspectWithFires(t, m, fs)

	mm, _, _ := m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle (no jump)", m.phase)
	}
	if m.schedule.view != scheduleInspect {
		t.Fatalf("view = %v, want scheduleInspect (stayed)", m.schedule.view)
	}
	if len(loader.calls) != 0 {
		t.Errorf("transcript loader should NOT be called, got %d calls", len(loader.calls))
	}
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "no session id") {
		t.Errorf("statusMsg = %q, want it to mention 'no session id'", got)
	}
}

// TestScheduleInspectJumpToFireNoTranscriptLoader asserts that without the
// authoritative transcript loader, Enter/t is a no-op and the footer does not
// advertise transcript inspection.
func TestScheduleInspectJumpToFireNoTranscriptLoader(t *testing.T) {
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sampleSchedule("nightly")},
		sched:     sampleSchedule("nightly"),
		fires: []client.ScheduleFire{
			{ID: "fire-1", ScheduleName: "nightly", SessionID: "sess-fire-1", Stop: "end_turn"},
		},
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithTranscript(t, conv, fs, scheduleCaps(), nil) // no transcript loader
	m = openInspectWithFires(t, m, fs)
	beforePhase := m.phase

	mm, _, _ := m.onScheduleInspectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.phase != beforePhase {
		t.Fatalf("phase = %v, want preserved %v", m.phase, beforePhase)
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
		t.Errorf("footer hint should NOT advertise the transcript action without a transcript loader:\n%s", out)
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
	loader := &fakeSessionTranscriptLoader{}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModelWithTranscript(t, conv, fs, scheduleCaps(), loader)
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

// TestScheduleCreateFormOpens asserts 'c' opens the Create form, the name field
// is focused, and esc returns to the panel.
func TestScheduleCreateFormOpens(t *testing.T) {
	fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("nightly")}}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: fs.schedules})

	// c — open create
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	if m.schedule.view != scheduleCreate {
		t.Fatalf("after c: view = %v, want scheduleCreate", m.schedule.view)
	}
	if !m.schedule.form.name.Focused() {
		t.Error("the name field should be focused on open")
	}
	// esc — back to panel
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.schedule.view != schedulePanel {
		t.Fatalf("after esc: view = %v, want schedulePanel", m.schedule.view)
	}
}

// TestScheduleCreateFormTypesAndSubmits asserts typing into the fields, then
// submitting via the mutating-toggle+enter path, fires CreateScheduleCmd and
// the new schedule appears on ScheduleMsg.
func TestScheduleCreateFormTypesAndSubmits(t *testing.T) {
	fs := &fakeScheduleLister{
		sched: sampleSchedule("my-sched"),
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: nil})

	// c — open create
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)

	// Type "my-sched" into the name field.
	for _, r := range "my-sched" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	if m.schedule.form.name.Value() != "my-sched" {
		t.Fatalf("name = %q, want my-sched", m.schedule.form.name.Value())
	}

	// Tab to prompt field.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	if m.schedule.form.focusIdx != 1 {
		t.Fatalf("focusIdx = %d, want 1", m.schedule.form.focusIdx)
	}
	// Type "run the tests".
	for _, r := range "run the tests" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}

	// Tab to trigger field.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	// Type a raw cron expression.
	for _, r := range "0 9 * * *" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}

	// Tab to the mutating toggle (focusIdx == scheduleFormFieldCount).
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	if m.schedule.form.focusIdx != scheduleFormFieldCount {
		t.Fatalf("focusIdx = %d, want %d (mutating toggle)", m.schedule.form.focusIdx, scheduleFormFieldCount)
	}

	// 'y' — toggle mutating to true.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = mm.(Model)
	if !m.schedule.form.mutating {
		t.Error("mutating should be true after 'y'")
	}

	// Enter — submit.
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.schedule.view != schedulePanel {
		t.Fatalf("after submit: view = %v, want schedulePanel (actionErr=%q)", m.schedule.view, m.schedule.actionErr)
	}
	if cmd == nil {
		t.Fatalf("submit should return the CreateSchedule command (actionErr=%q)", m.schedule.actionErr)
	}

	// Feed the CreateScheduleCmd result (ScheduleMsg with the new schedule).
	m = feedCmd(t, m, cmd)
	if fs.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", fs.createCalls)
	}
	if len(m.schedule.schedules) != 1 || m.schedule.schedules[0].Spec.Name != "my-sched" {
		t.Fatalf("schedules = %+v, want [my-sched]", m.schedule.schedules)
	}
}

// TestScheduleCreateFormNLTrigger asserts the trigger field compiles a
// natural-language phrase ("every 30 minutes") to a cron expression before
// POSTing.
func TestScheduleCreateFormNLTrigger(t *testing.T) {
	fs := &fakeScheduleLister{sched: sampleSchedule("nl-sched")}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: nil})

	// c — open create
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	// Name
	for _, r := range "nl-sched" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	// Tab to prompt, type it.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	for _, r := range "run" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	// Tab to trigger, type NL phrase.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	for _, r := range "every 30 minutes" {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}

	// Tab to mutating toggle and submit.
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
	m = mm.(Model)
	mm, cmd, _ := m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatalf("submit should return the CreateSchedule command (actionErr=%q)", m.schedule.actionErr)
	}
	feedCmd(t, m, cmd)
	if fs.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", fs.createCalls)
	}
}

// TestScheduleCreateFormValidation asserts missing required fields set actionErr
// and do NOT fire CreateSchedule.
func TestScheduleCreateFormValidation(t *testing.T) {
	fs := &fakeScheduleLister{}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	mm, _ := m.openSchedule()
	m = mm.(Model)
	m = applyAll(m, client.SchedulesMsg{Schedules: nil})

	// c — open create, submit immediately (all fields empty).
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	// Tab to the mutating toggle and submit.
	for i := 0; i < scheduleFormFieldCount; i++ {
		mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyTab, Text: "tab"})
		m = mm.(Model)
	}
	mm, _, _ = m.onScheduleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.schedule.view != scheduleCreate {
		t.Fatalf("should stay in create on validation error, view = %v", m.schedule.view)
	}
	if m.schedule.actionErr == "" {
		t.Error("actionErr should be set on validation failure")
	}
	if fs.createCalls != 0 {
		t.Errorf("create calls = %d, want 0 on validation error", fs.createCalls)
	}
}

// TestScheduleInspectRendersInFlightFire pins issue #386 (#386 fire-state): an
// IN-FLIGHT fire (Stop empty) renders the "in-flight" marker + its started/
// last-progress/deadline instants, never an empty stop; and the schedule-level
// in-flight summary line renders when the state shows a live fire.
func TestScheduleInspectRendersInFlightFire(t *testing.T) {
	start := time.Date(2026, 7, 6, 14, 0, 0, 0, time.UTC)
	prog := start.Add(20 * time.Second)
	deadline := start.Add(30 * time.Minute)
	sched := sampleSchedule("nightly")
	sched.Spec.FireTimeout = 30 * time.Minute
	sched.State.LastFireSessionID = "sess-inflight"
	sched.State.LastFireStartedAt = start
	sched.State.LastFireProgressAt = prog
	sched.State.FireDeadline = deadline
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sched},
		sched:     sched,
		fires: []client.ScheduleFire{
			{ID: "fire-inflight", ScheduleName: "nightly", SessionID: "sess-inflight", FiredAt: start, StartedAt: start, ProgressAt: prog, Deadline: deadline, Stop: ""},
		},
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	m = openInspectWithFires(t, m, fs)
	out := stripANSIstr(m.View().Content)
	for _, want := range []string{"in-flight", "fire-inflight", "started", "last-progress", "deadline", "fire_timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("in-flight inspect missing %q:\n%s", want, out)
		}
	}
}

// TestScheduleInspectRendersClaimedPending pins issue #386: a CLAIMED fire
// (LastFireSessionID == "pending", no run started, no fire records) renders an
// explicit "in-flight: claimed (session pending)" line, NOT "no fires recorded"
// (the genuinely-never-fired case).
func TestScheduleInspectRendersClaimedPending(t *testing.T) {
	sched := sampleSchedule("nightly")
	sched.State.LastFireSessionID = "pending"
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sched},
		sched:     sched,
		fires:     nil,
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	m = openInspectWithFires(t, m, fs)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "in-flight: claimed (session pending)") {
		t.Errorf("claimed-pending inspect missing the claimed line:\n%s", out)
	}
	if strings.Contains(out, "no fires recorded") {
		t.Errorf("claimed-pending inspect rendered \"no fires recorded\" (a claimed fire must NOT render as never-fired):\n%s", out)
	}
}

// TestScheduleInspectRendersNeverFired pins the genuine never-fired case still
// renders "no fires recorded" (the distinction from a claimed fire).
func TestScheduleInspectRendersNeverFired(t *testing.T) {
	sched := sampleSchedule("nightly")
	fs := &fakeScheduleLister{
		schedules: []client.Schedule{sched},
		sched:     sched,
		fires:     nil,
	}
	conv := newScheduleConv(scheduleCaps())
	m := newScheduleModel(t, conv, fs, scheduleCaps())
	m = openInspectWithFires(t, m, fs)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "no fires recorded") {
		t.Errorf("never-fired inspect missing \"no fires recorded\":\n%s", out)
	}
	if strings.Contains(out, "in-flight") {
		t.Errorf("never-fired inspect rendered \"in-flight\" (a never-claimed schedule must NOT render in-flight):\n%s", out)
	}
}
