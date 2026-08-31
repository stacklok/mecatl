package redisstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// legacyRecord builds the exact LIST element a pre-ADR-0250 mecatl wrote:
// the format-tagged envelope, RPUSHed onto mecatl:events:<id>.
//
// Hand-built rather than produced by an old binary because the ENCODING is the
// compatibility surface under test. If a future change alters the envelope, this
// fixture keeps asserting against what is actually on disk in a deployed
// keyspace, which a round-trip through current code could not.
func legacyRecord(t *testing.T, seq int64, text string) string {
	t.Helper()
	ev, err := json.Marshal(session.Event{Type: session.EvMessageDelta, Seq: seq, Text: text})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	rec, err := json.Marshal(map[string]any{"v": redisstore.EventLogFormat, "ev": json.RawMessage(ev)})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return string(rec)
}

func legacyStore(t *testing.T) (*miniredis.Miniredis, *redisstore.Store) {
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
	return mr, st
}

func readAllEvents(t *testing.T, log port.EventLog, id session.SessionID) []string {
	t.Helper()
	var out []string
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("Read(%q): %v", id, err)
		}
		out = append(out, ev.Text)
	}
	return out
}

// TestSDKServerEnablers_Scenario6_LegacyRedisListMigrates is AC6.6: existing
// Redis LIST event logs are readable after the Stream migration, and no session
// loses its history.
//
// This is the acceptance criterion with a real deployed keyspace behind it. An
// operator upgrading mecatl has `mecatl:events:*` LIST keys already written, and
// the migration must be invisible to them in both directions: a log not yet
// touched by the new binary stays readable, and one that IS touched keeps every
// record it had.
func TestSDKServerEnablers_Scenario6_LegacyRedisListMigrates(t *testing.T) {
	ctx := context.Background()
	const id session.SessionID = "legacy-list-session"
	key := "mecatl:events:" + string(id)

	t.Run("an unmigrated LIST stays readable", func(t *testing.T) {
		// The upgrade case that must not need a migration to work at all: the
		// new binary reads a log it has never written to.
		mr, st := legacyStore(t)
		mr.RPush(key, legacyRecord(t, 0, "one"), legacyRecord(t, 1, "two"))

		if got, want := readAllEvents(t, st, id), []string{"one", "two"}; !equalStrings(got, want) {
			t.Fatalf("legacy Read = %v, want %v", got, want)
		}
		if kind := mr.Type(key); kind != "list" {
			t.Errorf("reading migrated the key to %q; a READ must never mutate storage", kind)
		}
	})

	t.Run("an unmigrated LIST is cursor-readable", func(t *testing.T) {
		// A watcher must be able to attach to a log that predates the migration
		// without waiting for an append to convert it.
		mr, st := legacyStore(t)
		mr.RPush(key, legacyRecord(t, 0, "one"), legacyRecord(t, 1, "two"))

		recs := collect(t, st, id, "")
		if len(recs) != 2 || recs[0].Event.Text != "one" || recs[1].Event.Text != "two" {
			t.Fatalf("ReadAfter over a legacy list = %+v, want the two records in order", recs)
		}
		// And its cursor resumes correctly WITHIN the legacy basis.
		after := collect(t, st, id, recs[0].Cursor)
		if len(after) != 1 || after[0].Event.Text != "two" {
			t.Fatalf("ReadAfter(legacy cursor) = %+v, want just the second record", after)
		}
	})

	t.Run("appending migrates in place and keeps every record", func(t *testing.T) {
		// The core of AC6.6: history is CARRIED, not dropped, and the order is
		// preserved across the datatype change.
		mr, st := legacyStore(t)
		mr.RPush(key, legacyRecord(t, 0, "one"), legacyRecord(t, 1, "two"))

		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 2, Text: "three"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if kind := mr.Type(key); kind != "stream" {
			t.Fatalf("after an append the key is %q, want %q", kind, "stream")
		}
		if got, want := readAllEvents(t, st, id), []string{"one", "two", "three"}; !equalStrings(got, want) {
			t.Fatalf("after migration Read = %v, want %v — the pre-migration history was lost", got, want)
		}
		recs := collect(t, st, id, "")
		if len(recs) != 3 {
			t.Fatalf("ReadAfter surfaced %d records after migration, want 3", len(recs))
		}
		for i, want := range []string{"one", "two", "three"} {
			if recs[i].Event.Text != want {
				t.Errorf("record[%d] = %q, want %q", i, recs[i].Event.Text, want)
			}
		}
	})

	t.Run("a cursor from the legacy basis expires after migration", func(t *testing.T) {
		// The subtle half, and the reason the migration is safe rather than
		// merely lossless. Legacy positions are LIST INDICES; migrated positions
		// are stream IDs. A cursor carried across that change must not resolve —
		// index 1 and stream id 1-0 are both "valid", and honouring the old one
		// would silently deliver the wrong records.
		mr, st := legacyStore(t)
		mr.RPush(key, legacyRecord(t, 0, "one"), legacyRecord(t, 1, "two"))
		legacyCursors := collect(t, st, id, "")
		if len(legacyCursors) != 2 {
			t.Fatalf("expected 2 legacy records, got %d", len(legacyCursors))
		}
		stale := legacyCursors[0].Cursor

		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 2, Text: "three"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		var gotErr error
		for rec, err := range st.ReadAfter(ctx, id, stale, port.ReadOptions{}) {
			if err != nil {
				gotErr = err
				break
			}
			t.Errorf("a pre-migration cursor yielded record %+v; it must not resolve against the migrated log", rec)
		}
		if !errors.Is(gotErr, port.ErrCursorExpired) {
			t.Fatalf("ReadAfter(pre-migration cursor) error = %v, want ErrCursorExpired", gotErr)
		}
	})

	t.Run("deleting the log drops its generation so the basis really moves", func(t *testing.T) {
		// The generation lives in its own key, so the delete path has to drop it
		// explicitly (the two Lua scripts in metadata_index.go). If it survived a
		// delete, a recreated log would inherit the old basis and a cursor from
		// the PREVIOUS log would resolve against the new one — the exact silent
		// corruption generations exist to convert into a loud failure.
		mr, st := legacyStore(t)
		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "first log"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		first := collect(t, st, id, "")
		if len(first) != 1 {
			t.Fatalf("expected 1 record, got %d", len(first))
		}
		stale := first[0].Cursor

		if err := st.Delete(ctx, id); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if mr.Exists("mecatl:events-gen:" + string(id)) {
			t.Error("the generation key survived Delete; a recreated log would inherit the old basis")
		}
		if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "second log"}); err != nil {
			t.Fatalf("AppendEvent(after delete): %v", err)
		}

		var gotErr error
		for rec, err := range st.ReadAfter(ctx, id, stale, port.ReadOptions{}) {
			if err != nil {
				gotErr = err
				break
			}
			t.Errorf("a cursor from the deleted log yielded %+v; it must not resolve against the recreated log", rec)
		}
		if !errors.Is(gotErr, port.ErrCursorExpired) {
			t.Fatalf("a cursor from the deleted log's basis error = %v, want ErrCursorExpired", gotErr)
		}
	})
}

func collect(t *testing.T, log port.CursorEventLog, id session.SessionID, after port.Cursor) []port.LogRecord {
	t.Helper()
	var out []port.LogRecord
	for rec, err := range log.ReadAfter(context.Background(), id, after, port.ReadOptions{}) {
		if err != nil {
			t.Fatalf("ReadAfter(after=%q): %v", after, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestLegacyListCursorIsSessionScoped is the regression test for the bug review
// on #868 surfaced and this branch reproduced: a cursor issued for one session
// resolving against ANOTHER session's legacy LIST.
//
// It lives here rather than only in the shared conformance suite because the
// suite's cross-session case runs over Stream logs, and the LEGACY LIST path is
// where the bug was actually live. Two legacy logs both report the EMPTY
// generation — that is the whole point of the empty generation, it is what keeps
// pre-ADR-0250 logs readable — so the generation check cannot separate them, and
// a list INDEX needs no boundary alignment to land on a real record. Before the
// fix this returned session B's records with a nil error:
//
//	ReadAfter(sess-bbb, cursor-for-sess-aaa) -> [B-three B-four] err=<nil>
func TestLegacyListCursorIsSessionScoped(t *testing.T) {
	ctx := context.Background()
	mr, st := legacyStore(t)

	const idA session.SessionID = "legacy-scope-a"
	const idB session.SessionID = "legacy-scope-b"

	mr.RPush("mecatl:events:"+string(idA), legacyRecord(t, 0, "A-one"), legacyRecord(t, 1, "A-two"))
	// B is deliberately LONGER, so an index carried over from A lands on a real
	// record instead of failing incidentally past the end of the list.
	mr.RPush("mecatl:events:"+string(idB),
		legacyRecord(t, 0, "B-one"), legacyRecord(t, 1, "B-two"),
		legacyRecord(t, 2, "B-three"), legacyRecord(t, 3, "B-four"))

	// Exactly the cursor form readLegacyListAfter issues for a legacy log: the
	// empty generation plus a list index.
	curA := port.EncodeCursor(idA, "", "2")

	var gotErr error
	var yielded []string
	for rec, err := range st.ReadAfter(ctx, idB, curA, port.ReadOptions{}) {
		if err != nil {
			gotErr = err
			break
		}
		yielded = append(yielded, rec.Event.Text)
	}
	if gotErr == nil {
		t.Fatalf("a cursor issued for %q resolved against %q and yielded %v with no error", idA, idB, yielded)
	}
	if !errors.Is(gotErr, port.ErrCursorMalformed) {
		t.Errorf("cross-session legacy cursor error = %v, want ErrCursorMalformed", gotErr)
	}
	if len(yielded) != 0 {
		t.Errorf("yielded %v before failing; a cross-session cursor must surface no records at all", yielded)
	}

	// The same log still reads correctly with its OWN cursor — the rejection must
	// not be a blanket refusal of legacy cursors.
	curB := port.EncodeCursor(idB, "", "2")
	var own []string
	for rec, err := range st.ReadAfter(ctx, idB, curB, port.ReadOptions{}) {
		if err != nil {
			t.Fatalf("ReadAfter(B, cursor-for-B): %v", err)
		}
		own = append(own, rec.Event.Text)
	}
	if !equalStrings(own, []string{"B-three", "B-four"}) {
		t.Errorf("B read with its own legacy cursor = %v, want [B-three B-four]", own)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLegacyFollowerExpiresWhenMigrationLandsUnderIt covers the migration
// happening WHILE a follower is parked on the legacy LIST.
//
// It is redisstore-specific by necessity: the shared conformance suite's
// reset-mid-follow case exercises the Stream path, because that is the only
// datatype a backend-neutral suite can assume. The legacy path is both a
// different code path and the one with the WORSE trigger — a delete-and-recreate
// under a live follower is unlikely, whereas this log being migrated is
// GUARANTEED on its next append, which is exactly the upgrade window the legacy
// path exists to serve.
//
// The failure it pins is a misCLASSIFICATION rather than wrong data: LRANGE
// against the migrated key returns WRONGTYPE, which reached the consumer wrapped
// as an infrastructure fault. A client cannot act on that — "restart from the
// beginning because your positions are now stream IDs" and "Redis is unwell,
// retry" call for opposite responses. Raised in review on #869.
func TestLegacyFollowerExpiresWhenMigrationLandsUnderIt(t *testing.T) {
	t.Parallel()
	mr, st := legacyStore(t)
	const id session.SessionID = "legacy-follow-migrate"
	mr.RPush("mecatl:events:"+string(id), legacyRecord(t, 0, "legacy-one"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type outcome struct {
		rec  port.LogRecord
		err  error
		done bool
	}
	outcomes := make(chan outcome, 8)
	go func() {
		for rec, err := range st.ReadAfter(ctx, id, "", port.ReadOptions{Follow: true}) {
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			outcomes <- outcome{rec: rec}
		}
		outcomes <- outcome{done: true}
	}()

	next := func(what string) outcome {
		t.Helper()
		select {
		case got := <-outcomes:
			return got
		case <-time.After(30 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
			return outcome{}
		}
	}

	// Drain the pre-migration record, so the follower is provably parked on the
	// LIST path rather than still resolving its cursor.
	switch first := next("the legacy record present before the migration"); {
	case first.err != nil:
		t.Fatalf("legacy follow yielded error %v before the migration", first.err)
	case first.done:
		t.Fatal("legacy follow ended before the migration")
	case first.rec.Event.Text != "legacy-one":
		t.Fatalf("first legacy record = %q, want %q", first.rec.Event.Text, "legacy-one")
	}

	// The append migrates the log in place: the LIST becomes a Stream and a real
	// generation is minted, so list indices stop addressing anything.
	if _, err := st.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "post-migration"}); err != nil {
		t.Fatalf("AppendEvent (migrating): %v", err)
	}

	switch got := next("the legacy follower's reaction to the migration"); {
	case got.err != nil:
		if !errors.Is(got.err, port.ErrCursorExpired) {
			t.Errorf("legacy follower got error %v, want ErrCursorExpired; an infrastructure-shaped error leaves the consumer unable to tell a migrated basis from a broken Redis", got.err)
		}
	case got.done:
		t.Error("legacy follower ended cleanly across the migration, which is indistinguishable from the log having no more records")
	default:
		t.Errorf("legacy follower was handed record %q across the migration instead of ErrCursorExpired", got.rec.Event.Text)
	}
}
