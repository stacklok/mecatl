package app

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/automaticstore"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
)

type reservationOrderAttemptRepository struct {
	learning.AttemptRepository
	ledger learning.AutomaticAdmissionLedger
	mu     sync.Mutex
	held   bool
}

func (r *reservationOrderAttemptRepository) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	id, err := learning.AutomaticReservationIDForAttempt(create.ID)
	if err == nil {
		reservation, found, getErr := r.ledger.Get(ctx, id)
		r.mu.Lock()
		r.held = getErr == nil && found && reservation.Charge == learning.AutomaticChargeHeld && !reservation.AttemptCreated
		r.mu.Unlock()
	}
	return r.AttemptRepository.Create(ctx, partition, create)
}

func (r *reservationOrderAttemptRepository) sawHeldReservation() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held
}

func TestCloudNativeLearning_Scenario6_WeightedAdmissionUsesAttemptLifecycle(t *testing.T) {
	ctx := context.Background()
	owner := &session.Principal{Issuer: "test", Subject: "weighted-owner", GrantType: session.GrantTypeUser}
	messages := []session.Message{
		session.NewUserMessage("Run the established workflow"),
		{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "a", Name: "Read"}, {ID: "b", Name: "Grep"}}},
		{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c", Name: "Read"}, {ID: "d", Name: "Grep"}}},
		{Role: session.RoleAssistant, Text: "done"},
	}
	trajectory := learning.NewTrajectory("weighted-source", "/workspace", session.StopEndTurn, session.Usage{}, messages)
	trajectory.Principal = owner
	trajectory.RunID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	trajectory.Kind = session.SessionKindMain
	trajectory.Counters = session.Counters{Turns: 4, ToolCalls: 4}
	trajectory.Current = learning.MessageSpan{Start: 0, End: len(messages)}

	sources := memstore.New()
	source := session.New(trajectory.SessionID, session.ModeDefault, trajectory.Workspace, session.Limits{}, time.Unix(1, 0))
	source.Owner = owner.Clone()
	source.BeginRun(trajectory.RunID)
	if err := sources.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	attempts, err := attemptstore.New(filepath.Join(t.TempDir(), "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := automaticstore.New(filepath.Join(t.TempDir(), "automatic"))
	if err != nil {
		t.Fatal(err)
	}
	orderedAttempts := &reservationOrderAttemptRepository{AttemptRepository: attempts, ledger: ledger}
	proposals, err := reflectionstore.New(filepath.Join(t.TempDir(), "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newReflectionCoordinator(ctx, reflectionCoordinatorConfig{Workers: 1, Capacity: 2, Timeout: time.Second})
	t.Cleanup(coordinator.Close)
	provider := mockllm.New(mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/workflow","value":"Use the established workflow","description":"Established workflow preference","evidence":["m:0"]}]}`))
	cfg := Config{
		Model: "mock", LearningMode: learning.Review, LearningSensitivity: learning.Balanced,
		LearningAutomatic: defaultLearningAutomaticConfig(), attemptRepository: orderedAttempts,
		automaticAdmissionLedger: ledger, learningSourceStore: sources,
	}
	observer, ok := buildReflectionObserver(cfg, provider, cfg.Model, memmemory.New(), nil, proposals, coordinator, nil).(*reflectionObserver)
	if !ok {
		t.Fatal("weighted reflection observer was not built")
	}
	if err := observer.Observe(ctx, trajectory); err != nil {
		t.Fatal(err)
	}

	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var record learning.AttemptRecord
	for time.Now().Before(deadline) {
		page, listErr := attempts.List(ctx, partition, learning.AttemptList{})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(page.Records) == 1 {
			record = page.Records[0]
			if record.State.Terminal() {
				break
			}
		}
		runtime.Gosched()
	}
	if !orderedAttempts.sawHeldReservation() {
		t.Fatal("durable attempt was created before its global automatic reservation was held")
	}
	if record.State != learning.AttemptCompleted || record.Outcome != learning.AttemptOutcomeSucceeded || record.ProposalID == "" {
		t.Fatalf("weighted durable attempt = %+v, want completed success with downstream proposal link", record)
	}
	if record.Provenance.Class != learning.AdmissionWeighted {
		t.Fatalf("weighted durable attempt class = %q, want %q", record.Provenance.Class, learning.AdmissionWeighted)
	}
	reservationID, err := learning.AutomaticReservationIDForAttempt(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	reservation, found, err := ledger.Get(ctx, reservationID)
	if err != nil || !found {
		t.Fatalf("automatic reservation found=%v err=%v", found, err)
	}
	if reservation.Charge != learning.AutomaticChargeRetained || !reservation.AttemptCreated || reservation.AttemptID != record.ID {
		t.Fatalf("automatic reservation = %+v, want retained charge linked to the durable attempt", reservation)
	}

	release := make(chan struct{})
	started := make(chan session.SessionID, 1)
	blocked := &testReflector{start: started, release: release}
	capacityGate := newReflectionCoordinator(ctx, reflectionCoordinatorConfig{Workers: 1, Capacity: 1, PrincipalCapacity: 1, Timeout: time.Second})
	defer capacityGate.Close()
	if first, enqueueErr := capacityGate.Enqueue(testJob("weighted-owner", "running", blocked)); enqueueErr != nil || first.Disposition != reflectionQueued {
		t.Fatalf("seed capacity gate = %+v, err=%v", first, enqueueErr)
	}
	<-started
	reserveCalled := false
	overflow := testJob("weighted-owner", "overflow", blocked)
	overflow.reserve = func() bool { reserveCalled = true; return true }
	full, enqueueErr := capacityGate.Enqueue(overflow)
	close(release)
	if enqueueErr != nil || full.Disposition != reflectionQueueFull || reserveCalled {
		t.Fatalf("capacity result=%+v err=%v reserve_called=%v; want queue_full before reservation/create", full, enqueueErr, reserveCalled)
	}
}
