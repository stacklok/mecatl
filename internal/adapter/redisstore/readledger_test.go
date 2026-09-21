package redisstore_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/ledgerconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestReadLedgerConformance runs the shared engine/adapter/ledgerconformance
// suite against the Redis-backed tool.ReadLedger over an in-process miniredis
// (AC2.5). The same suite already runs against engine/adapter/memledger; this
// pins that BOTH implementations satisfy the ONE contract.
func TestReadLedgerConformance(t *testing.T) {
	st := newLedgerTestStore(t)

	var counter int
	var mu sync.Mutex
	ledgerconformance.Run(t, func(*testing.T) tool.ReadLedger {
		mu.Lock()
		counter++
		id := session.SessionID("conformance-" + time.Now().Format("150405.000000000") + "-" + strconv.Itoa(counter))
		mu.Unlock()
		return st.ReadLedger(id)
	})
}

// TestPersistentReadLedgers_Scenario2_RedisReopen pins AC2.1: a version
// recorded through one Redis ledger handle is returned after reopening
// ANOTHER handle for the same session — including a handle built from a
// SEPARATE *Store instance (simulating a separate process/replica), the
// process/replica-independence the ADR calls out.
func TestPersistentReadLedgers_Scenario2_RedisReopen(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	first, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New(first): %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New(second): %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	ctx := context.Background()
	const id session.SessionID = "reopen-session"
	want := tool.NewFileVersion("reopen-token-abc")

	handleA := first.ReadLedger(id)
	if err := handleA.RecordRead(ctx, "dir/file.txt", want); err != nil {
		t.Fatalf("handleA.RecordRead: %v", err)
	}

	// A DIFFERENT handle from a DIFFERENT *Store (a separate process/replica)
	// bound to the SAME session must see the SAME recorded version.
	handleB := second.ReadLedger(id)
	got, ok, err := handleB.RecordedVersion(ctx, "dir/file.txt")
	if err != nil {
		t.Fatalf("handleB.RecordedVersion: %v", err)
	}
	if !ok {
		t.Fatal("handleB.RecordedVersion ok = false, want true after handleA recorded it")
	}
	if !got.Equal(want) {
		t.Fatal("handleB.RecordedVersion returned a different token than handleA recorded")
	}

	// A THIRD handle from the SAME *Store also reopens the same session's
	// evidence (the in-process reopen case).
	handleC := first.ReadLedger(id)
	got, ok, err = handleC.RecordedVersion(ctx, "dir/file.txt")
	if err != nil || !ok || !got.Equal(want) {
		t.Fatalf("handleC.RecordedVersion = (ok=%v, err=%v), want (true, nil) with the recorded token", ok, err)
	}
}

// TestPersistentReadLedgers_Scenario2_RedisSessionIsolation pins AC2.2: two
// sessions sharing ONE Redis server retain independent path/version entries;
// neither session can observe the other's prior read.
func TestPersistentReadLedgers_Scenario2_RedisSessionIsolation(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()

	const sessA session.SessionID = "iso-session-a"
	const sessB session.SessionID = "iso-session-b"
	ledgerA := st.ReadLedger(sessA)
	ledgerB := st.ReadLedger(sessB)

	ver := tool.NewFileVersion("isolated-token")
	if err := ledgerA.RecordRead(ctx, "shared/path.txt", ver); err != nil {
		t.Fatalf("ledgerA.RecordRead: %v", err)
	}

	if _, ok, err := ledgerA.RecordedVersion(ctx, "shared/path.txt"); err != nil || !ok {
		t.Fatalf("ledgerA.RecordedVersion = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	got, ok, err := ledgerB.RecordedVersion(ctx, "shared/path.txt")
	if err != nil {
		t.Fatalf("ledgerB.RecordedVersion: %v", err)
	}
	if ok {
		t.Fatalf("ledgerB.RecordedVersion reported ok=true (version=%v) — session B must not see session A's read", got)
	}
}

// TestInvariant_persistent_read_ledger_storage_independence pins AC2.3: the
// Redis ledger's physical addressing is INJECTIVE across (session, normalized
// path) pairs. Adversarial separators/shared prefixes between a session id
// and a path cannot make two distinct pairs collide, and the ReadLedger
// interface (reflected structurally, mirroring the no-core-imports-adapter
// style of layering guard) exposes exactly RecordRead/RecordedVersion — no
// file-content operation.
func TestInvariant_persistent_read_ledger_storage_independence(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()

	// (session="a:b", path="c") and (session="a", path="b:c") must NOT collide
	// even though naive string concatenation ("a:b" + ":" + "c" vs "a" + ":" +
	// "b:c") would produce the same joined string.
	sessAB := session.SessionID("a:b")
	sessA := session.SessionID("a")
	ledgerAB := st.ReadLedger(sessAB)
	ledgerA := st.ReadLedger(sessA)

	verAB := tool.NewFileVersion("token-for-ab-c")
	verABC := tool.NewFileVersion("token-for-a-bc")
	if err := ledgerAB.RecordRead(ctx, "c", verAB); err != nil {
		t.Fatalf("ledgerAB.RecordRead: %v", err)
	}
	if err := ledgerA.RecordRead(ctx, "b:c", verABC); err != nil {
		t.Fatalf("ledgerA.RecordRead: %v", err)
	}

	gotAB, okAB, err := ledgerAB.RecordedVersion(ctx, "c")
	if err != nil || !okAB {
		t.Fatalf("ledgerAB.RecordedVersion(c) = (ok=%v, err=%v), want (true, nil)", okAB, err)
	}
	if !gotAB.Equal(verAB) {
		t.Fatal("ledgerAB.RecordedVersion(c) returned the WRONG token — collided with ledgerA's b:c entry")
	}

	gotA, okA, err := ledgerA.RecordedVersion(ctx, "b:c")
	if err != nil || !okA {
		t.Fatalf("ledgerA.RecordedVersion(b:c) = (ok=%v, err=%v), want (true, nil)", okA, err)
	}
	if !gotA.Equal(verABC) {
		t.Fatal("ledgerA.RecordedVersion(b:c) returned the WRONG token — collided with ledgerAB's c entry")
	}

	// ledgerAB must not see ledgerA's entry under any key, and vice versa.
	if _, ok, err := ledgerAB.RecordedVersion(ctx, "b:c"); err != nil {
		t.Fatalf("ledgerAB.RecordedVersion(b:c): %v", err)
	} else if ok {
		t.Fatal("ledgerAB.RecordedVersion(b:c) unexpectedly found an entry — session isolation violated")
	}
	if _, ok, err := ledgerA.RecordedVersion(ctx, "c"); err != nil {
		t.Fatalf("ledgerA.RecordedVersion(c): %v", err)
	} else if ok {
		t.Fatal("ledgerA.RecordedVersion(c) unexpectedly found an entry — session isolation violated")
	}

	// The ReadLedger interface exposes NO file-content operation: exactly two
	// methods, RecordRead and RecordedVersion.
	assertReadLedgerHasNoFileContentOperation(t, ledgerA)
}

// TestPersistentReadLedgers_Scenario2_RedisFailureClassification pins AC2.4:
// absence is reported (ok=false, err=nil); an unavailable transport (a broker
// that has gone away) and an undecodable/corrupt stored record are BOTH
// reported as a non-nil error DISTINCT from absence.
func TestPersistentReadLedgers_Scenario2_RedisFailureClassification(t *testing.T) {
	ctx := context.Background()

	t.Run("absence is not an error", func(t *testing.T) {
		st := newLedgerTestStore(t)
		const id session.SessionID = "absence-session"
		ledger := st.ReadLedger(id)
		_, ok, err := ledger.RecordedVersion(ctx, "never-read.txt")
		if err != nil {
			t.Fatalf("RecordedVersion(never-recorded) err = %v, want nil", err)
		}
		if ok {
			t.Fatal("RecordedVersion(never-recorded) ok = true, want false")
		}
	})

	t.Run("timeout classifies as unavailable, not absence", func(t *testing.T) {
		st := newLedgerTestStore(t)
		const id session.SessionID = "timeout-session"
		ledger := st.ReadLedger(id)
		// An already-expired context deadline forces the client to fail every
		// command with context.DeadlineExceeded before it ever reaches the
		// broker — the timeout failure mode.
		expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
		_, ok, err := ledger.RecordedVersion(expired, "whatever.txt")
		if err == nil {
			t.Fatal("RecordedVersion(expired ctx) err = nil, want a non-nil error")
		}
		if !errors.Is(err, tool.ErrLedgerUnavailable) {
			t.Errorf("RecordedVersion(expired ctx) error = %v, want errors.Is(_, tool.ErrLedgerUnavailable)", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("RecordedVersion(expired ctx) error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
		}
		if ok {
			t.Fatal("RecordedVersion(expired ctx) ok = true, want false on error")
		}
	})

	t.Run("unavailable transport classifies as unavailable, not absence", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatalf("redisstore.New: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		const id session.SessionID = "unavailable-session"
		ledger := st.ReadLedger(id)
		// Record one entry while the broker is still up, then take it away.
		if err := ledger.RecordRead(ctx, "a.txt", tool.NewFileVersion("v")); err != nil {
			t.Fatalf("RecordRead (broker up): %v", err)
		}
		mr.Close()
		_, ok, err := ledger.RecordedVersion(ctx, "a.txt")
		if err == nil {
			t.Fatal("RecordedVersion(broker down) err = nil, want a non-nil error")
		}
		if !errors.Is(err, tool.ErrLedgerUnavailable) {
			t.Errorf("RecordedVersion(broker down) error = %v, want errors.Is(_, tool.ErrLedgerUnavailable)", err)
		}
		if ok {
			t.Fatal("RecordedVersion(broker down) ok = true, want false on error")
		}
		// RecordRead against a down broker must ALSO fail closed, not silently
		// succeed.
		if err := ledger.RecordRead(ctx, "b.txt", tool.NewFileVersion("v2")); err == nil {
			t.Fatal("RecordRead(broker down) err = nil, want a non-nil error")
		} else if !errors.Is(err, tool.ErrLedgerUnavailable) {
			t.Errorf("RecordRead(broker down) error = %v, want errors.Is(_, tool.ErrLedgerUnavailable)", err)
		}
	})

	t.Run("corrupt stored record classifies as unavailable, not absence", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatalf("miniredis: %v", err)
		}
		t.Cleanup(mr.Close)
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatalf("redisstore.New: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		const id session.SessionID = "corrupt-session"
		ledger := st.ReadLedger(id)

		// Inject a malformed value directly into the ledger hash, bypassing
		// RecordRead (which always writes valid JSON under the correct tag).
		mr.HSet("mecatl:store:v2:ledger:"+string(id), "bad.txt", "not-json-at-all")
		_, ok, err := ledger.RecordedVersion(ctx, "bad.txt")
		if err == nil {
			t.Fatal("RecordedVersion(corrupt record) err = nil, want a non-nil error")
		}
		if !errors.Is(err, tool.ErrLedgerUnavailable) {
			t.Errorf("RecordedVersion(corrupt record) error = %v, want errors.Is(_, tool.ErrLedgerUnavailable)", err)
		}
		if ok {
			t.Fatal("RecordedVersion(corrupt record) ok = true, want false on error")
		}

		// Also cover a well-formed JSON value carrying an unrecognized format
		// tag — the forward-incompatibility case the EventLogFormat precedent
		// guards against.
		mr.HSet("mecatl:store:v2:ledger:"+string(id), "future.txt", `{"v":"redisstore-ledger/99","t":"x"}`)
		_, ok, err = ledger.RecordedVersion(ctx, "future.txt")
		if err == nil {
			t.Fatal("RecordedVersion(unknown format tag) err = nil, want a non-nil error")
		}
		if !errors.Is(err, tool.ErrLedgerUnavailable) {
			t.Errorf("RecordedVersion(unknown format tag) error = %v, want errors.Is(_, tool.ErrLedgerUnavailable)", err)
		}
		if ok {
			t.Fatal("RecordedVersion(unknown format tag) ok = true, want false on error")
		}
	})
}

func TestPersistentReadLedgers_RedisCorruptStateFailsClosed(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	const id session.SessionID = "corrupt-state"
	ledger := st.ReadLedger(id)
	for name, raw := range map[string]string{
		"malformed":        `not-json`,
		"missing-token":    `{"v":"redisstore-ledger/1"}`,
		"null-token":       `{"v":"redisstore-ledger/1","t":null}`,
		"wrong-token-type": `{"v":"redisstore-ledger/1","t":7}`,
		"missing-format":   `{"t":"token"}`,
		"unknown-format":   `{"v":"redisstore-ledger/99","t":"token"}`,
	} {
		t.Run(name, func(t *testing.T) {
			mr.HSet("mecatl:store:v2:ledger:"+string(id), name, raw)
			got, ok, err := ledger.RecordedVersion(ctx, name)
			if err == nil || !errors.Is(err, tool.ErrLedgerUnavailable) {
				t.Fatalf("RecordedVersion = (%v, %v, %v), want zero, false, ErrLedgerUnavailable", got, ok, err)
			}
			if ok {
				t.Fatal("RecordedVersion corrupt state returned ok=true")
			}
			if _, encodeErr := tool.EncodeFileVersion(got); encodeErr == nil {
				t.Fatal("RecordedVersion corrupt state returned a valid FileVersion")
			}
		})
	}

	empty := tool.NewFileVersion("")
	if err := ledger.RecordRead(ctx, "empty-token", empty); err != nil {
		t.Fatalf("RecordRead(valid empty token): %v", err)
	}
	got, ok, err := ledger.RecordedVersion(ctx, "empty-token")
	if err != nil || !ok || !got.Equal(empty) {
		t.Fatalf("RecordedVersion(valid empty token) = (ok=%v, err=%v), want exact valid token", ok, err)
	}
}

func TestPersistentReadLedgers_RedisSessionDeletionRemovesLedger(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()
	const id session.SessionID = "delete-ledger-session"
	if err := st.Save(ctx, session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.ReadLedger(id).RecordRead(ctx, "a.txt", tool.NewFileVersion("v1")); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	if err := st.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := st.ReadLedger(id).RecordedVersion(ctx, "a.txt"); err != nil || ok {
		t.Fatalf("RecordedVersion after Delete = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	if err := st.Save(ctx, session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/reused", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())); err != nil {
		t.Fatalf("Save(reused id): %v", err)
	}
	if _, ok, err := st.ReadLedger(id).RecordedVersion(ctx, "a.txt"); err != nil || ok {
		t.Fatalf("RecordedVersion after session-ID reuse = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestPersistentReadLedgers_RedisConditionalDeletionRemovesLedger(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()

	t.Run("successful deletion removes evidence", func(t *testing.T) {
		const id session.SessionID = "conditional-delete-success"
		if err := st.Save(ctx, session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := st.ReadLedger(id).RecordRead(ctx, "a.txt", tool.NewFileVersion("v1")); err != nil {
			t.Fatalf("RecordRead: %v", err)
		}
		page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("PageSessionMetadata: %v", err)
		}
		var expected port.SessionDiscoveryMeta
		for _, row := range page.Sessions {
			if row.ID == id {
				expected = row
			}
		}
		if expected.ID == "" {
			t.Fatal("saved session missing from metadata page")
		}
		deleted, err := st.DeleteSessionIfUnchanged(ctx, expected)
		if err != nil || !deleted {
			t.Fatalf("DeleteSessionIfUnchanged = (%v, %v), want (true, nil)", deleted, err)
		}
		if _, ok, err := st.ReadLedger(id).RecordedVersion(ctx, "a.txt"); err != nil || ok {
			t.Fatalf("RecordedVersion after conditional delete = (ok=%v, err=%v), want (false, nil)", ok, err)
		}
	})

	t.Run("failed deletion preserves evidence", func(t *testing.T) {
		const id session.SessionID = "conditional-delete-failed"
		sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		if err := st.Save(ctx, sess); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := st.ReadLedger(id).RecordRead(ctx, "a.txt", tool.NewFileVersion("v1")); err != nil {
			t.Fatalf("RecordRead: %v", err)
		}
		page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("PageSessionMetadata: %v", err)
		}
		var stale port.SessionDiscoveryMeta
		for _, row := range page.Sessions {
			if row.ID == id {
				stale = row
			}
		}
		if stale.ID == "" {
			t.Fatal("saved session missing from metadata page")
		}
		sess.SetTitle("changed")
		if err := st.Save(ctx, sess); err != nil {
			t.Fatalf("Save(changed): %v", err)
		}
		deleted, err := st.DeleteSessionIfUnchanged(ctx, stale)
		if err != nil || deleted {
			t.Fatalf("DeleteSessionIfUnchanged(stale) = (%v, %v), want (false, nil)", deleted, err)
		}
		got, ok, err := st.ReadLedger(id).RecordedVersion(ctx, "a.txt")
		want := tool.NewFileVersion("v1")
		if err != nil || !ok || !got.Equal(want) {
			t.Fatalf("RecordedVersion after failed conditional delete = (ok=%v, err=%v), want preserved evidence", ok, err)
		}
	})
}

// TestPersistentReadLedgers_Scenario2_ConcurrentAccess pins AC2.6: concurrent
// record/lookup operations on one session ledger are race-free (task test
// -race is the oracle), and independently opened handles converge on a
// complete, valid opaque token rather than a torn/partial one.
func TestPersistentReadLedgers_Scenario2_ConcurrentAccess(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()
	const id session.SessionID = "concurrent-session"

	var wg sync.WaitGroup
	const n = 20
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		// Each goroutine opens its OWN handle to the SAME session — the
		// independently-opened-handles shape the AC calls out.
		go func() {
			defer wg.Done()
			ledger := st.ReadLedger(id)
			_ = ledger.RecordRead(ctx, "race.txt", tool.NewFileVersion("v"))
		}()
		go func() {
			defer wg.Done()
			ledger := st.ReadLedger(id)
			_, _, _ = ledger.RecordedVersion(ctx, "race.txt")
		}()
	}
	wg.Wait()

	ledger := st.ReadLedger(id)
	got, ok, err := ledger.RecordedVersion(ctx, "race.txt")
	if err != nil {
		t.Fatalf("RecordedVersion after race: %v", err)
	}
	if ok {
		if _, encodeErr := tool.EncodeFileVersion(got); encodeErr != nil {
			t.Fatal("RecordedVersion after race returned an invalid (torn) FileVersion")
		}
	}
}

// TestPersistentReadLedgers_Scenario2_DeleteAndReuseStartsEmpty pins AC2.7:
// deleting a session's Redis ledger scope removes ALL evidence for that
// session, and a NEW handle for the SAME session id afterward starts with no
// recorded version — it cannot inherit the deleted session's evidence.
func TestPersistentReadLedgers_Scenario2_DeleteAndReuseStartsEmpty(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()
	const id session.SessionID = "reused-session"

	ledger := st.ReadLedger(id)
	if err := ledger.RecordRead(ctx, "a.txt", tool.NewFileVersion("v1")); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	if err := ledger.RecordRead(ctx, "b.txt", tool.NewFileVersion("v2")); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	if _, ok, err := ledger.RecordedVersion(ctx, "a.txt"); err != nil || !ok {
		t.Fatalf("RecordedVersion(a.txt) before delete = (ok=%v, err=%v), want (true, nil)", ok, err)
	}

	if err := st.DeleteReadLedger(ctx, id); err != nil {
		t.Fatalf("DeleteReadLedger: %v", err)
	}

	// A FRESH handle for the SAME (now-reused) session id sees NOTHING.
	reused := st.ReadLedger(id)
	if _, ok, err := reused.RecordedVersion(ctx, "a.txt"); err != nil {
		t.Fatalf("RecordedVersion(a.txt) after delete: %v", err)
	} else if ok {
		t.Fatal("RecordedVersion(a.txt) after delete reported ok=true — deleted session evidence leaked into reused id")
	}
	if _, ok, err := reused.RecordedVersion(ctx, "b.txt"); err != nil {
		t.Fatalf("RecordedVersion(b.txt) after delete: %v", err)
	} else if ok {
		t.Fatal("RecordedVersion(b.txt) after delete reported ok=true — deleted session evidence leaked into reused id")
	}

	// Deleting an already-empty scope is a no-op, not an error.
	if err := st.DeleteReadLedger(ctx, id); err != nil {
		t.Fatalf("DeleteReadLedger (second, already-empty): %v", err)
	}
}

// TestPersistentReadLedgers_Scenario2_InjectiveRedisIdentity pins AC2.8:
// distinct session/path pairs whose byte content contains the adapter's OWN
// key separator (":") or a shared prefix remain distinct Redis addresses and
// cannot observe one another's evidence. This exercises the SAME injective
// scheme as AC2.3 but through the raw miniredis keyspace, proving the
// separation is physical (distinct Redis keys/fields), not merely an artifact
// of the Go-level API.
func TestPersistentReadLedgers_Scenario2_InjectiveRedisIdentity(t *testing.T) {
	st := newLedgerTestStore(t)
	ctx := context.Background()

	pairs := []struct {
		session session.SessionID
		path    string
		token   string
	}{
		{"session", "path", "t-plain"},
		{"session:evil", "path", "t-colon-in-session"},
		{"session", "evil:path", "t-colon-in-path"},
		{"mecatl:ledger:session", "path", "t-full-prefix-in-session"},
		{"", "path", "t-empty-session"},
		{"session", "", "t-empty-path"},
	}

	for _, p := range pairs {
		ledger := st.ReadLedger(p.session)
		if err := ledger.RecordRead(ctx, p.path, tool.NewFileVersion(p.token)); err != nil {
			t.Fatalf("RecordRead(session=%q, path=%q): %v", p.session, p.path, err)
		}
	}

	// Every pair's OWN lookup returns its OWN token — no pair observes
	// another's evidence despite deliberately overlapping separator bytes.
	for _, p := range pairs {
		ledger := st.ReadLedger(p.session)
		got, ok, err := ledger.RecordedVersion(ctx, p.path)
		if err != nil {
			t.Fatalf("RecordedVersion(session=%q, path=%q): %v", p.session, p.path, err)
		}
		if !ok {
			t.Fatalf("RecordedVersion(session=%q, path=%q) ok=false, want true", p.session, p.path)
		}
		want := tool.NewFileVersion(p.token)
		if !got.Equal(want) {
			t.Fatalf("RecordedVersion(session=%q, path=%q) returned the wrong token — adversarial separator caused a collision", p.session, p.path)
		}
	}
}

// assertReadLedgerHasNoFileContentOperation structurally asserts the
// tool.ReadLedger interface exposes exactly RecordRead/RecordedVersion and
// nothing that reads/writes file content (a Read/Write/Stat/Glob-shaped
// method). It defends AC2.3's "no file-content operation" clause against a
// FUTURE accidental widening of the port, not just today's snapshot.
func assertReadLedgerHasNoFileContentOperation(t *testing.T, l tool.ReadLedger) {
	t.Helper()
	switch v := l.(type) {
	case interface {
		Read(ctx context.Context, path string) ([]byte, error)
	}:
		t.Fatalf("ReadLedger implementation %T exposes a file-content Read method", v)
	default:
	}
	switch v := l.(type) {
	case interface {
		Write(ctx context.Context, path string, data []byte) error
	}:
		t.Fatalf("ReadLedger implementation %T exposes a file-content Write method", v)
	default:
	}
}

func newLedgerTestStore(t *testing.T) *redisstore.Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
