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
		res, err := tl.Execute(context.Background(), scheduleCall(t, argsJSON), agent.EnvForWS(ws, nil))
		if err != nil {
			t.Fatalf("Execute harness-level error: %v", err)
		}
		return res
	}

	// create with a cron trigger maps the args onto the spec (read-leaning →
	// plan mode; the create-seam's own validation is not the tool's concern).
	res := run(`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *"}`)
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	if len(mgr.created) != 1 {
		t.Fatalf("created %d specs, want 1", len(mgr.created))
	}
	spec := mgr.created[0]
	if spec.Name != "nightly" || spec.Prompt != "check ci" || spec.Trigger.Cron != "0 3 * * *" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.EnvironmentRef.Valid() {
		t.Fatalf("spec EnvironmentRef = %+v, want no client-visible exact placement without an origin session", spec.EnvironmentRef)
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
	res = run(`{"verb":"create","name":"writer","prompt":"w","cron":"0 3 * * *","mutating":true}`)
	if res.IsError {
		t.Fatalf("mutating create = error %q", res.Content)
	}
	if mgr.created[1].Mode != session.ModeDefault {
		t.Fatalf("mutating create Mode = %q, want default", mgr.created[1].Mode)
	}

	// the Phase-2 one-shot retry fields map through VERBATIM (their rule
	// enforcement lives in the create-seam — the tool must never drop them).
	res = run(`{"verb":"create","name":"retryme","prompt":"w","one_shot":"2026-07-25T09:00:00Z","one_shot_retry":true,"one_shot_max_retries":7}`)
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
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"nightly"}`), agent.MemEnv("/ws"))
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
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"nightly"}`), agent.MemEnv("/ws"))
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
		res, err := tl.Execute(context.Background(), scheduleCall(t, a), agent.EnvForWS(ws, nil))
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

// TestScheduleTool_InspectRendersFires pins the inspect verb (a READ-ONLY verb,
// on the ScheduleQuery tool) surfaces the schedule plus its fires' terminal
// stop reasons.
func TestScheduleTool_InspectRendersFires(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleQueryTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := mgr.FireNow(context.Background(), "nightly"); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), agent.EnvForWS(ws, nil))
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

func TestScheduleQueryDeletionPendingIsDiscoverableWithoutTokenLeak(t *testing.T) {
	t.Parallel()
	const secretDeletionID = "secret-deletion-generation-token"
	mgr := newStubScheduleManager()
	mgr.scheds["cleanup"] = port.Schedule{
		Spec:  port.ScheduleSpec{Name: "cleanup", Trigger: port.TriggerSpec{Cron: "@daily"}},
		State: port.ScheduleState{DeletionID: secretDeletionID},
	}
	tl := agent.NewScheduleQueryTool(mgr)
	env := agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)

	for _, args := range []string{`{"verb":"list"}`, `{"verb":"inspect","name":"cleanup"}`} {
		res, err := tl.Execute(context.Background(), scheduleCall(t, args), env)
		if err != nil || res.IsError {
			t.Fatalf("ScheduleQuery(%s) = (%q, %v), want success", args, res.Content, err)
		}
		for _, want := range []string{"deletion_pending=true", "retry delete"} {
			if !strings.Contains(res.Content, want) {
				t.Errorf("ScheduleQuery(%s) = %q, want %q", args, res.Content, want)
			}
		}
		if strings.Contains(res.Content, secretDeletionID) {
			t.Errorf("ScheduleQuery(%s) leaked opaque deletion token in %q", args, res.Content)
		}
		if strings.Contains(res.Content, "disabled") {
			t.Errorf("ScheduleQuery(%s) mislabeled pending cleanup as disabled: %q", args, res.Content)
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

// TestScheduleTool_MutatingCreateGatedByPlanMode pins the plan-mode gate the
// Schedule tool carries (ADR 0073 decision 4, AC4.3): in a PLAN-MODE session a
// mutating: true create is DENIED (the plan-mode hard-deny on mutations — the
// plan-aware variant is what the plan-mode catalog advertises), while a
// read-leaning (mutating: false) create is ALLOWED (a schedule CREATE does not
// itself mutate the workspace — the FIRE's posture is pinned at create-time by
// the Mutating/Mode invariant). The default (non-plan) variant admits BOTH.
func TestScheduleTool_MutatingCreateGatedByPlanMode(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	ws := memfs.NewWorkspace("/ws")
	mutCreate := `{"verb":"create","name":"mut","prompt":"p","cron":"@every 1h","mutating":true}`
	roCreate := `{"verb":"create","name":"ro","prompt":"p","cron":"@every 1h"}`

	// The PLAN-MODE variant: a mutating create is hard-denied BEFORE the base
	// tool runs (the manager never sees it — the deny reason mirrors the
	// governance plan-mode reason), while a read-leaning create drives through.
	plan := agent.NewPlanAwareScheduleTool(agent.NewScheduleTool(mgr), mgr)
	if !plan.ReadOnly() {
		t.Fatal("the plan-aware Schedule tool must report ReadOnly()==true so the plan-mode catalog projection advertises it (the read-leaning verbs it admits do not mutate the workspace)")
	}
	res, err := plan.Execute(context.Background(), scheduleCall(t, mutCreate), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("plan Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("plan-mode mutating create = %q, want the plan-mode hard-deny (a mutating create must not run in plan mode)", res.Content)
	}
	if !strings.Contains(res.Content, "plan mode") {
		t.Fatalf("plan-mode mutating create deny = %q, want the plan-mode deny reason", res.Content)
	}
	if len(mgr.created) != 0 {
		t.Fatalf("plan-mode mutating create reached the manager (%d creates), want 0 (denied before the base tool)", len(mgr.created))
	}

	res, err = plan.Execute(context.Background(), scheduleCall(t, roCreate), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("plan Execute (read-leaning): %v", err)
	}
	if res.IsError {
		t.Fatalf("plan-mode read-leaning create = error %q, want allowed (a read-leaning create does not mutate the workspace)", res.Content)
	}
	if len(mgr.created) != 1 || mgr.created[0].Name != "ro" {
		t.Fatalf("plan-mode read-leaning create landed %+v, want exactly the ro schedule", mgr.created)
	}

	// AC4.3 (extended): a `fire` of a MUTATING schedule is ALSO hard-denied in
	// plan mode (the plan-mode hard-deny on mutations — a mutating schedule's
	// fire writes the workspace). A `fire` of a READ-LEANING schedule drives
	// through (the fire itself runs in plan mode, pinned at create-time).
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "mutsched", Prompt: "p", Trigger: port.TriggerSpec{Cron: "@every 1h"}, Mutating: true, Mode: session.ModeDefault,
	}); err != nil {
		t.Fatalf("CreateSchedule(mutsched): %v", err)
	}
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "rosched", Prompt: "p", Trigger: port.TriggerSpec{Cron: "@every 1h"}, Mutating: false, Mode: session.ModePlan,
	}); err != nil {
		t.Fatalf("CreateSchedule(rosched): %v", err)
	}
	firesBefore := len(mgr.fires["mutsched"]) + len(mgr.fires["rosched"])

	res, err = plan.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"mutsched"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("plan Execute (fire mutating): %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "plan mode") {
		t.Fatalf("plan-mode fire of a mutating schedule = %q, want the plan-mode hard-deny", res.Content)
	}
	if len(mgr.fires["mutsched"]) != 0 {
		t.Fatal("plan-mode fire of a mutating schedule reached the manager, want denied before it")
	}

	res, err = plan.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"rosched"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("plan Execute (fire read-leaning): %v", err)
	}
	if res.IsError {
		t.Fatalf("plan-mode fire of a read-leaning schedule = error %q, want allowed (the fire runs in plan mode)", res.Content)
	}
	if len(mgr.fires["rosched"]) != 1 {
		t.Fatalf("plan-mode fire of a read-leaning schedule fired %d times, want 1", len(mgr.fires["rosched"]))
	}
	if got := len(mgr.fires["mutsched"]) + len(mgr.fires["rosched"]); got != firesBefore+1 {
		t.Fatalf("total fires after the two plan-mode fire calls = %d, want exactly one new (the read-leaning one)", got)
	}

	// The DEFAULT (non-plan) variant admits BOTH — the gate is plan-mode-only.
	def := agent.NewScheduleTool(mgr)
	if def.ReadOnly() {
		t.Fatal("the default Schedule tool must report ReadOnly()==false (the read/mutate serialization contract — unchanged by the plan-aware variant)")
	}
	if res, err := def.Execute(context.Background(), scheduleCall(t, mutCreate), agent.EnvForWS(ws, nil)); err != nil || res.IsError {
		t.Fatalf("default-mode mutating create = (%v, %q), want allowed", err, res.Content)
	}
	if res, err := def.Execute(context.Background(), scheduleCall(t, `{"verb":"fire","name":"mutsched"}`), agent.EnvForWS(ws, nil)); err != nil || res.IsError {
		t.Fatalf("default-mode fire of a mutating schedule = (%v, %q), want allowed (no plan-mode gate outside plan mode)", err, res.Content)
	}
}

// TestScheduleTool_InspectRendersInFlightFire pins issue #386: an IN-FLIGHT
// fire (Stop empty) is rendered with an explicit "in-flight" marker plus its
// started/last-progress/deadline instants — NEVER silently rendered with an
// empty stop or as "fires: none". The schedule-level in-flight summary fires when
// the state shows a live fire.
func TestScheduleTool_InspectRendersInFlightFire(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleQueryTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"}, FireTimeout: 30 * time.Minute,
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	start := time.Unix(1_700_000_000, 0)
	prog := start.Add(10 * time.Second)
	deadline := start.Add(30 * time.Minute)
	inFlightFire := port.ScheduleFire{
		ID:           "fire-inflight",
		ScheduleName: "nightly",
		SessionID:    "sess-inflight",
		FiredAt:      start,
		StartedAt:    start,
		ProgressAt:   prog,
		Deadline:     deadline,
		Stop:         "", // in-flight
	}
	mgr.fires["nightly"] = append(mgr.fires["nightly"], inFlightFire)
	s := mgr.scheds["nightly"]
	s.State.LastFireSessionID = "sess-inflight"
	s.State.LastFireStartedAt = start
	s.State.LastFireProgressAt = prog
	s.State.FireDeadline = deadline
	mgr.scheds["nightly"] = s

	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect = error %q", res.Content)
	}
	for _, want := range []string{"in-flight", "fire-inflight", "started", "last-progress", "deadline"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("in-flight inspect result = %q, want %q (an in-flight fire must render the in-flight marker + its instants, never an empty stop or \"fires: none\")", res.Content, want)
		}
	}
	if strings.Contains(res.Content, "fires: none") {
		t.Fatalf("in-flight inspect rendered \"fires: none\" = %q (an in-flight fire must NOT render as none)", res.Content)
	}
	// FireTimeout surfaces in the spec summary.
	if !strings.Contains(res.Content, "fire_timeout=") {
		t.Fatalf("inspect result = %q, want fire_timeout= in the spec summary", res.Content)
	}
}

// TestScheduleTool_InspectRendersClaimedPending pins issue #386: a CLAIMED fire
// (Claim happened — LastFireSessionID == "pending" — but the run has not started
// — LastFireStartedAt zero — and no fire record yet) renders an explicit
// "in-flight: claimed (session pending)" line, NOT "fires: none" (the
// genuinely-never-fired case the claim must be distinguishable from).
func TestScheduleTool_InspectRendersClaimedPending(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleQueryTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Claim happened, run has not started, no fire record yet.
	s := mgr.scheds["nightly"]
	s.State.LastFireSessionID = "pending"
	mgr.scheds["nightly"] = s

	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect = error %q", res.Content)
	}
	if !strings.Contains(res.Content, "in-flight: claimed (session pending)") {
		t.Fatalf("claimed-pending inspect result = %q, want \"in-flight: claimed (session pending)\" (a claimed fire must NOT render as \"fires: none\")", res.Content)
	}
	if strings.Contains(res.Content, "fires: none") {
		t.Fatalf("claimed-pending inspect rendered \"fires: none\" = %q (a claimed fire must NOT render as none)", res.Content)
	}
}

// TestScheduleTool_InspectRendersNoFiresForNeverFired pins the genuine
// never-fired case still renders "fires: none" (the distinction from a claimed
// fire): a schedule that has never been Claimed (LastFireSessionID empty) and
// has no fire records renders "fires: none".
func TestScheduleTool_InspectRendersNoFiresForNeverFired(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleQueryTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect = error %q", res.Content)
	}
	if !strings.Contains(res.Content, "fires: none") {
		t.Fatalf("never-fired inspect result = %q, want \"fires: none\" (a never-claimed schedule genuinely has no fires)", res.Content)
	}
	if strings.Contains(res.Content, "in-flight") {
		t.Fatalf("never-fired inspect rendered \"in-flight\" = %q (a never-claimed schedule must NOT render in-flight)", res.Content)
	}
}

// TestScheduleTool_InspectRendersTerminalFire pins a TERMINAL fire (Stop set)
// renders the stop reason + error, unchanged — the in-flight path is the only
// new branch; terminal renders exactly as before.
func TestScheduleTool_InspectRendersTerminalFire(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	tl := agent.NewScheduleQueryTool(mgr)
	ws := memfs.NewWorkspace("/ws")
	if _, err := mgr.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "nightly", Prompt: "x", Trigger: port.TriggerSpec{Cron: "0 3 * * *"},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	terminalFire := port.ScheduleFire{
		ID:           "fire-terminal",
		ScheduleName: "nightly",
		SessionID:    "sess-terminal",
		FiredAt:      time.Unix(1_700_000_000, 0),
		Stop:         session.StopError,
		Err:          "boom",
	}
	mgr.fires["nightly"] = append(mgr.fires["nightly"], terminalFire)

	res, err := tl.Execute(context.Background(), scheduleCall(t, `{"verb":"inspect","name":"nightly"}`), agent.EnvForWS(ws, nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("inspect = error %q", res.Content)
	}
	for _, want := range []string{"fire-terminal", string(session.StopError), "boom"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("terminal inspect result = %q, want %q", res.Content, want)
		}
	}
	if strings.Contains(res.Content, "in-flight") {
		t.Fatalf("terminal inspect rendered \"in-flight\" = %q (a terminal fire must NOT render the in-flight marker)", res.Content)
	}
}
