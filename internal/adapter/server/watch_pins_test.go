package server_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// The pins in this file close claims that watch.go's doc comments MAKE but that
// nothing enforced. Each one survived mutation before it was written; a comment
// asserting a rule the suite cannot see is the defect shape this package is most
// prone to, precisely because its comments are good enough to be trusted.

// TestADR_0250_GapOutranksEveryOtherTerminal pins the precedence: a faulted watch
// reports the gap even when it had ALREADY recorded a different terminal.
//
// The lagging row is the one that matters and the one that was unpinned. A
// lagging termination tells the client "reconnect from your cursor, you lost
// nothing"; a gap tells it "records are missing and no retry recovers them". If
// lagging wins, the client is handed the reassuring message in exactly the case
// where it is false.
func TestADR_0250_GapOutranksEveryOtherTerminal(t *testing.T) {
	other := errors.New("some other terminal")
	for _, tc := range []struct {
		name     string
		gapped   bool
		recorded error
		wantGap  bool
		wantErr  error
	}{
		{name: "gap over a clean end", gapped: true, recorded: nil, wantGap: true},
		{name: "gap over a lagging termination", gapped: true, recorded: server.ErrWatchLagging, wantGap: true},
		{name: "gap over a backend fault", gapped: true, recorded: other, wantGap: true},
		{name: "no gap keeps the lagging terminal", gapped: false, recorded: server.ErrWatchLagging, wantErr: server.ErrWatchLagging},
		{name: "no gap keeps a backend fault", gapped: false, recorded: other, wantErr: other},
		{name: "no gap and no terminal is a clean end", gapped: false, recorded: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := server.WatchTerminalForTest(tc.gapped, tc.recorded)
			switch {
			case tc.wantGap:
				if !errors.Is(got, server.ErrActivityGap) {
					t.Fatalf("terminal = %v, want a gap — a known gap must never be masked by another outcome", got)
				}
			case tc.wantErr != nil:
				if !errors.Is(got, tc.wantErr) {
					t.Fatalf("terminal = %v, want %v", got, tc.wantErr)
				}
			default:
				if got != nil {
					t.Fatalf("terminal = %v, want a clean end", got)
				}
			}
		})
	}
}

// TestADR_0250_FaultTerminatesEveryAttachedWatcher pins the fan-out. Terminating
// only the first watcher on a session would satisfy AC7.6's single-watcher
// assertion while leaving every other client on that session reading a stream it
// believes is complete — and a fan-out registry whose fan-out is untested is a
// map with extra steps.
func TestADR_0250_FaultTerminatesEveryAttachedWatcher(t *testing.T) {
	log := newCountingCursorLog(memstore.NewEventLog())
	svc, client, id := watchedRun(t, log)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const watchers = 3
	terminals := make(chan error, watchers)
	ready := make(chan struct{}, watchers)
	for range watchers {
		envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
		if err != nil {
			t.Fatalf("WatchSessionEvents: %v", err)
		}
		go func() {
			announced := false
			var last error
			for env, iterErr := range envelopes {
				if iterErr != nil {
					last = iterErr
					break
				}
				if !announced && env.Event == nil && env.Phase == server.WatchPhaseLive {
					announced = true
					ready <- struct{}{}
				}
			}
			terminals <- last
		}()
	}
	for range watchers {
		select {
		case <-ready:
		case <-time.After(20 * time.Second):
			t.Fatal("not every watcher reached the live boundary")
		}
	}

	log.failAppends(true)
	done := driveRunBackground(t, client, id, "second")
	for i := range watchers {
		select {
		case err := <-terminals:
			if !errors.Is(err, server.ErrActivityGap) {
				t.Fatalf("watcher %d ended with %v, want ErrActivityGap — EVERY attached watcher is faulted, not just the first", i, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("watcher %d never terminated; the fault did not fan out", i)
		}
	}
	waitRun(t, done)
	log.failAppends(false)
}

// TestADR_0250_ShutdownEndsAttachedWatchesCleanly pins two halves of one rule.
//
// A watch attached at shutdown must END — goleak.VerifyTestMain depends on
// closeWatches silently, so a pump that outlived its Service would surface as an
// unrelated leak failure in whatever test ran last rather than as this one. And
// it must end CLEANLY: no append failed, so reporting a gap would be a lie the
// client would act on by discarding a transcript that is in fact whole.
func TestADR_0250_ShutdownEndsAttachedWatchesCleanly(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _, id := watchedRun(t, log)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	ready, ended := make(chan struct{}), make(chan error, 1)
	go func() {
		announced := false
		var last error
		for env, iterErr := range envelopes {
			if iterErr != nil {
				last = iterErr
				break
			}
			if !announced && env.Event == nil && env.Phase == server.WatchPhaseLive {
				announced = true
				close(ready)
			}
		}
		ended <- last
	}()
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("watch never reached the live boundary")
	}

	svc.Close()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("shutdown ended the watch with %v, want a clean end — no append failed, so a gap would be a lie", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("watch outlived Service.Close; the pump goroutine leaked")
	}
}

// TestADR_0250_WatchAfterCloseIsBornCancelled pins the other side of the same
// registry rule: a watch that RACES the close must not start following a log
// nobody is writing. registerWatch cancels a registration made after
// watchesClosed, and nothing else asserted that.
func TestADR_0250_WatchAfterCloseIsBornCancelled(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _, id := watchedRun(t, log)
	svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		return // refusing outright is equally correct, and strictly safer
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for range envelopes { //nolint:revive // draining to completion is the assertion
		}
	}()
	select {
	case <-ended:
	case <-time.After(20 * time.Second):
		t.Fatal("a watch attached after Close kept following; registerWatch did not cancel it")
	}
}

// TestADR_0250_BackendFaultDuringFollowTerminates pins the FOLLOW half of the
// pump's error handling. The replay half is exercised by the legacy-route pin;
// this arm is a separate loop with its own `terminal = err`, and a follow that
// swallowed a mid-stream backend error would leave a client parked on a stream
// that is no longer being read — the silent failure the whole feature exists to
// abolish.
func TestADR_0250_BackendFaultDuringFollowTerminates(t *testing.T) {
	inner := memstore.NewEventLog()
	log := &followFaultLog{CursorEventLog: inner}
	svc, _, id := watchedRun(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	var termErr error
	for _, iterErr := range envelopes {
		if iterErr != nil {
			termErr = iterErr
			break
		}
	}
	if !errors.Is(termErr, errFollowFailed) {
		t.Fatalf("watch ended with %v, want the backend follow fault surfaced to the client", termErr)
	}
}

// TestADR_0250_EmptyLogAnnouncesTheBoundaryImmediately pins the case a
// bootstrapping client hits FIRST: a session with nothing durable yet. The
// boundary must still arrive, because it is what tells the client the replay is
// complete. Deriving it from port.LogRecord.Live instead would hang here forever
// — there is no record to carry the flag — which is the whole reason the read is
// split in two.
func TestADR_0250_EmptyLogAnnouncesTheBoundaryImmediately(t *testing.T) {
	svc := watchService(t, memstore.NewEventLog(), false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	envelopes, err := svc.WatchSessionEvents(ctx, sess.ID, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	next, stop := iter.Pull2(envelopes)
	defer stop()

	got := make(chan server.WatchEnvelope, 1)
	go func() {
		env, iterErr, ok := next()
		if ok && iterErr == nil {
			got <- env
		}
		close(got)
	}()
	select {
	case env, ok := <-got:
		if !ok {
			t.Fatal("watch on an empty log produced no envelope at all")
		}
		if env.Event != nil || env.Phase != server.WatchPhaseLive {
			t.Fatalf("first envelope = %s/%v, want the event-less live boundary", env.Phase, env.Event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch on an empty log never announced the boundary; a bootstrapping client would hang")
	}
}

// errFollowFailed is a backend fault raised only during a following read.
var errFollowFailed = errors.New("backend follow failed")

// followFaultLog fails ReadAfter when — and only when — it is following, leaving
// the replay phase healthy so the fault lands in the second loop.
type followFaultLog struct{ port.CursorEventLog }

func (l *followFaultLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	if !opts.Follow {
		return l.CursorEventLog.ReadAfter(ctx, id, after, opts)
	}
	return func(yield func(port.LogRecord, error) bool) {
		yield(port.LogRecord{}, errFollowFailed)
	}
}
