package server

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type leaseLossUsageReviewer struct {
	entered chan<- struct{}
	release <-chan struct{}
	usage   session.AuxiliaryUsage
}

func (r leaseLossUsageReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.entered <- struct{}{}
	<-r.release
	return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, r.usage, nil
}

func TestLeaseLossRevokesAuxiliaryUsageBeforeCapabilityInvalidation(t *testing.T) {
	id := session.SessionID("lease-loss-usage-gap")
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New(id, session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	usage := session.Usage{InputTokens: 7}
	catalog := tool.NewCatalog()
	catalog.MustRegister(lifecycleMarkerTool{name: "Loop"})
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Loop", []byte(`{}`))), mockllm.TextTurn("done")),
		Catalog: catalog, Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		ToolReviewer: leaseLossUsageReviewer{entered: entered, release: release, usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
			session.UsageKindGuardrail: {Models: map[string]session.Usage{"provider/reviewer": usage}},
		}}},
	})
	run := engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "review"})
	runDone := make(chan struct{})
	go func() {
		for range run.Events() {
		}
		close(runDone)
	}()
	select {
	case <-entered:
	case <-runDone:
		t.Fatal("run ended without invoking the reviewer")
	case <-time.After(time.Second):
		run.Cancel(agent.CancelCauseRequested)
		close(release)
		t.Fatal("reviewer never started")
	}

	capability := NewSessionMutationCapability(true)
	capability.Grant(id)
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	defer cancelLease()
	held := &heldLease{ctx: leaseCtx, cancel: cancelLease, valid: true}
	svc := &Service{
		cfg:           Config{Store: memstore.New(), SessionLease: &auxiliaryUsageLease{}, Diagnostics: port.NopDiagnostics{}, MutationCapability: capability},
		heldLeases:    map[session.SessionID]*heldLease{id: held},
		lostOwnership: map[session.SessionID]struct{}{},
		runs:          map[session.SessionID]*runState{id: {run: run, sess: sess}},
	}
	// Hold Invalidate so the reviewer completes after the ownership notification
	// but before cancellation. The prior cancellation-only fence accepts its usage.
	capability.mu.RLock()
	lossDone := make(chan struct{})
	go func() {
		svc.onLeaseLost(context.Background(), id, held, errors.New("lease held by successor"))
		close(lossDone)
	}()
	deadline := time.Now().Add(time.Second)
	for capability.mu.TryRLock() {
		capability.mu.RUnlock()
		if time.Now().After(deadline) {
			capability.mu.RUnlock()
			close(release)
			run.Cancel(agent.CancelCauseRequested)
			select {
			case <-lossDone:
			case <-time.After(time.Second):
				t.Error("lease-loss handler remained blocked")
			}
			t.Fatal("lease loss never reached capability invalidation")
		}
		runtime.Gosched()
	}
	close(release)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		capability.mu.RUnlock()
		run.Cancel(agent.CancelCauseRequested)
		select {
		case <-lossDone:
		case <-time.After(time.Second):
			t.Error("lease-loss handler remained blocked")
		}
		t.Fatal("review did not complete before cancellation")
	}
	got := sess.UsageFor(session.UsageKindGuardrail)
	capability.mu.RUnlock()
	select {
	case <-lossDone:
	case <-time.After(time.Second):
		t.Fatal("lease-loss handler did not finish after invalidation")
	}
	if got != (session.Usage{}) {
		t.Fatalf("usage accepted between lease loss and cancellation: %+v", got)
	}
}

func TestMutationCapabilityInvalidateWaitsForAdmittedMutation(t *testing.T) {
	id := session.SessionID("mutation")
	capability := NewSessionMutationCapability(true)
	capability.Grant(id)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	mutated := make(chan error, 1)
	go func() {
		_, err := capability.withMutation(id, func() error {
			entered <- struct{}{}
			<-release
			return nil
		})
		mutated <- err
	}()
	<-entered

	invalidated := make(chan struct{})
	invalidateStarted := make(chan struct{})
	go func() {
		close(invalidateStarted)
		capability.Invalidate(id)
		close(invalidated)
	}()
	<-invalidateStarted
	deadline := time.Now().Add(time.Second)
	for capability.mu.TryRLock() {
		capability.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("invalidation did not begin waiting for the admitted mutation")
		}
		runtime.Gosched()
	}
	select {
	case <-invalidated:
		t.Fatal("invalidation overtook an admitted in-memory mutation")
	default:
	}
	close(release)
	if err := <-mutated; err != nil {
		t.Fatal(err)
	}
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not proceed after in-memory mutation completed")
	}
}
