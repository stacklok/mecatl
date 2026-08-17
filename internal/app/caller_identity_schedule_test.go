package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// alicePrincipal is the verified human caller these scenario tests attribute
// schedules to. Identity is the (issuer, subject) PAIR (ADR 0204 decision 1).
var alicePrincipal = session.Principal{
	Issuer:    "https://idp.example",
	Subject:   "alice",
	GrantType: session.GrantTypeUser,
	Name:      "Alice",
}

// bobPrincipal is the SECOND caller: the one whose context is present while
// Alice's session is the schedule's origin, so an origin-capture test can prove
// the origin session's owner wins over the calling context.
var bobPrincipal = session.Principal{
	Issuer:    "https://idp.example",
	Subject:   "bob",
	GrantType: session.GrantTypeUser,
}

// newScheduleOwnerFixture builds the create-seam under test over in-memory
// adapters: a memstore SessionStore (which is also the PrunableStore childgc
// sweeps) plus a memschedulestore ScheduleStore. It is the SAME
// server.NewScheduleManager composition wires into the Service, so the owner
// capture is exercised where it really lives — not in a stand-in.
func newScheduleOwnerFixture(t *testing.T) (*memstore.Store, interface {
	CreateSchedule(context.Context, port.ScheduleSpec) (port.Schedule, error)
	GetSchedule(context.Context, string) (port.Schedule, error)
	UpdateSchedule(context.Context, port.ScheduleSpec) (port.Schedule, error)
},
) {
	t.Helper()
	sessions := memstore.New()
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store:         sessions,
		ScheduleStore: memschedulestore.New(),
		Diagnostics:   port.NopDiagnostics{},
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager returned nil (no ScheduleStore wired)")
	}
	return sessions, mgr
}

// saveOwnedSession persists an idle session owned by owner (nil = ownerless).
func saveOwnedSession(t *testing.T, store *memstore.Store, id session.SessionID, owner *session.Principal) {
	t.Helper()
	sess := session.New(id, session.ModePlan, "/ws", session.Limits{}, time.Now())
	if err := sess.RestoreLabels(owner, ""); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save(%q): %v", id, err)
	}
}

// ownedScheduleSpec is a minimal valid one-shot spec (non-mutating ⇒ plan mode,
// default profile ⇒ a workspace is required).
func ownedScheduleSpec(name string, origin session.SessionID) port.ScheduleSpec {
	return port.ScheduleSpec{
		Name:            name,
		Prompt:          "check the build",
		Workspace:       "/ws",
		Mode:            session.ModePlan,
		Trigger:         port.TriggerSpec{OneShot: time.Now().Add(time.Hour)},
		OriginSessionID: origin,
	}
}

// TestCallerIdentity_Scenario4_ScheduleOwnerCapturedAtCreate pins AC4.3: a
// schedule created FROM Alice's session records Alice as its owner at CREATE
// time, and that owner survives the origin session being swept by childgc.
//
// The childgc half is the point (ADR 0204 decision 6): deriving the owner at
// FIRE time would read a session that no longer exists, so the capture must be
// durable on the schedule itself. The test therefore sweeps the origin away and
// re-reads the schedule from the store.
//
// It also proves the ORIGIN wins over the calling context: the create runs
// under BOB's principal while Alice's session is the origin, so a capture that
// lazily read the context principal would name Bob and fail here.
func TestCallerIdentity_Scenario4_ScheduleOwnerCapturedAtCreate(t *testing.T) {
	t.Parallel()
	sessions, mgr := newScheduleOwnerFixture(t)
	const originID = session.SessionID("origin-alice")
	saveOwnedSession(t, sessions, originID, &alicePrincipal)

	// The create runs under Bob's context — the origin session's owner must win.
	ctx := session.WithPrincipal(context.Background(), &bobPrincipal)
	created, err := mgr.CreateSchedule(ctx, ownedScheduleSpec("nightly", originID))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	assertOwner(t, "created schedule", created.Spec.Owner, alicePrincipal.Issuer, alicePrincipal.Subject, session.GrantTypeUser)

	// Sweep the origin session away, exactly as the retention sweeper does.
	gc := &childGC{
		store:  sessions,
		pager:  sessions,
		policy: childGCPolicy{mainRetention: time.Nanosecond},
		isLive: func(session.SessionID) bool { return false },
		now:    func() time.Time { return time.Now().Add(time.Hour) },
		diag:   port.NopDiagnostics{},
	}
	if deleted, _ := gc.sweep(context.Background()); deleted != 1 {
		t.Fatalf("childgc swept %d sessions, want 1 (the origin)", deleted)
	}
	if _, err := sessions.Load(context.Background(), originID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("origin session still loadable after sweep (err=%v); the AC4.3 premise requires it gone", err)
	}

	// The schedule still names its owner, read back from the store.
	loaded, err := mgr.GetSchedule(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("GetSchedule after sweep: %v", err)
	}
	assertOwner(t, "schedule after origin sweep", loaded.Spec.Owner, alicePrincipal.Issuer, alicePrincipal.Subject, session.GrantTypeUser)

	// Write-once: an Update must not re-own the schedule to the updating caller.
	updated, err := mgr.UpdateSchedule(session.WithPrincipal(context.Background(), &bobPrincipal), ownedScheduleSpec("nightly", ""))
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	assertOwner(t, "schedule after update", updated.Spec.Owner, alicePrincipal.Issuer, alicePrincipal.Subject, session.GrantTypeUser)
}

// vanishingStore serves the origin session on its FIRST Load and reports it
// gone on every later one, reproducing the childgc sweep landing between the
// create-seam's origin validation and its owner capture. It is a legal
// port.SessionStore: a deleted session is exactly ErrSessionNotFound.
type vanishingStore struct {
	inner *memstore.Store
	id    session.SessionID
	loads int
}

func (v *vanishingStore) Save(ctx context.Context, s *session.Session) error {
	return v.inner.Save(ctx, s)
}

func (v *vanishingStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if id == v.id {
		v.loads++
		if v.loads > 1 {
			return nil, port.ErrSessionNotFound
		}
	}
	return v.inner.Load(ctx, id)
}

// TestCallerIdentity_Scenario4_ScheduleOwnerSurvivesOriginDeletionRace pins the
// race half of AC4.3: the captured owner comes from the load the create-seam
// ALREADY validated, not from a second read of the origin session. A childgc
// sweep landing between the two reads must not turn a validated, owned create
// into a silently OWNERLESS schedule — an ownerless schedule can never be
// attributed, and validation had already passed.
func TestCallerIdentity_Scenario4_ScheduleOwnerSurvivesOriginDeletionRace(t *testing.T) {
	t.Parallel()
	const originID = session.SessionID("origin-alice-racing")
	sessions := memstore.New()
	store := &vanishingStore{inner: sessions, id: originID}
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store:         store,
		ScheduleStore: memschedulestore.New(),
		Diagnostics:   port.NopDiagnostics{},
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager returned nil")
	}
	saveOwnedSession(t, sessions, originID, &alicePrincipal)

	created, err := mgr.CreateSchedule(context.Background(), ownedScheduleSpec("racing", originID))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if store.loads != 1 {
		t.Errorf("origin session Loaded %d times, want 1 (the validated load is threaded, not re-read)", store.loads)
	}
	assertOwner(t, "schedule created across an origin sweep", created.Spec.Owner,
		alicePrincipal.Issuer, alicePrincipal.Subject, session.GrantTypeUser)
}

// TestCallerIdentity_Scenario4_OutOfBandScheduleOwnerFromContext pins AC4.6: a
// schedule created out of band (REST/CLI — no origin session) under a verified
// principal records the CONTEXT principal as its owner, not an origin lookup.
// An unauthenticated create stays ownerless — never a fabricated owner.
func TestCallerIdentity_Scenario4_OutOfBandScheduleOwnerFromContext(t *testing.T) {
	t.Parallel()
	_, mgr := newScheduleOwnerFixture(t)

	ctx := session.WithPrincipal(context.Background(), &bobPrincipal)
	created, err := mgr.CreateSchedule(ctx, ownedScheduleSpec("out-of-band", ""))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	assertOwner(t, "out-of-band schedule", created.Spec.Owner, bobPrincipal.Issuer, bobPrincipal.Subject, session.GrantTypeUser)

	// No principal on the context ⇒ ownerless, never fabricated.
	anon, err := mgr.CreateSchedule(context.Background(), ownedScheduleSpec("anon", ""))
	if err != nil {
		t.Fatalf("CreateSchedule (no principal): %v", err)
	}
	if anon.Spec.Owner != nil {
		t.Errorf("unauthenticated create recorded owner %+v, want nil (never fabricated)", *anon.Spec.Owner)
	}
}

// TestCallerIdentity_Scenario4_FireRunsAsOwnerClientCredentials pins AC4.4: a
// scheduled fire's "sched--" session records the SCHEDULE's owner subject with
// GrantType client_credentials — injected via the explicit WithOwner
// CreateSessionOption — while its durable EVENTS name the acting principal: the
// scheduler's SYSTEM principal.
//
// That split is the point. The owner is accountable ("whose is this?"); the
// scheduler is what acted ("who did this?"). Alice is still named on the session,
// but the events no longer claim she personally acted at 3am.
//
// The fire runs under the scheduler's SYSTEM principal (syscaller.Context), so
// without the injection the create-seam's resolveOwner would stamp
// mecatl:internal/scheduler as the fire session's OWNER. The test asserts that
// system principal does NOT leak onto the session.
func TestCallerIdentity_Scenario4_FireRunsAsOwnerClientCredentials(t *testing.T) {
	t.Parallel()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(mockllm.TextTurn("fire done"))
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
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	owner := alicePrincipal
	sched := port.Schedule{Spec: port.ScheduleSpec{
		Name:      "owned-fire",
		Prompt:    "say hi",
		Workspace: workspace,
		Mode:      session.ModePlan,
		Owner:     &owner,
	}}

	// The scheduler's fire goroutine runs under the system principal.
	ctx := syscaller.Context(context.Background(), syscaller.RootScheduler)
	fire := makeFireFunc(svc, store.ScheduleStore(), defaultFireTimeout, nil)
	rec, err := fire(ctx, sched, time.Now())
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if rec.Stop != session.StopEndTurn {
		t.Fatalf("fire stop = %q, want %q", rec.Stop, session.StopEndTurn)
	}

	fired, err := store.Load(context.Background(), rec.SessionID)
	if err != nil {
		t.Fatalf("Load fire session %q: %v", rec.SessionID, err)
	}
	assertOwner(t, "fire session", fired.Owner, owner.Issuer, owner.Subject, session.GrantTypeClientCredentials)
	if fired.Owner != nil && fired.Owner.Issuer == syscaller.Issuer {
		t.Errorf("fire session owner issuer = %q: the scheduler's system principal leaked through", fired.Owner.Issuer)
	}

	// The schedule LIFECYCLE event ("fired") is what the scheduler emits after the
	// FireFunc returns, on the same system-principal context. It must reach the log
	// through the ONE stamping chokepoint — not as an unstamped direct Append.
	svc.EmitScheduleEvent(ctx, session.SchedulePayload{
		ScheduleName: sched.Spec.Name,
		FireID:       rec.ID,
		SessionID:    rec.SessionID,
		Kind:         "fired",
		Stop:         rec.Stop,
	})

	// The fire's durable events name the ACTING principal — the scheduler's system
	// principal — not the accountable owner on the session.
	var events int
	var sawFired bool
	for ev, err := range store.Read(context.Background(), rec.SessionID) {
		if err != nil {
			t.Fatalf("EventLog.Read: %v", err)
		}
		events++
		if ev.Type == session.EvScheduleFired {
			sawFired = true
		}
		assertOwner(t, "event "+string(ev.Type)+" actor", ev.Actor,
			syscaller.Issuer, string(syscaller.RootScheduler), session.GrantTypeSystem)
	}
	if events == 0 {
		t.Fatal("the fire session's durable log is empty; the actor assertion never ran")
	}
	if !sawFired {
		t.Fatal("no schedule.fired event in the durable log; the lifecycle-stamp assertion never ran")
	}
}

// assertOwner asserts a recorded principal names the expected (issuer, subject)
// pair with the expected grant type. A nil principal is a failure — these ACs
// are about a PRESENT attribution.
func assertOwner(t *testing.T, what string, got *session.Principal, issuer, subject string, grant session.GrantType) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: owner is nil, want subject %q", what, subject)
	}
	if got.Issuer != issuer || got.Subject != subject {
		t.Errorf("%s: identity = (%q, %q), want (%q, %q)", what, got.Issuer, got.Subject, issuer, subject)
	}
	if got.GrantType != grant {
		t.Errorf("%s: grant type = %q, want %q", what, got.GrantType, grant)
	}
}
