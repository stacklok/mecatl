package redisstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// newTestStore stands up an in-process miniredis and a redisstore wired to it,
// returning the store as a port.SessionStore (the conformance suite's factory
// type). The miniredis + client are torn down via t.Cleanup, so each subtest
// gets a fresh, isolated broker — the conformance suite's freshness contract.
func newTestStore(t *testing.T) port.SessionStore {
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

// TestRedisStoreConformance runs the shared SessionStore conformance table
// against the Redis-backed store over an in-process miniredis (fully offline).
func TestRedisStoreConformance(t *testing.T) {
	storeconformance.Run(t, newTestStore)
}

// TestRedisStorePrunableConformance runs the shared PrunableStore (retention
// seam: List/Delete) conformance table against the Redis-backed store.
func TestRedisStorePrunableConformance(t *testing.T) {
	storeconformance.RunPrunable(t, newTestStore)
}

func TestSessionContinuityUX_Scenario3_PagerConformance(t *testing.T) {
	storeconformance.RunMetadataPager(t, newTestStore)
}

// TestRedisStoreEventLogConformance runs the shared EventLog conformance table
// against the Redis-backed store (the same Store that doubles as SessionStore):
// this is the Redis-transport half of the dual-path contract, run against the
// SAME suite the local jsonlstore and the gRPC driver pass.
func TestRedisStoreEventLogConformance(t *testing.T) {
	eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
		st, _ := newTestStore(t).(port.EventLog)
		return st
	})
}

// TestRedisStoreDeleteRemovesEventsAndTools is the adapter-specific invariant
// beyond the shared suites: Delete must tear down the event-log and tool-call
// sidecars alongside the session snapshot, so a re-created session id starts
// with empty logs (no cross-generation bleed).
func TestRedisStoreDeleteRemovesEventsAndTools(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	log, ok := st.(port.EventLog)
	if !ok {
		t.Fatalf("store %T does not implement port.EventLog", st)
	}
	const id session.SessionID = "redis-del-sidecar"
	if err := st.Save(ctx, newTestSession(t, id)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := log.Append(ctx, id, mustEvent()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.(port.PrunableStore).Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// After Delete, the session is gone (not-found) and the event log is empty.
	if _, err := st.Load(ctx, id); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("Load(deleted) = %v, want ErrSessionNotFound", err)
	}
	ranged := false
	for ev, err := range log.Read(ctx, id) {
		ranged = true
		if err != nil {
			t.Fatalf("Read(deleted) yielded error: %v", err)
		}
		t.Fatalf("Read(deleted) yielded event %+v, want empty", ev)
	}
	if ranged {
		t.Fatal("Read(deleted) ranged, want an empty sequence")
	}
}

func newTestSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	return session.New(id, session.ModeAccept, "/work", session.Limits{}, time.Now())
}

func mustEvent() session.Event {
	return session.Event{Type: session.EvMessageDelta, Seq: 1, Text: "hi"}
}

// TestToolCallRecordIsDurableAndShaped guards the ToolCallRecorder port
// implementation: a record written via ToolCall must land on the Redis tools
// list and parse as the expected toolCallRecord shape. jsonlstore has an
// analogous test (TestToolCallLogParseable); this pins the Redis transport's
// parity. The port has no read API, so we inspect the miniredis state directly
// (the same pattern TestReadRejectsUnknownFormatTag uses).
func TestToolCallRecordIsDurableAndShaped(t *testing.T) {
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

	rec := port.ToolCallRecorder(st)
	const id session.SessionID = "redis-toolcall"
	call := session.NewToolCall("call-1", "Read", json.RawMessage(`{"path":"a.txt"}`))
	result := session.NewToolResult("call-1", "contents of a.txt")
	rec.ToolCall(id, call, result, 5*time.Millisecond, 12*time.Millisecond)

	// Inspect the Redis tools list directly (no port read API).
	vals, err := mr.List("mecatl:tools:" + string(id))
	if err != nil {
		t.Fatalf("miniredis List tools: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("tools list has %d records, want 1", len(vals))
	}
	var got struct {
		Type       string `json:"type"`
		Tool       string `json:"tool"`
		CallID     string `json:"call_id"`
		Result     string `json:"result"`
		IsError    bool   `json:"is_error"`
		TookMicros int64  `json:"took_micros"`
	}
	if err := json.Unmarshal([]byte(vals[0]), &got); err != nil {
		t.Fatalf("parse tool-call record: %v", err)
	}
	if got.Type != "tool_call" || got.Tool != "Read" || got.CallID != "call-1" ||
		got.Result != "contents of a.txt" || got.IsError || got.TookMicros != 12000 {
		t.Errorf("tool-call record = %+v, want {tool_call Read call-1 ... 12000us}", got)
	}
}

// TestSessionIDWithColonRoundTrips guards the key-construction safety for an
// id containing a colon — a plausible mecatl id (delegation-family ids,
// askIDs use the `:` separator). Redis keys are opaque byte strings, so no
// sanitization is needed (unlike jsonlstore's filename-safe safeName), but
// this pins that: (1) Save/Load round-trips, (2) List recovers the id
// verbatim, (3) the events/tools sidecars are keyed distinctly (Delete does
// not cross-contaminate), and (4) the `mecatl:session:` prefix trimming in
// List does not mangle an id that itself contains the prefix delimiter.
func TestSessionIDWithColonRoundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const id session.SessionID = "team:42:lead"
	s := newTestSession(t, id)
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != id {
		t.Errorf("Load.ID = %q, want %q", got.ID, id)
	}
	// List must recover the id verbatim (the TrimPrefix must not eat the id's
	// own colons).
	entries, err := st.(port.PrunableStore).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not return id %q (got %d entries)", id, len(entries))
	}
}
