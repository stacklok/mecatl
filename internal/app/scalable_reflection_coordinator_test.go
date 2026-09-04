package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type coordinatorReflectorFunc func(context.Context, learning.Input) (learning.Outcome, error)

func (f coordinatorReflectorFunc) Reflect(ctx context.Context, input learning.Input) (learning.Outcome, error) {
	return f(ctx, input)
}

func TestScalableReflectionEvidence_Scenario2_SelectedEvidenceIdentityBoundary(t *testing.T) {
	release := make(chan struct{})
	started := make(chan session.SessionID, 4)
	reflector := &testReflector{start: started, release: release}
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 4})
	t.Cleanup(coordinator.Close)

	automatic := testJob("principal", "same-source", reflector)
	first, err := coordinator.Enqueue(automatic)
	if err != nil || first.Disposition != reflectionQueued {
		t.Fatalf("automatic enqueue = %+v, %v", first, err)
	}
	<-started

	explicit := testJob("principal", "same-source", reflector)
	explicit.input.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	explicit.input.Existing = []learning.ExistingFact{{Kind: learning.CandidateOperatorFact, Key: "style", Value: "concise"}}
	duplicate, err := coordinator.Enqueue(explicit)
	if err != nil || duplicate.Disposition != reflectionDuplicate || duplicate.ID != first.ID {
		t.Fatalf("same selected evidence did not singleflight: first=%+v duplicate=%+v err=%v", first, duplicate, err)
	}

	differentBoundary, err := coordinator.Enqueue(testJob("principal", "other-source", reflector))
	if err != nil || differentBoundary.Disposition != reflectionQueued || differentBoundary.ID == first.ID {
		t.Fatalf("different source boundary aliased: %+v, %v", differentBoundary, err)
	}
	differentEvidenceReceipt, err := coordinator.Enqueue(selectedTestJob("principal", "same-source", "Remember a different preference", reflector))
	if err != nil || differentEvidenceReceipt.Disposition != reflectionQueued || differentEvidenceReceipt.ID == first.ID {
		t.Fatalf("different selected evidence aliased: %+v, %v", differentEvidenceReceipt, err)
	}
	close(release)
}

func TestScalableReflectionEvidence_Scenario2_SelectedDigestDrivesProposalID(t *testing.T) {
	job := testJob("principal", "proposal-source", coordinatorReflectorFunc(func(_ context.Context, input learning.Input) (learning.Outcome, error) {
		ref, err := learning.MessageEvidenceRef(input, 0, "")
		if err != nil {
			return learning.Outcome{}, err
		}
		candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "style", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
		if err != nil {
			return learning.Outcome{}, err
		}
		return learning.Outcome{Kind: learning.OutcomeProposed, Candidates: []learning.Candidate{candidate}}, nil
	}))
	var mu sync.Mutex
	var proposalIDs []learning.ProposalID
	job.process = func(_ context.Context, digest string, outcome learning.Outcome) (reflectionReceipt, error) {
		id, err := learning.DeterministicProposalID(learning.ProposalPartition{Principal: "principal"}, digest, outcome.Candidates[0])
		if err == nil {
			mu.Lock()
			proposalIDs = append(proposalIDs, id)
			mu.Unlock()
		}
		return reflectionReceipt{Staged: 1}, err
	}

	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1})
	t.Cleanup(coordinator.Close)
	for attempt := 0; attempt < 2; attempt++ {
		current := job
		if attempt == 1 {
			current.input.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
			current.input.Existing = []learning.ExistingFact{{Kind: learning.CandidateOperatorFact, Key: "existing", Value: "comparison only"}}
		}
		queued, err := coordinator.Enqueue(current)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Wait(context.Background(), queued.ID); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	ids := append([]learning.ProposalID(nil), proposalIDs...)
	mu.Unlock()
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("retry proposal IDs = %v, want one convergent ID", ids)
	}

	ref, err := learning.MessageEvidenceRef(job.input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(job.input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "style", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := learning.DeterministicProposalID(learning.ProposalPartition{Principal: "principal"}, job.input.Manifest.Digest, candidate)
	if err != nil || ids[0] != expected {
		t.Fatalf("proposal ID = %q, selected-digest ID = %q, err=%v", ids[0], expected, err)
	}
	otherPartition, _ := learning.DeterministicProposalID(learning.ProposalPartition{Principal: "other"}, job.input.Manifest.Digest, candidate)
	candidate.Value = "detailed"
	otherCandidate, _ := learning.DeterministicProposalID(learning.ProposalPartition{Principal: "principal"}, job.input.Manifest.Digest, candidate)
	if otherPartition == expected || otherCandidate == expected {
		t.Fatalf("partition/candidate identity did not affect proposal ID: expected=%q partition=%q candidate=%q", expected, otherPartition, otherCandidate)
	}
}

func TestADR_0298_OneSelectedInputOneProviderCall(t *testing.T) {
	release := make(chan struct{})
	started := make(chan session.SessionID, 2)
	reflector := &testReflector{start: started, release: release}
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1})
	t.Cleanup(coordinator.Close)
	firstJob := testJob("principal", "one-pass", reflector)
	first, err := coordinator.Enqueue(firstJob)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	secondJob := testJob("principal", "one-pass", reflector)
	secondJob.input.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	duplicate, err := coordinator.Enqueue(secondJob)
	if err != nil || duplicate.Disposition != reflectionDuplicate {
		t.Fatalf("selected input duplicate = %+v, %v", duplicate, err)
	}
	close(release)
	if _, err := coordinator.Wait(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	reflector.mu.Lock()
	calls := len(reflector.calls)
	reflector.mu.Unlock()
	if calls != 1 {
		t.Fatalf("one selected input made %d provider calls, want exactly one", calls)
	}
}

func TestADR_0298_CoordinatorResourceSafetyUnchanged(t *testing.T) {
	release := make(chan struct{})
	starts := make(chan session.SessionID, 8)
	reflector := &testReflector{start: starts, release: release}
	sample := testJob("a", "a1", reflector)
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{
		Workers: 1, Capacity: 3, PrincipalCapacity: 2, Receipts: 1,
		JobBytes: sample.selectedBytes, QueueBytes: sample.selectedBytes * 2, Timeout: time.Second,
	})
	t.Cleanup(coordinator.Close)
	if coordinator.cfg.Receipts < coordinator.cfg.Capacity+coordinator.cfg.Workers {
		t.Fatalf("receipt capacity %d is below queue+worker bound", coordinator.cfg.Receipts)
	}
	first, err := coordinator.Enqueue(sample)
	if err != nil || first.Disposition != reflectionQueued {
		t.Fatalf("first enqueue = %+v, %v", first, err)
	}
	<-starts
	duplicate, err := coordinator.Enqueue(sample)
	if err != nil || duplicate.Disposition != reflectionDuplicate || duplicate.ID != first.ID {
		t.Fatalf("singleflight = %+v, %v", duplicate, err)
	}
	for _, job := range []reflectionJob{testJob("a", "a2", reflector), testJob("b", "b1", reflector)} {
		queued, queueErr := coordinator.Enqueue(job)
		if queueErr != nil || queued.Disposition != reflectionQueued {
			t.Fatalf("bounded enqueue = %+v, %v", queued, queueErr)
		}
	}
	full, err := coordinator.Enqueue(testJob("b", "b2", reflector))
	if err != nil || full.Disposition != reflectionQueueFull {
		t.Fatalf("aggregate selected-byte/count bound = %+v, %v", full, err)
	}
	close(release)
	var order []session.SessionID
	for range 2 {
		select {
		case id := <-starts:
			order = append(order, id)
		case <-time.After(time.Second):
			t.Fatal("fair queue stalled")
		}
	}
	if len(order) != 2 || order[0] != "b1" || order[1] != "a2" {
		t.Fatalf("fair principal rotation = %v, want [b1 a2]", order)
	}
	if receipt, waitErr := coordinator.Wait(context.Background(), first.ID); waitErr != nil || receipt.Disposition != reflectionCompleted {
		t.Fatalf("completion receipt = %+v, %v", receipt, waitErr)
	}

	blocked := &testReflector{release: make(chan struct{})}
	timed := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Timeout: time.Millisecond})
	queued, err := timed.Enqueue(testJob("timeout", "selected", blocked))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := timed.Wait(context.Background(), queued.ID)
	if err != nil || receipt.Disposition != reflectionTimedOut {
		t.Fatalf("timeout receipt = %+v, %v", receipt, err)
	}
	closed := make(chan struct{})
	go func() { timed.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel and join workers")
	}
	if after, closeErr := timed.Enqueue(testJob("closed", "selected", blocked)); closeErr != nil || after.Disposition != reflectionClosed {
		t.Fatalf("post-close admission = %+v, %v", after, closeErr)
	}
}
