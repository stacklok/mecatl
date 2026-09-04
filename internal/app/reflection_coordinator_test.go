package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type testReflector struct {
	mu      sync.Mutex
	calls   []session.SessionID
	start   chan session.SessionID
	release <-chan struct{}
}

type attemptBarrierReflector struct {
	started  chan int
	releases []chan struct{}
	mu       sync.Mutex
	calls    int
}

func (r *attemptBarrierReflector) Reflect(ctx context.Context, _ learning.Input) (learning.Outcome, error) {
	r.mu.Lock()
	attempt := r.calls
	r.calls++
	r.mu.Unlock()
	r.started <- attempt
	select {
	case <-r.releases[attempt]:
		return learning.Outcome{Kind: learning.OutcomeAbstained}, nil
	case <-ctx.Done():
		return learning.Outcome{}, ctx.Err()
	}
}

func (r *testReflector) Reflect(ctx context.Context, in learning.Input) (learning.Outcome, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in.Trajectory.SessionID)
	r.mu.Unlock()
	if r.start != nil {
		r.start <- in.Trajectory.SessionID
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return learning.Outcome{}, ctx.Err()
		}
	}
	return learning.Outcome{Kind: learning.OutcomeAbstained}, nil
}
func (*testReflector) RequestTokenEstimate(learning.Input) (int, error) {
	return 1, nil
}

func testJob(p, id string, r learning.Reflector) reflectionJob {
	tr := learning.NewTrajectory(session.SessionID(id), "/w", session.StopEndTurn, session.Usage{}, []session.Message{session.NewUserMessage("Remember that preference")})
	in := learning.NewInput(tr, nil, []learning.Signal{{Kind: learning.SignalExplicitRemember}}, nil)
	return reflectionJob{principal: p, input: in, reflector: r, process: func(context.Context, string, learning.Outcome) (reflectionReceipt, error) {
		return reflectionReceipt{}, nil
	}}
}
func TestReflectionCoordinatorBoundedFairSingleflightAndShutdown(t *testing.T) {
	release := make(chan struct{})
	starts := make(chan session.SessionID, 8)
	r := &testReflector{start: starts, release: release}
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 4, PrincipalCapacity: 2, Timeout: time.Second})
	first, e := c.Enqueue(testJob("a", "a1", r))
	if e != nil || first.Disposition != reflectionQueued {
		t.Fatalf("first=%+v err=%v", first, e)
	}
	if got := <-starts; got != "a1" {
		t.Fatal(got)
	}
	same, e := c.Enqueue(testJob("a", "a1", r))
	if e != nil || same.ID != first.ID || same.Disposition != reflectionDuplicate {
		t.Fatalf("duplicate=%+v err=%v", same, e)
	}
	for _, j := range []reflectionJob{testJob("a", "a2", r), testJob("b", "b1", r), testJob("b", "b2", r)} {
		x, e := c.Enqueue(j)
		if e != nil || x.Disposition != reflectionQueued {
			t.Fatalf("enqueue=%+v err=%v", x, e)
		}
	}
	full, e := c.Enqueue(testJob("b", "overflow", r))
	if e != nil || full.Disposition != reflectionQueueFull {
		t.Fatalf("full=%+v err=%v", full, e)
	}
	repeatedFull, e := c.Enqueue(testJob("b", "overflow", r))
	if e != nil || repeatedFull.ID != full.ID || repeatedFull.Disposition != reflectionQueueFull {
		t.Fatalf("repeated full=%+v err=%v", repeatedFull, e)
	}
	close(release)
	got := make([]session.SessionID, 0, 3)
	for range 3 {
		select {
		case id := <-starts:
			got = append(got, id)
		case <-time.After(time.Second):
			t.Fatal("stalled")
		}
	}
	want := []session.SessionID{"b1", "a2", "b2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cross-principal bounded fairness violated: got %v, want %v", got, want)
		}
	}
	c.Close()
	c.Close()
}
func TestReflectionCoordinatorTimeoutAndJoin(t *testing.T) {
	r := &testReflector{release: make(chan struct{})}
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Timeout: time.Millisecond})
	if _, e := c.Enqueue(testJob("a", "s", r)); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel and join")
	}
}

func TestReflectionCoordinatorClosePublishesQueuedReceipt(t *testing.T) {
	release := make(chan struct{})
	started := make(chan session.SessionID, 1)
	r := &testReflector{start: started, release: release}
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1})
	if _, err := c.Enqueue(testJob("a", "running", r)); err != nil {
		t.Fatal(err)
	}
	<-started
	queued, err := c.Enqueue(testJob("b", "queued", r))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	receipt, waitErr := c.Wait(context.Background(), queued.ID)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if receipt.Disposition != reflectionClosed {
		t.Fatalf("queued receipt = %+v", receipt)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel and join running job")
	}
}

func TestReflectionCoordinatorReservesReceiptBeforeAdmission(t *testing.T) {
	release := make(chan struct{})
	started := make(chan session.SessionID, 3)
	r := &testReflector{start: started, release: release}
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 2, Receipts: 1})
	if c.cfg.Receipts < c.cfg.Capacity+c.cfg.Workers {
		t.Fatalf("receipt capacity=%d, queue+running bound=%d", c.cfg.Receipts, c.cfg.Capacity+c.cfg.Workers)
	}
	var accepted []reflectionReceipt
	for i, job := range []reflectionJob{testJob("a", "one", r), testJob("b", "two", r), testJob("c", "three", r)} {
		receipt, err := c.Enqueue(job)
		if err != nil || receipt.Disposition != reflectionQueued {
			t.Fatalf("enqueue %d = %+v, %v", i, receipt, err)
		}
		accepted = append(accepted, receipt)
		if i == 0 {
			<-started
		}
	}
	close(release)
	for range 2 {
		<-started
	}
	for _, admission := range accepted {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		receipt, waitErr := c.Wait(ctx, admission.ID)
		cancel()
		if waitErr != nil {
			t.Fatalf("accepted job %s lost completion: %v", admission.ID, waitErr)
		}
		if receipt.Disposition != reflectionCompleted {
			t.Fatalf("completion = %+v", receipt)
		}
	}
	c.Close()
}

func TestReflectionCoordinatorDuplicateWaitersBeforeAndAfterCompletion(t *testing.T) {
	release := make(chan struct{})
	started := make(chan session.SessionID, 1)
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1})
	t.Cleanup(c.Close)
	job := testJob("owner", "same", &testReflector{start: started, release: release})
	first, err := c.Enqueue(job)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	duplicate, err := c.Enqueue(job)
	if err != nil || duplicate.ID != first.ID || duplicate.Disposition != reflectionDuplicate {
		t.Fatalf("in-flight duplicate = %+v, %v", duplicate, err)
	}

	before := make(chan reflectionReceipt, 1)
	go func() {
		receipt, waitErr := c.Wait(context.Background(), duplicate.ID)
		if waitErr != nil {
			receipt.Err = waitErr.Error()
		}
		before <- receipt
	}()
	close(release)
	if receipt := <-before; receipt.ID != first.ID || receipt.Disposition != reflectionCompleted || !receipt.Abstained || receipt.Err != "" {
		t.Fatalf("waiter registered before completion = %+v", receipt)
	}
	late, err := c.Wait(context.Background(), duplicate.ID)
	if err != nil || late.ID != first.ID || late.Disposition != reflectionCompleted || !late.Abstained {
		t.Fatalf("duplicate waiter after completion = %+v, %v", late, err)
	}
}

func TestReflectionCoordinatorRerunCannotStealEarlierAttemptReceipt(t *testing.T) {
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	reflector := &attemptBarrierReflector{started: make(chan int, 2), releases: releases}
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 1, Receipts: 1})
	t.Cleanup(c.Close)
	job := testJob("owner", "same", reflector)

	first, err := c.Enqueue(job)
	if err != nil {
		t.Fatal(err)
	}
	if attempt := <-reflector.started; attempt != 0 {
		t.Fatalf("first started attempt = %d", attempt)
	}
	c.mu.Lock()
	firstDone := c.receipts[first.ID].done
	c.mu.Unlock()
	close(releases[0])
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first attempt did not publish its receipt")
	}

	second, err := c.Enqueue(job)
	if err != nil || second.Disposition != reflectionQueued || second.ID == first.ID {
		t.Fatalf("second attempt = %+v, %v; first=%+v", second, err, first)
	}
	if attempt := <-reflector.started; attempt != 1 {
		t.Fatalf("second started attempt = %d", attempt)
	}

	waited := make(chan reflectionReceipt, 1)
	go func() {
		receipt, waitErr := c.Wait(context.Background(), first.ID)
		if waitErr != nil {
			receipt.Err = waitErr.Error()
		}
		waited <- receipt
	}()
	select {
	case receipt := <-waited:
		if receipt.ID != first.ID || receipt.Disposition != reflectionCompleted || !receipt.Abstained || receipt.Err != "" {
			t.Fatalf("first receipt after second started = %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("first attempt waiter was redirected to the blocked second attempt")
	}
	close(releases[1])
	secondDone, err := c.Wait(context.Background(), second.ID)
	if err != nil || secondDone.ID != second.ID || secondDone.Disposition != reflectionCompleted || !secondDone.Abstained {
		t.Fatalf("second receipt = %+v, %v", secondDone, err)
	}
}

func TestReflectionCoordinatorStartsWorkersLazily(t *testing.T) {
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{})
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if started {
		t.Fatal("constructor started workers before admission")
	}
	c.Close()
}

func TestReflectionObserverRejectsMediaHeavyInputWithoutQueueGrowth(t *testing.T) {
	c := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{})
	t.Cleanup(c.Close)
	observer := &reflectionObserver{coordinator: c, reflector: &testReflector{}, repository: memproposal.New(), operatorMemory: memmemory.New(), mode: learning.Review}
	trajectory := learning.NewTrajectory("media", "/w", session.StopEndTurn, session.Usage{}, []session.Message{
		session.NewUserMessageWithParts("remember this", []session.Content{{Kind: session.MediaImage, Data: make([]byte, defaultReflectionJobBytes+1)}}),
	})
	if _, err := observer.Reflect(context.Background(), trajectory, false); err == nil {
		t.Fatal("media-heavy trajectory was accepted")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued != 0 || c.queuedBytes != 0 || len(c.pending) != 0 {
		t.Fatalf("rejected input grew queue: queued=%d bytes=%d pending=%d", c.queued, c.queuedBytes, len(c.pending))
	}
}
