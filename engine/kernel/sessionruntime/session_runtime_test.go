package sessionruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
	"github.com/stacklok/mecatl/engine/kernel/sessionruntime"
)

func TestSkeletonCannotRunOrReportAcceptedState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, canceled := range []bool{false, true} {
		if canceled {
			cancel()
		}
		for _, r := range []*sessionruntime.SessionRuntime{nil, {}} {
			if r.GroupID() != (groupid.GroupID{}) || r.SessionID() != (sessionid.SessionID{}) {
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
