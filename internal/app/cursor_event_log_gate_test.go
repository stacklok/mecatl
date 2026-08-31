package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// This file is Scenario 6's cross-backend gate: the acceptance criteria that are
// claims about the SET of backends rather than about any one of them, sited here
// for the same reason internal/app owns the ADR 0027 Phase 3 gate — the
// composition layer is the only one permitted to import every adapter, and the
// backends span two Go modules (memstore in engine/, the rest in the root).
//
// The shared suite in engine/adapter/eventlogconformance proves each backend
// individually, invoked from that backend's own package. These tests do NOT
// restate it. They assert the things a per-backend run structurally cannot: that
// the additive port left the legacy contract intact, that a gap is an envelope
// rather than an event, and that the durable backends the ADR names are actually
// present in the set rather than quietly absent.

// cursorBackend is one row of the gate's backend table.
//
// 6b appends Redis and the gRPC driver here; nothing else in this file changes,
// which is the point of the table. durable marks a backend whose state outlives
// the process and is therefore bound by the cross-process obligation — memstore
// is legitimately not one, and saying so in data beats a per-test skip.
type cursorBackend struct {
	name    string
	durable bool
	// new returns a fresh, isolated log.
	new func(t *testing.T) port.CursorEventLog
	// newPair returns two INDEPENDENTLY CONSTRUCTED handles over the same
	// durable state. Nil for a non-durable backend.
	newPair func(t *testing.T) (port.CursorEventLog, port.CursorEventLog)
}

func cursorBackends(t *testing.T) []cursorBackend {
	t.Helper()
	newJSONL := func(t *testing.T, dir string) port.CursorEventLog {
		t.Helper()
		st, err := jsonlstore.New(dir)
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	}
	return []cursorBackend{
		{
			name: "memstore",
			new:  func(*testing.T) port.CursorEventLog { return memstore.NewEventLog() },
		},
		{
			name:    "jsonlstore",
			durable: true,
			new:     func(t *testing.T) port.CursorEventLog { return newJSONL(t, t.TempDir()) },
			newPair: func(t *testing.T) (port.CursorEventLog, port.CursorEventLog) {
				dir := t.TempDir()
				return newJSONL(t, dir), newJSONL(t, dir)
			},
		},
	}
}

// requiredDurableCursorBackends is the set ADR 0250 obliges to prove durable,
// cross-process follow. It is asserted as a SET rather than left implicit so
// that dropping a backend from the table fails loudly here instead of silently
// reducing what AC6.5 covers.
//
// 6b adds "redisstore" — the entry that makes multi-replica attachment real.
var requiredDurableCursorBackends = []string{"jsonlstore"}

// TestADR_0250_EventLogContractUnbroken is AC6.2: existing port.EventLog
// behaviour is unchanged for every backend — the additive port breaks no
// consumer.
//
// The oracle that matters is not "EventLog still compiles". It is that a log
// written through the NEW cursor path is read by the OLD Read exactly as it
// would have been before cursors existed: same events, same order, nothing
// extra. Cursors introduce two record shapes the legacy reader has never seen (a
// gap marker, and on some backends a generation header), and every existing
// consumer of Read — most importantly the event-sourced fold of ADR 0038 — would
// be corrupted by either one leaking through.
func TestADR_0250_EventLogContractUnbroken(t *testing.T) {
	ctx := context.Background()
	for _, be := range cursorBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			log := be.new(t)

			// Compile-time and runtime: a cursor log IS an event log, so every
			// existing consumer keeps working by construction.
			var legacy port.EventLog = log
			if legacy == nil {
				t.Fatal("a CursorEventLog is not usable as a port.EventLog")
			}

			const id session.SessionID = "ac62-unbroken"
			want := []string{"alpha", "beta", "gamma"}
			for i, text := range want {
				if _, err := log.AppendEvent(ctx, id, session.Event{
					Type: session.EvMessageDelta, Seq: int64(i), Text: text,
				}); err != nil {
					t.Fatalf("AppendEvent(%q): %v", text, err)
				}
				// A gap between every pair of events: the adversarial layout for
				// a legacy reader, and the one a naive implementation surfaces.
				if _, err := log.AppendGap(ctx, id, "injected between events"); err != nil {
					t.Fatalf("AppendGap: %v", err)
				}
			}

			var got []string
			for ev, err := range legacy.Read(ctx, id) {
				if err != nil {
					t.Fatalf("legacy Read: %v", err)
				}
				if ev.Type != session.EvMessageDelta {
					t.Errorf("legacy Read surfaced a %q event; the gap envelope has leaked into the event stream", ev.Type)
				}
				got = append(got, ev.Text)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("legacy Read = %v, want %v (events only, in append order)", got, want)
			}

			// Absence stays data on the legacy path too: a session that was
			// never appended to is an empty sequence, not an error. A backend
			// that mints a generation header eagerly could regress this into
			// "one undecodable record".
			for ev, err := range legacy.Read(ctx, "ac62-never-written") {
				t.Fatalf("legacy Read of an unknown session yielded (%+v, %v), want an empty sequence", ev, err)
			}
		})
	}
}

// TestADR_0250_GapMarkerIsEnvelopeNotEvent is AC6.7: a gap marker occupies an
// append position and advances cursors, is surfaced by ReadAfter, and is skipped
// by the legacy EventLog.Read.
//
// Decision 5 of ADR 0250 turns on a gap being a fact about DELIVERY rather than
// something that happened in the run. That distinction is only real if the gap
// carries no event: a gap whose Event field were populated would be an event in
// all but name, and the first consumer to fold it would put it in a transcript.
func TestADR_0250_GapMarkerIsEnvelopeNotEvent(t *testing.T) {
	ctx := context.Background()
	for _, be := range cursorBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			log := be.new(t)
			const id session.SessionID = "ac67-gap"

			first, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 0, Text: "before"})
			if err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
			gapCur, err := log.AppendGap(ctx, id, "backend rejected the record")
			if err != nil {
				t.Fatalf("AppendGap: %v", err)
			}
			if gapCur == "" || gapCur == first {
				t.Fatalf("AppendGap returned cursor %q (first was %q); a gap must occupy its OWN position or cursors skip past it", gapCur, first)
			}
			if _, err := log.AppendEvent(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "after"}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}

			recs := readAll(t, log, id, "", port.ReadOptions{})
			if len(recs) != 3 {
				t.Fatalf("ReadAfter surfaced %d records, want 3 (event, gap, event)", len(recs))
			}
			gap := recs[1]
			if gap.Kind != port.LogRecordGap {
				t.Fatalf("record[1].Kind = %q, want %q", gap.Kind, port.LogRecordGap)
			}
			// The load-bearing half: a gap is an ENVELOPE, so it carries no
			// event at all.
			if !reflect.DeepEqual(gap.Event, session.Event{}) {
				t.Errorf("the gap record carries event %+v; a gap must decode to the ZERO event, not an event-shaped record", gap.Event)
			}
			if gap.GapReason == "" {
				t.Error("the gap record carries no reason; an operator has nothing to diagnose from")
			}
			for _, ev := range []port.LogRecord{recs[0], recs[2]} {
				if ev.Kind != port.LogRecordEvent {
					t.Errorf("an ordinary record has Kind %q, want %q", ev.Kind, port.LogRecordEvent)
				}
				if ev.GapReason != "" {
					t.Errorf("an ordinary record carries GapReason %q", ev.GapReason)
				}
			}

			// Cursors advance THROUGH the gap: resuming from its own cursor
			// lands on the next record, never re-delivering the gap.
			after := readAll(t, log, id, gapCur, port.ReadOptions{})
			if len(after) != 1 || after[0].Event.Text != "after" {
				t.Fatalf("ReadAfter(gap cursor) surfaced %d records, want exactly the one event following the gap", len(after))
			}

			// And it is invisible to the shipped port.
			var texts []string
			for ev, err := range log.Read(ctx, id) {
				if err != nil {
					t.Fatalf("legacy Read: %v", err)
				}
				texts = append(texts, ev.Text)
			}
			if !reflect.DeepEqual(texts, []string{"before", "after"}) {
				t.Errorf("legacy Read = %v, want [before after] — it must SKIP the gap", texts)
			}
		})
	}
}

// TestADR_0250_CrossProcessWatchObservesAppends is AC6.5: a watcher holding one
// handle observes durable appends made through another, for every backend whose
// state outlives the process.
//
// Two independently-constructed handles over the same durable state is the
// faithful in-process stand-in for two replicas, and not a weaker one: the
// backends bound by this obligation coordinate exclusively through the durable
// medium (jsonlstore via cross-process flock over the file; Redis via the
// server), so the handles share no Go state for the test to accidentally lean
// on. An in-process channel registry — which is what mecatl's live Subscribe is
// today — passes every other cursor subtest and fails this one.
func TestADR_0250_CrossProcessWatchObservesAppends(t *testing.T) {
	ctx := context.Background()

	covered := map[string]bool{}
	for _, be := range cursorBackends(t) {
		if !be.durable {
			continue
		}
		covered[be.name] = true
		t.Run(be.name, func(t *testing.T) {
			if be.newPair == nil {
				t.Fatalf("backend %q is marked durable but supplies no independent second handle", be.name)
			}
			writer, reader := be.newPair(t)
			const id session.SessionID = "ac65-cross-handle"

			// Replay: what the writer durably appended before the reader existed.
			if _, err := writer.AppendEvent(ctx, id, session.Event{
				Type: session.EvMessageDelta, Seq: 0, Text: "written elsewhere",
			}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
			replayed := readAll(t, reader, id, "", port.ReadOptions{})
			if len(replayed) != 1 || replayed[0].Event.Text != "written elsewhere" {
				t.Fatalf("the second handle replayed %d records, want the writer's single durable append", len(replayed))
			}

			// Follow: what the writer appends while the reader is parked at the
			// tail. This is the half that makes reconnect-to-another-replica work.
			followCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			got := make(chan port.LogRecord, 1)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for rec, err := range reader.ReadAfter(followCtx, id, replayed[0].Cursor, port.ReadOptions{Follow: true}) {
					if err != nil {
						return
					}
					select {
					case got <- rec:
					default:
					}
					return
				}
			}()
			// Joined before the test returns: the goleak gate over this package
			// treats a lingering follower as the leak it would be in production.
			defer func() {
				cancel()
				wg.Wait()
			}()

			if _, err := writer.AppendEvent(ctx, id, session.Event{
				Type: session.EvMessageDelta, Seq: 1, Text: "live from elsewhere",
			}); err != nil {
				t.Fatalf("AppendEvent while following: %v", err)
			}
			select {
			case rec := <-got:
				if rec.Event.Text != "live from elsewhere" {
					t.Errorf("followed record = %q, want %q", rec.Event.Text, "live from elsewhere")
				}
			case <-followCtx.Done():
				t.Fatal("the second handle never observed an append made through the first; cross-process follow is not durable")
			}
		})
	}

	// The set assertion: a durable backend the ADR names must be in the table,
	// not quietly missing. Without this, deleting a row silently narrows AC6.5
	// to whatever happens to remain.
	for _, name := range requiredDurableCursorBackends {
		if !covered[name] {
			t.Errorf("AC6.5 requires durable cross-handle proof for %q, but no such backend is in the gate's table", name)
		}
	}
}

// readAll drains a bounded ReadAfter, failing on the first error.
func readAll(t *testing.T, log port.CursorEventLog, id session.SessionID, after port.Cursor, opts port.ReadOptions) []port.LogRecord {
	t.Helper()
	var out []port.LogRecord
	for rec, err := range log.ReadAfter(context.Background(), id, after, opts) {
		if err != nil {
			if errors.Is(err, port.ErrCursorMalformed) || errors.Is(err, port.ErrCursorExpired) {
				t.Fatalf("ReadAfter(after=%q) rejected the cursor: %v", after, err)
			}
			t.Fatalf("ReadAfter(after=%q): %v", after, err)
		}
		out = append(out, rec)
	}
	return out
}
