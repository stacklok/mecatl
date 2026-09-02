package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner pins ADR
// 0102's creation rule: the verified owner is persisted before visibility, the
// same immutable request is retry-idempotent, and another owner learns only
// absence from a colliding caller-selected ID.
func TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner(t *testing.T) {
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store:             store,
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(0, 0) },
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	id := session.SessionID("caller-selected-id")
	alice := &session.Principal{Issuer: "https://idp.example/alice", Subject: "same", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "https://idp.example/bob", Subject: "same", GrantType: session.GrantTypeUser}
	request := func(ctx context.Context, workspace string) (*session.Session, error) {
		return svc.CreateSessionWithProfile(ctx, workspace, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
	}

	created, err := request(session.WithPrincipal(context.Background(), alice), "/ws")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.Owner == nil || !created.Owner.SameIdentity(alice) {
		t.Fatalf("created owner = %+v, want Alice's verified identity", created.Owner)
	}

	retry, err := request(session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: alice.Issuer, Subject: alice.Subject, GrantType: session.GrantTypeClientCredentials, Name: "untrusted display",
	}), "/ws")
	if err != nil {
		t.Fatalf("same-owner identical retry: %v", err)
	}
	if retry.ID != created.ID || retry.Owner == nil || !retry.Owner.SameIdentity(alice) {
		t.Fatalf("retry = %+v, want the original owned session", retry)
	}

	if retry, err := request(session.WithPrincipal(context.Background(), alice), "/other"); err != nil || retry.ID != created.ID {
		t.Fatalf("client workspace must be ignored by server-owned placement: retry=%+v err=%v", retry, err)
	}
	if _, err := request(session.WithPrincipal(context.Background(), bob), "/ws"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("cross-owner collision error = %v, want ErrNotFound", err)
	}
	persisted, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load original: %v", err)
	}
	if persisted.Owner == nil || !persisted.Owner.SameIdentity(alice) {
		t.Fatalf("cross-owner collision overwrote or adopted owner: %+v", persisted.Owner)
	}
}

func callerSeparationFixture(t *testing.T) (*server.Service, *memstore.Store, *memschedulestore.Store, context.Context, context.Context) {
	t.Helper()
	sessions := memstore.New()
	schedules := memschedulestore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:  sessions, EventLog: memstore.NewEventLog(),
		ScheduleManager:  server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules, OwnershipEnforced: true}),
		DefaultWorkspace: "/ws",
		Workspaces:       func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:              func() time.Time { return time.Unix(0, 0) }, OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "bob", GrantType: session.GrantTypeUser})
	return svc, sessions, schedules, alice, bob
}

func callerSchedule(name string) port.ScheduleSpec {
	return port.ScheduleSpec{Name: name, Prompt: "do work", Mode: session.ModePlan, Trigger: port.TriggerSpec{OneShot: time.Now().Add(time.Hour)}}
}

func TestCallerSeparation_Scenario1_OwnerCanAccessOwnedResources(t *testing.T) {
	svc, _, _, alice, _ := callerSeparationFixture(t)
	sess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.GetSession(alice, sess.ID); err != nil {
		t.Fatalf("GetSession owned: %v", err)
	}
	if _, err := svc.CreateSchedule(alice, callerSchedule("alice-schedule")); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if _, err := svc.GetSchedule(alice, "alice-schedule"); err != nil {
		t.Fatalf("GetSchedule owned: %v", err)
	}
}

func TestCallerSeparation_Scenario1_OwnerlessResourcesAreNotAdopted(t *testing.T) {
	svc, sessions, schedules, alice, _ := callerSeparationFixture(t)
	ownerless := session.New("ownerless", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := sessions.Save(context.Background(), ownerless); err != nil {
		t.Fatalf("save ownerless session: %v", err)
	}
	if err := schedules.Save(context.Background(), port.Schedule{Spec: callerSchedule("ownerless-schedule")}); err != nil {
		t.Fatalf("save ownerless schedule: %v", err)
	}
	if _, err := svc.GetSession(alice, ownerless.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("GetSession ownerless = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetSchedule(alice, "ownerless-schedule"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("GetSchedule ownerless = %v, want ErrScheduleNotFound", err)
	}
	stored, err := sessions.Load(context.Background(), ownerless.ID)
	if err != nil || stored.Owner != nil {
		t.Fatalf("ownerless session after access = %+v, %v; want nil owner", stored, err)
	}
	storedSchedule, err := schedules.Load(context.Background(), "ownerless-schedule")
	if err != nil || storedSchedule.Spec.Owner != nil {
		t.Fatalf("ownerless schedule after access = %+v, %v; want nil owner", storedSchedule, err)
	}
}

func TestCallerSeparation_Scenario1_ForkAndCarryoverAuthorizeSource(t *testing.T) {
	svc, sessions, _, alice, bob := callerSeparationFixture(t)
	source, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession source: %v", err)
	}
	if _, err := svc.ForkSession(bob, source.ID, "", ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Bob ForkSession = %v, want ErrNotFound", err)
	}
	if _, err := svc.CreateSessionWithProfile(bob, "/ws", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(source.ID)); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Bob carryover = %v, want ErrNotFound", err)
	}
	stored, err := sessions.Load(context.Background(), source.ID)
	if err != nil || stored.Owner == nil || !stored.Owner.SameIdentity(session.PrincipalFromContext(alice)) {
		t.Fatalf("source changed after denied references: %+v, %v", stored, err)
	}
	if _, err := svc.ForkSession(alice, source.ID, "", ""); err != nil {
		t.Fatalf("Alice ForkSession: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "/ws", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(source.ID)); err != nil {
		t.Fatalf("Alice carryover: %v", err)
	}
}

func TestCallerSeparation_Scenario2_ListMetadataIsOwnerScoped(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)
	if _, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(bob, "/ws", session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSchedule(alice, callerSchedule("alice-list")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSchedule(bob, callerSchedule("bob-list")); err != nil {
		t.Fatal(err)
	}
	aliceSessions, err := svc.ListSessions(alice)
	if err != nil || len(aliceSessions) != 1 {
		t.Fatalf("Alice sessions = %+v, %v; want exactly one", aliceSessions, err)
	}
	aliceSchedules, err := svc.ListSchedules(alice)
	if err != nil || len(aliceSchedules) != 1 || aliceSchedules[0].Spec.Name != "alice-list" {
		t.Fatalf("Alice schedules = %+v, %v; want only alice-list", aliceSchedules, err)
	}
}

func TestCallerSeparation_Scenario2_OwnerMismatchIsNotFound(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)
	sess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSession(bob, sess.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Bob GetSession = %v, want ErrNotFound", err)
	}
	if _, err := svc.SetMode(bob, sess.ID, session.ModePlan); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Bob SetMode = %v, want ErrNotFound", err)
	}
}

func TestCallerSeparation_Scenario2_EventStreamsResolveParentOwner(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)
	sess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StreamSessionEvents(bob, sess.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Bob event stream = %v, want ErrNotFound", err)
	}
	if _, err := svc.StreamSessionEvents(alice, sess.ID); err != nil {
		t.Fatalf("Alice event stream: %v", err)
	}
}

// TestCallerSeparation_Scenario2_ScheduleNotFoundDoesNotLeakPhysicalKey pins
// the schedule half of the absence-shaped-error contract against a REAL
// owner-namespacing backend: redisstore.Load's not-found error embeds
// whatever key string it was handed (`%w: %q`), and the key the manager hands
// it is the OWNER-NAMESPACED PHYSICAL key, not the caller's literal name. An
// unnormalized not-found error therefore leaks the SHA-256 owner-namespace
// hash to the client — an internal-implementation-detail leak, not a
// cross-owner DATA leak, but still a regression against "absence-shaped, not
// a distinguishing error" (this is exactly what the live-cluster walkthrough
// in .scratch/caller-separation-walkthrough.md surfaced; memschedulestore's
// bare sentinel error can't reproduce it, so this test goes through the real
// redisstore backend instead).
func TestCallerSeparation_Scenario2_ScheduleNotFoundDoesNotLeakPhysicalKey(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rst, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	sessions := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:             sessions,
		ScheduleManager:   server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: rst.ScheduleStore(), OwnershipEnforced: true}),
		DefaultWorkspace:  "/ws",
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "bob", GrantType: session.GrantTypeUser})

	if _, err := svc.CreateSchedule(alice, callerSchedule("alice-only")); err != nil {
		t.Fatalf("Alice CreateSchedule: %v", err)
	}

	_, err = svc.GetSchedule(bob, "alice-only")
	if !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("Bob GetSchedule = %v, want ErrScheduleNotFound", err)
	}
	if !strings.Contains(err.Error(), "alice-only") {
		t.Fatalf("error %q does not name the literal schedule name", err.Error())
	}
	if strings.Contains(err.Error(), "schedule/") || strings.Contains(err.Error(), "\x00") {
		t.Fatalf("error %q leaks the owner-namespaced physical key", err.Error())
	}
}

// TestCallerSeparation_Scenario6_SameNameDifferentOwnersDoNotCollide pins
// AC6.1: a schedule name already used by a DIFFERENT owner is not a collision
// at all — the create-seam namespaces the store-facing key by verified owner
// (issue #368, ADR-0212 decision 1), so two owners may use the identical
// literal name, each independently loadable/updatable/deletable.
func TestCallerSeparation_Scenario6_SameNameDifferentOwnersDoNotCollide(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)

	if _, err := svc.CreateSchedule(alice, callerSchedule("shared-name")); err != nil {
		t.Fatalf("Alice CreateSchedule: %v", err)
	}
	if _, err := svc.CreateSchedule(bob, callerSchedule("shared-name")); err != nil {
		t.Fatalf("Bob CreateSchedule (same name, different owner) = %v, want success (no collision)", err)
	}

	aliceSched, err := svc.GetSchedule(alice, "shared-name")
	if err != nil {
		t.Fatalf("Alice GetSchedule: %v", err)
	}
	if aliceSched.Spec.Name != "shared-name" || aliceSched.Spec.Owner == nil || !aliceSched.Spec.Owner.SameIdentity(session.PrincipalFromContext(alice)) {
		t.Fatalf("Alice schedule = %+v, want literal name + Alice owner", aliceSched.Spec)
	}
	bobSched, err := svc.GetSchedule(bob, "shared-name")
	if err != nil {
		t.Fatalf("Bob GetSchedule: %v", err)
	}
	if bobSched.Spec.Name != "shared-name" || bobSched.Spec.Owner == nil || !bobSched.Spec.Owner.SameIdentity(session.PrincipalFromContext(bob)) {
		t.Fatalf("Bob schedule = %+v, want literal name + Bob owner", bobSched.Spec)
	}

	// Independently updatable: updating Bob's does not touch Alice's.
	bobUpdate := callerSchedule("shared-name")
	bobUpdate.Prompt = "bob's updated prompt"
	if _, err := svc.UpdateSchedule(bob, bobUpdate); err != nil {
		t.Fatalf("Bob UpdateSchedule: %v", err)
	}
	aliceAfterBobUpdate, err := svc.GetSchedule(alice, "shared-name")
	if err != nil {
		t.Fatalf("Alice GetSchedule after Bob's update: %v", err)
	}
	if aliceAfterBobUpdate.Spec.Prompt != "do work" {
		t.Fatalf("Alice schedule prompt = %q after Bob's update, want unaffected %q", aliceAfterBobUpdate.Spec.Prompt, "do work")
	}

	// Independently deletable: deleting Bob's leaves Alice's intact.
	if err := svc.DeleteSchedule(bob, "shared-name"); err != nil {
		t.Fatalf("Bob DeleteSchedule: %v", err)
	}
	if _, err := svc.GetSchedule(bob, "shared-name"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("Bob GetSchedule after delete = %v, want ErrScheduleNotFound", err)
	}
	if _, err := svc.GetSchedule(alice, "shared-name"); err != nil {
		t.Fatalf("Alice GetSchedule after Bob's delete: %v, want Alice's schedule still present", err)
	}
}

// TestCallerSeparation_Scenario6_SameOwnerCollisionStillRejected pins AC6.2: a
// create using a name already used by the SAME owner is still rejected,
// unchanged from today's behavior — only the CROSS-owner case changed.
func TestCallerSeparation_Scenario6_SameOwnerCollisionStillRejected(t *testing.T) {
	svc, _, _, alice, _ := callerSeparationFixture(t)

	if _, err := svc.CreateSchedule(alice, callerSchedule("dup")); err != nil {
		t.Fatalf("first CreateSchedule: %v", err)
	}
	if _, err := svc.CreateSchedule(alice, callerSchedule("dup")); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("same-owner duplicate CreateSchedule = %v, want ErrInvalidArgument", err)
	}
}

// TestCallerSeparation_Scenario6_CollisionProbeDoesNotLeakOtherOwner pins
// AC6.3: creating under a name already used by a DIFFERENT owner is
// indistinguishable from creating under a name nobody has used — the ONLY way
// to make a cross-owner name-reuse probe truly non-leaking is for it not to
// conflict at all, so both cases succeed identically.
func TestCallerSeparation_Scenario6_CollisionProbeDoesNotLeakOtherOwner(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)

	if _, err := svc.CreateSchedule(alice, callerSchedule("probe-name")); err != nil {
		t.Fatalf("Alice CreateSchedule: %v", err)
	}

	// Bob probing a name Alice already owns and a name nobody has ever used
	// both succeed, with the same shape of result — no error, no field, no
	// distinguishing signal that "probe-name" was already taken by someone
	// else.
	collision, collisionErr := svc.CreateSchedule(bob, callerSchedule("probe-name"))
	fresh, freshErr := svc.CreateSchedule(bob, callerSchedule("never-used-name"))
	if collisionErr != nil || freshErr != nil {
		t.Fatalf("collision err = %v, fresh err = %v; want both nil (no distinguishing failure)", collisionErr, freshErr)
	}
	if collision.Spec.Name != "probe-name" || fresh.Spec.Name != "never-used-name" {
		t.Fatalf("returned literal names = %q, %q; want the caller-supplied names echoed back unprefixed", collision.Spec.Name, fresh.Spec.Name)
	}
	if collision.Spec.Owner == nil || !collision.Spec.Owner.SameIdentity(session.PrincipalFromContext(bob)) {
		t.Fatalf("collision-probe schedule owner = %+v, want Bob", collision.Spec.Owner)
	}
}

// TestCallerSeparation_Scenario6_OwnerlessNamespaceUnchanged pins AC6.4: with
// no verifier wired (OwnershipEnforced=false), schedule creation and same-name
// collision detection stay byte-identical to today — a single flat namespace,
// where the store-facing physical key is exactly the literal caller-supplied
// name (no owner prefix at all).
func TestCallerSeparation_Scenario6_OwnerlessNamespaceUnchanged(t *testing.T) {
	sessions := memstore.New()
	schedules := memschedulestore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:  sessions, EventLog: memstore.NewEventLog(),
		ScheduleManager:  server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules}),
		DefaultWorkspace: "/ws",
		Workspaces:       func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:              func() time.Time { return time.Unix(0, 0) },
		// OwnershipEnforced left false: the byte-identical no-verifier posture.
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()

	if _, err := svc.CreateSchedule(ctx, callerSchedule("flat-name")); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// The physical key is EXACTLY the literal name — no owner prefix.
	stored, err := schedules.Load(ctx, "flat-name")
	if err != nil {
		t.Fatalf("direct store Load(%q): %v (the physical key must equal the literal name)", "flat-name", err)
	}
	if stored.Spec.Name != "flat-name" {
		t.Fatalf("stored Spec.Name = %q, want the unprefixed literal %q", stored.Spec.Name, "flat-name")
	}

	// A second create under the same name is still rejected — one flat
	// namespace, no owner to disambiguate.
	if _, err := svc.CreateSchedule(ctx, callerSchedule("flat-name")); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("duplicate CreateSchedule = %v, want ErrInvalidArgument (single flat namespace)", err)
	}
}

func redisCallerScheduleFixture(t *testing.T) (*server.Service, port.ScheduleStore, context.Context, context.Context) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rst, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	sessions := memstore.New()
	schedules := rst.ScheduleStore()
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:             sessions,
		ScheduleManager:   server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules, OwnershipEnforced: true}),
		DefaultWorkspace:  "/ws",
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: "bob", GrantType: session.GrantTypeUser})
	return svc, schedules, alice, bob
}

func TestCallerSeparation_Scenario2_ScheduleQueryListFiltersForeignSchedules(t *testing.T) {
	svc, _, alice, bob := redisCallerScheduleFixture(t)
	if _, err := svc.CreateSchedule(alice, callerSchedule("alice-secret")); err != nil {
		t.Fatalf("Alice CreateSchedule: %v", err)
	}
	if _, err := svc.CreateSchedule(bob, callerSchedule("bob-visible")); err != nil {
		t.Fatalf("Bob CreateSchedule: %v", err)
	}

	query := agent.NewScheduleQueryTool(svc.ScheduleManager())
	result, err := query.Execute(bob, session.ToolCall{ID: "list", Name: agent.ScheduleQueryToolName, Args: []byte(`{"verb":"list"}`)}, callerSeparationTestEnv())
	if err != nil {
		t.Fatalf("ScheduleQuery list: %v", err)
	}
	if result.IsError {
		t.Fatalf("ScheduleQuery list error: %q", result.Content)
	}
	if !strings.Contains(result.Content, "bob-visible") {
		t.Fatalf("ScheduleQuery list = %q, want Bob's schedule", result.Content)
	}
	for _, forbidden := range []string{"alice-secret", "schedule/", "\x00"} {
		if strings.Contains(result.Content, forbidden) {
			t.Fatalf("ScheduleQuery list = %q, leaks %q", result.Content, forbidden)
		}
	}
}

func TestCallerSeparation_Scenario2_GetFireRejectsForeignPhysicalParent(t *testing.T) {
	svc, schedules, alice, bob := redisCallerScheduleFixture(t)
	if _, err := svc.CreateSchedule(alice, callerSchedule("alice-only")); err != nil {
		t.Fatalf("Alice CreateSchedule: %v", err)
	}
	stored, err := schedules.List(context.Background())
	if err != nil || len(stored) != 1 {
		t.Fatalf("raw schedule list = %+v, %v; want Alice physical schedule", stored, err)
	}
	physicalName := stored[0].Spec.Name
	if err := schedules.RecordFire(context.Background(), port.ScheduleFire{ID: "alice-fire", ScheduleName: physicalName, SessionID: "alice-fire-session", Stop: session.StopEndTurn}); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}
	_, err = svc.GetFire(bob, "alice-fire")
	if !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("Bob GetFire without decoy = %v, want ErrScheduleNotFound", err)
	}
	for _, forbidden := range []string{physicalName, "schedule/", "\x00", "alice-only"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Bob GetFire without decoy error %q leaks %q", err, forbidden)
		}
	}

	if _, err := svc.CreateSchedule(bob, callerSchedule(physicalName)); err != nil {
		t.Fatalf("Bob CreateSchedule decoy: %v", err)
	}

	_, err = svc.GetFire(bob, "alice-fire")
	if !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatalf("Bob GetFire = %v, want ErrScheduleNotFound", err)
	}
	for _, forbidden := range []string{physicalName, "schedule/", "\x00", "alice-only"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Bob GetFire error %q leaks %q", err, forbidden)
		}
	}

	fire, err := svc.GetFire(alice, "alice-fire")
	if err != nil {
		t.Fatalf("Alice GetFire: %v", err)
	}
	if fire.ScheduleName != "alice-only" {
		t.Fatalf("Alice fire schedule name = %q, want literal name", fire.ScheduleName)
	}
}

func TestCallerSeparation_Scenario2_OwnerlessGetFireKeepsOrphanCompatibility(t *testing.T) {
	sessions := memstore.New()
	schedules := memschedulestore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:           agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:            sessions,
		ScheduleManager:  server.NewScheduleManager(server.ScheduleManagerConfig{Store: sessions, ScheduleStore: schedules}),
		DefaultWorkspace: "/ws",
		Workspaces:       func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateSchedule(ctx, callerSchedule("deleted-parent")); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "orphan-fire", ScheduleName: "deleted-parent", SessionID: "orphan-session", Stop: session.StopEndTurn}); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}
	if err := svc.DeleteSchedule(ctx, "deleted-parent"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	fire, err := svc.GetFire(ctx, "orphan-fire")
	if err != nil {
		t.Fatalf("GetFire orphan: %v", err)
	}
	if fire.ScheduleName != "deleted-parent" {
		t.Fatalf("orphan fire schedule name = %q, want deleted-parent", fire.ScheduleName)
	}
}

// callerScheduleWithOrigin is callerSchedule plus an OriginSessionID — the
// field a schedule's fire delivery (internal/app deliverFireResult /
// deliverFireStarted) trusts as the session to enqueue content into.
func callerScheduleWithOrigin(name string, origin session.SessionID) port.ScheduleSpec {
	spec := callerSchedule(name)
	spec.OriginSessionID = origin
	return spec
}

// TestCallerSeparation_Scenario_ForeignScheduleOriginIsRejected pins review
// finding 1 (issue #368, ADR 0212): under ownership enforcement, a caller who
// merely KNOWS another caller's session id must not be able to name it as a
// schedule's OriginSessionID — that field is what the fire delivery path later
// trusts to enqueue the fire's content into. The create must be rejected
// fail-closed (ErrInvalidArgument), and NOTHING must be persisted (a
// half-created schedule pointing at a foreign session would still be a hole).
func TestCallerSeparation_Scenario_ForeignScheduleOriginIsRejected(t *testing.T) {
	svc, sessions, schedules, alice, bob := callerSeparationFixture(t)

	bobSess, err := svc.CreateSession(bob, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Bob CreateSession: %v", err)
	}

	_, err = svc.CreateSchedule(alice, callerScheduleWithOrigin("alice-targets-bob", bobSess.ID))
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("Alice CreateSchedule with Bob's session as origin = %v, want ErrInvalidArgument", err)
	}
	if strings.Contains(err.Error(), string(bobSess.ID)) {
		// The error still names the CALLER-SUPPLIED origin id (that's not a
		// leak — Alice already knows the id she typed); it must not, however,
		// name Bob or his session's content.
		if strings.Contains(err.Error(), "bob") {
			t.Fatalf("error %q names Bob", err.Error())
		}
	}

	// Fail-closed: nothing was persisted anywhere.
	if _, gerr := svc.GetSchedule(alice, "alice-targets-bob"); !errors.Is(gerr, port.ErrScheduleNotFound) {
		t.Fatalf("the rejected schedule was visible to Alice: %v", gerr)
	}
	stored, lerr := schedules.List(context.Background())
	if lerr != nil {
		t.Fatalf("raw schedule list: %v", lerr)
	}
	if len(stored) != 0 {
		t.Fatalf("raw schedule store = %+v, want empty (fail-closed means not saved)", stored)
	}

	// Bob's session itself is untouched by the rejected attempt.
	stillBob, serr := sessions.Load(context.Background(), bobSess.ID)
	if serr != nil {
		t.Fatalf("load Bob's session: %v", serr)
	}
	if stillBob.Owner == nil || !stillBob.Owner.SameIdentity(session.PrincipalFromContext(bob)) {
		t.Fatalf("Bob's session owner changed: %+v", stillBob.Owner)
	}
}

// TestCallerSeparation_Scenario_ForeignAndMissingScheduleOriginErrorsAreIndistinguishable
// pins the second half of review finding 1: naming a session that belongs to
// someone else and naming a session that does not exist at all must be
// BYTE-IDENTICAL errors for the same id (never a distinguishing "exists but
// isn't yours" vs "no such session") — otherwise the error itself becomes an
// existence/ownership oracle a caller could probe with candidate session ids.
func TestCallerSeparation_Scenario_ForeignAndMissingScheduleOriginErrorsAreIndistinguishable(t *testing.T) {
	svc, _, _, alice, bob := callerSeparationFixture(t)

	bobSess, err := svc.CreateSession(bob, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Bob CreateSession: %v", err)
	}

	_, foreignErr := svc.CreateSchedule(alice, callerScheduleWithOrigin("probe-foreign", bobSess.ID))
	if !errors.Is(foreignErr, server.ErrInvalidArgument) {
		t.Fatalf("foreign-origin CreateSchedule = %v, want ErrInvalidArgument", foreignErr)
	}

	// A missing session id, formatted to the SAME length/shape as Bob's real
	// id so the comparison below isn't accidentally trivial.
	missingID := session.SessionID(strings.Repeat("z", len(string(bobSess.ID))))
	_, missingErr := svc.CreateSchedule(alice, callerScheduleWithOrigin("probe-missing", missingID))
	if !errors.Is(missingErr, server.ErrInvalidArgument) {
		t.Fatalf("missing-origin CreateSchedule = %v, want ErrInvalidArgument", missingErr)
	}

	// Substitute each error's own origin id back to a common placeholder and
	// compare: the two messages must be identical apart from the id itself.
	normalize := func(err error, id session.SessionID) string {
		return strings.ReplaceAll(err.Error(), string(id), "<id>")
	}
	got, want := normalize(foreignErr, bobSess.ID), normalize(missingErr, missingID)
	if got != want {
		t.Fatalf("foreign-origin error %q and missing-origin error %q are distinguishable (normalized: %q vs %q)",
			foreignErr.Error(), missingErr.Error(), got, want)
	}
}

// TestCallerSeparation_Scenario_OwnScheduleOriginIsStillAccepted pins the
// non-regression half: a caller naming a session THEY themselves own as the
// origin must keep working exactly as before under enforcement.
func TestCallerSeparation_Scenario_OwnScheduleOriginIsStillAccepted(t *testing.T) {
	svc, _, _, alice, _ := callerSeparationFixture(t)

	aliceSess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Alice CreateSession: %v", err)
	}
	created, err := svc.CreateSchedule(alice, callerScheduleWithOrigin("alice-own-origin", aliceSess.ID))
	if err != nil {
		t.Fatalf("Alice CreateSchedule with her OWN session as origin = %v, want success", err)
	}
	if created.Spec.Owner == nil || !created.Spec.Owner.SameIdentity(session.PrincipalFromContext(alice)) {
		t.Fatalf("created schedule owner = %+v, want Alice", created.Spec.Owner)
	}
}
