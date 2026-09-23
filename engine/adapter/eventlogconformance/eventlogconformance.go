// Package eventlogconformance provides a shared conformance test suite for the
// port.EventLog interface. Adapters (the local JSONL replay store, the in-memory
// sibling, remote drivers, ...) call Run with a factory that constructs a fresh
// event log, and the suite exercises only the port.EventLog interface against
// events built through the session package's public value types.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests,
// the conventional Go pattern for shared conformance suites (cf. testing/fstest
// and the sibling storeconformance/memconformance packages).
//
// The suite pins the CONTRACT, not the implementation: on-disk layout, the
// envelope format tag, and the encoding are adapter-internal and deliberately
// NOT asserted here. It is the SAME suite the local jsonlstore and the gRPC
// driver client (over bufconn) both pass — the dual-path contract-unification:
// the Go port is the contract, the wire is one adapter.
package eventlogconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Run executes the shared EventLog conformance table against the log produced
// by newLog. newLog must return a fresh, isolated log each call.
func Run(t *testing.T, newLog func(t *testing.T) port.EventLog) {
	t.Helper()
	ctx := context.Background()

	t.Run("append then read returns events in append order", func(t *testing.T) {
		log := newLog(t)
		const id session.SessionID = "conf-eventlog-order"
		want := representativeEvents()
		for i, ev := range want {
			if err := log.Append(ctx, id, ev); err != nil {
				t.Fatalf("Append #%d: %v", i, err)
			}
		}
		got := collect(t, log, id)
		if len(got) != len(want) {
			t.Fatalf("Read returned %d events, want %d", len(got), len(want))
		}
		for i := range want {
			assertEventEqual(t, i, got[i], want[i])
		}
	})

	t.Run("read of an unknown session is an empty sequence", func(t *testing.T) {
		// A MISS is data, not an error: the iterator yields nothing and never an
		// error item. Kills a driver that maps "never appended" to NOT_FOUND.
		log := newLog(t)
		var ranged bool
		for ev, err := range log.Read(ctx, "conf-eventlog-never-appended") {
			ranged = true
			t.Fatalf("Read(unknown id) yielded (%+v, %v), want an empty sequence", ev, err)
		}
		if ranged {
			t.Fatal("Read(unknown id) yielded at least once, want an empty sequence")
		}
	})

	t.Run("distinct sessions keep distinct logs", func(t *testing.T) {
		// Kills a single-slot log: events must be keyed by session id, not held
		// as one global stream.
		log := newLog(t)
		mustAppend(t, log, "conf-eventlog-a", session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "for a"})
		mustAppend(t, log, "conf-eventlog-b", session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "for b"})
		mustAppend(t, log, "conf-eventlog-a", session.Event{Type: session.EvMessageDelta, Seq: 2, Text: "also a"})

		a := collect(t, log, "conf-eventlog-a")
		b := collect(t, log, "conf-eventlog-b")
		if len(a) != 2 || a[0].Text != "for a" || a[1].Text != "also a" {
			t.Errorf("session a log = %+v, want its own two events in order", a)
		}
		if len(b) != 1 || b[0].Text != "for b" {
			t.Errorf("session b log = %+v, want its own single event", b)
		}
	})

	t.Run("read does not reorder by Seq", func(t *testing.T) {
		// The contract is raw APPEND order, NOT Seq order. Append events whose
		// Seq is DESCENDING; Read must return them in the order appended.
		log := newLog(t)
		const id session.SessionID = "conf-eventlog-append-order"
		seqs := []int64{30, 10, 20}
		for _, s := range seqs {
			mustAppend(t, log, id, session.Event{Type: session.EvMessageDelta, Seq: s})
		}
		got := collect(t, log, id)
		if len(got) != len(seqs) {
			t.Fatalf("Read returned %d events, want %d", len(got), len(seqs))
		}
		for i, s := range seqs {
			if got[i].Seq != s {
				t.Errorf("event[%d].Seq = %d, want %d (Read must preserve APPEND order, not sort by Seq)", i, got[i].Seq, s)
			}
		}
	})

	t.Run("large event round-trips", func(t *testing.T) {
		// A single multi-kilobyte event body must round-trip — a wire-backed log
		// that truncates or mis-frames one message fails here.
		log := newLog(t)
		const id session.SessionID = "conf-eventlog-large"
		big := strings.Repeat("reasoning summary line, non-degenerate. ", 4096) // ~160 KiB
		mustAppend(t, log, id, session.Event{Type: session.EvReasoningDelta, Seq: 1, Text: big})
		got := collect(t, log, id)
		if len(got) != 1 {
			t.Fatalf("Read returned %d events, want 1", len(got))
		}
		if got[0].Text != big {
			t.Errorf("large event Text is %d bytes and/or differs from the appended %d bytes", len(got[0].Text), len(big))
		}
	})

	t.Run("cumulative log crosses the framing boundary", func(t *testing.T) {
		// The size-contract subtest, the EventLog analogue of storeconformance's
		// large-snapshot: a server-STREAMING Read frames each event as its own
		// message, so a long log must chunk across messages rather than
		// accumulate into one oversized response. Append enough non-degenerate
		// events that the CUMULATIVE bytes cross a realistic single-message
		// boundary (gRPC's 4 MiB default), then assert every one reads back in
		// order. A unary-Read implementation, or one that buffers the whole log
		// into one message, would hit the cap and fail here; a correctly-streamed
		// (or local-file) log passes.
		log := newLog(t)
		const id session.SessionID = "conf-eventlog-cumulative"
		const (
			perEvent = 64 << 10 // 64 KiB body each
			count    = 80       // ~5 MiB cumulative, past the 4 MiB default cap
		)
		body := strings.Repeat("x", perEvent)
		for i := 0; i < count; i++ {
			mustAppend(t, log, id, session.Event{
				Type: session.EvReasoningDelta,
				Seq:  int64(i),
				Text: fmt.Sprintf("%05d:%s", i, body), // unique prefix so order is verifiable
			})
		}
		got := collect(t, log, id)
		if len(got) != count {
			t.Fatalf("Read returned %d events, want %d (a unary/one-message Read would hit the framing cap)", len(got), count)
		}
		for i := 0; i < count; i++ {
			if want := fmt.Sprintf("%05d:", i); !strings.HasPrefix(got[i].Text, want) {
				t.Fatalf("event[%d] prefix = %q, want %q (order/identity preserved across the framing boundary)", i, got[i].Text[:min(6, len(got[i].Text))], want)
			}
		}
	})

	t.Run("read early-break releases resources", func(t *testing.T) {
		// The iter.Seq2 early-exit obligation: breaking out of the range before
		// the stream/file is drained must release the underlying resource (the
		// jsonlstore file handle via defer Close, the grpcdriver stream via the
		// child-context cancel). Under the adapter's goleak gate (grpcdriver's
		// TestMain) a leaked stream goroutine fails the package; here we assert
		// the break itself yields no error and no panic. We read only the FIRST
		// of several appended events, then break.
		log := newLog(t)
		const id session.SessionID = "conf-eventlog-early-break"
		for i := 0; i < 5; i++ {
			mustAppend(t, log, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)})
		}
		var seen int
		for ev, err := range log.Read(context.Background(), id) {
			if err != nil {
				t.Fatalf("Read yielded error before the break: %v", err)
			}
			seen++
			if ev.Seq != 0 {
				t.Fatalf("first yielded event Seq = %d, want 0", ev.Seq)
			}
			break // exit early — the iterator must release its resource here
		}
		if seen != 1 {
			t.Fatalf("ranged %d times before break, want exactly 1", seen)
		}
		// A subsequent full Read must still succeed (the early break must not have
		// corrupted or left the log in a half-open state).
		if got := collect(t, log, id); len(got) != 5 {
			t.Fatalf("Read after an early break returned %d events, want 5", len(got))
		}
	})

	t.Run("concurrent append and read are safe", func(t *testing.T) {
		// The port's CONCURRENCY contract: implementations MUST be safe for
		// concurrent Append and Read across session ids (the server shares ONE
		// EventLog across all relay goroutines). Run under -race: concurrent
		// Appends across DISTINCT ids, plus Append-while-Read, must not race or
		// drop. Per-id append order is well-defined only for a SINGLE session's
		// serialized appends, so each goroutine owns its own id.
		log := newLog(t)
		const (
			sessions       = 8
			perSession     = 25
			readerInterval = 5
		)
		var wg sync.WaitGroup
		for s := 0; s < sessions; s++ {
			wg.Add(1)
			go func(s int) {
				defer wg.Done()
				id := session.SessionID(fmt.Sprintf("conf-eventlog-conc-%d", s))
				for i := 0; i < perSession; i++ {
					if err := log.Append(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
						t.Errorf("concurrent Append(%q, %d): %v", id, i, err)
						return
					}
					// Interleave a Read on the SAME id while appends continue
					// (Append-while-Read): drain it fully so the resource closes.
					if i%readerInterval == 0 {
						for _, rerr := range log.Read(ctx, id) {
							if rerr != nil {
								t.Errorf("concurrent Read(%q): %v", id, rerr)
								break
							}
						}
					}
				}
			}(s)
		}
		wg.Wait()
		// After the dust settles each id must hold exactly its own perSession
		// appends, in order — no cross-id bleed, no lost write.
		for s := 0; s < sessions; s++ {
			id := session.SessionID(fmt.Sprintf("conf-eventlog-conc-%d", s))
			got := collect(t, log, id)
			if len(got) != perSession {
				t.Errorf("session %q has %d events after concurrent run, want %d", id, len(got), perSession)
				continue
			}
			for i := range got {
				if got[i].Seq != int64(i) {
					t.Errorf("session %q event[%d].Seq = %d, want %d (append order lost under concurrency)", id, i, got[i].Seq, i)
					break
				}
			}
		}
	})
}

// representativeEvents builds a small timeline exercising the payload-bearing
// event shapes the log must round-trip verbatim: a reasoning delta, a tool-call
// card, a resolved approval verdict, and a terminal result. Every shape carries
// a non-empty payload so a marshal/unmarshal asymmetry surfaces.
func representativeEvents() []session.Event {
	call := session.NewToolCall("call-1", "Read", json.RawMessage(`{"path":"a.txt"}`))
	return []session.Event{
		{Type: session.EvReasoningDelta, Seq: 1, Turn: 0, Text: "let me read the file"},
		{Type: session.EvToolCall, Seq: 2, Turn: 0, ToolCall: &call},
		{Type: session.EvApproval, Seq: 3, Turn: 0, Approval: &session.ApprovalPayload{
			AskID:       "conf-eventlog-order:0:call-1:r1",
			Verdict:     session.VerdictStringAllowAlways,
			Tool:        "Read",
			Call:        "call-1",
			AllowAlways: true,
		}},
		{Type: session.EvResult, Seq: 4, Turn: 0, Result: &session.ResultPayload{
			Stop:  session.StopEndTurn,
			Usage: session.Usage{InputTokens: 100, OutputTokens: 20},
		}},
	}
}

// collect drains Read into a slice, failing the test on the first yielded error
// (the port contract: Read yields no further events after an error).
func collect(t *testing.T, log port.EventLog, id session.SessionID) []session.Event {
	t.Helper()
	var out []session.Event
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("Read(%q) yielded error: %v", id, err)
		}
		out = append(out, ev)
	}
	return out
}

// mustAppend appends ev under id, failing the test on error.
func mustAppend(t *testing.T, log port.EventLog, id session.SessionID, ev session.Event) {
	t.Helper()
	if err := log.Append(context.Background(), id, ev); err != nil {
		t.Fatalf("Append(%q): %v", id, err)
	}
}

// assertEventEqual compares a loaded event against the appended one across the
// fields the log contract must preserve (type/seq/turn/text plus the
// payload pointers a representative timeline carries).
func assertEventEqual(t *testing.T, i int, got, want session.Event) {
	t.Helper()
	if got.Type != want.Type {
		t.Errorf("event[%d].Type = %q want %q", i, got.Type, want.Type)
	}
	if got.Seq != want.Seq {
		t.Errorf("event[%d].Seq = %d want %d", i, got.Seq, want.Seq)
	}
	if got.Turn != want.Turn {
		t.Errorf("event[%d].Turn = %d want %d", i, got.Turn, want.Turn)
	}
	if got.Text != want.Text {
		t.Errorf("event[%d].Text = %q want %q", i, got.Text, want.Text)
	}
	// Compare payloads via JSON so the comparison tracks whatever fields the
	// session.Event encoding carries, without re-asserting each one here.
	if gj, wj := mustJSON(t, got), mustJSON(t, want); gj != wj {
		t.Errorf("event[%d] round-trip differs:\n got %s\nwant %s", i, gj, wj)
	}
}

func mustJSON(t *testing.T, ev session.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event for comparison: %v", err)
	}
	return string(b)
}
