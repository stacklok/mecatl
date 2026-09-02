package server_test

import (
	"context"
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
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// alice and bob are the two verified principals the Scenario 3 tests use. They
// differ in BOTH halves of the identity pair (issuer + subject), so a test that
// accidentally compares only `sub` still distinguishes them.
var (
	alice = &session.Principal{Issuer: "https://idp.example/alice-realm", Subject: "alice", GrantType: session.GrantTypeUser, Name: "Alice"}
	bob   = &session.Principal{Issuer: "https://idp.example/bob-realm", Subject: "bob", GrantType: session.GrantTypeUser, Name: "Bob"}
)

// ownerOf is the assertion helper: it reports the session/row owner as a
// comparable value, treating nil as "unowned" without dereferencing.
func ownerOf(p *session.Principal) session.Principal {
	if p == nil {
		return session.Principal{}
	}
	return *p
}

// rowFor returns the ListSessions row for id, or fails.
func rowFor(t *testing.T, rows []server.SessionSummary, id session.SessionID) server.SessionSummary {
	t.Helper()
	for _, r := range rows {
		if r.SessionID == string(id) {
			return r
		}
	}
	t.Fatalf("no ListSessions row for %q (rows: %+v)", id, rows)
	return server.SessionSummary{}
}

// TestCallerIdentity_Scenario3_OwnerRecordedAndListed pins AC3.1: a session
// created under a verified principal records that principal as its owner — on
// the returned aggregate, on the persisted snapshot re-Loaded from the store,
// and on the ListSessions row. The row is asserted on BOTH listing paths in ONE
// test (jsonlstore implements port.MetaLister → the cheap fast path that skips
// Load; memstore does not → the Load-per-row slow path), because an
// empty-on-fast-path divergence is the recurring per-path bug class this AC
// names.
func TestCallerIdentity_Scenario3_OwnerRecordedAndListed(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), alice)

	stores := map[string]func(t *testing.T) port.SessionStore{
		"MetaLister fast path (jsonlstore)": func(t *testing.T) port.SessionStore {
			st, err := jsonlstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("jsonlstore: %v", err)
			}
			return st
		},
		"Load-per-row slow path (memstore)": func(*testing.T) port.SessionStore {
			return memstore.New()
		},
	}

	for name, mk := range stores {
		t.Run(name, func(t *testing.T) {
			store := mk(t)
			svc := newServiceWithStore(t, store)

			sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{MaxTurns: 3})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if got := ownerOf(sess.Owner); got != *alice {
				t.Fatalf("created session owner = %+v, want %+v", got, *alice)
			}

			// The persisted snapshot names her: re-Load straight from the store
			// (not the Service cache) so this is the durable record, not memory.
			loaded, err := store.Load(ctx, sess.ID)
			if err != nil {
				t.Fatalf("store.Load: %v", err)
			}
			if got := ownerOf(loaded.Owner); got != *alice {
				t.Errorf("persisted snapshot owner = %+v, want %+v", got, *alice)
			}

			rows, err := svc.ListSessions(ctx)
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if got := ownerOf(rowFor(t, rows, sess.ID).Owner); got != *alice {
				t.Errorf("ListSessions row owner = %+v, want %+v", got, *alice)
			}
		})
	}
}

// TestCallerIdentity_Scenario3_OwnerSurvivesReopenRestart pins AC3.2: the owner
// survives reopen (a second prompt on a completed session), interrupt, recover,
// a process restart (two Services over the SAME store — the two-Build pattern),
// AND a rehydrateSession per-session engine rebuild (a persisted selector
// session whose engine died with the process).
func TestCallerIdentity_Scenario3_OwnerSurvivesReopenRestart(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), alice)
	store := memstore.New()

	sel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}
	var (
		gotSel atomic.Value
		calls  atomic.Int32
	)
	svc1 := selectorServiceOverStore(t, store, selectorRecordingFactory("PRE-RESTART", &gotSel, &calls), nil)

	sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, sel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}

	// Turn 1 completes the session; turn 2 goes through loadAndReopen (the
	// completed → Reopen run-entry seam).
	for _, prompt := range []string{"first turn", "second turn (reopen)"} {
		run, rerr := svc1.StartRun(ctx, sess.ID, prompt)
		if rerr != nil {
			t.Fatalf("StartRun %q: %v", prompt, rerr)
		}
		drainServerRun(run)
		svc1.FinishRun(sess.ID, run)
	}
	after, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("store.Load after reopen: %v", err)
	}
	if got := ownerOf(after.Owner); got != *alice {
		t.Errorf("owner after reopen = %+v, want %+v", got, *alice)
	}

	// Interrupt and Recover are the other two run-entry recovery seams; both
	// preserve the owner (they are aggregate transitions, not re-labellings).
	for _, tc := range []struct {
		name string
		to   func(*session.Session) error
	}{
		{"interrupt", func(s *session.Session) error {
			if err := s.BeginTurn(); err != nil {
				return err
			}
			if err := s.Cancel(); err != nil {
				return err
			}
			return s.Interrupt()
		}},
		{"recover", func(s *session.Session) error {
			if err := s.BeginTurn(); err != nil {
				return err
			}
			if err := s.Fail(); err != nil {
				return err
			}
			return s.Recover()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, lerr := store.Load(ctx, sess.ID)
			if lerr != nil {
				t.Fatalf("store.Load: %v", lerr)
			}
			if terr := tc.to(s); terr != nil {
				t.Fatalf("%s transition: %v", tc.name, terr)
			}
			if got := ownerOf(s.Owner); got != *alice {
				t.Errorf("owner after %s = %+v, want %+v", tc.name, got, *alice)
			}
		})
	}

	// Process restart: a NEW Service over the SAME store, empty registries. The
	// run-entry seam rehydrates the per-session engine from the persisted
	// selector; the owner comes back off the snapshot, unchanged.
	var (
		gotSel2 atomic.Value
		calls2  atomic.Int32
	)
	svc2 := selectorServiceOverStore(t, store, selectorRecordingFactory("REHYDRATED", &gotSel2, &calls2), nil)
	run2, err := svc2.StartRunContent(ctx, sess.ID, "post-restart turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	drainServerRun(run2)
	if calls2.Load() == 0 {
		t.Fatal("rehydrateSession never rebuilt the per-session engine; the AC's rehydrate arm is vacuous")
	}
	restored, err := svc2.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after restart: %v", err)
	}
	if got := ownerOf(restored.Owner); got != *alice {
		t.Errorf("owner after restart+rehydrate = %+v, want %+v", got, *alice)
	}
}

// TestCallerIdentity_Scenario3_ForkInheritsSourceOwner pins AC3.3 at the
// service layer: a session forked from a source (WithSourceSession, the
// model-switch carryover fork) carries the SOURCE's owner, not the forking
// caller's — otherwise fork is an ownership-laundering path (Bob forks Alice's
// session and the copy becomes his). The engine-side child half (a subagent
// child inheriting the parent session's owner) is pinned by the same-named test
// in engine/agent.
func TestCallerIdentity_Scenario3_ForkInheritsSourceOwner(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)

	src, err := svc.CreateSession(session.WithPrincipal(context.Background(), alice), session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession (source): %v", err)
	}

	// Bob forks Alice's session. The fork must NOT be attributed to Bob.
	forked, err := svc.CreateSessionWithProfile(
		session.WithPrincipal(context.Background(), bob), session.ModeDefault, session.Limits{MaxTurns: 3},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSourceSession(src.ID),
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSourceSession: %v", err)
	}
	if got := ownerOf(forked.Owner); got != *alice {
		t.Fatalf("forked session owner = %+v, want the SOURCE's owner %+v (fork must not launder ownership to its caller)", got, *alice)
	}
	loaded, err := store.Load(context.Background(), forked.ID)
	if err != nil {
		t.Fatalf("store.Load(fork): %v", err)
	}
	if got := ownerOf(loaded.Owner); got != *alice {
		t.Errorf("persisted fork owner = %+v, want %+v", got, *alice)
	}

	// The explicit ForkSession seam is the other fork path — same rule: Bob
	// forking Alice's session produces one owned by ALICE.
	forkID, err := svc.ForkSession(session.WithPrincipal(context.Background(), bob), src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkSess, err := store.Load(context.Background(), forkID)
	if err != nil {
		t.Fatalf("store.Load(ForkSession): %v", err)
	}
	if got := ownerOf(forkSess.Owner); got != *alice {
		t.Errorf("ForkSession owner = %+v, want the SOURCE's owner %+v", got, *alice)
	}

	// An OWNERLESS source yields an ownerless fork — never the caller's, never a
	// fabricated one (the no-auth path must stay byte-identical).
	plain, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession (ownerless source): %v", err)
	}
	forkedPlain, err := svc.CreateSessionWithProfile(
		session.WithPrincipal(context.Background(), bob), session.ModeDefault, session.Limits{MaxTurns: 3},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSourceSession(plain.ID),
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSourceSession (ownerless): %v", err)
	}
	if forkedPlain.Owner != nil {
		t.Errorf("fork of an OWNERLESS source got owner %+v, want nil (never fabricated, never the caller's)", *forkedPlain.Owner)
	}
}

// TestCallerIdentity_Scenario3_WithOwnerBeatsContextPrincipal pins the
// scheduler's owner-injection seam (task 06's consumer): an explicit WithOwner
// option overrides the context principal, and no option keeps the context
// principal (the byte-identical default).
func TestCallerIdentity_Scenario3_WithOwnerBeatsContextPrincipal(t *testing.T) {
	svc := newServiceWithStore(t, memstore.New())
	ctx := session.WithPrincipal(context.Background(), bob)

	sess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithOwner(alice))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithOwner: %v", err)
	}
	if got := ownerOf(sess.Owner); got != *alice {
		t.Errorf("owner = %+v, want the explicit WithOwner principal %+v", got, *alice)
	}

	// WithOwner(nil) is the explicit ownerless injection: it must not fall back
	// to the context principal (a system caller with no owner to capture).
	none, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithOwner(nil))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithOwner(nil): %v", err)
	}
	if none.Owner != nil {
		t.Errorf("WithOwner(nil) owner = %+v, want nil", *none.Owner)
	}
}

// TestCallerIdentity_Scenario3_PreShipSessionNeverBackfilled pins AC3.4: a
// session persisted BEFORE this plan (no owner in its snapshot) stays ownerless
// no matter who touches it — it is never adopted by the first caller to load,
// run, or list it. The pre-ship snapshot is simulated the only honest way: a
// session persisted with no principal in the context at all.
func TestCallerIdentity_Scenario3_PreShipSessionNeverBackfilled(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithEngineOverStore(t, store, mockllm.New(mockllm.TextTurn("ok"), mockllm.TextTurn("ok")))

	// "Pre-ship": created with NO principal in the context.
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Owner != nil {
		t.Fatalf("pre-ship session created with owner %+v, want nil", *sess.Owner)
	}

	// Bob now touches it every way the service allows: a run through the
	// run-entry funnel, a GetSession, and a listing.
	bobCtx := session.WithPrincipal(context.Background(), bob)
	run, err := svc.StartRunContent(bobCtx, sess.ID, "bob's prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	drainServerRun(run)
	if _, err := svc.GetSession(bobCtx, sess.ID); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	rows, err := svc.ListSessions(bobCtx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if got := rowFor(t, rows, sess.ID); got.Owner != nil {
		t.Errorf("ListSessions row owner = %+v, want nil (an ownerless session renders as unowned, never adopted)", *got.Owner)
	}
	after, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if after.Owner != nil {
		t.Errorf("persisted owner after Bob ran the session = %+v, want nil (never backfilled)", *after.Owner)
	}
}

// TestCallerIdentity_Scenario3_ListRowOwnerIsDisplayOnly pins AC3.5: the owner
// on the list row is DISPLAY only. A request bearing Bob's principal still
// lists Alice's session — this plan accepts and threads identity, it does not
// enforce isolation (that is the isolation track's, #368). Asserted on BOTH
// listing paths so a filter cannot be smuggled into one of them.
func TestCallerIdentity_Scenario3_ListRowOwnerIsDisplayOnly(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T) port.SessionStore{
		"MetaLister fast path (jsonlstore)": func(t *testing.T) port.SessionStore {
			st, err := jsonlstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("jsonlstore: %v", err)
			}
			return st
		},
		"Load-per-row slow path (memstore)": func(*testing.T) port.SessionStore {
			return memstore.New()
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := mk(t)
			svc := newServiceWithStore(t, store)

			aliceSess, err := svc.CreateSession(session.WithPrincipal(context.Background(), alice), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession (alice): %v", err)
			}
			bobSess, err := svc.CreateSession(session.WithPrincipal(context.Background(), bob), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession (bob): %v", err)
			}
			unowned, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession (unowned): %v", err)
			}

			// Bob lists. He sees ALL three rows, each naming its own owner.
			rows, err := svc.ListSessions(session.WithPrincipal(context.Background(), bob))
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if len(rows) != 3 {
				t.Fatalf("Bob's ListSessions returned %d rows, want 3 (display only — no filtering)", len(rows))
			}
			if got := ownerOf(rowFor(t, rows, aliceSess.ID).Owner); got != *alice {
				t.Errorf("Alice's row owner = %+v, want %+v", got, *alice)
			}
			if got := ownerOf(rowFor(t, rows, bobSess.ID).Owner); got != *bob {
				t.Errorf("Bob's row owner = %+v, want %+v", got, *bob)
			}
			if r := rowFor(t, rows, unowned.ID); r.Owner != nil {
				t.Errorf("unowned row owner = %+v, want nil", *r.Owner)
			}
		})
	}
}

// liveSessionStore is a SessionStore whose Load hands back the SAME
// *session.Session on every call (a legal implementation — the port requires a
// reconstruction, not a fresh copy per Load; a caching or in-process store looks
// exactly like this). It is the fixture that makes an aliasing bug in a
// Load-per-row consumer OBSERVABLE: a consumer that hands a caller a live
// pointer into the loaded session lets that caller corrupt the store.
type liveSessionStore struct {
	sessions map[session.SessionID]*session.Session
	saved    []port.StoredSession
	now      time.Time
}

func (s *liveSessionStore) Save(_ context.Context, sess *session.Session) error {
	if s.sessions == nil {
		s.sessions = map[session.SessionID]*session.Session{}
	}
	if _, ok := s.sessions[sess.ID]; !ok {
		s.saved = append(s.saved, port.StoredSession{ID: sess.ID, ModifiedAt: s.now})
	}
	s.sessions[sess.ID] = sess
	return nil
}

func (s *liveSessionStore) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	sess, ok := s.sessions[id]
	if !ok {
		return nil, port.ErrSessionNotFound
	}
	return sess, nil
}

func (s *liveSessionStore) List(_ context.Context) ([]port.StoredSession, error) {
	return s.saved, nil
}

// Delete completes port.PrunableStore, which is what ListSessions type-asserts
// for on the Load-per-row path.
func (s *liveSessionStore) Delete(_ context.Context, id session.SessionID) error {
	delete(s.sessions, id)
	return nil
}

// TestListSessionsRowOwnerIsNotAliased pins the copy half of AC3.5: a
// ListSessions row's owner is a COPY, not a live pointer into the loaded
// session. Handing out the live pointer lets any consumer of the row (a mapper,
// a UI, a later plan's isolation check) rewrite the session's recorded owner —
// the aliasing class the shared Principal.Clone exists to close.
func TestListSessionsRowOwnerIsNotAliased(t *testing.T) {
	store := &liveSessionStore{now: time.Unix(0, 0)}
	svc := newServiceWithStore(t, store)

	sess, err := svc.CreateSession(session.WithPrincipal(context.Background(), alice), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rows, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	row := rowFor(t, rows, sess.ID)
	if row.Owner == nil {
		t.Fatal("row owner is nil; the fixture did not exercise the Load-per-row path")
	}
	row.Owner.Subject = "attacker"

	stored, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if got := ownerOf(stored.Owner); got != *alice {
		t.Fatalf("mutating the list row rewrote the session's owner: got %+v, want %+v", got, *alice)
	}
	rows2, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("second ListSessions: %v", err)
	}
	if got := ownerOf(rowFor(t, rows2, sess.ID).Owner); got != *alice {
		t.Fatalf("second listing owner = %+v, want %+v", got, *alice)
	}
}

type stableOwnerMetaStore struct {
	owner *session.Principal
}

func (*stableOwnerMetaStore) Save(context.Context, *session.Session) error { return nil }
func (*stableOwnerMetaStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, port.ErrSessionNotFound
}
func (s *stableOwnerMetaStore) MetaList(context.Context) ([]port.SessionMeta, error) {
	return []port.SessionMeta{{ID: "meta-session", Owner: s.owner}}, nil
}

func TestMetaListerRowOwnerIsNotAliased(t *testing.T) {
	stable := &session.Principal{Issuer: alice.Issuer, Subject: alice.Subject, GrantType: alice.GrantType, Name: alice.Name}
	store := &stableOwnerMetaStore{owner: stable}
	svc := newServiceWithStore(t, store)

	rows, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	row := rowFor(t, rows, "meta-session")
	if row.Owner == nil {
		t.Fatal("row owner is nil; the fixture did not exercise the MetaLister path")
	}
	if row.Owner == stable {
		t.Fatal("row owner aliases the MetaLister's stable owner pointer")
	}
	row.Owner.Subject = "attacker"
	if got := ownerOf(stable); got != *alice {
		t.Fatalf("mutating the list row rewrote the MetaLister owner: got %+v, want %+v", got, *alice)
	}
}

// newServiceWithEngineOverStore is newServiceWithStore with a scripted LLM, so a
// test can drive a real run through the run-entry funnel over a caller-supplied
// store.
func newServiceWithEngineOverStore(t *testing.T, store port.SessionStore, llm *mockllm.Provider) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     eng,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}
