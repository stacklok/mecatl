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

	if _, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), "m", 1); !errors.Is(err, ErrStaleRunControl) {
		t.Fatalf("generation loser = %v, want ErrStaleRunControl", err)
	}

	state.cancelling = true
	if _, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), "m", 2); !errors.Is(err, ErrStaleRunControl) {
		t.Fatalf("cancelling loser = %v, want ErrStaleRunControl", err)
	}
	state.cancelling = false

	svc.cfg.SessionLease = strictControlLease{}
	svc.leaseDisabled = false
	if _, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), "m", 2); !errors.Is(err, ErrSessionLeasedElsewhere) {
		t.Fatalf("lease loser = %v, want ErrSessionLeasedElsewhere", err)
	}

	svc.cfg.SessionLease = nil
	if _, err := svc.cancelRunSteer(t.Context(), id, "replacement", "m", 2); !errors.Is(err, ErrStaleRunControl) {
		t.Fatalf("replacement loser = %v, want ErrStaleRunControl", err)
	}

	ack, err := svc.cancelRunSteer(t.Context(), id, run.RunID(), "m", 2)
	if err != nil || ack.RunID != run.RunID() || ack.MessageID != "m" {
		t.Fatalf("exact live transition = (%+v, %v)", ack, err)
	}
	if ack.Outcome == "" {
		t.Fatal("successful retraction returned an unspecified outcome")
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
	entered := make(chan struct{})
	release := make(chan struct{})
	provider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			close(entered)
			<-release
		}),
	}, mockllm.TextTurn("done"))
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New("strict-control-run", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	eng := agent.NewEngine(agent.Deps{
		LLM:     provider,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), permstore.New()),
		Model:   "test-model",
	})
	run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go", RunID: "run-old"})
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
