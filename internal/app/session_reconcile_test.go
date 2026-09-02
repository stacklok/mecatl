package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// crashOrphanedSessionFixture builds a StateRunning session whose trailing
// assistant message carries a dangling tool_use call that never got a
// result — the exact shape a crash leaves behind (mirrors
// internal/adapter/server's own crashOrphanedSession fixture, issue #475).
func crashOrphanedSessionFixture(t *testing.T, id session.SessionID, createdAt time.Time) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/tmp/ws", Revision: "in-tree-v1"}, session.Limits{}, createdAt)
	if err := sess.RecordUserPrompt("do the thing", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{session.NewToolCall("call-a", "read_file", nil)}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if sess.State != session.StateRunning {
		t.Fatalf("precondition: state = %q, want running", sess.State)
	}
	return sess
}

// awaitingSessionFixture builds a StateAwaiting session (paused on a
// permission ask) — never a sweep candidate regardless of age.
func awaitingSessionFixture(t *testing.T, id session.SessionID, createdAt time.Time) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/tmp/ws", Revision: "in-tree-v1"}, session.Limits{}, createdAt)
	if err := sess.RecordUserPrompt("do the thing", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask-1", Tool: "bash", Call: "call-a"}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if sess.State != session.StateAwaiting {
		t.Fatalf("precondition: state = %q, want awaiting", sess.State)
	}
	return sess
}

// listCountingStore wraps a *memstore.Store, counting calls to List — the
// ONE method sweepStaleSessions' svc.LeaseSweepDisabled() guard is meant to
// prevent from ever running when the guard trips. Mutation-testing the guard
// (deleting it) showed the pre-existing test couldn't tell "the whole pass
// was skipped" from "SessionStale declined every candidate anyway", because
// nothing observed whether ListSessions (and hence this List) was ever
// called. Embeds *memstore.Store so Save/Load/Delete forward unchanged
// (mirrors the countingStore pattern in
// internal/adapter/server/staterunning_repair_test.go, adapted from counting
// Save to counting List).
type listCountingStore struct {
	*memstore.Store
	mu    sync.Mutex
	lists int
}

func (c *listCountingStore) List(ctx context.Context) ([]port.StoredSession, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Store.List(ctx)
}

func (c *listCountingStore) listCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists
}

var _ port.PrunableStore = (*listCountingStore)(nil)

// reconcileFixture builds a deterministic sweep harness: a memstore whose
// Save/ModifiedAt times AND the Service's own staleness clock come from one
// shared fake clock — mirroring childgc_test.go's gcFixture pattern. The
// Service is wired to a listCountingStore over that same memstore so any test
// can pin whether a sweep pass actually reached ListSessions.
type reconcileFixture struct {
	store *memstore.Store
	lists *listCountingStore
	svc   *server.Service
	now   time.Time
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	return newReconcileFixtureWithLease(t, nil)
}

// newReconcileFixtureWithLease is newReconcileFixture with an OPTIONAL
// port.SessionLease wired into the Service, so a test can drive
// Service.SessionStale into the ErrLeaseUnsupported sticky-disable path (nil
// mirrors the default no-lease fixture byte-for-byte).
func newReconcileFixtureWithLease(t *testing.T, lease port.SessionLease) *reconcileFixture {
	t.Helper()
	f := &reconcileFixture{now: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	f.store = memstore.New(memstore.WithNow(func() time.Time { return f.now }))
	f.lists = &listCountingStore{Store: f.store}
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
	})
	svc, err := newTestServerService(server.Config{
		Engine:       engine,
		Store:        f.lists,
		Workspaces:   func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:          func() time.Time { return f.now },
		SessionLease: lease,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	f.svc = svc
	return f
}

// stubUnsupportedLease is a minimal port.SessionLease whose Acquire always
// returns port.ErrLeaseUnsupported — the sticky-disable trigger — mirroring
// internal/adapter/server's own fakeLease (a different package, so this test
// needs its own tiny stub rather than importing the unexported test type).
type stubUnsupportedLease struct{ acquires int }

func (s *stubUnsupportedLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	s.acquires++
	return port.Lease{}, port.ErrLeaseUnsupported
}

func (*stubUnsupportedLease) Renew(_ context.Context, l port.Lease) (port.Lease, error) {
	return l, nil
}

func (*stubUnsupportedLease) Release(context.Context, port.Lease) error { return nil }

var _ port.SessionLease = (*stubUnsupportedLease)(nil)

func (f *reconcileFixture) save(t *testing.T, sess *session.Session) {
	t.Helper()
	if err := f.store.Save(context.Background(), sess); err != nil {
		t.Fatalf("save(%q): %v", sess.ID, err)
	}
}

func (f *reconcileFixture) state(t *testing.T, id session.SessionID) session.State {
	t.Helper()
	reloaded, err := f.store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("reload(%q): %v", id, err)
	}
	return reloaded.State
}

// TestStaleSessionReconcileSettlesChildCandidate pins the population the
// confirmed real bug came from (issue #475): a subagent-* child crash-orphaned
// in StateRunning is a sweep candidate exactly like a top-level session — the
// run-entry funnel's own repair (Step 3) never reaches it (nothing ever calls
// StartRunContent on a child id), so this sweep is its ONLY repair path.
func TestStaleSessionReconcileSettlesChildCandidate(t *testing.T) {
	f := newReconcileFixture(t)
	const id session.SessionID = "subagent-orphan"
	f.save(t, crashOrphanedSessionFixture(t, id, f.now))
	f.now = f.now.Add(2 * time.Hour) // past the staleness age window

	sweepStaleSessions(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile), f.svc, port.NopDiagnostics{})

	if got := f.state(t, id); got != session.StateIdle {
		t.Fatalf("state after sweep = %q, want idle", got)
	}
	reloaded, err := f.store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := session.ValidateToolPairing(reloaded.Conversation.Messages); err != nil {
		t.Fatalf("tool pairing invalid after settle: %v", err)
	}
}

// TestStaleSessionReconcileExcludesScheduleFireSessions pins the exclusion:
// a "sched--"-prefixed fire session, however stale-looking, is left untouched
// by this sweep — it is the scheduler's OWN reconciler's job (see the note in
// scheduler_reconcile.go).
func TestStaleSessionReconcileExcludesScheduleFireSessions(t *testing.T) {
	f := newReconcileFixture(t)
	const id session.SessionID = "sched--nightly-000123"
	f.save(t, crashOrphanedSessionFixture(t, id, f.now))
	f.now = f.now.Add(2 * time.Hour)

	sweepStaleSessions(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile), f.svc, port.NopDiagnostics{})

	if got := f.state(t, id); got != session.StateRunning {
		t.Fatalf("state after sweep = %q, want running (sched-- must be excluded)", got)
	}
}

// TestStaleSessionReconcileNeverTouchesAwaiting pins that StateAwaiting is
// never a candidate: only state=="running" rows are even considered.
func TestStaleSessionReconcileNeverTouchesAwaiting(t *testing.T) {
	f := newReconcileFixture(t)
	const id session.SessionID = "operator-awaiting"
	f.save(t, awaitingSessionFixture(t, id, f.now))
	f.now = f.now.Add(2 * time.Hour)

	sweepStaleSessions(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile), f.svc, port.NopDiagnostics{})

	if got := f.state(t, id); got != session.StateAwaiting {
		t.Fatalf("state after sweep = %q, want awaiting (never a candidate)", got)
	}
}

// TestStaleSessionReconcileLeavesFreshRunningAlone proves the sweep actually
// calls Service.SessionStale (age-horizon-first) rather than reimplementing a
// weaker check of its own: a running session inside the staleness window is
// left untouched.
func TestStaleSessionReconcileLeavesFreshRunningAlone(t *testing.T) {
	f := newReconcileFixture(t)
	const id session.SessionID = "operator-fresh"
	f.save(t, crashOrphanedSessionFixture(t, id, f.now))
	// No clock advance: still well inside the age window.

	sweepStaleSessions(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile), f.svc, port.NopDiagnostics{})

	if got := f.state(t, id); got != session.StateRunning {
		t.Fatalf("state after sweep = %q, want running (fresh, inside the age window)", got)
	}
}

// TestSweepStaleSessionsSkipsWhenLeaseSweepDisabled pins Finding 2 of the
// Step-4 follow-up (issue #475): once SessionStale has stickily disabled the
// sweep for a lease backend that answers ErrLeaseUnsupported,
// sweepStaleSessions must skip its ENTIRE pass — not merely let each
// per-candidate SessionStale call fail safe internally. It seeds a candidate
// that is DEFINITELY stale (past the age window, StateRunning) and proves the
// guard actually skips work: the candidate is left completely untouched,
// which only holds if the sweep never reaches ListSessions/SettleIfStale for
// it.
func TestSweepStaleSessionsSkipsWhenLeaseSweepDisabled(t *testing.T) {
	lease := &stubUnsupportedLease{}
	f := newReconcileFixtureWithLease(t, lease)

	// Trigger the sticky-disable: one SessionStale call against a past-window,
	// not-live candidate reaches the lease branch and gets ErrLeaseUnsupported.
	triggerID := session.SessionID("trigger")
	f.save(t, crashOrphanedSessionFixture(t, triggerID, f.now))
	f.now = f.now.Add(2 * time.Hour)
	stale := f.svc.SessionStale(context.Background(), port.SessionMeta{
		ID:         triggerID,
		ModifiedAt: f.now.Add(-2 * time.Hour),
		State:      session.StateRunning,
	})
	if stale {
		t.Fatal("SessionStale on ErrLeaseUnsupported = true, want false")
	}
	if !f.svc.LeaseSweepDisabled() {
		t.Fatal("LeaseSweepDisabled() = false after ErrLeaseUnsupported, want true")
	}

	// Seed a SECOND, definitely-stale candidate and run a sweep pass.
	const id session.SessionID = "definitely-stale"
	f.save(t, crashOrphanedSessionFixture(t, id, f.now))
	f.now = f.now.Add(2 * time.Hour)

	sweepStaleSessions(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile), f.svc, port.NopDiagnostics{})

	// This is the assertion that actually pins the sweep-level guard: with it
	// removed, SessionStale still declines every candidate on its OWN
	// leaseSweepDisabled check, so the candidate would stay untouched either
	// way — the state assertion below can't tell the two apart. The guard's
	// only observable effect is that ListSessions is never called at all.
	if got := f.lists.listCount(); got != 0 {
		t.Fatalf("ListSessions called %d times, want 0 (LeaseSweepDisabled must skip the whole pass before listing)", got)
	}
	if got := f.state(t, id); got != session.StateRunning {
		t.Fatalf("state after sweep = %q, want running (LeaseSweepDisabled must skip the whole pass)", got)
	}
}

// TestStartStaleSessionReconcileExitsOnCancel pins the goroutine-exit
// contract: the sweeper goroutine started by startStaleSessionReconcile
// returns when its returned close func is called. The package's goleak
// TestMain is the actual leak gate; this test additionally proves the
// specific goroutine reacts to cancellation promptly rather than relying on
// process exit to hide a leak.
func TestStartStaleSessionReconcileExitsOnCancel(t *testing.T) {
	f := newReconcileFixture(t)
	stop := startStaleSessionReconcile(Config{Diagnostics: port.NopDiagnostics{}}, f.svc)
	stop()
	// If the goroutine leaked, goleak (wired at TestMain for this package)
	// fails the whole test binary; nothing further to assert here beyond
	// giving the goroutine a moment to observe ctx.Done() before the test
	// (and svc.Close in Cleanup) returns.
	time.Sleep(20 * time.Millisecond)
}
