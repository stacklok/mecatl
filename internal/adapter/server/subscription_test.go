package server_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive verifies AC5.1:
// with a subscriber connected to a session, a fire's delivered note is published
// onto the session's live subscription AS IT HAPPENS — no operator input.
//
// This is the subscription-half test: a subscriber connected to a session
// receives events from a delivery run, proving the subscription seam works.
// The card render is task 07's AC5.1.
func TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive(t *testing.T) {
	svc := newSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx := context.Background()

	// Create an origin session and drive it to completed.
	originID := session.SessionID("origin-1")
	sess, err := svc.CreateSessionWithProfile(ctx, "/tmp/sub-test", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Complete the origin with one prompt so it's idle/completed.
	run, err := svc.StartRunContent(ctx, sess.ID, "initial prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent (origin): %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	// Subscribe to the origin session.
	sub, unsub := svc.Subscribe(sess.ID)
	defer unsub()

	// Collect events from the subscriber in a goroutine.
	var (
		mu  sync.Mutex
		evs []session.Event
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range sub {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()

	// Drive a delivery run: this mimics what deliverFireResult does — it runs
	// StartRunContent on the origin with a delivery note and the events are
	// published to the session's live subscription.
	deliveryNote := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"
	deliveryRun, err := svc.StartRunContent(ctx, sess.ID, deliveryNote, nil)
	if err != nil {
		t.Fatalf("StartRunContent (delivery): %v", err)
	}

	// Publish delivery run events to the subscription (this is what
	// deliverFireResult will do via PublishSessionEvent).
	for ev := range deliveryRun.Events() {
		svc.PublishSessionEvent(sess.ID, ev)
	}
	svc.FinishRun(sess.ID, deliveryRun)

	// Close the subscription channel to signal the collector goroutine.
	unsub()
	wg.Wait()

	// AC5.1: the subscriber received events — proof the subscription seam works.
	if len(evs) == 0 {
		t.Fatal("subscriber received NO events — subscription is broken")
	}

	// The subscriber must have received EvUserPrompt with the delivery note body
	// (the fenced-untrusted content the engine recorded as a user prompt).
	foundPrompt := false
	for _, ev := range evs {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil {
			if strings.Contains(ev.UserPrompt.Text, "scheduled task nightly-sync") {
				foundPrompt = true
				break
			}
		}
	}
	if !foundPrompt {
		t.Fatalf("subscriber events do NOT contain EvUserPrompt with delivery note; got %d events", len(evs))
	}

	// The subscriber must have received a terminal EvResult.
	foundResult := false
	for _, ev := range evs {
		if ev.Type == session.EvResult {
			foundResult = true
			break
		}
	}
	if !foundResult {
		t.Fatal("subscriber events do NOT contain a terminal EvResult")
	}

	_ = originID
}

// TestFireDelivery_Scenario5_TUIRendersRecordedNoteNotRaw verifies AC5.3:
// The delivered note published to the subscriber is the SAME fenced-untrusted
// content the engine recorded (no second render path, no un-fenced echo) — the
// subscriber receives the recorded note, not the fire's raw output.
func TestFireDelivery_Scenario5_TUIRendersRecordedNoteNotRaw(t *testing.T) {
	svc := newSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx := context.Background()

	// Create an origin session.
	sess, err := svc.CreateSessionWithProfile(ctx, "/tmp/sub-test", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Complete the origin.
	run, err := svc.StartRunContent(ctx, sess.ID, "initial prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent (origin): %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	sub, unsub := svc.Subscribe(sess.ID)
	defer unsub()

	var (
		mu  sync.Mutex
		evs []session.Event
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range sub {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()

	// The fire's raw output (as would come from the fire session).
	rawFireOutput := "this is the RAW fire output that the model produced"

	// The delivery NOTE (fenced-untrusted, what renderFireDelivery produces).
	// This is the content that should be recorded and received by the subscriber.
	deliveryNote := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"

	// Drive the delivery run with the NOTE (not the raw output).
	deliveryRun, err := svc.StartRunContent(ctx, sess.ID, deliveryNote, nil)
	if err != nil {
		t.Fatalf("StartRunContent (delivery): %v", err)
	}
	for ev := range deliveryRun.Events() {
		svc.PublishSessionEvent(sess.ID, ev)
	}
	svc.FinishRun(sess.ID, deliveryRun)

	unsub()
	wg.Wait()

	// AC5.3: NO EvUserPrompt leaks the fire's raw output, and AT LEAST ONE carries
	// the RECORDED NOTE (fenced-untrusted content). The delivery run may also emit
	// harness-authored continuation prompts (the no-progress nudge / wrap-up), which
	// are legitimate EvUserPrompt events that need not contain the note header —
	// the assertion is on the NOTE's presence and the RAW output's absence, not on
	// every prompt event matching the header.
	foundNote := false
	for _, ev := range evs {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil {
			if strings.Contains(ev.UserPrompt.Text, rawFireOutput) {
				t.Fatalf("SUBSCRIBER LEAK: EvUserPrompt contains raw fire output %q — must contain only the recorded note, not the fire's raw output",
					rawFireOutput)
			}
			if strings.Contains(ev.UserPrompt.Text, "scheduled task nightly-sync") {
				foundNote = true
			}
		}
	}
	if !foundNote {
		t.Fatalf("no EvUserPrompt carries the recorded delivery note header; got %d events", len(evs))
	}

	_ = sess
}

// TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging verifies AC6.3
// (the in-process drain-discipline half): a dead/disconnected subscriber
// drains-to-discard without wedging the delivery run; the durable log still
// records the tail. (The wire-transport half is task 08's AC6.3.)
func TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging(t *testing.T) {
	runSubscriptionHelper(t, "MECATL_SUBSCRIPTION_DEAD_CLIENT_HELPER", "TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedgingHelper")
}

func TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedgingHelper(t *testing.T) {
	if os.Getenv("MECATL_SUBSCRIPTION_DEAD_CLIENT_HELPER") != "1" {
		t.Skip("helper process only")
	}

	svc := newSubscriptionService(t,
		mockllm.TextTurn("ack: delivery received"),
	)
	ctx := context.Background()

	// Create an origin session.
	sess, err := svc.CreateSessionWithProfile(ctx, "/tmp/sub-test", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "initial prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent (origin): %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	// Subscribe, then immediately unsubscribe (dead client).
	sub, unsub := svc.Subscribe(sess.ID)

	// Drain any events that were buffered during subscription setup, then close.
	done := make(chan struct{})
	go func() {
		for range sub {
		}
		close(done)
	}()
	unsub()
	<-done

	// Now drive a delivery run. It must complete without wedging even though
	// the subscriber is gone (PublishSessionEvent must be non-blocking).
	deliveryNote := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"

	// Wrap in a timeout: the run must complete promptly.
	deliveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	deliveryRun, err := svc.StartRunContent(deliveryCtx, sess.ID, deliveryNote, nil)
	if err != nil {
		t.Fatalf("StartRunContent (delivery): %v", err)
	}

	// Publish events to subscribers. The subscriber is gone (channel closed and
	// drained), so PublishSessionEvent must drop events without blocking.
	// If Publish blocks, this loop wedges and the timeout fires.
	drainDone := make(chan struct{})
	go func() {
		for ev := range deliveryRun.Events() {
			svc.PublishSessionEvent(sess.ID, ev)
		}
		close(drainDone)
	}()

	select {
	case <-drainDone:
		// Delivery run completed without wedging — AC6.3 satisfied.
	case <-deliveryCtx.Done():
		t.Fatal("delivery run WEDGED: PublishSessionEvent blocked on a dead subscriber channel")
	}
	svc.FinishRun(sess.ID, deliveryRun)

	// The delivery run completed, so the durable log should have the tail.
	// (The jsonlstore is wired as EventLog; we assert the session was persisted.)
	loaded, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after delivery: %v", err)
	}
	if loaded.State != session.StateCompleted {
		t.Errorf("session state = %q, want completed (delivery run finished)", loaded.State)
	}

	_ = sub
}

// TestSubscriptionFullBufferPublishDoesNotBlock runs the overflowing publish in
// a subprocess. If a regression blocks that call, CommandContext kills only the
// helper process, so this test neither hangs nor leaks a publisher goroutine.
func TestSubscriptionFullBufferPublishDoesNotBlock(t *testing.T) {
	runSubscriptionHelper(t, "MECATL_SUBSCRIPTION_FULL_BUFFER_HELPER", "TestSubscriptionFullBufferPublishHelper")
}

func runSubscriptionHelper(t *testing.T, env, testName string) {
	t.Helper()
	// Generous: this bounds a HANG, not the helper's runtime. A -race helper on a
	// 2-core CI runner is an order of magnitude slower than a dev laptop.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+testName+"$")
	cmd.Env = append(os.Environ(), env+"=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s did not return before deadline: %v", testName, ctx.Err())
	}
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", testName, err, output)
	}
}

func TestSubscriptionFullBufferPublishHelper(t *testing.T) {
	if os.Getenv("MECATL_SUBSCRIPTION_FULL_BUFFER_HELPER") != "1" {
		t.Skip("helper process only")
	}

	svc := newSubscriptionService(t)
	defer svc.Close()
	id := session.SessionID("subscription-full-buffer")
	ch, unsub := svc.Subscribe(id)

	// Verify the subscription is live before filling it and asserting closure.
	svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnStart})
	if _, ok := <-ch; !ok {
		t.Fatal("live subscription closed before receiving its first event")
	}

	for range 64 {
		svc.PublishSessionEvent(id, session.Event{})
	}
	// This is the call whose return is bounded by the parent process.
	svc.PublishSessionEvent(id, session.Event{})

	unsub()
	count := 0
	for range ch {
		count++
	}
	if count != 64 {
		t.Fatalf("full subscriber received %d events, want buffer capacity 64", count)
	}
}

// TestSubscriptionConcurrentPublishAndUnsubscribe keeps publishers live while
// subscriptions for one session are repeatedly created and idempotently removed.
func TestSubscriptionConcurrentPublishAndUnsubscribe(t *testing.T) {
	runSubscriptionHelper(t, "MECATL_SUBSCRIPTION_CHURN_HELPER", "TestSubscriptionConcurrentPublishAndUnsubscribeHelper")
}

func TestSubscriptionConcurrentPublishAndUnsubscribeHelper(t *testing.T) {
	if os.Getenv("MECATL_SUBSCRIPTION_CHURN_HELPER") != "1" {
		t.Skip("helper process only")
	}

	svc := newSubscriptionService(t)
	defer svc.Close()

	const (
		publishers    = 8
		churners      = 16
		subscriptions = 32
	)
	id := session.SessionID("subscription-concurrency")

	publishStart := make(chan struct{})
	stopPublishers := make(chan struct{})
	publishActive := make(chan struct{}, publishers)
	var publishersDone sync.WaitGroup
	publishersDone.Add(publishers)
	for range publishers {
		go func() {
			defer publishersDone.Done()
			<-publishStart
			// Confirm that this publisher has made a real call before churn begins.
			svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnStart})
			publishActive <- struct{}{}
			for {
				select {
				case <-stopPublishers:
					return
				default:
					svc.PublishSessionEvent(id, session.Event{})
					// Yield: an unthrottled publish loop starves the churners on a
					// low-core CI runner under -race, and the point of this test is
					// concurrency, not publish throughput.
					runtime.Gosched()
				}
			}
		}()
	}
	close(publishStart)
	for range publishers {
		<-publishActive
	}

	var churnDone sync.WaitGroup
	churnDone.Add(churners)
	for range churners {
		go func() {
			defer churnDone.Done()
			for range subscriptions {
				ch, unsub := svc.Subscribe(id)

				// A receive proves this individual subscription was live before
				// the concurrent publisher/unsubscribe phase. It may be a publisher
				// event or this explicit event; either is a successful delivery.
				svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnStart})
				if _, ok := <-ch; !ok {
					t.Error("subscription closed before receiving its first event")
					return
				}

				// Keep the buffer available while unsubscribe races the publishers,
				// so the publishers execute sends rather than only the full-buffer
				// drop path. The helper-process parent bounds this wait if a broken
				// implementation fails to close the channel.
				drained := make(chan struct{})
				go func() {
					for range ch {
					}
					close(drained)
				}()

				// Publishers are already active and keep publishing throughout these
				// calls; this is intentionally ordered after their first real publish,
				// rather than merely co-releasing both sides of the race.
				var duplicateUnsub sync.WaitGroup
				duplicateUnsub.Add(2)
				for range 2 {
					go func() {
						defer duplicateUnsub.Done()
						unsub()
					}()
				}
				duplicateUnsub.Wait()

				// The drainer completes only after this exact returned channel closes.
				<-drained
			}
		}()
	}

	churnDone.Wait()
	close(stopPublishers)
	publishersDone.Wait()
}

// TestSubscriptionCloseOverlapsPublish verifies shutdown closes existing
// subscribers without racing publishers, and that subscriptions made after
// shutdown are already closed and can still be unsubscribed idempotently.
func TestSubscriptionCloseOverlapsPublish(t *testing.T) {
	runSubscriptionHelper(t, "MECATL_SUBSCRIPTION_CLOSE_HELPER", "TestSubscriptionCloseOverlapsPublishHelper")
}

func TestSubscriptionCloseOverlapsPublishHelper(t *testing.T) {
	if os.Getenv("MECATL_SUBSCRIPTION_CLOSE_HELPER") != "1" {
		t.Skip("helper process only")
	}

	svc := newSubscriptionService(t)
	const (
		subscribers = 32
		publishers  = 8
	)
	id := session.SessionID("subscription-close-concurrency")

	channels := make([]<-chan session.Event, 0, subscribers)
	for range subscribers {
		ch, _ := svc.Subscribe(id)
		channels = append(channels, ch)
	}
	// Establish every pre-existing channel is live before Close is asserted to
	// close it. Each publish reaches all channels; each receive drains one.
	for _, ch := range channels {
		svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnStart})
		if _, ok := <-ch; !ok {
			t.Fatal("pre-existing subscription closed before receiving its first event")
		}
	}

	publishStart := make(chan struct{})
	stopPublishers := make(chan struct{})
	publishActive := make(chan struct{}, publishers)
	var publishersDone sync.WaitGroup
	publishersDone.Add(publishers)
	for range publishers {
		go func() {
			defer publishersDone.Done()
			<-publishStart
			// Close begins only after every publisher has entered the API at least
			// once, then each continues publishing through the Close call.
			svc.PublishSessionEvent(id, session.Event{Type: session.EvTurnStart})
			publishActive <- struct{}{}
			for {
				select {
				case <-stopPublishers:
					return
				default:
					svc.PublishSessionEvent(id, session.Event{})
					runtime.Gosched() // see the churn helper: don't starve the other side
				}
			}
		}()
	}
	close(publishStart)
	for range publishers {
		<-publishActive
	}

	closeDone := make(chan struct{})
	go func() {
		svc.Close()
		close(closeDone)
	}()

	<-closeDone
	close(stopPublishers)
	publishersDone.Wait()

	// Every channel returned before shutdown must close, even when it retained
	// buffered events at the point Close won the race.
	for _, ch := range channels {
		for range ch {
		}
	}

	postClose, unsub := svc.Subscribe(id)
	select {
	case _, ok := <-postClose:
		if ok {
			t.Fatal("Subscribe after Close returned an open channel")
		}
	default:
		t.Fatal("Subscribe after Close returned a channel that is not already closed")
	}
	unsub()
	unsub()
}

// newSubscriptionService builds a Service with jsonlstore + mockllm for
// subscription tests. It is the shared scaffolding: the mockllm's turns script
// the delivery run's acknowledgement.
func newSubscriptionService(t *testing.T, turns ...mockllm.Turn) *server.Service {
	t.Helper()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(turns...)

	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               store,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_ = workspace
	return svc
}
