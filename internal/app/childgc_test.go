package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// gcFixture builds a deterministic sweep harness: a memstore whose Save times
// come from an injected fake clock, plus a childGC reading the same clock.
type gcFixture struct {
	store *memstore.Store
	gc    *childGC
	// now is the fake clock's CURRENT reading; advance it between saves to
	// give snapshots distinct ages.
	now time.Time
}

func newGCFixture(t *testing.T, policy childGCPolicy) *gcFixture {
	t.Helper()
	f := &gcFixture{now: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	f.store = memstore.New(memstore.WithNow(func() time.Time { return f.now }))
	f.gc = &childGC{
		store:  f.store,
		policy: policy,
		isLive: func(session.SessionID) bool { return false },
		now:    func() time.Time { return f.now },
		diag:   port.NopDiagnostics{},
	}
	return f
}

// save stores an empty session under id at the fixture clock's current time.
func (f *gcFixture) save(t *testing.T, id session.SessionID) {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, f.now)
	if err := f.store.Save(context.Background(), s); err != nil {
		t.Fatalf("Save(%q): %v", id, err)
	}
}

// ids returns the set of ids currently in the store.
func (f *gcFixture) ids(t *testing.T) map[session.SessionID]bool {
	t.Helper()
	entries, err := f.store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make(map[session.SessionID]bool, len(entries))
	for _, e := range entries {
		out[e.ID] = true
	}
	return out
}

// TestChildGCAgePass pins the age pass: child snapshots older than retention
// are deleted, younger ones retained, across all three families.
func TestChildGCAgePass(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{retention: 24 * time.Hour})
	f.save(t, "subagent-old")
	f.save(t, "parallel-old-0")
	f.save(t, "team-old-lead")
	f.now = f.now.Add(48 * time.Hour) // the old ones are now 48h old
	f.save(t, "subagent-young")

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 3 || retained != 1 {
		t.Errorf("sweep = (deleted %d, retained %d), want (3, 1)", deleted, retained)
	}
	got := f.ids(t)
	for _, old := range []session.SessionID{"subagent-old", "parallel-old-0", "team-old-lead"} {
		if got[old] {
			t.Errorf("aged-out child %q survived the age pass", old)
		}
	}
	if !got["subagent-young"] {
		t.Error("young child was deleted by the age pass")
	}
}

// TestChildGCMainSessionsNeverDeleted is the load-bearing SAFETY test: an
// UNPREFIXED (operator/service) session is never deleted — not by the age
// pass even when ancient, and not by the cap pass however many there are.
// (Mutation-verified: removing the prefix gate in sweep makes this fail.)
func TestChildGCMainSessionsNeverDeleted(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{retention: time.Hour, maxPerFamily: 1})
	// Ancient main sessions, including ids that merely CONTAIN family words.
	mains := []session.SessionID{
		"abc123def", "my-subagent-notes", "session-team-x", "S-parallel-1",
	}
	for _, id := range mains {
		f.save(t, id)
	}
	f.now = f.now.Add(1000 * time.Hour) // all of them far past retention

	deleted, _ := f.gc.sweep(context.Background())
	if deleted != 0 {
		t.Errorf("sweep deleted %d sessions, want 0 (only child-prefixed ids are prunable)", deleted)
	}
	got := f.ids(t)
	for _, id := range mains {
		if !got[id] {
			t.Errorf("UNPREFIXED main session %q was deleted — the safety invariant is broken", id)
		}
	}
}

// TestChildGCMainSessionAgePass pins the MAIN age pass (issue #79): with
// mainRetention set, UNPREFIXED (operator/service) sessions older than the
// threshold are deleted while younger ones survive, and CHILD snapshots are
// left to the child passes (none configured here).
func TestChildGCMainSessionAgePass(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{mainRetention: 24 * time.Hour})
	f.save(t, "main-old-a")
	f.save(t, "main-old-b")
	f.save(t, "subagent-old") // a child: no child pass configured -> retained
	f.now = f.now.Add(48 * time.Hour)
	f.save(t, "main-young")

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (2, 2)", deleted, retained)
	}
	got := f.ids(t)
	for _, old := range []session.SessionID{"main-old-a", "main-old-b"} {
		if got[old] {
			t.Errorf("aged-out main %q survived the main age pass", old)
		}
	}
	if !got["main-young"] {
		t.Error("young main session was deleted by the age pass")
	}
	if !got["subagent-old"] {
		t.Error("a child snapshot was deleted by the MAIN pass (no child pass was configured)")
	}
}

// TestChildGCMainSessionCountCap pins the GLOBAL main count cap (issue #79):
// past mainMaxTotal, the OLDEST main snapshots go first, the cap is store-wide
// (not per-prefix), and a live main keeps its slot (the next-oldest non-live is
// deleted in its stead).
func TestChildGCMainSessionCountCap(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{mainMaxTotal: 2})
	live := map[session.SessionID]bool{"main-a": true}
	f.gc.isLive = func(id session.SessionID) bool { return live[id] }
	for i, id := range []session.SessionID{"main-a", "main-b", "main-c", "main-d"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}

	// 4 mains > cap 2, oldest-first with the live main-a skipped => main-b and
	// main-c deleted (main-a kept though oldest; main-d newest).
	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (2, 2)", deleted, retained)
	}
	got := f.ids(t)
	if !got["main-a"] {
		t.Error("LIVE main was deleted — the liveness exclusion is broken for the main cap")
	}
	if got["main-b"] || got["main-c"] {
		t.Errorf("cap pass kept the oldest non-live mains (b=%v c=%v), want them deleted", got["main-b"], got["main-c"])
	}
	if !got["main-d"] {
		t.Error("newest main was deleted under the cap pass")
	}
}

// TestChildGCMainPassDisabledByDefault pins that with the main knobs at 0,
// UNPREFIXED sessions are NEVER touched even when ancient — the historical
// "main sessions are never deleted" guarantee holds for the zero-config default
// (the mecated posture). It is the safety twin of TestChildGCMainSessionsNeverDeleted
// but with the CHILD passes also off, so ONLY the main knobs could touch them.
func TestChildGCMainPassDisabledByDefault(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{retention: time.Hour, maxPerFamily: 1}) // child passes only
	mains := []session.SessionID{"main-a", "main-b", "main-c"}
	for _, id := range mains {
		f.save(t, id)
	}
	f.now = f.now.Add(1000 * time.Hour) // ancient

	deleted, _ := f.gc.sweep(context.Background())
	if deleted != 0 {
		t.Errorf("sweep deleted %d sessions, want 0 (main passes disabled -> mains untouched)", deleted)
	}
	got := f.ids(t)
	for _, id := range mains {
		if !got[id] {
			t.Errorf("main session %q was deleted with the main passes OFF — the zero-config safety invariant is broken", id)
		}
	}
}

// TestChildGCMainAgePassSkipsLive pins the liveness exclusion on the MAIN AGE
// pass specifically (issue #79): a LIVE main older than mainRetention keeps its
// slot — the in-flight run protects it from the age pass, mirroring the
// child-side TestChildGCSkipsLiveChildren age-pass leg. (Mutation-verified:
// removing the `!g.isLive(e.ID)` guard from sweepMain's age loop makes this
// fail.)
func TestChildGCMainAgePassSkipsLive(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{mainRetention: 24 * time.Hour})
	live := map[session.SessionID]bool{"main-live-old": true}
	f.gc.isLive = func(id session.SessionID) bool { return live[id] }

	f.save(t, "main-live-old") // ancient but live: must survive the age pass
	f.save(t, "main-dead-old") // ancient and dead: age pass takes it
	f.now = f.now.Add(48 * time.Hour)

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 1 || retained != 1 {
		t.Errorf("sweep = (deleted %d, retained %d), want (1, 1)", deleted, retained)
	}
	got := f.ids(t)
	if !got["main-live-old"] {
		t.Error("LIVE main was deleted by the age pass — the liveness exclusion is broken for the main age pass")
	}
	if got["main-dead-old"] {
		t.Error("dead aged-out main survived the age pass")
	}
}

// TestChildGCMainAgeThenCap pins the two-pass interaction for mains (issue #79):
// the age pass deletes the ancient mains, then the GLOBAL cap trims the fresh
// SURVIVORS down to mainMaxTotal. Exercises the `survivors` plumbing that the
// single-knob tests never hit together (an off-by-one in the carry-over from
// the age pass to the cap would surface here).
func TestChildGCMainAgeThenCap(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{mainRetention: 24 * time.Hour, mainMaxTotal: 2})
	// Two ancient mains the age pass must reap.
	f.save(t, "main-old-a")
	f.save(t, "main-old-b")
	// Jump past the retention horizon, then save four FRESH mains with distinct
	// ages so the cap evicts deterministically oldest-first.
	f.now = f.now.Add(48 * time.Hour)
	for i, id := range []session.SessionID{"main-new-a", "main-new-b", "main-new-c", "main-new-d"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}

	// Age pass deletes old-a/old-b (2). Survivors = the 4 fresh mains > cap 2,
	// oldest-first => new-a and new-b deleted (2). Total deleted 4, retained 2.
	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 4 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (4, 2)", deleted, retained)
	}
	got := f.ids(t)
	for _, gone := range []session.SessionID{"main-old-a", "main-old-b", "main-new-a", "main-new-b"} {
		if got[gone] {
			t.Errorf("%q survived, want deleted (age pass took the old, cap trimmed the oldest survivors)", gone)
		}
	}
	for _, kept := range []session.SessionID{"main-new-c", "main-new-d"} {
		if !got[kept] {
			t.Errorf("%q was deleted, want retained (the two newest survivors fit under the cap)", kept)
		}
	}
}

// TestChildGCCapPassOldestFirst pins the per-family count cap: past the cap,
// the OLDEST snapshots go first, and the cap is per FAMILY (a full subagent
// family does not evict team members).
func TestChildGCCapPassOldestFirst(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{maxPerFamily: 2})
	for i, id := range []session.SessionID{"subagent-a", "subagent-b", "subagent-c", "subagent-d"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}
	f.save(t, "team-t-lead") // a second family, under its own cap

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 3 {
		t.Errorf("sweep = (deleted %d, retained %d), want (2, 3)", deleted, retained)
	}
	got := f.ids(t)
	for _, id := range []session.SessionID{"subagent-a", "subagent-b"} {
		if got[id] {
			t.Errorf("oldest-past-cap %q survived the cap pass", id)
		}
	}
	for _, id := range []session.SessionID{"subagent-c", "subagent-d", "team-t-lead"} {
		if !got[id] {
			t.Errorf("%q was deleted, want retained (newest-2 per family + the other family)", id)
		}
	}
}

// TestChildGCSkipsLiveChildren pins the liveness exclusion: an id with an
// in-flight run keeps its slot under the cap (the next-oldest non-live id is
// deleted instead) and is never deleted by the age pass.
func TestChildGCSkipsLiveChildren(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{retention: 24 * time.Hour, maxPerFamily: 2})
	live := map[session.SessionID]bool{"subagent-live-old": true}
	f.gc.isLive = func(id session.SessionID) bool { return live[id] }

	f.save(t, "subagent-live-old") // ancient but live: must survive BOTH passes
	f.save(t, "subagent-dead-old") // ancient and dead: age pass takes it
	f.now = f.now.Add(48 * time.Hour)
	for i, id := range []session.SessionID{"subagent-y1", "subagent-y2", "subagent-y3"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}

	// Age pass: dead-old deleted, live-old skipped. Survivors: live-old + y1..y3
	// = 4 > cap 2, oldest-first with live-old skipped => y1 and y2 deleted.
	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 3 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (3, 2)", deleted, retained)
	}
	got := f.ids(t)
	if !got["subagent-live-old"] {
		t.Error("LIVE child was deleted — the liveness exclusion is broken")
	}
	if got["subagent-dead-old"] {
		t.Error("dead aged-out child survived")
	}
	if got["subagent-y1"] || got["subagent-y2"] {
		t.Errorf("cap pass kept the oldest non-live ids (y1=%v y2=%v), want them deleted in the live id's stead", got["subagent-y1"], got["subagent-y2"])
	}
	if !got["subagent-y3"] {
		t.Error("newest child was deleted under the cap pass")
	}
}

// TestChildGCDisabledPolicyIsNoOp pins that the all-zero policy never lists or
// deletes: startChildGC with a zero policy starts nothing.
func TestChildGCDisabledPolicyIsNoOp(t *testing.T) {
	if (childGCPolicy{}).enabled() {
		t.Fatal("zero policy reports enabled")
	}
	f := newGCFixture(t, childGCPolicy{})
	f.save(t, "subagent-ancient")
	f.now = f.now.Add(10000 * time.Hour)
	// startChildGC must return without starting a goroutine or sweeping.
	startChildGC(context.Background(), Config{}, f.store, f.gc.isLive)
	if !f.ids(t)["subagent-ancient"] {
		t.Error("disabled GC still deleted a child")
	}
}

// TestChildGCNonPrunableStoreIsNoOp pins the degradation posture: a store
// without the PrunableStore seam is never swept — startChildGC no-ops with an
// INFO (never an error, never a panic).
func TestChildGCNonPrunableStoreIsNoOp(t *testing.T) {
	rec := &recordingDiag{}
	cfg := Config{
		ChildRetention:             time.Hour,
		ChildRetentionMaxPerFamily: 10,
		Diagnostics:                rec,
	}
	startChildGC(context.Background(), cfg, plainSessionStore{inner: memstore.New()}, func(session.SessionID) bool { return false })
	if !rec.has("not prunable") {
		t.Errorf("expected the 'not prunable' INFO, got %q", rec.messages())
	}
}

// TestChildGCListErrorIsNonFatal pins the best-effort posture: a TRANSIENT
// List failure (I/O error, timeout) is one WARN and a skipped sweep — never
// fatal, and never disabling (the next sweep retries). The PERMANENT case (a
// store signalling port.ErrPruneUnsupported) is the separate sticky-disable
// posture pinned by TestChildGCPruneUnsupportedDisablesStickily.
func TestChildGCListErrorIsNonFatal(t *testing.T) {
	rec := &recordingDiag{}
	gc := &childGC{
		store:  failingPrunable{},
		policy: childGCPolicy{retention: time.Hour},
		isLive: func(session.SessionID) bool { return false },
		now:    time.Now,
		diag:   rec,
	}
	deleted, retained := gc.sweep(context.Background())
	if deleted != 0 || retained != 0 {
		t.Errorf("failed-List sweep = (%d, %d), want (0, 0)", deleted, retained)
	}
	if !rec.has("list failed") {
		t.Errorf("expected the list-failed WARN, got %q", rec.messages())
	}
}

// TestChildSessionPrefixesMatchEngineConvention is the drift guard for the
// prefix wiring: composition's childSessionPrefixes must be EXACTLY the three
// engine-exported prefix constants (engine/agent/childregistry.go — the same
// constants the minting sites derive from), all three legs real. Plus the one
// exported minting helper (agent.MemberSessionID) and the documented shapes of
// the other two families must classify into their families.
func TestChildSessionPrefixesMatchEngineConvention(t *testing.T) {
	wantPrefixes := []string{
		agent.SubagentSessionPrefix,
		agent.ParallelSessionPrefix,
		agent.TeamSessionPrefix,
	}
	if len(childSessionPrefixes) != len(wantPrefixes) {
		t.Fatalf("childSessionPrefixes = %v, want exactly the engine constants %v", childSessionPrefixes, wantPrefixes)
	}
	for i, want := range wantPrefixes {
		if childSessionPrefixes[i] != want {
			t.Errorf("childSessionPrefixes[%d] = %q, want the engine constant %q", i, childSessionPrefixes[i], want)
		}
	}

	id := agent.MemberSessionID("tid", "lead")
	family, ok := isChildSession(id)
	if !ok || family != agent.TeamSessionPrefix {
		t.Errorf("isChildSession(%q) = (%q, %v), want (%q, true) — the engine's member-id scheme drifted from childSessionPrefixes", id, family, ok, agent.TeamSessionPrefix)
	}
	// The canonical shapes of the other two families (the documented
	// "subagent-<callID>" / "parallel-<callID>-<i>" conventions, minted from
	// the same constants).
	for _, c := range []struct {
		id   session.SessionID
		want string
	}{
		{session.SessionID(agent.SubagentSessionPrefix + "call123"), agent.SubagentSessionPrefix},
		{session.SessionID(agent.ParallelSessionPrefix + "call123-0"), agent.ParallelSessionPrefix},
	} {
		family, ok := isChildSession(c.id)
		if !ok || family != c.want {
			t.Errorf("isChildSession(%q) = (%q, %v), want (%q, true)", c.id, family, ok, c.want)
		}
	}
}

// plainSessionStore strips a store down to bare Save/Load so the type
// assertion in startChildGC sees a NON-prunable store.
type plainSessionStore struct{ inner port.SessionStore }

func (p plainSessionStore) Save(ctx context.Context, s *session.Session) error {
	return p.inner.Save(ctx, s)
}

func (p plainSessionStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return p.inner.Load(ctx, id)
}

// failingPrunable always fails List (the remote-UNIMPLEMENTED stand-in).
type failingPrunable struct{}

func (failingPrunable) Save(context.Context, *session.Session) error { return nil }
func (failingPrunable) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, port.ErrSessionNotFound
}

func (failingPrunable) List(context.Context) ([]port.StoredSession, error) {
	return nil, context.DeadlineExceeded
}
func (failingPrunable) Delete(context.Context, session.SessionID) error { return nil }

// recordingDiag captures diagnostics lines for assertion.
type recordingDiag struct{ msgs []string }

func (r *recordingDiag) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	r.msgs = append(r.msgs, msg)
}
func (r *recordingDiag) With(...any) port.Diagnostics { return r }
func (r *recordingDiag) has(substr string) bool {
	for _, m := range r.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}
func (r *recordingDiag) messages() []string { return r.msgs }

// TestChildGCCapPassTieBreakDeterministic pins the cap pass's eviction order
// under EQUAL ModifiedAt (coarse file mtimes / batch saves are realistic): the
// ID tiebreak makes the same siblings lose every time, regardless of the
// store's (map-iteration) List order. Without the tiebreak which sibling
// survives would be random per sweep.
func TestChildGCCapPassTieBreakDeterministic(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{maxPerFamily: 2})
	// Saved at the SAME fixture instant — all four ModifiedAt values tie.
	// Deliberately not in ID order, so only the tiebreak can order them.
	for _, id := range []session.SessionID{"subagent-c", "subagent-a", "subagent-d", "subagent-b"} {
		f.save(t, id)
	}

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 2 {
		t.Fatalf("sweep = (deleted %d, retained %d), want (2, 2)", deleted, retained)
	}
	got := f.ids(t)
	for _, id := range []session.SessionID{"subagent-a", "subagent-b"} {
		if got[id] {
			t.Errorf("%q survived; on a full ModifiedAt tie the LOWEST ids must be evicted first (deterministic)", id)
		}
	}
	for _, id := range []session.SessionID{"subagent-c", "subagent-d"} {
		if !got[id] {
			t.Errorf("%q was evicted; on a full ModifiedAt tie the HIGHEST ids must survive (deterministic)", id)
		}
	}
}

// TestChildGCAgeBoundaryIsStrict pins the cutoff comparison's strictness: a
// snapshot EXACTLY retention old is RETAINED (ModifiedAt.Before(cutoff) is
// strict), and one a nanosecond past it is deleted.
func TestChildGCAgeBoundaryIsStrict(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{retention: 24 * time.Hour})
	f.save(t, "subagent-edge")

	f.now = f.now.Add(24 * time.Hour) // exactly AT the cutoff
	if deleted, _ := f.gc.sweep(context.Background()); deleted != 0 {
		t.Errorf("exactly-at-cutoff sweep deleted %d, want 0 (the comparison must be strictly Before)", deleted)
	}
	if !f.ids(t)["subagent-edge"] {
		t.Fatal("exactly-at-cutoff snapshot was deleted — the boundary must retain")
	}

	f.now = f.now.Add(time.Nanosecond) // one tick PAST the cutoff
	if deleted, _ := f.gc.sweep(context.Background()); deleted != 1 {
		t.Errorf("just-past-cutoff sweep deleted %d, want 1", deleted)
	}
	if f.ids(t)["subagent-edge"] {
		t.Error("just-past-cutoff snapshot survived")
	}
}

// countingPrunable wraps memstore, counting List calls (and optionally
// signalling each one / failing each one) so tests can assert HOW OFTEN the
// sweeper consults the store.
type countingPrunable struct {
	*memstore.Store
	lists   atomic.Int32
	listed  chan struct{} // when non-nil, receives one (non-blocking) signal per List
	listErr error         // when non-nil, every List fails with it
}

func (c *countingPrunable) List(ctx context.Context) ([]port.StoredSession, error) {
	c.lists.Add(1)
	if c.listed != nil {
		select {
		case c.listed <- struct{}{}:
		default:
		}
	}
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.Store.List(ctx)
}

// pruneUnsupportedErr mimics the grpcdriver client's mapping of a driver
// UNIMPLEMENTED onto the port sentinel.
func pruneUnsupportedErr() error {
	return fmt.Errorf("grpcdriver: list: %w: rpc error: code = Unimplemented", port.ErrPruneUnsupported)
}

// TestChildGCPruneUnsupportedDisablesStickily pins the permanent-posture
// degradation: a store signalling port.ErrPruneUnsupported gets ONE INFO
// ("disabling session GC"), never a WARN, and every subsequent sweep is a no-op
// that does not even List.
func TestChildGCPruneUnsupportedDisablesStickily(t *testing.T) {
	rec := &recordingDiag{}
	cs := &countingPrunable{Store: memstore.New(), listErr: pruneUnsupportedErr()}
	gc := &childGC{
		store:  cs,
		policy: childGCPolicy{retention: time.Hour},
		isLive: func(session.SessionID) bool { return false },
		now:    time.Now,
		diag:   rec,
	}

	gc.sweep(context.Background())
	if got := cs.lists.Load(); got != 1 {
		t.Fatalf("first sweep performed %d Lists, want 1", got)
	}
	if !gc.disabled {
		t.Fatal("ErrPruneUnsupported did not set the sticky disable")
	}
	if !rec.has("disabling session GC") {
		t.Errorf("expected the one disabling INFO, got %q", rec.messages())
	}
	if rec.has("list failed") {
		t.Errorf("the permanent posture must not surface as the transient WARN, got %q", rec.messages())
	}

	// Second sweep: sticky — no List, no further logs.
	gc.sweep(context.Background())
	if got := cs.lists.Load(); got != 1 {
		t.Errorf("post-disable sweep still consulted the store (%d Lists, want 1)", got)
	}
	var infos int
	for _, m := range rec.messages() {
		if strings.Contains(m, "disabling session GC") {
			infos++
		}
	}
	if infos != 1 {
		t.Errorf("the disabling INFO fired %d times, want exactly once", infos)
	}
}

// TestChildGCTickerStopsAfterPruneUnsupported pins the goroutine half of the
// sticky disable: after the startup sweep hits ErrPruneUnsupported, no later
// tick performs a List (the sweeper goroutine exits; goleak at TestMain is the
// leak gate).
func TestChildGCTickerStopsAfterPruneUnsupported(t *testing.T) {
	cs := &countingPrunable{Store: memstore.New(), listed: make(chan struct{}, 16), listErr: pruneUnsupportedErr()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{
		ChildRetention:  time.Hour,
		ChildGCInterval: 2 * time.Millisecond,
		Diagnostics:     port.NopDiagnostics{},
	}
	startChildGC(ctx, cfg, cs, func(session.SessionID) bool { return false })
	select {
	case <-cs.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("the startup sweep never consulted the store")
	}
	time.Sleep(60 * time.Millisecond) // many would-be ticks
	if got := cs.lists.Load(); got != 1 {
		t.Errorf("sweeper kept Listing after ErrPruneUnsupported (%d Lists, want 1)", got)
	}
}

// TestChildGCStartupOnlySweepsOnceAndExits pins the ChildGCInterval=0 mode:
// exactly one startup sweep, then the goroutine exits (no ticker; goleak at
// TestMain catches a lingering goroutine).
func TestChildGCStartupOnlySweepsOnceAndExits(t *testing.T) {
	cs := &countingPrunable{Store: memstore.New(), listed: make(chan struct{}, 4)}
	cfg := Config{
		ChildRetention: time.Hour, // ChildGCInterval deliberately zero
		Diagnostics:    port.NopDiagnostics{},
	}
	startChildGC(context.Background(), cfg, cs, func(session.SessionID) bool { return false })
	select {
	case <-cs.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("the startup sweep never consulted the store")
	}
	time.Sleep(50 * time.Millisecond)
	if got := cs.lists.Load(); got != 1 {
		t.Errorf("startup-only mode performed %d Lists, want exactly 1", got)
	}
}

func jsonlSnapshotPath(t *testing.T, dir string, id session.SessionID) string {
	t.Helper()
	for _, scanDir := range []string{dir, filepath.Join(dir, "sid-v1")} {
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".session.jsonl") {
				continue
			}
			path := filepath.Join(scanDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var head struct {
				ID session.SessionID `json:"id"`
			}
			if len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), &head) == nil && head.ID == id {
				return path
			}
		}
	}
	t.Fatalf("snapshot for %q not found", id)
	return ""
}

// TestBuildChildGCSweepsStaleJSONLChild is the build-level E2E: a REAL jsonl
// store dir seeded with a stale subagent-* snapshot (aged via os.Chtimes — the
// jsonl store's ModifiedAt is the file mtime) plus a fresh main session, fed
// through a real app.Build with retention enabled in startup-only mode. The
// startup sweep must delete the stale child's files while the main session
// survives untouched.
func TestBuildChildGCSweepsStaleJSONLChild(t *testing.T) {
	storeDir := t.TempDir()
	seed, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	bg := context.Background()
	created := time.Now().Add(-48 * time.Hour)
	for _, id := range []session.SessionID{"subagent-stale", "operator-main"} {
		if err := seed.Save(bg, session.New(id, session.ModeDefault, "/ws", session.Limits{}, created)); err != nil {
			t.Fatalf("seed Save(%q): %v", id, err)
		}
	}
	staleFile := jsonlSnapshotPath(t, storeDir, "subagent-stale")
	mainFile := jsonlSnapshotPath(t, storeDir, "operator-main")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(staleFile, old, old); err != nil {
		t.Fatalf("Chtimes(stale child): %v", err)
	}
	if err := os.Chtimes(mainFile, old, old); err != nil { // main is ancient too — and must STILL survive
		t.Fatalf("Chtimes(main): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	built, err := Build(ctx, Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		StoreDir:       storeDir,
		ChildRetention: 24 * time.Hour, // ChildGCInterval=0: startup-only (the goroutine exits; goleak gates)
		Diagnostics:    port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// The startup sweep runs on its own goroutine; poll for its effect.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(staleFile); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the startup sweep never deleted the stale subagent-* session file")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(mainFile); err != nil {
		t.Fatalf("the MAIN session file did not survive the sweep (stat: %v) — the safety invariant is broken end-to-end", err)
	}
}

// TestBuildZeroConfigChildGCIsNoOp is the build-level posture guard: a
// zero-config Build (in-memory store, no retention fields set) narrates the
// DISABLED fact and starts no sweeper goroutine (the package's goleak TestMain
// is the leak gate — a lingering ticker goroutine after ctx cancel would trip
// it).
func TestBuildZeroConfigChildGCIsNoOp(t *testing.T) {
	diag := newCapturingDiagnostics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	built, err := Build(ctx, Config{
		Workspace:   t.TempDir(),
		Model:       "mock",
		UseMock:     true,
		Diagnostics: diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	if got := diag.countContaining("session GC DISABLED"); got != 1 {
		t.Errorf("zero-config Build narrated 'session GC DISABLED' %d times, want exactly 1", got)
	}
	if got := diag.countContaining("session GC ENABLED"); got != 0 {
		t.Errorf("zero-config Build narrated 'session GC ENABLED' %d times, want 0", got)
	}
}

// TestBuildEnabledChildGCNarratesAndStops pins the enabled wiring end-to-end:
// a Build with retention configured narrates ENABLED, and the ticker goroutine
// exits on ctx cancel (goleak at TestMain is the assertion).
func TestBuildEnabledChildGCNarratesAndStops(t *testing.T) {
	diag := newCapturingDiagnostics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	built, err := Build(ctx, Config{
		Workspace:                  t.TempDir(),
		Model:                      "mock",
		UseMock:                    true,
		ChildRetention:             168 * time.Hour,
		ChildRetentionMaxPerFamily: 500,
		ChildGCInterval:            time.Hour,
		Diagnostics:                diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	if got := diag.countContaining("session GC ENABLED"); got != 1 {
		t.Errorf("Build narrated 'session GC ENABLED' %d times, want exactly 1", got)
	}
}

// --- Schedule-fire retention (ADR 0059 decision #7 Phase-2) -----------------

// TestScheduleFireGCAgePass pins the schedule-fire age pass: "sched--"-prefixed
// sessions older than ScheduleFireRetention are deleted by the schedule-fire
// pass, younger ones survive, and an UNPREFIXED main session is NOT touched by
// the schedule-fire pass.
func TestScheduleFireGCAgePass(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{scheduleFireRetention: 24 * time.Hour})
	f.save(t, "sched--nightly-old")
	f.save(t, "sched--hourly-old")
	f.save(t, "main-old")             // a main, not sched--: the fire pass must not touch it
	f.now = f.now.Add(48 * time.Hour) // the old ones are now 48h old
	f.save(t, "sched--nightly-young")

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (2, 2)", deleted, retained)
	}
	got := f.ids(t)
	for _, old := range []session.SessionID{"sched--nightly-old", "sched--hourly-old"} {
		if got[old] {
			t.Errorf("aged-out sched-- fire %q survived the schedule-fire age pass", old)
		}
	}
	if !got["sched--nightly-young"] {
		t.Error("young sched-- fire was deleted by the schedule-fire age pass")
	}
	if !got["main-old"] {
		t.Error("an UNPREFIXED main was deleted by the schedule-fire pass — it must be invisible to this pass")
	}
}

// TestScheduleFireGCSkipsLive pins the liveness exclusion on the schedule-fire
// age pass: a LIVE sched-- session (one mid-run) older than the horizon keeps
// its slot — the in-flight run protects it, mirroring the main/child age passes.
func TestScheduleFireGCSkipsLive(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{scheduleFireRetention: 24 * time.Hour})
	live := map[session.SessionID]bool{"sched--live-old": true}
	f.gc.isLive = func(id session.SessionID) bool { return live[id] }

	f.save(t, "sched--live-old") // ancient but live: must survive
	f.save(t, "sched--dead-old") // ancient and dead: age pass takes it
	f.now = f.now.Add(48 * time.Hour)

	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 1 || retained != 1 {
		t.Errorf("sweep = (deleted %d, retained %d), want (1, 1)", deleted, retained)
	}
	got := f.ids(t)
	if !got["sched--live-old"] {
		t.Error("LIVE sched-- fire was deleted by the age pass — the liveness exclusion is broken for the schedule-fire pass")
	}
	if got["sched--dead-old"] {
		t.Error("dead aged-out sched-- fire survived the age pass")
	}
}

// TestScheduleFireGCCountCap pins the schedule-fire GLOBAL count cap (ADR 0059
// Phase-2, the symmetric peer of the main cap): with more sched-- fire sessions than
// scheduleFireMaxTotal, the OLDEST fire snapshots go first, the cap is store-wide,
// and a LIVE fire keeps its slot (the next-oldest non-live is deleted in its
// stead). Mirrors TestChildGCMainSessionCountCap.
func TestScheduleFireGCCountCap(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{scheduleFireMaxTotal: 2})
	live := map[session.SessionID]bool{"sched--a": true}
	f.gc.isLive = func(id session.SessionID) bool { return live[id] }
	for i, id := range []session.SessionID{"sched--a", "sched--b", "sched--c", "sched--d"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}

	// 4 fires > cap 2, oldest-first with the live sched--a skipped => sched--b and
	// sched--c deleted (sched--a kept though oldest; sched--d newest).
	deleted, retained := f.gc.sweep(context.Background())
	if deleted != 2 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (2, 2)", deleted, retained)
	}
	got := f.ids(t)
	if !got["sched--a"] {
		t.Error("LIVE fire was deleted — the liveness exclusion is broken for the schedule-fire cap")
	}
	if got["sched--b"] || got["sched--c"] {
		t.Errorf("cap pass kept the oldest non-live fires (b=%v c=%v), want them deleted", got["sched--b"], got["sched--c"])
	}
	if !got["sched--d"] {
		t.Error("newest fire was deleted under the schedule-fire cap pass")
	}
}

// TestScheduleFireGCAgeThenCap pins that the age pass runs BEFORE the cap and the
// cap trims the SURVIVORS down to scheduleFireMaxTotal — the same age→cap
// plumbing sweepMain exercises, now shared by the schedule-fire pass.
func TestScheduleFireGCAgeThenCap(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{scheduleFireRetention: 24 * time.Hour, scheduleFireMaxTotal: 2})
	// Two ancient fires (age pass deletes both) + three recent (cap trims to 2).
	f.save(t, "sched--ancient-1")
	f.save(t, "sched--ancient-2")
	f.now = f.now.Add(48 * time.Hour)
	for i, id := range []session.SessionID{"sched--recent-1", "sched--recent-2", "sched--recent-3"} {
		f.save(t, id)
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
	}

	deleted, retained := f.gc.sweep(context.Background())
	// 2 aged out + 1 over the cap of 2 => 3 deleted, 2 retained.
	if deleted != 3 || retained != 2 {
		t.Errorf("sweep = (deleted %d, retained %d), want (3, 2)", deleted, retained)
	}
	got := f.ids(t)
	if got["sched--ancient-1"] || got["sched--ancient-2"] {
		t.Error("an aged-out fire survived the age pass")
	}
	if got["sched--recent-1"] {
		t.Error("the oldest survivor was kept over the cap — cap should evict oldest-first")
	}
	if !got["sched--recent-2"] || !got["sched--recent-3"] {
		t.Error("the two newest survivors were not kept under the cap")
	}
}

// TestScheduleFireSessionNeverEntersMainOrChildPass is the partition guard: a
// "sched--" session is swept ONLY by the schedule-fire pass. With the child and
// main passes ENABLED (which would otherwise delete ancient sessions) and the
// schedule-fire pass DISABLED, a sched-- session is NEVER touched — it is
// invisible to both the main and child passes. (Mutation-verified: if the
// partition regressed so sched-- fell into mains, the main age pass would delete
// it here.)
func TestScheduleFireSessionNeverEntersMainOrChildPass(t *testing.T) {
	f := newGCFixture(t, childGCPolicy{
		retention:     time.Hour, // child age pass ON
		maxPerFamily:  1,         // child cap ON
		mainRetention: time.Hour, // main age pass ON
		mainMaxTotal:  1,         // main cap ON
		// scheduleFireRetention deliberately 0 — the fire pass is OFF
	})
	f.save(t, "sched--ancient-fire")
	f.save(t, "subagent-old")           // a child, swept by the child pass
	f.save(t, "main-old")               // a main, swept by the main pass
	f.now = f.now.Add(1000 * time.Hour) // all ancient

	deleted, _ := f.gc.sweep(context.Background())
	// The child (subagent-old) and main (main-old) are deleted; the sched-- fire
	// is NOT (its pass is off, and it never enters the other passes).
	if deleted != 2 {
		t.Errorf("sweep deleted %d, want 2 (the child + the main, NOT the sched-- fire)", deleted)
	}
	got := f.ids(t)
	if !got["sched--ancient-fire"] {
		t.Error("a sched-- fire was deleted by the main or child pass with the schedule-fire pass OFF — the partition is broken")
	}
}

// TestScheduleFirePartitionExcludesPrefixSubstrings pins that the "sched--"
// prefix match is a true prefix, not a substring: a main session whose id merely
// CONTAINS "sched--" (but does not start with it) stays a main, and a child
// whose id contains it stays a child. This is the substring-safety twin of
// TestChildGCMainSessionsNeverDeleted.
func TestScheduleFirePartitionExcludesPrefixSubstrings(t *testing.T) {
	// isMainSession must be false for a genuine sched-- prefix...
	if isMainSession("sched--real-fire") {
		t.Error(`isMainSession("sched--real-fire") = true, want false`)
	}
	// ...and true for an id that merely contains the substring.
	if !isMainSession("my-sched--notes") {
		t.Error(`isMainSession("my-sched--notes") = false, want true (substring, not a prefix)`)
	}
	if !isMainSession("pre-sched---post") {
		t.Error(`isMainSession("pre-sched---post") = false, want true`)
	}
	// A child-prefixed id stays a child (the sched-- check does not steal it).
	if _, ok := isChildSession("subagent-x"); !ok {
		t.Error(`isChildSession("subagent-x") = false, want true (child prefix must win)`)
	}
	if isScheduleFireSession("subagent-x") {
		t.Error(`isScheduleFireSession("subagent-x") = true, want false (child prefix, not a fire)`)
	}
}
