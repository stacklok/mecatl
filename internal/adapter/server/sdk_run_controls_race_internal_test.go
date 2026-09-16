package server

import (
	"context"
	"errors"
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
