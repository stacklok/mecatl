package eventlogconformance

// This file is the cursor half of the suite: it pins port.CursorEventLog, the
// durable-position contract ADR 0250 adds on top of port.EventLog.
//
// RunCursor is deliberately a SEPARATE entry point from Run. EventLog is a
// shipped port with backends that have not opted into cursors, and folding the
// two suites together would turn "this backend has not opted in yet" into a
// build failure rather than a choice.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// CursorSuite parameterises the cursor conformance run.
type CursorSuite struct {
	// New returns a fresh, isolated cursor log.
	New func(t *testing.T) port.CursorEventLog

	// Reset replaces the positional basis of id's log, as deleting the session
	// and recording a new one under the same id would.
	//
	// It is REQUIRED rather than optional because the guarantee it proves is the
	// one a positional cursor cannot provide by itself: without a generation, a
	// cursor into a rebuilt log does not fail, it silently addresses a DIFFERENT
	// record. A backend that cannot express "the basis moved" cannot honestly
	// claim to implement this port.
	Reset func(t *testing.T, log port.CursorEventLog, id session.SessionID)

	// SkipCrossProcess opts a backend out of the durability subtest that requires
	// a second reader over independent state. memstore is the only legitimate
	// user: a second process shares no memory with it, so the obligation is
	// meaningless there rather than unmet.
	SkipCrossProcess bool

	// NewPair returns two INDEPENDENTLY CONSTRUCTED handles over the SAME
	// durable state — the in-process stand-in for two replicas sharing a store.
	//
	// It is required unless SkipCrossProcess is set. Reusing New twice would not
	// do: New returns an ISOLATED log, so a cross-reader subtest built on it
	// would pass trivially against a backend that shares nothing, which is the
	// exact failure it exists to catch.
	NewPair func(t *testing.T) (writer, reader port.CursorEventLog)
}

// followTimeout bounds every subtest that waits on a follower. Generous enough
// for a loaded CI box and a size-polling backend, short enough that a genuine
// hang fails the run instead of stalling it.
const followTimeout = 30 * time.Second

// RunCursor executes the shared CursorEventLog conformance table.
func RunCursor(t *testing.T, s CursorSuite) {
	t.Helper()
	if s.New == nil || s.Reset == nil {
		t.Fatal("CursorSuite requires both New and Reset")
	}
	if !s.SkipCrossProcess && s.NewPair == nil {
		t.Fatal("CursorSuite requires NewPair unless SkipCrossProcess is set")
	}
	ctx := context.Background()

	t.Run("append returns cursors and ReadAfter replays from the beginning", func(t *testing.T) {
		log := s.New(t)
		const id session.SessionID = "conf-cursor-replay"
		want := representativeEvents()
		for i, ev := range want {
			cur, err := log.AppendEvent(ctx, id, ev)
			if err != nil {
				t.Fatalf("AppendEvent #%d: %v", i, err)
			}
			if cur == "" {
				t.Fatalf("AppendEvent #%d returned the ZERO cursor, which means the beginning of the log", i)
			}
		}
		got := collectRecords(t, log, id, "", port.ReadOptions{})
		if len(got) != len(want) {
			t.Fatalf("ReadAfter(zero cursor) returned %d records, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].Kind != port.LogRecordEvent {
				t.Errorf("record[%d].Kind = %q, want %q", i, got[i].Kind, port.LogRecordEvent)
			}
			assertEventEqual(t, i, got[i].Event, want[i])
			if got[i].Live {
				t.Errorf("record[%d].Live = true during a non-follow replay; nothing here arrived after the tail", i)
			}
		}
	})

	t.Run("a cursor resumes strictly after its record", func(t *testing.T) {
		// The core promise: hand back the cursor you last processed and you get
		// what came next — no repeats, no gaps. An off-by-one here is the whole
		// feature failing, in the direction that loses an event.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-resume"
		var cursors []port.Cursor
		for i := range 5 {
			cur, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)})
			if err != nil {
				t.Fatalf("AppendEvent #%d: %v", i, err)
			}
			cursors = append(cursors, cur)
		}
		for after := range 5 {
			got := collectRecords(t, log, id, cursors[after], port.ReadOptions{})
			want := 4 - after
			if len(got) != want {
				t.Fatalf("ReadAfter(cursor of record %d) returned %d records, want %d", after, len(got), want)
			}
			for i, rec := range got {
				if wantSeq := int64(after + 1 + i); rec.Event.Seq != wantSeq {
					t.Errorf("ReadAfter(cursor of record %d) record[%d].Seq = %d, want %d", after, i, rec.Event.Seq, wantSeq)
				}
			}
		}
	})

	t.Run("the newest cursor yields nothing", func(t *testing.T) {
		log := s.New(t)
		const id session.SessionID = "conf-cursor-tail"
		var last port.Cursor
		for i := range 3 {
			var err error
			if last, err = log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		if got := collectRecords(t, log, id, last, port.ReadOptions{}); len(got) != 0 {
			t.Fatalf("ReadAfter(newest cursor) returned %d records, want 0 — a caught-up reader must not re-receive its own tail", len(got))
		}
	})

	t.Run("ReadAfter of an unknown session is an empty sequence", func(t *testing.T) {
		// Absence is data, exactly as on the legacy Read. A backend that maps
		// "never appended" to NOT_FOUND makes first-attach an error case.
		log := s.New(t)
		for rec, err := range log.ReadAfter(ctx, "conf-cursor-never-appended", "", port.ReadOptions{}) {
			t.Fatalf("ReadAfter(unknown id) yielded (%+v, %v), want an empty sequence", rec, err)
		}
	})

	t.Run("Limit bounds the batch and its last cursor resumes exactly", func(t *testing.T) {
		// Bounded paging is the capability the legacy Read structurally cannot
		// offer: it reads the whole log and stops. Paging is only useful if the
		// page boundary is resumable, so the two are asserted together.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-paging"
		const total = 10
		for i := range total {
			if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		var seen int
		var cur port.Cursor
		for page := range total {
			batch := collectRecords(t, log, id, cur, port.ReadOptions{Limit: 3})
			if len(batch) == 0 {
				break
			}
			if len(batch) > 3 {
				t.Fatalf("page %d returned %d records, want at most the Limit of 3", page, len(batch))
			}
			for _, rec := range batch {
				if rec.Event.Seq != int64(seen) {
					t.Fatalf("paged record %d has Seq %d, want %d — paging must not skip or repeat", seen, rec.Event.Seq, seen)
				}
				seen++
			}
			cur = batch[len(batch)-1].Cursor
		}
		if seen != total {
			t.Errorf("paging surfaced %d of %d records", seen, total)
		}
	})

	// B1 (review, #868): the legacy Append path must land in the SAME cursor
	// position space as AppendEvent.
	t.Run("a legacy Append is visible and positioned in the cursor space", func(t *testing.T) {
		// Every backend's doc comment claims its legacy port.EventLog.Append
		// routes through the same machinery as AppendEvent, but until this case
		// existed nothing proved it: a backend that special-cased Append into a
		// side channel passed the whole suite while silently excluding
		// legacy-path appends from cursor visibility — so a follower would never
		// see events written by the shipped relay, which calls Append.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-legacy-append"

		first, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "via AppendEvent"})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		// The embedded legacy port, exactly as the relay calls it.
		if err := log.Append(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "via Append"}); err != nil {
			t.Fatalf("Append: %v", err)
		}

		after := collectRecords(t, log, id, first, port.ReadOptions{})
		if len(after) != 1 {
			t.Fatalf("ReadAfter(cursor before the legacy Append) returned %d records, want 1 — a legacy Append must occupy a cursor position", len(after))
		}
		if got := after[0].Event.Text; got != "via Append" {
			t.Errorf("record after the AppendEvent cursor = %q, want the legacy-Append record", got)
		}
		if after[0].Cursor == "" || after[0].Cursor == first {
			t.Errorf("legacy-Append record cursor = %q (previous %q); it must advance", after[0].Cursor, first)
		}

		// And the whole log reads back in append order from the beginning.
		all := collectRecords(t, log, id, "", port.ReadOptions{})
		if len(all) != 2 || all[0].Event.Text != "via AppendEvent" || all[1].Event.Text != "via Append" {
			t.Errorf("full replay = %+v, want the AppendEvent record then the Append record", all)
		}
	})

	// B2 (review, #868): a position is only meaningful against ONE log.
	t.Run("a cursor issued for another session is rejected, never resolved", func(t *testing.T) {
		// Without this, a cursor for session A applied to session B decodes
		// cleanly whenever the two share a basis value and resolves to a real —
		// but WRONG — record. That is reachable in practice on the legacy path,
		// where a log predating generations reports the EMPTY generation, so two
		// legacy logs share a basis and the generation check cannot separate them.
		// The port enforces the session check for every backend; this case is what
		// keeps a backend from routing around it.
		log := s.New(t)
		const idA session.SessionID = "conf-cursor-session-a"
		const idB session.SessionID = "conf-cursor-session-b"

		curA, err := log.AppendEvent(ctx, idA, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "A-one"})
		if err != nil {
			t.Fatalf("AppendEvent(A): %v", err)
		}
		// B is made LONGER than A so a positional cursor from A would land on a
		// real record in B rather than failing incidentally past the end.
		for i := range 5 {
			if _, err := log.AppendEvent(ctx, idB, session.Event{Type: session.EvMessageDelta, Seq: int64(i), Text: "B-record"}); err != nil {
				t.Fatalf("AppendEvent(B): %v", err)
			}
		}

		var gotErr error
		var yielded int
		for rec, err := range log.ReadAfter(ctx, idB, curA, port.ReadOptions{}) {
			if err != nil {
				gotErr = err
				break
			}
			yielded++
			t.Errorf("session B yielded %+v for a cursor issued against session A", rec)
		}
		if gotErr == nil {
			t.Fatalf("ReadAfter(B, cursor-for-A) yielded %d records and no error; a cross-session cursor must never resolve", yielded)
		}
		if !errors.Is(gotErr, port.ErrCursorMalformed) {
			t.Errorf("cross-session cursor error = %v, want ErrCursorMalformed (nothing moved underneath the caller — the cursor was never valid here)", gotErr)
		}
	})

	// N5 (review, #868): a gap is a record, so it must consume a page slot.
	t.Run("a gap consumes a Limit slot like an event", func(t *testing.T) {
		// The port says a gap "occupies a position and advances cursors". If a
		// backend counted only events against Limit, a page could silently
		// overrun its budget, and a caller sizing pages against a memory or
		// message-size bound would have that bound broken by an invisible record.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-gap-limit"
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "one"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if _, err := log.AppendGap(ctx, id, "append failed here"); err != nil {
			t.Fatalf("AppendGap: %v", err)
		}
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "two"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		page := collectRecords(t, log, id, "", port.ReadOptions{Limit: 2})
		if len(page) != 2 {
			t.Fatalf("Limit 2 over [event, gap, event] returned %d records, want exactly 2", len(page))
		}
		if page[0].Kind != port.LogRecordEvent || page[1].Kind != port.LogRecordGap {
			t.Errorf("page kinds = [%s %s], want [event gap] — the gap must occupy the second slot, not be skipped", page[0].Kind, page[1].Kind)
		}
		// And the page boundary still resumes exactly, gap included.
		rest := collectRecords(t, log, id, page[1].Cursor, port.ReadOptions{})
		if len(rest) != 1 || rest[0].Event.Text != "two" {
			t.Errorf("resume after the gap = %+v, want just the trailing event", rest)
		}
	})

	// N3 (review, #868): Limit is a TOTAL, not a per-wake-up budget.
	t.Run("Limit ends a following read rather than re-arming per wake-up", func(t *testing.T) {
		// Two readings of Limit+Follow are possible — N records ever, or N per
		// tail wake-up — and they are indistinguishable until a follower quietly
		// streams forever. The port documents the total; this pins it, because a
		// backend implementing the other reading would never terminate here.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-limit-follow"
		for i := range 3 {
			if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		followCtx, cancel := context.WithTimeout(ctx, followTimeout)
		defer cancel()

		var got int
		for rec, err := range log.ReadAfter(followCtx, id, "", port.ReadOptions{Limit: 2, Follow: true}) {
			if err != nil {
				t.Fatalf("ReadAfter: %v", err)
			}
			_ = rec
			got++
			if got > 2 {
				t.Fatalf("a following read with Limit 2 yielded %d records; Limit must be a total, not a per-wake-up budget", got)
			}
		}
		if got != 2 {
			t.Errorf("following read with Limit 2 yielded %d records, want 2 and a clean end", got)
		}
		if followCtx.Err() != nil {
			t.Error("the read only ended because the context expired; Limit must end a following read on its own")
		}
	})

	t.Run("follow delivers appends made after the read started", func(t *testing.T) {
		log := s.New(t)
		const id session.SessionID = "conf-cursor-follow"
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "before"}); err != nil {
			t.Fatalf("AppendEvent(before): %v", err)
		}

		followCtx, cancel := context.WithTimeout(ctx, followTimeout)
		defer cancel()
		recs := make(chan port.LogRecord, 8)
		errs := make(chan error, 1)
		go func() {
			defer close(recs)
			for rec, err := range log.ReadAfter(followCtx, id, "", port.ReadOptions{Follow: true}) {
				if err != nil {
					errs <- err
					return
				}
				recs <- rec
			}
		}()

		first := mustRecv(t, recs, "the record present before the follow started")
		if first.Text() != "before" {
			t.Errorf("first followed record = %q, want %q", first.Text(), "before")
		}
		if first.Live {
			t.Error("a record that already existed when the follow started is marked Live; it is replay")
		}

		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "after"}); err != nil {
			t.Fatalf("AppendEvent(after): %v", err)
		}
		second := mustRecv(t, recs, "the record appended while following")
		if second.Text() != "after" {
			t.Errorf("second followed record = %q, want %q", second.Text(), "after")
		}
		if !second.Live {
			t.Error("a record appended while following is not marked Live; a client cannot tell replay from live")
		}
		select {
		case err := <-errs:
			t.Fatalf("follow yielded an error: %v", err)
		default:
		}

		// A cancelled follow ends cleanly — cancellation is how a watch is meant
		// to stop, and surfacing it as a fault makes every clean detach look like
		// a failure.
		cancel()
		waitClosed(t, recs, "the follow iterator after its context was cancelled")
		select {
		case err := <-errs:
			t.Errorf("a cancelled follow yielded error %v, want a clean end", err)
		default:
		}
	})

	t.Run("a gap marker occupies a position and is skipped by the legacy Read", func(t *testing.T) {
		// AC6.7. The gap must be visible to a cursor reader (that is its entire
		// purpose — telling a watcher it missed something) and invisible to
		// port.EventLog.Read, whose shipped contract is that it returns events.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-gap"
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "one"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		gapCur, err := log.AppendGap(ctx, id, "backend rejected the record")
		if err != nil {
			t.Fatalf("AppendGap: %v", err)
		}
		if gapCur == "" {
			t.Fatal("AppendGap returned the ZERO cursor; a gap must occupy a real position or cursors skip past it")
		}
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "two"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		recs := collectRecords(t, log, id, "", port.ReadOptions{})
		if len(recs) != 3 {
			t.Fatalf("ReadAfter surfaced %d records, want 3 (event, gap, event)", len(recs))
		}
		if recs[1].Kind != port.LogRecordGap {
			t.Errorf("record[1].Kind = %q, want %q", recs[1].Kind, port.LogRecordGap)
		}
		if recs[1].GapReason == "" {
			t.Error("the gap record carries no reason; an operator has nothing to diagnose from")
		}

		// Resuming from the gap's own cursor must land on the record after it.
		after := collectRecords(t, log, id, gapCur, port.ReadOptions{})
		if len(after) != 1 || after[0].Text() != "two" {
			t.Errorf("ReadAfter(gap cursor) = %d records, want the single event after the gap", len(after))
		}

		var events []session.Event
		for ev, err := range log.Read(ctx, id) {
			if err != nil {
				t.Fatalf("legacy Read: %v", err)
			}
			events = append(events, ev)
		}
		if len(events) != 2 {
			t.Fatalf("legacy Read returned %d events, want 2 — it must SKIP the gap, not surface it", len(events))
		}
		if events[0].Text != "one" || events[1].Text != "two" {
			t.Errorf("legacy Read returned %q,%q, want %q,%q", events[0].Text, events[1].Text, "one", "two")
		}
	})

	t.Run("a cursor from a superseded generation expires", func(t *testing.T) {
		// AC6.3. The failure mode this prevents is not an error — it is a
		// positional cursor cheerfully resolving to a real record that is not the
		// one the client last saw.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-generation"
		stale, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "old basis"})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		s.Reset(t, log, id)
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "new basis"}); err != nil {
			t.Fatalf("AppendEvent after reset: %v", err)
		}

		var yielded int
		var gotErr error
		for rec, err := range log.ReadAfter(ctx, id, stale, port.ReadOptions{}) {
			if err != nil {
				gotErr = err
				break
			}
			yielded++
			t.Errorf("a stale-generation cursor yielded record %+v instead of expiring — this is the silent-wrong-data case", rec)
		}
		if gotErr == nil {
			t.Fatalf("ReadAfter(stale cursor) yielded %d records and no error, want ErrCursorExpired", yielded)
		}
		if !errors.Is(gotErr, port.ErrCursorExpired) {
			t.Errorf("ReadAfter(stale cursor) error = %v, want ErrCursorExpired so a client knows to restart rather than retry", gotErr)
		}
	})

	t.Run("a reset during a live follow expires the cursor", func(t *testing.T) {
		// The live-follow sibling of the case above, and a genuinely different
		// code path: the cold case is caught by the entry-point DecodeCursor,
		// whereas a follower has ALREADY passed that check and is parked at the
		// tail when the basis moves under it. A backend that reads the generation
		// once at entry and then trusts it for the life of the iterator passes
		// the cold case and silently streams the replacement log's records here.
		//
		// Raised in review on #869 against redisstore, which did exactly that.
		// memstore and jsonlstore both re-checked per cycle already, each with a
		// comment explaining why — a divergence on a documented contract point
		// that no backend's own tests could catch, which is precisely what a
		// shared suite is for.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-reset-mid-follow"
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "old basis"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		// One ordered channel rather than separate record and error channels: the
		// assertion IS which of the two arrives, and a goroutine that closes its
		// record channel on the way out of an error makes both ready at once, so
		// a select over two channels would pick between them at random.
		type outcome struct {
			rec  port.LogRecord
			err  error
			done bool
		}
		followCtx, cancel := context.WithTimeout(ctx, followTimeout)
		defer cancel()
		outcomes := make(chan outcome, 8)
		go func() {
			for rec, err := range log.ReadAfter(followCtx, id, "", port.ReadOptions{Follow: true}) {
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
			case <-time.After(followTimeout):
				t.Fatalf("timed out waiting for %s", what)
				return outcome{}
			}
		}

		// Drain the pre-reset record first, so the follower is provably parked at
		// the tail — past its entry-point cursor check — before the basis moves.
		// Without this the backend could still be inside its initial drain and
		// the test would prove nothing about the follow path.
		switch first := next("the record present before the reset"); {
		case first.err != nil:
			t.Fatalf("follow yielded error %v before the reset", first.err)
		case first.done:
			t.Fatal("follow ended before the reset")
		case record{first.rec}.Text() != "old basis":
			t.Fatalf("first followed record = %q, want %q", record{first.rec}.Text(), "old basis")
		}

		s.Reset(t, log, id)
		if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "new basis"}); err != nil {
			t.Fatalf("AppendEvent after reset: %v", err)
		}

		switch got := next("the follower's reaction to its log being reset"); {
		case got.err != nil:
			if !errors.Is(got.err, port.ErrCursorExpired) {
				t.Errorf("a follower whose log was reset got error %v, want ErrCursorExpired; anything else leaves a client unable to tell a moved basis from an infrastructure fault it should retry", got.err)
			}
		case got.done:
			t.Error("a follower whose log was reset ended cleanly, which is indistinguishable from the log simply having no more records; it must yield ErrCursorExpired")
		default:
			t.Errorf("a follower whose log was reset was handed record %q from the REPLACEMENT log instead of ErrCursorExpired — this is the silent-wrong-data case generations exist to prevent", record{got.rec}.Text())
		}
	})

	t.Run("a malformed cursor is rejected, never coerced", func(t *testing.T) {
		// AC6.4. Every one of these could plausibly be "helpfully" treated as the
		// beginning of the log. That help is what loses data: a client whose
		// cursor got corrupted in transit would silently receive a full replay it
		// believes is an increment.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-malformed"
		issued, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		// N1 (review, #868): pure garbage must be MALFORMED specifically, not
		// "malformed or expired". The two sentinels are a deliberate distinction
		// the port keeps — a client may restart an expired cursor from the
		// beginning, whereas a malformed one means a bug or tampering and
		// restarting silently would hide it — so accepting either here would let
		// a backend collapse them and still pass. Only a position the backend
		// cannot align stays ambiguous: whether an unresolvable position reads as
		// corruption or as a moved basis is genuinely backend-shaped.
		strict := map[string]port.Cursor{
			"not base64":         "!!!not-base64!!!",
			"base64 of non-JSON": port.Cursor(base64.RawURLEncoding.EncodeToString([]byte("hello"))),
			"unknown version":    tamper(t, issued, func(e *envelope) { e.V = "cur/999" }),
			"unknown field":      port.Cursor(base64.RawURLEncoding.EncodeToString([]byte(`{"v":"cur/1","s":"x","g":"x","p":"1","extra":true}`))),
			"foreign session":    tamper(t, issued, func(e *envelope) { e.S = "a-different-session" }),
		}
		ambiguous := map[string]port.Cursor{
			"unresolvable position": tamper(t, issued, func(e *envelope) {
				e.P = "not-a-position-any-backend-can-resolve"
			}),
		}

		reject := func(t *testing.T, name string, cur port.Cursor) error {
			t.Helper()
			var gotErr error
			var yielded int
			for rec, err := range log.ReadAfter(ctx, id, cur, port.ReadOptions{}) {
				if err != nil {
					gotErr = err
					break
				}
				yielded++
				t.Errorf("yielded record %+v", rec)
			}
			if gotErr == nil {
				t.Fatalf("ReadAfter(%s) yielded %d records and no error; a cursor that cannot be honoured EXACTLY must fail", name, yielded)
			}
			return gotErr
		}

		for name, cur := range strict {
			t.Run(name, func(t *testing.T) {
				if err := reject(t, name, cur); !errors.Is(err, port.ErrCursorMalformed) {
					t.Errorf("ReadAfter(%s) error = %v, want ErrCursorMalformed exactly", name, err)
				}
			})
		}
		for name, cur := range ambiguous {
			t.Run(name, func(t *testing.T) {
				if err := reject(t, name, cur); !errors.Is(err, port.ErrCursorMalformed) && !errors.Is(err, port.ErrCursorExpired) {
					t.Errorf("ReadAfter(%s) error = %v, want ErrCursorMalformed or ErrCursorExpired", name, err)
				}
			})
		}
	})

	t.Run("concurrent appends and a follower are safe", func(t *testing.T) {
		// The port's concurrency contract plus the one thing follow adds: a
		// reader parked at the tail while the same id is being appended to. Run
		// under -race.
		log := s.New(t)
		const id session.SessionID = "conf-cursor-concurrent"
		const total = 50

		followCtx, cancel := context.WithTimeout(ctx, followTimeout)
		defer cancel()
		done := make(chan []port.LogRecord, 1)
		go func() {
			var seen []port.LogRecord
			for rec, err := range log.ReadAfter(followCtx, id, "", port.ReadOptions{Follow: true}) {
				if err != nil {
					break
				}
				seen = append(seen, rec)
				if len(seen) == total {
					break
				}
			}
			done <- seen
		}()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range total {
				if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
					t.Errorf("concurrent AppendEvent %d: %v", i, err)
					return
				}
			}
		}()
		wg.Wait()

		select {
		case seen := <-done:
			if len(seen) != total {
				t.Fatalf("follower saw %d records, want %d — a follow must not drop under load", len(seen), total)
			}
			for i, rec := range seen {
				if rec.Event.Seq != int64(i) {
					t.Fatalf("followed record[%d].Seq = %d, want %d — follow must preserve append order", i, rec.Event.Seq, i)
				}
			}
		case <-time.After(followTimeout):
			t.Fatal("timed out waiting for the follower to observe every append")
		}
	})

	if !s.SkipCrossProcess {
		t.Run("a second reader over the same durable state observes appends", func(t *testing.T) {
			// AC6.5. The obligation that makes multi-replica attachment possible:
			// the log is the shared medium, so a reader constructed independently
			// of the writer must see what the writer durably appended. An
			// in-process channel registry — which is what mecatl has today —
			// passes every other subtest here and fails this one.
			writer, reader := s.NewPair(t)
			const id session.SessionID = "conf-cursor-cross-process"
			if _, err := writer.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "written elsewhere"}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
			got := collectRecords(t, reader, id, "", port.ReadOptions{})
			if len(got) != 1 {
				t.Fatalf("an independently-constructed reader saw %d records, want 1", len(got))
			}
			if got[0].Text() != "written elsewhere" {
				t.Errorf("cross-reader record = %q, want %q", got[0].Text(), "written elsewhere")
			}

			followCtx, cancel := context.WithTimeout(ctx, followTimeout)
			defer cancel()
			recs := make(chan port.LogRecord, 4)
			go func() {
				defer close(recs)
				for rec, err := range reader.ReadAfter(followCtx, id, got[0].Cursor, port.ReadOptions{Follow: true}) {
					if err != nil {
						return
					}
					recs <- rec
				}
			}()
			if _, err := writer.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "live from elsewhere"}); err != nil {
				t.Fatalf("AppendEvent while following: %v", err)
			}
			live := mustRecv(t, recs, "an append made through a different handle")
			if live.Text() != "live from elsewhere" {
				t.Errorf("followed record = %q, want %q", live.Text(), "live from elsewhere")
			}
		})
	}
}

// envelope mirrors the cursor's encoded shape so the suite can build a cursor
// that is structurally valid but semantically wrong.
//
// Duplicating the field names here is deliberate: it makes the opacity claim
// testable from OUTSIDE port, and if the encoding ever changes shape, this
// suite's tamper cases stop constructing what they claim to construct and say so
// rather than passing vacuously.
type envelope struct {
	V string `json:"v"`
	S string `json:"s"`
	G string `json:"g"`
	P string `json:"p"`
}

// tamper decodes a real cursor, applies mutate, and re-encodes it — the
// determined client ADR 0250 says the encoding must survive.
func tamper(t *testing.T, cur port.Cursor, mutate func(*envelope)) port.Cursor {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(string(cur))
	if err != nil {
		t.Fatalf("a real cursor is not base64url (%q): %v", cur, err)
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("a real cursor does not decode to the documented envelope (%s): %v", raw, err)
	}
	mutate(&e)
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("re-encode tampered cursor: %v", err)
	}
	return port.Cursor(base64.RawURLEncoding.EncodeToString(out))
}

// collectRecords drains a ReadAfter into a slice, failing on the first error.
func collectRecords(t *testing.T, log port.CursorEventLog, id session.SessionID, after port.Cursor, opts port.ReadOptions) []record {
	t.Helper()
	var out []record
	for rec, err := range log.ReadAfter(context.Background(), id, after, opts) {
		if err != nil {
			t.Fatalf("ReadAfter(%q) yielded error: %v", id, err)
		}
		if rec.Cursor == "" {
			t.Fatalf("ReadAfter(%q) yielded a record with the ZERO cursor; it could not be resumed from", id)
		}
		out = append(out, record{rec})
	}
	return out
}

// record adds a Text accessor so subtests read as prose rather than as nested
// field access.
type record struct{ port.LogRecord }

func (r record) Text() string { return r.Event.Text }

// mustRecv waits for one record, failing with what was being waited on.
func mustRecv(t *testing.T, ch <-chan port.LogRecord, what string) record {
	t.Helper()
	select {
	case rec, ok := <-ch:
		if !ok {
			t.Fatalf("the record stream closed while waiting for %s", what)
		}
		return record{rec}
	case <-time.After(followTimeout):
		t.Fatalf("timed out waiting for %s", what)
		return record{}
	}
}

// waitClosed asserts a channel closes, draining anything still in flight.
func waitClosed(t *testing.T, ch <-chan port.LogRecord, what string) {
	t.Helper()
	deadline := time.After(followTimeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s to close", what)
			return
		}
	}
}
