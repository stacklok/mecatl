package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type saveProbeStore struct {
	save func(context.Context, *session.Session) error
}

func (s saveProbeStore) Save(ctx context.Context, sess *session.Session) error {
	return s.save(ctx, sess)
}

func (saveProbeStore) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	return nil, fmt.Errorf("%w: %q", port.ErrSessionNotFound, id)
}

func cancelledSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	return sess
}

func TestSavePersistsCancelledTerminalStateWithDetachedContext(t *testing.T) {
	type contextKey struct{}
	original := context.WithValue(t.Context(), contextKey{}, "correlation")
	ctx, cancel := context.WithCancel(original)
	cancel()

	var saved session.State
	store := saveProbeStore{save: func(saveCtx context.Context, sess *session.Session) error {
		if err := saveCtx.Err(); err != nil {
			t.Fatalf("detached Save context is already done: %v", err)
		}
		if got := saveCtx.Value(contextKey{}); got != "correlation" {
			t.Fatalf("detached Save context lost values: got %v", got)
		}
		saved = sess.State
		return nil
	}}
	e := NewEngine(Deps{Store: store})
	sess := cancelledSession(t, "sess-cancelled-save")
	e.save(ctx, &Run{diag: e.bindRunDiag(sess.ID)}, sess)

	if saved != session.StateCancelled {
		t.Fatalf("persisted state = %q, want %q", saved, session.StateCancelled)
	}
}

func TestCancelledSaveContextIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var remaining time.Duration
	store := saveProbeStore{save: func(saveCtx context.Context, _ *session.Session) error {
		deadline, ok := saveCtx.Deadline()
		if !ok {
			t.Fatal("cancel-detached Save context has no deadline")
		}
		remaining = time.Until(deadline)
		return nil
	}}
	e := NewEngine(Deps{Store: store})
	sess := cancelledSession(t, "sess-bounded-save")
	e.save(ctx, &Run{diag: e.bindRunDiag(sess.ID)}, sess)

	if remaining <= 0 || remaining > sessionPersistenceTimeout {
		t.Fatalf("detached Save deadline remaining = %v, want (0, %v]", remaining, sessionPersistenceTimeout)
	}
}

func TestSaveLiveContextFastPath(t *testing.T) {
	ctx := t.Context()
	store := saveProbeStore{save: func(saveCtx context.Context, _ *session.Session) error {
		if saveCtx != ctx {
			t.Fatal("live Save context was wrapped; want original fast-path context")
		}
		return nil
	}}
	e := NewEngine(Deps{Store: store})
	sess := session.New("sess-live-save", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	e.save(ctx, &Run{diag: e.bindRunDiag(sess.ID)}, sess)
}

type saveContextDiagnostic struct {
	logs   int
	ctxErr error
	attrs  map[string]any
}

func (d *saveContextDiagnostic) Log(ctx context.Context, _ port.Level, _ string, args ...any) {
	d.logs++
	d.ctxErr = ctx.Err()
	for i := 0; i+1 < len(args); i += 2 {
		if key, ok := args[i].(string); ok {
			d.attrs[key] = args[i+1]
		}
	}
}

func (d *saveContextDiagnostic) With(args ...any) port.Diagnostics {
	for i := 0; i+1 < len(args); i += 2 {
		if key, ok := args[i].(string); ok {
			d.attrs[key] = args[i+1]
		}
	}
	return d
}

func TestCancelledSaveFailureWarnsOnceWithOriginalCorrelation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	boom := errors.New("save failed")
	attempts := 0
	store := saveProbeStore{save: func(context.Context, *session.Session) error {
		attempts++
		return boom
	}}
	diag := &saveContextDiagnostic{attrs: make(map[string]any)}
	e := NewEngine(Deps{Store: store, Diagnostics: diag})
	sess := cancelledSession(t, "sess-cancelled-warn")
	r := &Run{diag: e.bindRunDiag(sess.ID)}

	e.save(ctx, r, sess)
	e.save(ctx, r, sess)

	if attempts != 2 {
		t.Fatalf("Save attempts = %d, want 2", attempts)
	}
	if diag.logs != 1 {
		t.Fatalf("persistence warnings = %d, want 1", diag.logs)
	}
	if !errors.Is(diag.ctxErr, context.Canceled) {
		t.Fatalf("persistence warning context error = %v, want original context cancellation", diag.ctxErr)
	}
	if got := diag.attrs["session"]; got != "sess-cancelled-warn" {
		t.Fatalf("warning session correlation = %#v, want %q", got, "sess-cancelled-warn")
	}
	if got := diag.attrs["error"]; got != boom {
		t.Fatalf("warning error = %#v, want %v", got, boom)
	}
}
