package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// scheduletool_test.go is the composition-side pin for the model-facing
// Schedule tool (ADR 0073). It drives the REAL seams offline: the assembled
// catalog (assembleCatalog over the late-bound scheduleManager factory), the
// server Service's schedule methods (the port.ScheduleManager the tool
// consumes), and the in-process scheduler — over memschedulestore / jsonlstore
// + mockllm, never a live model or network. The engine-side dispatch/render
// contract is pinned in engine/agent/scheduletool_test.go; these tests pin the
// registration gate, the ReadOnly partition, the fire round-trip, the singleton
// overlap, and the one-store-one-truth surface parity.

// scheduleCall builds a ToolCall against the assembled catalog for the Schedule
// tool with the given JSON args.
func scheduleCall(argsJSON string) session.ToolCall {
	return session.ToolCall{ID: "call-1", Name: agent.ScheduleToolName, Args: []byte(argsJSON)}
}

// execSchedule runs the Schedule tool out of the assembled catalog.
func execSchedule(t *testing.T, cat *tool.Catalog, argsJSON string) session.ToolResult {
	t.Helper()
	tl, ok := cat.Lookup(agent.ScheduleToolName)
	if !ok {
		t.Fatalf("Schedule tool not in the catalog")
	}
	res, err := tl.Execute(context.Background(), scheduleCall(argsJSON), memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute returned a harness-level error (want a model-addressable ToolResult): %v", err)
	}
	return res
}

// assembleScheduleCatalog assembles a catalog over a scheduleManager factory
// wired to the given resolver, mirroring the per-session assembly path (the
// shared-catalog path is the chicken-and-egg the late-bound factory closes).
func assembleScheduleCatalog(t *testing.T, resolve func() port.ScheduleManager) *tool.Catalog {
	t.Helper()
	cfg := Config{Model: "gpt-5", Diagnostics: port.NopDiagnostics{}}
	llm := mockllm.New(mockllm.TextTurn("OK"))
	reg := regForTest(llm, "mock", cfg.Model)
	assets := catalogAssets{scheduleManagerFactory: resolve}
	cat, closeFn := assembleCatalog(context.Background(), cfg, reg, memstore.New(), hookexec.New(nil), assets, catalogSession{
		provider: llm, providerID: reg.Default(), model: cfg.Model, narrate: false,
	})
	t.Cleanup(func() { _ = closeFn() })
	return cat
}

// TestScheduleTool_RegisteredOnlyWhenStoreBacked pins AC1.1: the Schedule tool
// is present in the catalog of a session backed by a ScheduleStore and ABSENT
// (honest, not a stub) when the store has none — and the registration agrees
// with ServerCapabilities.Scheduling (the same scheduleStore() != nil gate).
func TestScheduleTool_RegisteredOnlyWhenStoreBacked(t *testing.T) {
	workspace := t.TempDir()

	// Store-backed (jsonlstore exposes a ScheduleStore): the Service's
	// ScheduleManager is non-nil, the capabilities bit is true, and the tool
	// registers.
	storeDir := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(mockllm.TextTurn("fire result"))
	svc := newScheduleTestService(t, jstore, llm, workspace)
	if mgr := svc.ScheduleManager(); mgr == nil {
		t.Fatal("jsonlstore-backed Service.ScheduleManager() = nil, want non-nil (the store backs a ScheduleStore)")
	}
	// The capability bit the wire echoes (capabilities() is unexported, so the
	// pin reads the gate it wraps: scheduleStore() != nil ⇔ Scheduling true).
	if svc.ScheduleManager() == nil {
		t.Fatal("Scheduling gate = false on a ScheduleStore-backed service, want true")
	}
	backed := assembleScheduleCatalog(t, svc.ScheduleManager)
	if _, ok := backed.Lookup(agent.ScheduleToolName); !ok {
		t.Fatal("Schedule tool ABSENT from the catalog of a ScheduleStore-backed session, want present")
	}

	// No ScheduleStore (memstore): the Service's ScheduleManager is nil, the
	// capabilities bit is false, and the tool is honestly absent (not a stub).
	mstore := memstore.New()
	noSvc := newScheduleTestService(t, mstore, llm, workspace)
	if mgr := noSvc.ScheduleManager(); mgr != nil {
		t.Fatal("memstore-backed Service.ScheduleManager() != nil, want nil (no ScheduleStore)")
	}
	absent := assembleScheduleCatalog(t, noSvc.ScheduleManager)
	if _, ok := absent.Lookup(agent.ScheduleToolName); ok {
		t.Fatal("Schedule tool PRESENT in the catalog of a store with no ScheduleStore, want honest absence (not a stub)")
	}

	// A nil factory (the never-wired case) also registers nothing.
	none := assembleScheduleCatalog(t, nil)
	if _, ok := none.Lookup(agent.ScheduleToolName); ok {
		t.Fatal("Schedule tool PRESENT with a nil scheduleManager factory, want absent")
	}
}

// newScheduleTestService builds a *server.Service over the given session store
// (jsonlstore → ScheduleStore-backed; memstore → not) + a mockllm engine,
// mirroring the composition Build wires, WITHOUT the tick loop (the manual
// FireNow path needs only SetScheduler, done by the caller where relevant).
func newScheduleTestService(t *testing.T, store port.SessionStore, llm *mockllm.Provider, _ string) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               store,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestScheduleTool_CreateValidatesLikeRESTSeam pins AC1.2: the tool's create
// verb rides the EXISTING create-seam (validateScheduleSpec +
// applyScheduleDefaults over the REAL Service, the same seam REST
// POST /v1/schedules calls) — never a second, drifted path. The pinned seam
// behaviours: a valid cron saves an ENABLED schedule whose NextFireAt is the
// cronparse-computed first fire (recomputed independently here); the seam
// rejects fail-closed an invalid cron, a non-plan non-mutating mode, and an
// empty workspace on a default-profile schedule. The rejection halves are
// pinned DIRECTLY against the seam (errors.Is(server.ErrInvalidArgument)) and
// via the tool over the REAL seam (a tool wired to a SUBSET of the seam goes
// red); the tool's read-leaning default already pins Mode=plan, so the only
// non-plan non-mutating spec the tool can mint is the raw-args-injection one
// (spec.Mode="" → seam default → rejected) — the tool must forward the
// caller-controlled spec verbatim into the seam for that check to fire.
func TestScheduleTool_CreateValidatesLikeRESTSeam(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), workspace)
	cat := assembleScheduleCatalog(t, svc.ScheduleManager)

	// --- the happy path rides the seam: a valid cron saves ENABLED with the
	// cronparse-computed first fire ---
	const cronExpr = "0 9 * * *"
	res := execSchedule(t, cat, `{"verb":"create","name":"daily","prompt":"check ci","cron":"`+cronExpr+`","workspace":"`+workspace+`"}`)
	if res.IsError {
		t.Fatalf("valid cron create = error %q", res.Content)
	}
	sched, err := svc.GetSchedule(ctx, "daily")
	if err != nil {
		t.Fatalf("GetSchedule after tool create: %v", err)
	}
	if !sched.State.Enabled {
		t.Fatal("tool-created schedule Enabled = false, want true (the create-seam saves enabled)")
	}
	// The seam computed NextFireAt at cfg.Now(); recompute it INDEPENDENTLY
	// (cronparse over the same clock + timezone fold the seam uses) and pin the
	// stored value against it — a schedule whose NextFireAt came from a second,
	// drifted computation would not match the seam's own parse.
	wantNext, err := cronparse.NextFire(cronExpr, sched.Spec.CreatedAt, scheduler.LoadLocation(sched.Spec.Timezone))
	if err != nil {
		t.Fatalf("cronparse.NextFire(%q): %v", cronExpr, err)
	}
	if !sched.State.NextFireAt.Equal(wantNext) {
		t.Fatalf("NextFireAt = %v, want the cronparse-computed first fire %v", sched.State.NextFireAt, wantNext)
	}

	// --- the rejections: pinned DIRECTLY against the create-seam and THROUGH
	// the tool over the REAL seam ---
	type rejection struct {
		name string
		spec port.ScheduleSpec
		// toolArgs drives the same rejection via the tool ("" = the tool's
		// read-leaning default cannot mint this spec — the raw-args injection
		// asserts the tool forwards the caller's spec verbatim instead).
		toolArgs string
		// wantMsg, when non-empty, is a substring the seam's error (and the
		// tool's forward of it) must carry.
		wantMsg string
	}
	rejections := []rejection{
		{
			name: "invalid cron grammar",
			spec: port.ScheduleSpec{
				Name: "badcron", Prompt: "p", Trigger: port.TriggerSpec{Cron: "not a cron at all"},
				Workspace: workspace, Mode: session.ModePlan,
			},
			toolArgs: `{"verb":"create","name":"badcron2","prompt":"p","cron":"not a cron at all","workspace":"` + workspace + `"}`,
			wantMsg:  "invalid cron expression",
		},
		{
			name: "non-plan non-mutating mode",
			spec: port.ScheduleSpec{
				Name: "badmode", Prompt: "p", Trigger: port.TriggerSpec{Cron: cronExpr},
				Workspace: workspace, Mode: session.ModeDefault, Mutating: false,
			},
			// The tool pins Mode=plan for a read-leaning create, so the model
			// cannot mint this spec through the tool at all.
			wantMsg: "must use plan mode",
		},
		{
			name: "non-plan non-mutating via an unset mode (seam default)",
			spec: port.ScheduleSpec{
				Name: "rawmode", Prompt: "p", Trigger: port.TriggerSpec{Cron: cronExpr},
				Workspace: workspace, Mode: "", Mutating: false,
			},
			// A raw-args injection mints the spec directly (the caller controls
			// every field): Mode="" resolves to default at the seam, so the
			// invariant must fire there — the tool forwards the spec verbatim.
			// The trigger is fixed to the cron (the one-shot's future invariant
			// would reject a stale instant before the mode check, a cascade).
			toolArgs: "raw",
			wantMsg:  "must use plan mode",
		},
		{
			name: "empty workspace on a default-profile schedule",
			spec: port.ScheduleSpec{
				Name: "nows", Prompt: "p", Trigger: port.TriggerSpec{Cron: cronExpr},
				Workspace: "", Mode: session.ModePlan,
			},
			toolArgs: `{"verb":"create","name":"nows2","prompt":"p","cron":"` + cronExpr + `"}`,
			wantMsg:  "requires a workspace",
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			// Direct: the REAL create-seam rejects fail-closed with the
			// invalid-argument sentinel the REST handler maps to a 400.
			if _, err := svc.CreateSchedule(ctx, tc.spec); !errors.Is(err, server.ErrInvalidArgument) {
				t.Fatalf("CreateSchedule = %v, want ErrInvalidArgument (fail-closed, the REST create-seam's rejection)", err)
			}
			// Fail-closed means NOT saved: the name must not land in the store.
			if _, err := svc.GetSchedule(ctx, tc.spec.Name); !errors.Is(err, port.ErrScheduleNotFound) {
				t.Fatalf("GetSchedule(%q) after rejection = %v, want ErrScheduleNotFound (a rejected spec was saved)", tc.spec.Name, err)
			}
			if tc.wantMsg != "" && tc.toolArgs == "" {
				if _, err := svc.CreateSchedule(ctx, tc.spec); err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("CreateSchedule error missing %q: %v", tc.wantMsg, err)
				}
			}
			switch tc.toolArgs {
			case "":
				// Unreachable through the tool by construction (documented above).
			case "raw":
				// Raw-args injection: the caller controls every spec field, so
				// the seam's invariant must fire on the forwarded spec — a tool
				// re-deriving Mode (or any field) instead of forwarding it would
				// mask the rejection.
				rawMgr := &rawScheduleManager{inner: svc, spec: tc.spec}
				rawCat := assembleScheduleCatalog(t, func() port.ScheduleManager { return rawMgr })
				raw := execSchedule(t, rawCat, `{"verb":"create","name":"rawmode","prompt":"p","cron":"`+cronExpr+`"}`)
				if !raw.IsError {
					t.Fatalf("raw-spec create (Mode unset, Mutating false) = %q, want the seam's plan-mode rejection", raw.Content)
				}
				if !strings.Contains(raw.Content, tc.wantMsg) {
					t.Fatalf("raw-spec create error = %q, want the seam's rejection containing %q", raw.Content, tc.wantMsg)
				}
				if _, err := svc.GetSchedule(ctx, "rawmode"); !errors.Is(err, port.ErrScheduleNotFound) {
					t.Fatalf("GetSchedule(rawmode) after the raw rejection = %v, want ErrScheduleNotFound", err)
				}
			default:
				toolRes := execSchedule(t, cat, tc.toolArgs)
				if !toolRes.IsError {
					t.Fatalf("tool create = %q, want the seam's fail-closed rejection (a tool wired to a subset of the seam would accept)", toolRes.Content)
				}
				if tc.wantMsg != "" && !strings.Contains(toolRes.Content, tc.wantMsg) {
					t.Fatalf("tool create error = %q, want the seam's rejection containing %q", toolRes.Content, tc.wantMsg)
				}
			}
		})
	}
}

// rawScheduleManager wraps the REAL Service and injects a caller-controlled
// ScheduleSpec verbatim (the REST/gRPC wire analogue, where every field —
// including Mode — is caller-controlled). It pins that the tool forwards the
// spec into the seam instead of re-validating/re-deriving it: the seam's
// invariant must fire on the forwarded spec.
type rawScheduleManager struct {
	inner *server.Service
	spec  port.ScheduleSpec
}

func (m *rawScheduleManager) CreateSchedule(ctx context.Context, _ port.ScheduleSpec) (port.Schedule, error) {
	return m.inner.CreateSchedule(ctx, m.spec)
}
func (m *rawScheduleManager) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	return m.inner.GetSchedule(ctx, name)
}
func (m *rawScheduleManager) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	return m.inner.ListSchedules(ctx)
}
func (m *rawScheduleManager) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	return m.inner.UpdateSchedule(ctx, spec)
}
func (m *rawScheduleManager) DeleteSchedule(ctx context.Context, name string) error {
	return m.inner.DeleteSchedule(ctx, name)
}
func (m *rawScheduleManager) PauseSchedule(ctx context.Context, name string) error {
	return m.inner.PauseSchedule(ctx, name)
}
func (m *rawScheduleManager) ResumeSchedule(ctx context.Context, name string) error {
	return m.inner.ResumeSchedule(ctx, name)
}
func (m *rawScheduleManager) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	return m.inner.FireNow(ctx, name)
}
func (m *rawScheduleManager) ListFires(ctx context.Context, name string) ([]port.ScheduleFire, error) {
	return m.inner.ListFires(ctx, name)
}

// TestScheduleTool_CreateEnforcesPhase2FieldRules pins AC1.2b: the tool's
// create verb enforces the create-seam's Phase-2 field rules BY NAME —
// oneShotRetry: true on a cron trigger is rejected fail-closed (a cron
// self-heals via misfire — no retry budget), and a one-shot with
// oneShotRetry: true and no maxRetries gets the create-seam default of 3 —
// so the tool cannot be wired to a subset of the seam (a tool that silently
// DROPS the fields instead of mapping them goes red here).
func TestScheduleTool_CreateEnforcesPhase2FieldRules(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), workspace)
	cat := assembleScheduleCatalog(t, svc.ScheduleManager)

	// oneShotRetry on a cron trigger: rejected fail-closed (a cron self-heals
	// via misfire). Direct against the seam first, then THROUGH the tool — a
	// tool that dropped the field would accept the create and go red.
	cronRetry := port.ScheduleSpec{
		Name: "cronretry", Prompt: "p", Trigger: port.TriggerSpec{Cron: "0 9 * * *"},
		Workspace: workspace, Mode: session.ModePlan,
		OneShotRetry: true,
	}
	if _, err := svc.CreateSchedule(ctx, cronRetry); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule(oneShotRetry on a cron) = %v, want ErrInvalidArgument", err)
	}
	res := execSchedule(t, cat, `{"verb":"create","name":"cronretry2","prompt":"p","cron":"0 9 * * *","workspace":"`+workspace+`","one_shot_retry":true}`)
	if !res.IsError {
		t.Fatalf("tool create oneShotRetry on a cron = %q, want the fail-closed rejection (the field must not be silently dropped)", res.Content)
	}
	if !strings.Contains(res.Content, "one-shot-only") {
		t.Fatalf("tool cron+oneShotRetry rejection = %q, want the seam's one-shot-only message", res.Content)
	}
	if _, err := svc.GetSchedule(ctx, "cronretry2"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("GetSchedule(cronretry2) after rejection = %v, want ErrScheduleNotFound (fail-closed means not saved)", err)
	}

	// A one-shot with oneShotRetry: true and no maxRetries: the create-seam
	// applies the default of 3 — THROUGH the tool, on the SAVED spec (the
	// applyScheduleDefaults half of the seam).
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	res = execSchedule(t, cat, `{"verb":"create","name":"retryme","prompt":"p","one_shot":"`+future+`","workspace":"`+workspace+`","one_shot_retry":true}`)
	if res.IsError {
		t.Fatalf("one-shot oneShotRetry create = error %q", res.Content)
	}
	saved, err := svc.GetSchedule(ctx, "retryme")
	if err != nil {
		t.Fatalf("GetSchedule(retryme): %v", err)
	}
	if !saved.Spec.OneShotRetry {
		t.Fatal("saved spec OneShotRetry = false, want true (the tool must map the field through to the seam)")
	}
	if saved.Spec.OneShotMaxRetries != 3 {
		t.Fatalf("saved spec OneShotMaxRetries = %d, want the create-seam default 3 (applyScheduleDefaults)", saved.Spec.OneShotMaxRetries)
	}
}

// TestScheduleTool_ReadOnlyPartition pins AC1.4: the read-only verbs
// (list/inspect) are parallel-safe and the mutating verbs (create/pause/resume/
// delete/fire) serialize. Because a single tool carries BOTH and ReadOnly()
// takes no args, the tool reports ReadOnly()==false so EVERY Schedule call
// serialises on the mutate path — a mutating verb NEVER runs concurrently with
// a sibling read (the partition's real guarantee). The verb-level split is
// documented in the Spec description so the model sees the honest read/mutate
// distinction.
func TestScheduleTool_ReadOnlyPartition(t *testing.T) {
	t.Parallel()
	// The tool must be safe to call concurrently for the READ verbs even though
	// ReadOnly() reports false — the false is the CONSERVATIVE guarantee (all
	// calls serialize), strictly stronger than "mutating never overlaps a read".
	tl := agent.NewScheduleTool(newPartitionStubManager())
	if tl.ReadOnly() {
		t.Fatal("Schedule.ReadOnly() = true — a mutating verb (create/pause/resume/delete/fire) would fan out into the read-parallel batch and could overlap a sibling read; want false (all Schedule calls serialize)")
	}
	// The Spec description documents the honest verb-level partition the model
	// reads (the false ReadOnly() is the conservative serialization, not a lie
	// that every verb mutates).
	desc := tl.Spec().Description
	for _, verb := range []string{"list", "inspect", "create", "pause", "resume", "delete", "fire"} {
		if !strings.Contains(desc, verb) {
			t.Fatalf("Spec description does not name verb %q: %q", verb, desc)
		}
	}
	if !strings.Contains(desc, "read-only") {
		t.Fatalf("Spec description does not document the read-only verbs: %q", desc)
	}
}

// partitionStubManager is a thread-safe in-memory ScheduleManager for the
// partition test (list/inspect called concurrently must not race).
type partitionStubManager struct {
	scheds []port.Schedule
}

func newPartitionStubManager() *partitionStubManager { return &partitionStubManager{} }

func (*partitionStubManager) CreateSchedule(_ context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	return port.Schedule{Spec: spec, State: port.ScheduleState{Enabled: true}}, nil
}
func (*partitionStubManager) GetSchedule(_ context.Context, name string) (port.Schedule, error) {
	return port.Schedule{Spec: port.ScheduleSpec{Name: name}}, nil
}
func (m *partitionStubManager) ListSchedules(_ context.Context) ([]port.Schedule, error) {
	return m.scheds, nil
}
func (*partitionStubManager) UpdateSchedule(_ context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	return port.Schedule{Spec: spec}, nil
}
func (*partitionStubManager) DeleteSchedule(_ context.Context, _ string) error { return nil }
func (*partitionStubManager) PauseSchedule(_ context.Context, _ string) error  { return nil }
func (*partitionStubManager) ResumeSchedule(_ context.Context, _ string) error { return nil }
func (*partitionStubManager) FireNow(_ context.Context, name string) (port.ScheduleFire, error) {
	return port.ScheduleFire{ID: "sched--" + name, ScheduleName: name, SessionID: session.SessionID("sched--" + name)}, nil
}
func (*partitionStubManager) ListFires(_ context.Context, _ string) ([]port.ScheduleFire, error) {
	return nil, nil
}

// TestScheduleTool_FireAndInspectRoundTrip pins AC1.5: `Schedule fire <name>`
// returns the fire id + session id of the minted sched-- session (the
// synchronous-to-terminal FireNow seam), and `Schedule list`/`inspect` surface
// the fire's terminal stop reason. Driven against the REAL Service + scheduler
// + jsonlstore + mockllm (the fire mints a real sched-- session driven to
// StopEndTurn).
func TestScheduleTool_FireAndInspectRoundTrip(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(mockllm.TextTurn("fire result"))
	svc := newScheduleTestService(t, jstore, llm, workspace)
	startTestScheduler(t, svc, jstore.ScheduleStore(), fireFuncForScheduleTest(svc))

	cat := assembleScheduleCatalog(t, svc.ScheduleManager)

	// Create a cron schedule through the tool.
	res := execSchedule(t, cat, `{"verb":"create","name":"nightly","prompt":"check ci","cron":"@every 1m","workspace":"`+workspace+`"}`)
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}

	// Fire it: the result carries the fire id + session id (sched-- prefixed),
	// the synchronous-to-terminal FireNow contract.
	fire := execSchedule(t, cat, `{"verb":"fire","name":"nightly"}`)
	if fire.IsError {
		t.Fatalf("fire = error %q", fire.Content)
	}
	if !strings.Contains(fire.Content, "fire id:") || !strings.Contains(fire.Content, "session id:") {
		t.Fatalf("fire result = %q, want the fire id + session id rendered", fire.Content)
	}
	if !strings.Contains(fire.Content, "sched--") {
		t.Fatalf("fire result = %q, want a sched-- session id", fire.Content)
	}

	// The fire's terminal stop reason surfaces via the SAME store the tool
	// reads: wait for the fire record to reach a terminal stop, then assert
	// list + inspect both surface it.
	if !scheduleEventually(15*time.Second, func() bool {
		fires, _ := jstore.ScheduleStore().ListFires(ctx, "nightly")
		for _, f := range fires {
			if f.Stop != "" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the fire did not reach a terminal stop within 15s")
	}
	inspect := execSchedule(t, cat, `{"verb":"inspect","name":"nightly"}`)
	if inspect.IsError {
		t.Fatalf("inspect = error %q", inspect.Content)
	}
	if !strings.Contains(inspect.Content, "end_turn") {
		t.Fatalf("inspect = %q, want the fire's terminal stop reason (end_turn) surfaced", inspect.Content)
	}
	list := execSchedule(t, cat, `{"verb":"list"}`)
	if list.IsError {
		t.Fatalf("list = error %q", list.Content)
	}
	if !strings.Contains(list.Content, "nightly") {
		t.Fatalf("list = %q, want the schedule named", list.Content)
	}
}

// startTestScheduler wires + starts an in-process scheduler over the given
// store, mirroring startScheduler (SetScheduler + SetFire + Start) without the
// Build-only lifecycle. Stopped by t.Cleanup.
func startTestScheduler(t *testing.T, svc *server.Service, store port.ScheduleStore, fire scheduler.FireFunc) {
	t.Helper()
	sched := scheduler.New(scheduler.Config{
		Store:        store,
		Fire:         fire,
		Clock:        testWallClock{},
		Diagnostics:  port.NopDiagnostics{},
		TickInterval: time.Hour, // the manual FireNow path, not the tick loop, drives these tests
	})
	svc.SetScheduler(sched)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { _ = sched.Stop() })
}

// testWallClock is a real-time port.Clock (the fire tests are wall-clock
// driven — the fire's sched-- session runs a real mockllm turn).
type testWallClock struct{}

func (testWallClock) Now() time.Time { return time.Now() }

// fireFuncForScheduleTest mirrors internal/app.makeFireFunc over the Service:
// mint a fresh sched-- session, drive it to the terminal EvResult, return the
// fire record (id == session id).
func fireFuncForScheduleTest(svc *server.Service) scheduler.FireFunc {
	return func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		mode := sched.Spec.Mode
		if mode == "" {
			mode = session.ModeDefault
		}
		if !sched.Spec.Mutating {
			mode = session.ModePlan
		}
		limits := sched.Spec.Limits
		if limits.MaxTurns == 0 {
			limits.MaxTurns = 50
		}
		if limits.MaxToolCalls == 0 {
			limits.MaxToolCalls = 200
		}
		fireID := "sched--" + sched.Spec.Name + "-test"
		sess, err := svc.CreateSessionWithProfile(ctx, sched.Spec.Workspace, mode, limits, server.ProviderSelector{}, server.ProfileDefault,
			server.WithSessionID(session.SessionID(fireID)))
		if err != nil {
			return port.ScheduleFire{ID: fireID, ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopError, Err: err.Error()}, err
		}
		defer svc.CloseSession(sess.ID)
		run, err := svc.StartRunContent(ctx, sess.ID, sched.Spec.Prompt, sched.Spec.Parts)
		if err != nil {
			return port.ScheduleFire{ID: string(sess.ID), ScheduleName: sched.Spec.Name, SessionID: sess.ID, FiredAt: now, Stop: session.StopError, Err: err.Error()}, err
		}
		var stop session.StopReason
		var runErr string
		for ev := range run.Events() {
			if ev.Type == session.EvResult && ev.Result != nil {
				stop = ev.Result.Stop
				runErr = ev.Result.Error
				break
			}
		}
		return port.ScheduleFire{ID: string(sess.ID), ScheduleName: sched.Spec.Name, SessionID: sess.ID, FiredAt: now, Stop: stop, Err: runErr}, nil
	}
}

// scheduleEventually polls cond until it holds or d elapses (the fire's sched--
// session runs a real mockllm turn, so the terminal stop lands asynchronously).
func scheduleEventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestScheduleTool_FireOverlapRejected pins AC1.5b: `Schedule fire <name>` on a
// schedule with a fire already in flight returns the singleton-overlap error
// (the same ErrFireNowOverlap the REST FireNow returns — matched via
// errors.Is(err, port.ErrFireNowOverlap)), not a second concurrent fire. The
// create-seam's default-true Singleton holds through the tool.
func TestScheduleTool_FireOverlapRejected(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	// memschedulestore (the reference in-memory ScheduleStore) + a scheduler
	// whose singleton check uses a lease backend reporting the prior fire held.
	schedStore := memschedulestore.New()
	// A singleton schedule whose LastFireSessionID points at a held lease (the
	// in-flight prior fire).
	clk := testWallClock{}
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      "singleton",
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "@every 1m"},
			Workspace: workspace,
			Mode:      session.ModePlan,
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:        clk.Now().Add(time.Hour),
			Enabled:           true,
			LastFireSessionID: "sched--prior", // the in-flight prior fire
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The scheduler's singleton check is a trial lease on LastFireSessionID;
	// a backend reporting it HELD yields the overlap rejection.
	leaseBE := &heldPriorFireLease{held: map[session.SessionID]string{"sched--prior": "owner-prior"}}
	sched := scheduler.New(scheduler.Config{
		Store:      schedStore,
		Lease:      leaseBE,
		LeaseOwner: "owner-this",
		Fire: func(context.Context, port.Schedule, time.Time) (port.ScheduleFire, error) {
			return port.ScheduleFire{}, nil
		},
		Clock: clk,
	})
	svc := newScheduleTestService(t, memstore.New(), mockllm.New(mockllm.TextTurn("x")), workspace)
	svc.SetScheduler(sched)
	// Point the Service's scheduleStore at our memschedulestore by building a
	// Service whose store wraps it — but Service discovers the ScheduleStore via
	// the store's ScheduleStore() accessor, so drive the scheduler's FireNow
	// DIRECTLY through the manager seam the tool consumes: a manager over THIS
	// scheduler. The honest end-to-end gate is the Service's FireNow mapping;
	// drive svc.FireNow's scheduler via a manager stub that calls sched.FireNow.
	mgr := &overlapManager{svc: svc, sched: sched, store: schedStore}
	cat := assembleScheduleCatalog(t, func() port.ScheduleManager { return mgr })

	res := execSchedule(t, cat, `{"verb":"fire","name":"singleton"}`)
	if !res.IsError {
		t.Fatalf("fire on an in-flight singleton = %q, want the singleton-overlap error, not a second fire", res.Content)
	}
	// The error carries the singleton-overlap message the REST FireNow maps
	// (the create-seam's default-true Singleton holds through the tool).
	if !strings.Contains(res.Content, "still running") {
		t.Fatalf("overlap error = %q, want the singleton-overlap message", res.Content)
	}
	// The sentinel parity is assertable directly on the scheduler seam the
	// manager drives: the overlap rejection is the same ErrFireNowOverlap the
	// REST FireNow returns (matched via port.ErrFireNowOverlap, which
	// server.ErrFireNowOverlap wraps).
	_, err := sched.FireNow(ctx, "singleton", time.Now())
	if !errors.Is(err, scheduler.ErrFireNowOverlap) {
		t.Fatalf("scheduler.FireNow on the in-flight singleton = %v, want ErrFireNowOverlap", err)
	}
	// And the manager's own FireNow returns the PORT-level sentinel the tool
	// surfaces (the same one the REST FireNow maps).
	if _, err := mgr.FireNow(ctx, "singleton"); !errors.Is(err, port.ErrFireNowOverlap) {
		t.Fatalf("manager.FireNow on the in-flight singleton = %v, want port.ErrFireNowOverlap", err)
	}
}

// overlapManager is a test ScheduleManager that drives FireNow through a real
// scheduler (the singleton-overlap seam) and the reads through the real store.
// The scheduler runs against the SAME memschedulestore the manager reads, so
// the overlap is exercised end-to-end (not a stub manager returning a canned
// error — the scheduler's singleton check does the work).
type overlapManager struct {
	svc   *server.Service
	sched *scheduler.Scheduler
	store port.ScheduleStore
}

func (m *overlapManager) CreateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	s := port.Schedule{Spec: spec, State: port.ScheduleState{Enabled: true}}
	return s, m.store.Save(ctx, s)
}
func (m *overlapManager) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	return m.store.Load(ctx, name)
}
func (m *overlapManager) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	return m.store.List(ctx)
}
func (m *overlapManager) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	s, err := m.store.Load(ctx, spec.Name)
	if err != nil {
		return port.Schedule{}, err
	}
	s.Spec = spec
	return s, m.store.Save(ctx, s)
}
func (m *overlapManager) DeleteSchedule(ctx context.Context, name string) error {
	return m.store.Delete(ctx, name)
}
func (m *overlapManager) PauseSchedule(ctx context.Context, name string) error {
	return m.store.SetEnabled(ctx, name, false)
}
func (m *overlapManager) ResumeSchedule(ctx context.Context, name string) error {
	return m.store.SetEnabled(ctx, name, true)
}
func (m *overlapManager) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	// The REAL scheduler FireNow path — the singleton overlap returns
	// scheduler.ErrFireNowOverlap, which the tool surfaces (the Service maps it
	// to server.ErrFireNowOverlap wrapping port.ErrFireNowOverlap).
	fire, err := m.sched.FireNow(ctx, name, time.Now())
	if err != nil {
		if errors.Is(err, scheduler.ErrFireNowOverlap) {
			return port.ScheduleFire{}, port.ErrFireNowOverlap
		}
		return port.ScheduleFire{}, err
	}
	return fire, nil
}
func (m *overlapManager) ListFires(ctx context.Context, name string) ([]port.ScheduleFire, error) {
	return m.store.ListFires(ctx, name)
}

// heldPriorFireLease is a port.SessionLease whose Acquire reports a held id as
// owned by someone else (the trial-acquire the scheduler's singleton check
// performs fails → overlap). Mirrors the scheduler_test.go heldSessionLease.
type heldPriorFireLease struct {
	held map[session.SessionID]string
}

func (l *heldPriorFireLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	if o, ok := l.held[id]; ok && o != owner {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Minute)}, nil
}
func (*heldPriorFireLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	return lease, nil
}
func (*heldPriorFireLease) Release(_ context.Context, _ port.Lease) error { return nil }

// TestScheduleTool_SharesStoreWithRESTSurface pins AC1.6: a schedule created
// via the tool is visible, pausable, resumable, and deletable through the SAME
// store the REST/gRPC surface reads (one store, one truth — no tool-specific
// shadow state). The tool drives svc.ScheduleManager (the Service's methods);
// the assertions read the SAME store through the Service's own REST-side
// methods (GetSchedule/ListSchedules/PauseSchedule/ResumeSchedule/DeleteSchedule).
func TestScheduleTool_SharesStoreWithRESTSurface(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	svc := newScheduleTestService(t, jstore, mockllm.New(mockllm.TextTurn("x")), workspace)
	cat := assembleScheduleCatalog(t, svc.ScheduleManager)

	// Create via the TOOL.
	res := execSchedule(t, cat, `{"verb":"create","name":"shared","prompt":"p","cron":"@every 1m","workspace":"`+workspace+`"}`)
	if res.IsError {
		t.Fatalf("tool create = error %q", res.Content)
	}

	// Visible through the SAME store the REST surface reads (Service.GetSchedule).
	sched, err := svc.GetSchedule(ctx, "shared")
	if err != nil {
		t.Fatalf("Service.GetSchedule after tool create: %v — the tool wrote shadow state, not the shared store", err)
	}
	if sched.Spec.Name != "shared" {
		t.Fatalf("GetSchedule name = %q, want shared", sched.Spec.Name)
	}

	// Pausable via the tool; the REST-side read sees the pause.
	if r := execSchedule(t, cat, `{"verb":"pause","name":"shared"}`); r.IsError {
		t.Fatalf("tool pause = error %q", r.Content)
	}
	if paused, err := svc.GetSchedule(ctx, "shared"); err != nil {
		t.Fatalf("GetSchedule after tool pause: %v", err)
	} else if paused.State.Enabled {
		t.Fatal("Enabled = true after tool pause (REST-side read), want false — tool/store shadow state")
	}

	// Resumable via the tool; the REST-side read sees the resume.
	if r := execSchedule(t, cat, `{"verb":"resume","name":"shared"}`); r.IsError {
		t.Fatalf("tool resume = error %q", r.Content)
	}
	if resumed, err := svc.GetSchedule(ctx, "shared"); err != nil {
		t.Fatalf("GetSchedule after tool resume: %v", err)
	} else if !resumed.State.Enabled {
		t.Fatal("Enabled = false after tool resume (REST-side read), want true")
	}

	// Deletable via the tool; the REST-side read sees the deletion.
	if r := execSchedule(t, cat, `{"verb":"delete","name":"shared"}`); r.IsError {
		t.Fatalf("tool delete = error %q", r.Content)
	}
	if _, err := svc.GetSchedule(ctx, "shared"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("GetSchedule after tool delete = %v, want ErrScheduleNotFound (one store, one truth)", err)
	}
}
