package fileserver

import (
	"context"
	"errors"
	"testing"
)

func TestPathLockWaitEndsWithContextAndEntriesAreDropped(t *testing.T) {
	l := pathLocks{held: map[string]*pathLock{}}
	unlock, err := l.lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.lock(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting on a held path = %v, want context.Canceled", err)
	}
	other, err := l.lock(ctx, "b")
	if err == nil {
		other()
	}
	unlock()
	again, err := l.lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if len(l.held) != 0 {
		t.Fatalf("lock table keeps %d idle entries", len(l.held))
	}
}
