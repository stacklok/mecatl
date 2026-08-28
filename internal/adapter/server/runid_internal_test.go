package server

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestADR_0245_RunlessEventSetIsClosed is AC4.6.
//
// An empty Event.RunID is a MEANING — "session-scoped, not run-scoped" — and the
// set of event types allowed to carry one is closed. This test is the gate that
// makes adding to it a deliberate act rather than a silent widening.
//
// The consequence a reviewer must weigh before adding an entry: an ergonomic
// attachment filters to a single run and so will NEVER deliver the new type,
// while a session activity stream will. That asymmetry is why the two are
// separate operations, and it is invisible unless someone states it.
func TestADR_0245_RunlessEventSetIsClosed(t *testing.T) {
	// Exactly the scheduler lifecycle triple, which composition emits outside any
	// agent loop. If this fails, either a type was added without review or one was
	// removed and the map is stale.
	want := map[session.EventType]struct{}{
		session.EvScheduleFired:   {},
		session.EvScheduleSkipped: {},
		session.EvScheduleFailed:  {},
	}
	for typ := range want {
		if !allowsEmptyRunID(typ) {
			t.Errorf("%q is expected to be run-less but the set does not contain it", typ)
		}
	}
	for typ := range runlessEventTypes {
		if _, ok := want[typ]; !ok {
			t.Errorf("%q was added to the run-less set without updating this gate.\n"+
				"Before allowing it: an attachment filters to ONE run, so it will never deliver %q; "+
				"only a session activity stream will. Confirm that is intended, then add it here.", typ, typ)
		}
	}
	if len(runlessEventTypes) != len(want) {
		t.Errorf("run-less set has %d entries, gate expects %d", len(runlessEventTypes), len(want))
	}

	// Every member must genuinely be a schedule lifecycle event — the stated
	// justification is that composition emits them outside a loop. A non-schedule
	// type slipping in would mean the rationale no longer matches the contents.
	for typ := range runlessEventTypes {
		if !strings.HasPrefix(string(typ), "schedule.") {
			t.Errorf("%q is in the run-less set but is not a schedule.* event; the set's rationale "+
				"is that the scheduler emits outside any run, so a non-schedule member needs its own", typ)
		}
	}

	// And a run-scoped type must NOT be exempt, or the gate proves nothing.
	for _, typ := range []session.EventType{
		session.EvResult, session.EvToolCall, session.EvToolResult, session.EvPermissionAsk, session.EvTurnEnd,
	} {
		if allowsEmptyRunID(typ) {
			t.Errorf("%q is run-scoped and must never be allowed an empty run id", typ)
		}
	}
}

// TestADR_0245_MintedRunIDsAreOpaqueUniqueAndColonFree pins the properties the
// askID grammar and the CWE-863 replay guard depend on.
func TestADR_0245_MintedRunIDsAreOpaqueUniqueAndColonFree(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for range n {
		id := newRunID()
		if strings.Contains(id, ":") {
			t.Fatalf("minted run id %q contains a colon; it feeds the askID grammar", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("minted a duplicate run id %q; uniqueness backs the askID replay guard", id)
		}
		seen[id] = struct{}{}
		if !strings.HasPrefix(id, "run_") {
			t.Fatalf("minted run id %q lost its prefix", id)
		}
		// Opaque: nothing but the prefix and the encoded entropy. In particular it
		// must not embed a session id, a timestamp, or a counter a client could
		// come to parse and depend on.
		body := strings.TrimPrefix(id, "run_")
		for _, r := range body {
			isLower := r >= 'a' && r <= 'z'
			isDigit := r >= '2' && r <= '7' // base32 digit alphabet
			if !isLower && !isDigit {
				t.Fatalf("run id body %q contains %q, outside the lowercase base32 alphabet", body, r)
			}
		}
	}
}
