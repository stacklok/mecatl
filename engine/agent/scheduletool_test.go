package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// scheduletool_test.go pins the ScheduleTool's in-engine behaviour: the verb
// dispatch, the args→spec mapping, the model-readable rendering, and the
// ReadOnly partition. The injected port.ScheduleManager is satisfied by a
// deterministic test manager (a scripted in-process fake — the engine module
// has no Service; the REAL seam parity is pinned by the composition-level tests
// over internal/adapter/server.Service).

// stubScheduleManager is a scripted in-process port.ScheduleManager: a minimal
// in-memory map backing the verbs, enough to pin the tool's dispatch/render
// contract offline. It is NOT a mock-framework mock of a port (the banned
// pattern) — it is a hand-rolled, deterministic in-memory fake scoped to this
// package's dispatch assertions; the create-seam validation + store parity are
// pinned against the REAL Service in internal/app.
type stubScheduleManager struct {
	scheds map[string]port.Schedule
	fires  map[string][]port.ScheduleFire
	// fireErr, when non-nil, is returned by FireNow (e.g. the singleton-overlap
	// sentinel the tool must surface verbatim).
	fireErr error
	// created records the specs CreateSchedule received (the args→spec mapping
	// assertion).
	created []port.ScheduleSpec
}

func newStubScheduleManager() *stubScheduleManager {
	return &stubScheduleManager{scheds: map[string]port.Schedule{}, fires: map[string][]port.ScheduleFire{}}
}

func (m *stubScheduleManager) CreateSchedule(_ context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	m.created = append(m.created, spec)
	sched := port.Schedule{
		Spec: spec,
		State: port.ScheduleState{
			NextFireAt: spec.Trigger.OneShot, // zero for a cron — the stub is render-focused
			Enabled:    true,
		},
	}
	m.scheds[spec.Name] = sched
	return sched, nil
}

func (m *stubScheduleManager) GetSchedule(_ context.Context, name string) (port.Schedule, error) {
	s, ok := m.scheds[name]
	if !ok {
		return port.Schedule{}, port.ErrScheduleNotFound
	}
	return s, nil
}

func (m *stubScheduleManager) ListSchedules(_ context.Context) ([]port.Schedule, error) {
	out := make([]port.Schedule, 0, len(m.scheds))
	for _, s := range m.scheds {
		out = append(out, s)
	}
	return out, nil
}

func (m *stubScheduleManager) UpdateSchedule(_ context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	if _, ok := m.scheds[spec.Name]; !ok {
		return port.Schedule{}, port.ErrScheduleNotFound
	}
	s := m.scheds[spec.Name]
	s.Spec = spec
	m.scheds[spec.Name] = s
	return s, nil
}

func (m *stubScheduleManager) DeleteSchedule(_ context.Context, name string) error {
	delete(m.scheds, name)
	return nil
}

func (m *stubScheduleManager) PauseSchedule(_ context.Context, name string) error {
	s, ok := m.scheds[name]
	if !ok {
		return port.ErrScheduleNotFound
	}
	s.State.Enabled = false
	m.scheds[name] = s
	return nil
}

func (m *stubScheduleManager) ResumeSchedule(_ context.Context, name string) error {
	s, ok := m.scheds[name]
	if !ok {
		return port.ErrScheduleNotFound
	}
	s.State.Enabled = true
	m.scheds[name] = s
	return nil
}

func (m *stubScheduleManager) FireNow(_ context.Context, name string) (port.ScheduleFire, error) {
	if m.fireErr != nil {
		return port.ScheduleFire{}, m.fireErr
	}
	if _, ok := m.scheds[name]; !ok {
		return port.ScheduleFire{}, port.ErrScheduleNotFound
	}
	f := port.ScheduleFire{
		ID:           "sched--" + name + "-test",
		ScheduleName: name,
		SessionID:    session.SessionID("sched--" + name + "-test"),
		FiredAt:      time.Unix(1_700_000_000, 0),
		Stop:         session.StopEndTurn,
	}
	m.fires[name] = append(m.fires[name], f)
	return f, nil
}

func (m *stubScheduleManager) ListFires(_ context.Context, name string) ([]port.ScheduleFire, error) {
	return m.fires[name], nil
}

// scheduleCall builds a ToolCall with the given JSON args.
func scheduleCall(t *testing.T, argsJSON string) session.ToolCall {
	t.Helper()
	return session.ToolCall{ID: "call-1", Name: agent.ScheduleToolName, Args: []byte(argsJSON)}
}

// TestScheduleTool_VerbDispatch exercises the verb dispatch + args→spec mapping:
// create maps the args onto a spec (trigger XOR, plan-mode-for-read-leaning),
// and a name-addressed verb rejects a missing name. This is the tool's
// own-logic unit pin — the seam validation is NOT re-tested here.
func TestScheduleTool_VerbDispatch(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	run := func(argsJSON string) session.ToolResult {
		res, err := tl.Execute(context.Background(), scheduleCall(t, argsJSON), ws)
		if err != nil {
			t.Fatalf("Execute harness-level error: %v", err)
		}
		return res
	}

	// create with a cron trigger maps the args onto the spec (read-leaning →
	// plan mode; the create-seam's own validation is not the tool's concern).
	res := run(`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`)
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	if len(mgr.created) != 1 {
		t.Fatalf("created %d specs, want 1", len(mgr.created))
	}
	spec := mgr.created[0]
	if spec.Name != "nightly" || spec.Prompt != "check ci" || spec.Trigger.Cron != "0 3 * * *" || spec.Workspace != "/repo" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Mode != session.ModePlan {
		t.Fatalf("read-leaning create Mode = %q, want plan (the tool pins read-leaning to plan)", spec.Mode)
	}
	if spec.Mutating {
		t.Fatal("read-leaning create Mutating = true, want false")
	}
	if spec.OneShotRetry || spec.OneShotMaxRetries != 0 {
		t.Fatalf("unset one-shot retry fields mapped through as (%v, %d), want (false, 0)", spec.OneShotRetry, spec.OneShotMaxRetries)
	}
	if !strings.Contains(res.Content, "nightly") {
		t.Fatalf("create result = %q, want the schedule name rendered", res.Content)
	}

	// a mutating create pins the default mode (not plan).
	res = run(`{"verb":"create","name":"writer","prompt":"w","cron":"0 3 * * *","workspace":"/r","mutating":true}`)
	if res.IsError {
		t.Fatalf("mutating create = error %q", res.Content)
	}
	if mgr.created[1].Mode != session.ModeDefault {
		t.Fatalf("mutating create Mode = %q, want default", mgr.created[1].Mode)
	}

	// the Phase-2 one-shot retry fields map through VERBATIM (their rule
	// enforcement lives in the create-seam — the tool must never drop them).
	res = run(`{"verb":"create","name":"retryme","prompt":"w","one_shot":"2026-07-25T09:00:00Z","workspace":"/r","one_shot_retry":true,"one_shot_max_retries":7}`)
	if res.IsError {
		t.Fatalf("one-shot retry create = error %q", res.Content)
	}
	if got := mgr.created[2]; !got.OneShotRetry || got.OneShotMaxRetries != 7 {
		t.Fatalf("one-shot retry fields mapped as (retry=%v, max=%d), want (true, 7) verbatim", got.OneShotRetry, got.OneShotMaxRetries)
	}

	// a trigger with neither cron nor one_shot is a model-addressable error.
	if res := run(`{"verb":"create","name":"bad","prompt":"x"}`); !res.IsError {
		t.Fatalf("create with no trigger = %q, want an error", res.Content)
	}
	// both set is rejected too.
	if res := run(`{"verb":"create","name":"bad","prompt":"x","cron":"* * * * *","one_shot":"2026-07-25T09:00:00Z"}`); !res.IsError {
		t.Fatalf("create with both triggers = %q, want an error", res.Content)
	}
	// an unknown verb is a model-addressable error.
	if res := run(`{"verb":"explode"}`); !res.IsError {
		t.Fatalf("unknown verb = %q, want an error", res.Content)
	}
	// a name-addressed verb with no name is a model-addressable error.
	if res := run(`{"verb":"inspect"}`); !res.IsError {
		t.Fatalf("inspect with no name = %q, want an error", res.Content)
	}
}

// TestScheduleTool_FireRendersIDs pins the fire verb's rendering contract: the
// fire id + session id + terminal stop reason appear in the result text.
func TestScheduleTool_FireRendersIDs(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleTool(mgr)
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"nightly"}`), memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("fire = error %q", res.Content)
	}
	for _, want := range []string{"fire id:", "session id:", "sched--nightly-test", "end_turn"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("fire result = %q, want %q", res.Content, want)
		}
	}
}

// TestScheduleTool_FireOverlapSurfaces pins that a FireNow error (the
// singleton-overlap sentinel) surfaces as a model-addressable error, not a
// harness error and not a swallowed success.
func TestScheduleTool_FireOverlapSurfaces(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	mgr.fireErr = errors.New("scheduler: fire-now skipped (prior fire still running)")
	tl := agent.NewScheduleTool(mgr)
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"nightly"}`), memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute harness-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("overlapping fire = %q, want a model-addressable error", res.Content)
	}
	if !strings.Contains(res.Content, "still running") {
		t.Fatalf("overlap error = %q, want the singleton-overlap message surfaced", res.Content)
	}
}

// TestScheduleTool_PauseResumeDelete pins the name-addressed mutating verbs map
// onto the manager (pause disables, resume re-enables, delete removes) against
// the stub's state.
func TestScheduleTool_PauseResumeDelete(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	run := func(a string) session.ToolResult {
		res, err := tl.Execute(context.Background(), scheduleCall(t, a), ws)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return res
	}
	if res := run(`{"verb":"pause","name":"nightly"}`); res.IsError {
		t.Fatalf("pause = error %q", res.Content)
	}
	if s, _ := mgr.GetSchedule(context.Background(), "nightly"); s.State.Enabled {
		t.Fatal("Enabled = true after pause, want false")
	}
	if res := run(`{"verb":"resume","name":"nightly"}`); res.IsError {
		t.Fatalf("resume = error %q", res.Content)
	}
	if s, _ := mgr.GetSchedule(context.Background(), "nightly"); !s.State.Enabled {
		t.Fatal("Enabled = false after resume, want true")
	}
	if res := run(`{"verb":"delete","name":"nightly"}`); res.IsError {
		t.Fatalf("delete = error %q", res.Content)
	}
	if _, err := mgr.GetSchedule(context.Background(), "nightly"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("GetSchedule after delete = %v, want ErrScheduleNotFound", err)
	}
}

// TestScheduleTool_InspectRendersFires pins the inspect verb surfaces the
// schedule plus its fires' terminal stop reasons.
func TestScheduleTool_InspectRendersFires(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := mgr.FireNow(context.Background(), "nightly"); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), ws)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect = error %q", res.Content)
	}
	for _, want := range []string{"nightly", "fire", "end_turn"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("inspect result = %q, want %q", res.Content, want)
		}
	}
}

// TestScheduleTool_NilManagerPanics pins the constructor's non-nil contract (a
// composition-root programming error must not silently mint a dead tool).
func TestScheduleTool_NilManagerPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("NewScheduleTool(nil) did not panic")
		}
	}()
	_ = agent.NewScheduleTool(nil)
}
