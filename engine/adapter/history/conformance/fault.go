package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// lostAckBackend drops one successful backend receipt after real persistence.
// History must preserve this uncertainty; the wrapper never retries the write.
type lostAckBackend struct {
	history.Backend
	calls int
	lost  error
}

func (s *lostAckBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, b history.ByteCommit) (history.Observation, error) {
	s.calls++
	got, err := s.Backend.Append(ctx, id, expected, b)
	if err == nil && got.Head == b.Ref && got.Commit == b.Ref && s.calls == 1 {
		return history.Observation{}, s.lost
	}
	return got, err
}

func lostAcknowledgement(t *testing.T, factory Factory) {
	transportErr := errors.New("transport lost acknowledgement")
	backend := &lostAckBackend{Backend: factory(t), lost: transportErr}
	client, err := history.New(backend, policy())
	if err != nil {
		t.Fatal(err)
	}
	id := history.NewHistoryID()
	root, _ := encoded(t, id, history.CommitRef{}, "root")
	r1, err := client.Identify(id, root)
	if err != nil {
		t.Fatal(err)
	}
	second, r2 := encoded(t, id, r1, "second")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := client.Append(ctx, id, root)
	cancel()
	if !errors.Is(err, transportErr) || got != (history.Resolution{}) || backend.calls != 1 {
		t.Fatalf("lost acknowledgement = %+v, %v; calls %d", got, err, backend.calls)
	}
	got, err = client.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusAccepted, r1, r1)
	appendAccepted(t, client, id, second, r2)
	got, err = client.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusAccepted, r1, r2)
	got, err = client.Append(context.Background(), id, root)
	wantResolution(t, got, err, history.StatusAccepted, r1, r2)
	if backend.calls != 3 {
		t.Fatalf("History retried automatically: %d calls", backend.calls)
	}
	page, err := client.Read(context.Background(), id, history.ReadRequest{Through: r2, MaxCommits: 2})
	if err != nil || page.Next != r2 || len(page.Commits) != 2 || !page.Commits[0].Commit.Equal(root) || !page.Commits[1].Commit.Equal(second) {
		t.Fatalf("recovery changed accepted bytes = %+v, %v", page, err)
	}
}

// appendResult carries one worker's receipt without blocking its completion if
// a test fails before collecting it.
type appendResult struct {
	resolution history.Resolution
	err        error
}

// delayedStore models one serialized request already owned by a transport when
// the caller abandons it. Forwarding is detached from caller cancellation, but
// controlled by release. The backend itself never reports an accepted write as
// canceled. Use only once; no request registry or retry machinery is involved.
type delayedStore struct {
	*history.History
	started        chan struct{}
	release        chan struct{}
	done           chan struct{}
	result         chan appendResult
	forwardContext context.Context
}

func (s *delayedStore) Append(ctx context.Context, id history.HistoryID, b format.Commit) (history.Resolution, error) {
	serialized := b.Clone()
	go func() {
		defer close(s.done)
		<-s.release
		got, err := s.History.Append(s.forwardContext, id, serialized)
		s.result <- appendResult{got, err}
	}()
	close(s.started)
	<-ctx.Done()
	return history.Resolution{}, ctx.Err()
}

// waitSignal bounds test failures, never operation ordering; channels provide
// every synchronization edge in the fault scenario.
func waitSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for test worker")
	}
}

func delayedRequest(t *testing.T, factory historyFactory) {
	backend := factory(t, policy())
	forwardCtx, stopForward := context.WithCancel(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	client := &delayedStore{History: backend, started: make(chan struct{}), release: make(chan struct{}),
		done: make(chan struct{}), result: make(chan appendResult, 1), forwardContext: forwardCtx}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(client.release) }) }
	callerDone := make(chan struct{})
	callerResult := make(chan appendResult, 1)
	id := history.NewHistoryID()
	root, _ := encoded(t, id, history.CommitRef{}, "root")
	r1, err := backend.Identify(id, root)
	if err != nil {
		t.Fatal(err)
	}
	original := root.Clone()
	child, r2 := encoded(t, id, r1, "child")
	t.Cleanup(func() {
		cancel()
		release()
		stopForward()
		for _, done := range []<-chan struct{}{callerDone, client.done} {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("timed out joining test worker during cleanup")
			}
		}
	})
	go func() {
		defer close(callerDone)
		got, err := client.Append(ctx, id, root)
		callerResult <- appendResult{got, err}
	}()
	waitSignal(t, client.started)
	cancel()
	waitSignal(t, callerDone)
	canceled := <-callerResult
	if !errors.Is(canceled.err, context.Canceled) || canceled.resolution != (history.Resolution{}) {
		t.Fatalf("caller cancellation = %+v, %v", canceled.resolution, canceled.err)
	}
	// Caller ownership ends at return, not at detached forwarding completion.
	root.Records[0].Payload[0] ^= 0xff
	got, err := backend.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusNotObserved, r1, history.CommitRef{})
	otherID := history.NewHistoryID()
	other, otherRef := encoded(t, otherID, history.CommitRef{}, "independent history")
	appendAccepted(t, backend, otherID, other, otherRef)
	got, err = backend.Append(context.Background(), id, child)
	wantResolution(t, got, err, history.StatusConflict, r2, history.CommitRef{})
	release()
	waitSignal(t, client.done)
	accepted := <-client.result
	wantResolution(t, accepted.resolution, accepted.err, history.StatusAccepted, r1, r1)
	got, err = backend.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusAccepted, r1, r1)
	// Exactly the previously rejected bytes become eligible; no queued child.
	appendAccepted(t, backend, id, child, r2)
	page, err := backend.Read(context.Background(), id, history.ReadRequest{Through: r2, MaxCommits: 2})
	if err != nil || page.Next != r2 || len(page.Commits) != 2 || !page.Commits[0].Commit.Equal(original) || !page.Commits[1].Commit.Equal(child) {
		t.Fatalf("delayed acceptance bytes = %+v, %v", page, err)
	}
}
