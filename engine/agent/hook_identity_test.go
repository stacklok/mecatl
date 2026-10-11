package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestHookPublicationDroppedFromFullRunChannelStillConsumesRevision(t *testing.T) {
	ctx := t.Context()
	sess := session.New("origin", session.ModeDefault, MemEnv("/ws").Ref(), session.Limits{}, time.Now())
	sess.RestoreHookRevision(3)
	before, err := sessnap.Of(sess)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	r := &Run{events: make(chan session.Event, 1), sess: sess, ctx: ctx, hardAbort: make(chan struct{})}
	r.events <- session.Event{Type: session.EvMessageDelta}
	close(r.hardAbort) // Full channel: both sends would park; only abort can win.
	e.emit(r, session.Event{Type: session.EvHook, Hook: &session.HookPayload{Phase: "Stop"}})
	if got := len(r.events); got != 1 {
		t.Fatalf("aborted hook reached stream: buffered %d", got)
	}
	retained := sink.snapshotEvents()
	if len(retained) != 1 || retained[0].Hook == nil || retained[0].Hook.Revision != 4 || sess.HookRevision() != 4 {
		t.Fatalf("dropped stream hook sink/revision: %+v, highwater %d", retained, sess.HookRevision())
	}
	if before.HookRevision != 3 {
		t.Fatalf("stale snapshot unexpectedly updated: %d", before.HookRevision)
	}
	log := memstore.NewEventLog()
	if err := log.Append(ctx, sess.ID, retained[0]); err != nil {
		t.Fatal(err)
	}
	restored, err := before.Restore()
	if err != nil {
		t.Fatal(err)
	}
	for ev, readErr := range log.Read(ctx, sess.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		restored.RestoreHookRevision(ev.Hook.Revision)
	}
	next := &Run{events: make(chan session.Event, 1), sess: restored, ctx: ctx, hardAbort: make(chan struct{})}
	e.emit(next, session.Event{Type: session.EvHook, Hook: &session.HookPayload{Phase: "Stop"}})
	if got := (<-next.events).Hook.Revision; got != 5 {
		t.Fatalf("next admitted revision = %d, want 5", got)
	}
}

func TestHookPublicationDistinctRecordsAndConcurrentRevisions(t *testing.T) {
	sess := session.New("origin", session.ModeDefault, MemEnv("/ws").Ref(), session.Limits{}, time.Now())
	r := &Run{events: make(chan session.Event, 128), sess: sess, ctx: context.Background(), hardAbort: make(chan struct{})}
	e := &Engine{}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			e.emit(r, session.Event{Type: session.EvHook, Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Read", CallID: "private", InvocationID: "tc_public"}})
		})
	}
	wg.Wait()
	ids := make(map[string]bool)
	revisions := make(map[uint64]bool)
	for range 40 {
		ev := <-r.events
		if ev.Hook == nil || ev.Hook.HookID == "" || ev.Hook.SessionID != sess.ID || ev.Hook.InvocationID != "tc_public" || ev.Hook.Revision == 0 || ids[ev.Hook.HookID] || revisions[ev.Hook.Revision] {
			t.Fatalf("duplicate or invalid hook: %+v", ev.Hook)
		}
		ids[ev.Hook.HookID], revisions[ev.Hook.Revision] = true, true
	}
	if sess.HookRevision() != 40 {
		t.Fatalf("revision = %d", sess.HookRevision())
	}
	snap, err := sessnap.Of(sess)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.NextHookRevision(); got != 41 {
		t.Fatalf("restored next revision = %d", got)
	}
}

func TestReviewHookKeepsRecordAcrossUpdates(t *testing.T) {
	sess := session.New("origin", session.ModeDefault, MemEnv("/ws").Ref(), session.Limits{}, time.Now())
	r := &Run{events: make(chan session.Event, 2), sess: sess, ctx: context.Background(), hardAbort: make(chan struct{})}
	e := &Engine{}
	call := session.ToolCall{ID: "private", InvocationID: "tc_public"}
	id := reviewRecordID(sess.ID, call, ReviewJobInbound)
	for _, disposition := range []string{"hold", "release_result"} {
		e.emit(r, session.Event{Type: session.EvHook, Hook: &session.HookPayload{CallID: call.ID, InvocationID: call.InvocationID, Guardrail: &session.GuardrailReviewPayload{ReviewID: id, Disposition: disposition}}})
	}
	first, second := <-r.events, <-r.events
	if first.Hook.HookID != id || second.Hook.HookID != id || first.Hook.Revision >= second.Hook.Revision || first.Hook.InvocationID != call.InvocationID || second.Hook.SessionID != sess.ID {
		t.Fatalf("review updates: %+v, %+v", first.Hook, second.Hook)
	}
	if id == reviewRecordID(sess.ID, call, ReviewJobAction) || id == reviewRecordID(sess.ID, call, ReviewJobPermission) {
		t.Fatal("review jobs collided")
	}
}
