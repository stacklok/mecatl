package server

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type strictControlLease struct{}

func (strictControlLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	return port.Lease{}, port.ErrLeaseHeld
}
func (strictControlLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	return lease, nil
}
func (strictControlLease) Release(context.Context, port.Lease) error { return nil }

func TestSDKRunControls_Scenario4_CancelSteerRaceAndLeaseGates(t *testing.T) {
	id := session.SessionID("strict-cancel-steer")
	stored := awaitingControlSession(t, id)
	base := memstore.New()
	if err := base.Save(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	run := activeStrictControlRun(t)
	state := &runState{run: run, sess: stored, settled: make(chan struct{})}
	svc := &Service{
		cfg:                 Config{Store: base, MutationCapability: NewSessionMutationCapability(false)},
		runs:                map[session.SessionID]*runState{id: state},
		runEntryGenerations: map[session.SessionID]uint64{id: 2},
		heldLeases:          map[session.SessionID]*heldLease{},
	}

	seedSentinel := func(label string) string {
		t.Helper()
		messageID := "sentinel-" + label
		outcome, err := run.EnqueueSteerWithMessageID("pending "+label, nil, messageID)
		if err != nil || outcome != agent.SteerAccepted {
			t.Fatalf("seed %s sentinel = (%s, %v), want accepted", label, outcome, err)
		}
		return messageID
	}
	assertRejectedWithoutRetraction := func(label string, want error, reject func(string) error) {
		t.Helper()
		messageID := seedSentinel(label)
		if err := reject(messageID); !errors.Is(err, want) {
			t.Fatalf("%s loser = %v, want %v", label, err, want)
		}
		outcome, err := run.CancelSteer()
		if err != nil || outcome != agent.SteerRetracted {
			t.Fatalf("%s loser consumed sentinel: direct retraction = (%s, %v), want retracted", label, outcome, err)
		}
	}

	assertRejectedWithoutRetraction("generation", ErrStaleRunControl, func(messageID string) error {
		_, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), messageID, 1)
		return err
	})

	assertRejectedWithoutRetraction("cancelling", ErrStaleRunControl, func(messageID string) error {
		svc.mu.Lock()
		state.cancelling = true
		svc.mu.Unlock()
		_, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), messageID, 2)
		svc.mu.Lock()
		state.cancelling = false
		svc.mu.Unlock()
		return err
	})

	assertRejectedWithoutRetraction("cancel-signaled", ErrStaleRunControl, func(messageID string) error {
		state.persistMu.Lock()
		state.cancelSignaled = true
		state.persistMu.Unlock()
		_, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), messageID, 2)
		state.persistMu.Lock()
		state.cancelSignaled = false
		state.persistMu.Unlock()
		return err
	})

	assertRejectedWithoutRetraction("lease", ErrSessionLeasedElsewhere, func(messageID string) error {
		svc.cfg.SessionLease = strictControlLease{}
		svc.leaseDisabled = false
		_, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), messageID, 2)
		svc.cfg.SessionLease = nil
		return err
	})

	// Model the delayed-control case in the contract: a successor is now the
	// registered run, and the stale request still names the predecessor. Plant
	// the sentinel on the successor so the assertion proves it cannot be touched.
	replacement := activeStrictControlRunWithID(t, "run-successor")
	replacementState := &runState{run: replacement, sess: stored, settled: make(chan struct{})}
	const replacementMessageID = "sentinel-replacement"
	if outcome, err := replacement.EnqueueSteerWithMessageID("pending replacement", nil, replacementMessageID); err != nil || outcome != agent.SteerAccepted {
		t.Fatalf("seed replacement sentinel = (%s, %v), want accepted", outcome, err)
	}
	svc.mu.Lock()
	svc.runs[id] = replacementState
	svc.mu.Unlock()
	if _, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), replacementMessageID, 2); !errors.Is(err, ErrStaleRunControl) {
		t.Fatalf("replacement loser = %v, want ErrStaleRunControl", err)
	}
	if outcome, err := replacement.CancelSteer(); err != nil || outcome != agent.SteerRetracted {
		t.Fatalf("replacement loser consumed successor sentinel: direct retraction = (%s, %v), want retracted", outcome, err)
	}
	svc.mu.Lock()
	svc.runs[id] = state
	svc.mu.Unlock()

	messageID := seedSentinel("valid")
	ack, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), messageID, 2)
	if err != nil || ack.Outcome != agent.SteerRetracted || ack.RunID != run.RunID() || ack.MessageID != messageID {
		t.Fatalf("exact live transition = (%+v, %v)", ack, err)
	}
	if outcome, err := run.CancelSteer(); err != nil || outcome != agent.SteerNonePending {
		t.Fatalf("valid retraction left sentinel pending: direct retraction = (%s, %v), want none_pending", outcome, err)
	}
}

func TestSDKRunControls_CancellationWinnerDoesNotRetractPendingSteer(t *testing.T) {
	id := session.SessionID("strict-cancellation-winner")
	stored := awaitingControlSession(t, id)
	base := memstore.New()
	if err := base.Save(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	// activeStrictControlRun is held inside its provider observer until cleanup.
	// CancelRun therefore marks and signals cancellation while the engine cannot
	// yet reach its terminal steer drain, making this ordering deterministic.
	run := activeStrictControlRun(t)
	state := &runState{run: run, sess: stored, settled: make(chan struct{})}
	svc := &Service{
		cfg:                 Config{Store: base, MutationCapability: NewSessionMutationCapability(false)},
		runs:                map[session.SessionID]*runState{id: state},
		runEntryGenerations: map[session.SessionID]uint64{id: 1},
		heldLeases:          map[session.SessionID]*heldLease{},
	}
	const messageID = "sentinel-cancellation-winner"
	if outcome, err := run.EnqueueSteerWithMessageID("pending cancellation winner", nil, messageID); err != nil || outcome != agent.SteerAccepted {
		t.Fatalf("seed cancellation sentinel = (%s, %v), want accepted", outcome, err)
	}
	if ack, err := svc.CancelRun(t.Context(), id, run.RunID()); err != nil || ack.RunID != run.RunID() {
		t.Fatalf("CancelRun = (%+v, %v), want exact acknowledgement", ack, err)
	}

	stale := make(chan error, 1)
	go func() {
		_, err := svc.cancelRunSteer(context.Background(), id, run.RunID(), messageID, 1)
		stale <- err
	}()
	if err := <-stale; !errors.Is(err, ErrStaleRunControl) {
		t.Fatalf("retraction after cancellation won = %v, want ErrStaleRunControl", err)
	}
	if outcome, err := run.CancelSteer(); err != nil || outcome != agent.SteerRetracted {
		t.Fatalf("stale retraction consumed cancellation sentinel: direct retraction = (%s, %v), want retracted", outcome, err)
	}
}

func TestSDKRunControls_CancelAndDrainLinearizeAgainstPendingSteerRetraction(t *testing.T) {
	for _, trigger := range []string{"cancel", "drain"} {
		t.Run(trigger, func(t *testing.T) {
			id := session.SessionID("strict-retraction-" + trigger)
			stored := awaitingControlSession(t, id)
			base := memstore.New()
			if err := base.Save(t.Context(), stored); err != nil {
				t.Fatal(err)
			}
			run := pendingSteerControlRun(t)
			state := &runState{run: run, sess: stored, settled: make(chan struct{})}
			svc := &Service{
				cfg: Config{
					Store: base, MutationCapability: NewSessionMutationCapability(false),
					Diagnostics: port.NopDiagnostics{},
				},
				runs:                map[session.SessionID]*runState{id: state},
				runEntryGenerations: map[session.SessionID]uint64{id: 1},
				heldLeases:          map[session.SessionID]*heldLease{},
			}
			outcome, err := run.EnqueueSteerWithMessageID("sentinel", nil, "sentinel-message")
			if err != nil || outcome != agent.SteerAccepted {
				t.Fatalf("seed sentinel steer = (%s, %v), want accepted", outcome, err)
			}

			start := make(chan struct{})
			cancelled := make(chan error, 1)
			retracted := make(chan struct {
				ack RunSteerAcknowledgement
				err error
			}, 1)
			var settleOnce sync.Once
			go func() {
				<-start
				if trigger == "cancel" {
					_, cancelErr := svc.CancelRun(context.Background(), id, run.RunID())
					cancelled <- cancelErr
					return
				}
				go func() {
					for range run.Events() {
					}
					settleOnce.Do(func() { close(state.settled) })
				}()
				cancelled <- svc.GracefulDrain(context.Background())
			}()
			go func() {
				<-start
				retractAck, retractErr := svc.CancelRunSteer(context.Background(), id, run.RunID(), "sentinel-message")
				retracted <- struct {
					ack RunSteerAcknowledgement
					err error
				}{ack: retractAck, err: retractErr}
			}()
			close(start)
			if err := <-cancelled; err != nil {
				t.Fatalf("%s = %v", trigger, err)
			}
			result := <-retracted
			switch {
			case result.err == nil:
				if result.ack.Outcome != agent.SteerRetracted || result.ack.RunID != run.RunID() || result.ack.MessageID != "sentinel-message" {
					t.Fatalf("winning sentinel retraction = %+v, want exact retracted acknowledgement", result.ack)
				}
			case errors.Is(result.err, ErrStaleRunControl):
				// Cancellation won the persistMu transition.
			default:
				t.Fatalf("sentinel retraction race = %v, want exact success or ErrStaleRunControl", result.err)
			}
			state.persistMu.Lock()
			marked := state.cancelSignaled
			state.persistMu.Unlock()
			if !marked {
				t.Fatalf("%s reached Run.Cancel without the cancellation sentinel", trigger)
			}
			settleOnce.Do(func() { close(state.settled) })
		})
	}
}

type strictBlockingProvider struct {
	entered chan struct{}
}

func (p strictBlockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		close(p.entered)
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (strictBlockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func pendingSteerControlRun(t *testing.T) *agent.Run {
	t.Helper()
	entered := make(chan struct{})
	provider := strictBlockingProvider{entered: entered}
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New("strict-steer-run", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	eng := agent.NewEngine(agent.Deps{
		LLM:         provider,
		Catalog:     tool.NewCatalog(),
		Policy:      permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), permstore.New()),
		Model:       "test-model",
		EnableSteer: true,
	})
	run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go", RunID: "run-old"})
	t.Cleanup(func() {
		run.Cancel()
		for range run.Events() {
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("run did not enter the provider")
	}
	return run
}

func activeStrictControlRun(t *testing.T) *agent.Run {
	t.Helper()
	return activeStrictControlRunWithID(t, "run-old")
}

func activeStrictControlRunWithID(t *testing.T, runID string) *agent.Run {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			close(entered)
			<-release
		}),
	}, mockllm.TextTurn("done"))
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New(session.SessionID("strict-control-"+runID), session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	eng := agent.NewEngine(agent.Deps{
		LLM:         provider,
		Catalog:     tool.NewCatalog(),
		Policy:      permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), permstore.New()),
		Model:       "test-model",
		EnableSteer: true,
	})
	run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go", RunID: runID})
	t.Cleanup(func() {
		close(release)
		run.Cancel()
		for range run.Events() {
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("run did not enter the provider")
	}
	return run
}
