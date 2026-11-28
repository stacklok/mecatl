package sessionruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/sessionruntime"
)

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestTypedSugarIsNonoperational(t *testing.T) {
	sid, err := sessionid.ParseSessionID("SES1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if sessionruntime.ErrNotImplemented != resourcegroup.ErrNotImplemented {
		t.Fatal("typed sugar must share the generic skeleton sentinel")
	}
	for _, ctx := range []context.Context{nil, context.Background(), canceledContext()} {
		for _, group := range []*resourcegroup.Group{nil, {}} {
			for _, id := range []sessionid.SessionID{{}, sid} {
				if got, err := sessionruntime.Load(ctx, group, id); got != nil || err != sessionruntime.ErrNotImplemented {
					t.Fatalf("Load() = %v, %v", got, err)
				}
			}
			got, id, err := sessionruntime.Create(ctx, group)
			if got != nil || id != (sessionid.SessionID{}) || err != sessionruntime.ErrNotImplemented {
				t.Fatalf("Create() = %v, %v, %v", got, id, err)
			}
		}
	}
}

func TestSkeletonCannotRunOrReportAcceptedState(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background(), canceledContext()} {
		for _, r := range []*sessionruntime.SessionRuntime{nil, {}} {
			if r.GroupID() != (groupid.GroupID{}) || r.SessionID() != (sessionid.SessionID{}) || r.ID() != r.SessionID() {
				t.Fatal("unconstructed runtime has identity")
			}
			snapshot, err := r.Snapshot(ctx)
			if snapshot.SessionID() != (sessionid.SessionID{}) || !errors.Is(err, sessionruntime.ErrNotImplemented) {
				t.Fatalf("Snapshot claimed accepted state: %v, %v", snapshot.SessionID(), err)
			}
			if err := r.Run(ctx, session.UserInput{}); !errors.Is(err, sessionruntime.ErrNotImplemented) {
				t.Fatalf("Run claimed execution: %v", err)
			}
			if err := r.Cancel(ctx); !errors.Is(err, sessionruntime.ErrNotImplemented) {
				t.Fatalf("Cancel claimed cancellation: %v", err)
			}
		}
	}
}
