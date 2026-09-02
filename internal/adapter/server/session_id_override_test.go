package server_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestCreateSessionWithSessionIDOverride: a caller passing WithSessionID mints
// the session under that id (the override wins over the Service's NewID). Pins
// ADR 0059 decision #7 Phase-2: the scheduler fire path passes a "sched--"
// id and the persisted session carries it.
func TestCreateSessionWithSessionIDOverride(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), nil)

	const want = "sched--nightly-20260713-010203-deadbeef"
	sess, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID(session.SessionID(want)),
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSessionID: %v", err)
	}
	if string(sess.ID) != want {
		t.Errorf("session id = %q, want the override %q", sess.ID, want)
	}
	// The override id is persisted: a GetSession returns the same id.
	loaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if string(loaded.ID) != want {
		t.Errorf("loaded id = %q, want %q", loaded.ID, want)
	}
}

// TestCreateSessionWithSessionIDNoOverrideIsByteIdentical: with no
// WithSessionID option, the Service's NewID generator mints the id — the
// pre-Phase-2 path is unchanged.
func TestCreateSessionWithSessionIDNoOverrideIsByteIdentical(t *testing.T) {
	var newIDCalls atomic.Int32
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     shared,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		NewID: func() session.SessionID {
			newIDCalls.Add(1)
			return "generated-abc"
		},
		DefaultCapabilities: mockllm.New().Capabilities(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile (no opts): %v", err)
	}
	if string(sess.ID) != "generated-abc" {
		t.Errorf("session id = %q, want the generated id", sess.ID)
	}
	if newIDCalls.Load() != 1 {
		t.Errorf("NewID called %d times, want 1", newIDCalls.Load())
	}
}

func TestGeneratedSessionIDCollisionDoesNotOverwriteForeignSnapshot(t *testing.T) {
	store := memstore.New()
	bob := session.Principal{Issuer: "https://issuer.example", Subject: "bob"}
	winner := session.New("forced-collision", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/bob", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := winner.RestoreLabels(&bob, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(winner): %v", err)
	}
	if err := winner.SeedHistory([]session.Message{session.NewUserMessage("BOB_HISTORY")}); err != nil {
		t.Fatalf("SeedHistory(winner): %v", err)
	}
	if err := store.Save(context.Background(), winner); err != nil {
		t.Fatalf("Save(winner): %v", err)
	}

	shared := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine: shared, Store: store, OwnershipEnforced: true,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(2, 0) }, NewID: func() session.SessionID { return "forced-collision" },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	_, err = svc.CreateSession(session.WithPrincipal(context.Background(), &alice), "/alice", session.ModeDefault, session.Limits{})
	if !errors.Is(err, port.ErrSessionAlreadyExists) {
		t.Fatalf("CreateSession(collision) = %v, want ErrSessionAlreadyExists", err)
	}
	got, err := store.Load(context.Background(), "forced-collision")
	if err != nil {
		t.Fatalf("Load(winner): %v", err)
	}
	if got.Owner == nil || !got.Owner.SameIdentity(&bob) || got.EnvironmentRef.ID != "/bob" || len(got.Conversation.Messages) != 1 {
		t.Fatalf("foreign winner was changed: owner=%+v workspace=%q history=%+v", got.Owner, got.EnvironmentRef.ID, got.Conversation.Messages)
	}
}

func TestOwnershipServiceCannotCreateWithoutAtomicStoreCapability(t *testing.T) {
	legacy := &saveLoadOnlyStore{inner: memstore.New()}
	shared := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine: shared, Store: legacy, OwnershipEnforced: true,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		NewID:      func() session.SessionID { return "must-not-upsert" },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	_, err = svc.CreateSession(session.WithPrincipal(context.Background(), &alice), "/ws", session.ModeDefault, session.Limits{})
	if !errors.Is(err, server.ErrConfig) {
		t.Fatalf("CreateSession(save-only ownership store) = %v, want ErrConfig", err)
	}
	if _, err := legacy.Load(context.Background(), "must-not-upsert"); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("save-only store was mutated: %v", err)
	}
}

func TestForkDestinationCollisionDoesNotOverwriteForeignSnapshot(t *testing.T) {
	store := memstore.New()
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	bob := session.Principal{Issuer: "https://issuer.example", Subject: "bob"}
	source := session.New("source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/alice", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := source.RestoreLabels(&alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(source): %v", err)
	}
	destination := session.New("forced-fork-destination", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/bob", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := destination.RestoreLabels(&bob, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(destination): %v", err)
	}
	if err := destination.SeedHistory([]session.Message{session.NewUserMessage("BOB_FORK_DESTINATION")}); err != nil {
		t.Fatalf("SeedHistory(destination): %v", err)
	}
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatalf("Save(source): %v", err)
	}
	if err := store.Save(context.Background(), destination); err != nil {
		t.Fatalf("Save(destination): %v", err)
	}

	shared := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine: shared, Store: store, OwnershipEnforced: true,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(2, 0) }, NewID: func() session.SessionID { return destination.ID },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.ForkSession(session.WithPrincipal(context.Background(), &alice), source.ID, "", ""); !errors.Is(err, port.ErrSessionAlreadyExists) {
		t.Fatalf("ForkSession(collision) = %v, want ErrSessionAlreadyExists", err)
	}
	got, err := store.Load(context.Background(), destination.ID)
	if err != nil {
		t.Fatalf("Load(destination): %v", err)
	}
	if got.Owner == nil || !got.Owner.SameIdentity(&bob) || got.EnvironmentRef.ID != "/bob" || !strings.Contains(got.Conversation.Messages[0].Text, "BOB_FORK_DESTINATION") {
		t.Fatalf("foreign fork destination changed: owner=%+v workspace=%q history=%+v", got.Owner, got.EnvironmentRef.ID, got.Conversation.Messages)
	}
}

func TestExplicitCreateRetryRejectsDifferentSessionTaxonomy(t *testing.T) {
	store := memstore.New()
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	const id session.SessionID = "taxonomy-collision"
	existing := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := existing.RestoreLabels(&alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(existing): %v", err)
	}
	rel := session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: "origin"}
	if err := existing.RestoreSessionMetadata(session.SessionKindScheduled, rel); err != nil {
		t.Fatalf("RestoreSessionMetadata(existing): %v", err)
	}
	if err := store.Save(context.Background(), existing); err != nil {
		t.Fatalf("Save(existing): %v", err)
	}
	shared := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine: shared, Store: store, OwnershipEnforced: true,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = svc.CreateSessionWithProfile(session.WithPrincipal(context.Background(), &alice), "/ws", session.ModeDefault,
		session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
	if !errors.Is(err, server.ErrInvalidArgument) || !strings.Contains(err.Error(), "different request") {
		t.Fatalf("main create over scheduled session = %v, want different-request refusal", err)
	}
}

// TestCreateSessionWithSessionIDCollisionRejected: an override id that collides
// with a LIVE per-session engine (one already in the sessionEngines map) is
// rejected with ErrInvalidArgument — it would shadow an in-flight session.
func TestCreateSessionWithSessionIDCollisionRejected(t *testing.T) {
	var closed atomic.Int32
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("per-session reply")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc := newMCPService(t, "SHARED-REPLY", factory)

	// Create a session under a known id via a non-zero selector (registers a
	// per-session engine in the sessionEngines map).
	const live = "sched--live-1234"
	first, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-x"},
		server.ProfileDefault,
		server.WithSessionID(session.SessionID(live)),
	)
	if err != nil {
		t.Fatalf("first CreateSessionWithProfile: %v", err)
	}
	if string(first.ID) != live {
		t.Fatalf("first session id = %q, want %q", first.ID, live)
	}
	defer svc.CloseSession(first.ID)

	// A second create with the SAME override id must be rejected (the live engine
	// collides). This is the shared-engine fast path (zero selector), which still
	// consults the sessionEngines map for the collision check.
	_, err = svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{},
		server.ProfileDefault,
		server.WithSessionID(session.SessionID(live)),
	)
	if err == nil {
		t.Fatal("second CreateSessionWithProfile with a colliding id succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("collision err = %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), live) {
		t.Errorf("collision err = %v, want it to name the colliding id %q", err, live)
	}
}

// TestCreateSessionWithSessionIDSharedEngineCollisionRejected: a SHARED-engine
// session (empty selector, default profile, default workspace) does NOT register
// in sessionEngines — it is only persisted in the store. A second create under
// the same id must still be rejected via the store-probe collision source, not
// silently succeed and clobber the persisted session. This closes the
// fast-path-no-op gap: the pre-fix check only read sessionEngines.
func TestCreateSessionWithSessionIDSharedEngineCollisionRejected(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), nil)

	const id = "sched--shared-5678"
	first, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID(session.SessionID(id)),
	)
	if err != nil {
		t.Fatalf("first CreateSessionWithProfile: %v", err)
	}
	if string(first.ID) != id {
		t.Fatalf("first session id = %q, want %q", first.ID, id)
	}

	// A second create with the SAME id must be rejected — the first create took
	// the shared-engine fast path (never entered sessionEngines) but IS persisted,
	// so the store probe detects the collision.
	_, err = svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID(session.SessionID(id)),
	)
	if err == nil {
		t.Fatal("second CreateSessionWithProfile with a colliding persisted id succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("collision err = %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("collision err = %v, want it to name the colliding id %q", err, id)
	}
}

// TestCreateSessionWithSessionIDConcurrentTOCTOU is the headline TOCTOU guard:
// two concurrent creates with the SAME WithSessionID id must resolve to EXACTLY
// ONE success and one ErrInvalidArgument rejection — never two successes (the
// old race passed a check that read only sessionEngines and released s.mu before
// the factory call + registration). The reservedIDs in-flight map (or the store
// probe, if the winner already persisted) rejects the loser deterministically.
// Run under -race.
func TestCreateSessionWithSessionIDConcurrentTOCTOU(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), nil)
	const id = "sched--race-9999"

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := svc.CreateSessionWithProfile(
				context.Background(), "/ws", session.ModeDefault, session.Limits{},
				server.ProviderSelector{}, server.ProfileDefault,
				server.WithSessionID(session.SessionID(id)),
			)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	nSuccess, nReject := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			nSuccess++
		case errors.Is(err, server.ErrInvalidArgument):
			nReject++
		default:
			t.Fatalf("unexpected error from concurrent create: %v", err)
		}
	}
	if nSuccess != 1 || nReject != 1 {
		t.Fatalf("concurrent creates = %d success, %d reject; want exactly 1 and 1 (errs=%v)", nSuccess, nReject, errs)
	}
}

func TestCreateSessionWithSessionIDCrossServiceRetryIsIdempotent(t *testing.T) {
	store := &barrierCreateStore{Store: memstore.New(), release: make(chan struct{})}
	shared := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	newSvc := func() *server.Service {
		svc, err := server.NewService(server.Config{
			Engine: shared, Store: store, OwnershipEnforced: true,
			Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		return svc
	}
	services := []*server.Service{newSvc(), newSvc()}
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	ctx := session.WithPrincipal(context.Background(), &alice)
	const id session.SessionID = "cross-service-idempotent"

	var wg sync.WaitGroup
	results := make([]*session.Session, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := range services {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = services[i].CreateSessionWithProfile(ctx, "/ws", session.ModeDefault, session.Limits{},
				server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || results[i] == nil || results[i].ID != id {
			t.Fatalf("creator %d = (%+v, %v), want idempotent success", i, results[i], errs[i])
		}
	}
	stored, err := store.Load(context.Background(), id)
	if err != nil || stored.Owner == nil || !stored.Owner.SameIdentity(&alice) {
		t.Fatalf("stored winner = (%+v, %v), want Alice-owned session", stored, err)
	}
}

type barrierCreateStore struct {
	*memstore.Store
	entered atomic.Int32
	release chan struct{}
}

func (s *barrierCreateStore) Create(ctx context.Context, sess *session.Session) error {
	if s.entered.Add(1) == 2 {
		close(s.release)
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.Create(ctx, sess)
}

// TestCreateSessionWithSessionIDEmptyRejected pins that WithSessionID("") is
// REJECTED (the option's doc promises it) rather than silently minting a random
// id. idSet distinguishes "called with empty" from "never called".
func TestCreateSessionWithSessionIDEmptyRejected(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), nil)
	_, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID(""),
	)
	if err == nil {
		t.Fatal("WithSessionID(\"\") succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

// loadErrStore wraps a memstore so Load returns a synthetic INFRA fault (a
// non-ErrSessionNotFound error), exercising the reserveSessionID probe branch
// that must PROPAGATE the fault rather than silently treat the id as clear.
type loadErrStore struct {
	*memstore.Store
	loadErr error
}

func (s *loadErrStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, s.loadErr
}

// TestCreateSessionWithSessionIDLoadProbeInfraFault: when the store's Load
// returns an infra fault (not ErrSessionNotFound) during the collision probe,
// createSession PROPAGATES it — it must not be swallowed (a silent pass could
// clobber a persisted session) nor mislabelled ErrInvalidArgument.
func TestCreateSessionWithSessionIDLoadProbeInfraFault(t *testing.T) {
	infra := errors.New("boom: store backend unreachable")
	store := &loadErrStore{Store: memstore.New(), loadErr: infra}
	svc := newServiceWithStore(t, store)

	_, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID("sched--probe-1"),
	)
	if !errors.Is(err, infra) {
		t.Fatalf("err = %v, want the infra fault propagated", err)
	}
	if errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("infra fault must NOT be classified ErrInvalidArgument: %v", err)
	}
}
