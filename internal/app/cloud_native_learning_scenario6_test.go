package app

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type reservationOrderAttemptRepository struct {
	learning.AttemptRepository
	ledger   learning.AutomaticAdmissionLedger
	mu       sync.Mutex
	retained bool
}

func (r *reservationOrderAttemptRepository) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	id, err := learning.AutomaticReservationIDForAttempt(create.ID)
	if err == nil {
		reservation, found, getErr := r.ledger.Get(ctx, id)
		r.mu.Lock()
		r.retained = getErr == nil && found && reservation.Charge == learning.AutomaticChargeRetained && reservation.AttemptCreated
		r.mu.Unlock()
	}
	return r.AttemptRepository.Create(ctx, partition, create)
}

func (r *reservationOrderAttemptRepository) sawRetainedReservation() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retained
}

func TestCloudNativeLearning_Scenario6_WeightedAdmissionUsesAttemptLifecycle(t *testing.T) {
	ctx := context.Background()
	owner := &session.Principal{Issuer: "test", Subject: "weighted-owner", GrantType: session.GrantTypeUser}
	calls := []session.ToolCall{
		{ID: "a", Name: "Read"}, {ID: "b", Name: "Grep"},
		{ID: "c", Name: "Read"}, {ID: "d", Name: "Grep"},
	}
	messages := []session.Message{
		session.NewUserMessage("Run the established workflow"),
		session.NewAssistantMessage("", "", calls[:2]),
		session.NewToolMessage(session.NewToolResult("a", "first read")),
		session.NewToolMessage(session.NewToolResult("b", "first grep")),
		session.NewAssistantMessage("", "", calls[2:]),
		session.NewToolMessage(session.NewToolResult("c", "second read")),
		session.NewToolMessage(session.NewToolResult("d", "second grep")),
		session.NewAssistantMessage("done", "", nil),
	}
	trajectory := learning.NewTrajectory("weighted-source", "/workspace", session.StopEndTurn, session.Usage{}, messages)
	trajectory.Principal = owner
	trajectory.RunID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	trajectory.Kind = session.SessionKindMain
	trajectory.Counters = session.Counters{Turns: 3, ToolCalls: 4}
	trajectory.Current = learning.MessageSpan{Start: 0, End: len(messages)}

	sources := memstore.New()
	events := memstore.NewEventLog()
	source := session.New(trajectory.SessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: trajectory.Workspace, Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	source.Owner = owner.Clone()
	source.BeginRun(trajectory.RunID)
	if err := sources.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	sequence := []session.Event{
		{Type: session.EvUserPrompt, Seq: 1, RunID: trajectory.RunID, UserPrompt: &session.UserPromptPayload{Text: messages[0].Text}},
		{Type: session.EvTurnStart, Seq: 2, RunID: trajectory.RunID, Turn: 0},
		{Type: session.EvToolCall, Seq: 3, RunID: trajectory.RunID, Turn: 0, ToolCall: &calls[0]},
		{Type: session.EvToolCall, Seq: 4, RunID: trajectory.RunID, Turn: 0, ToolCall: &calls[1]},
		{Type: session.EvToolResult, Seq: 5, RunID: trajectory.RunID, Turn: 0, ToolResult: messages[2].ToolResult},
		{Type: session.EvToolResult, Seq: 6, RunID: trajectory.RunID, Turn: 0, ToolResult: messages[3].ToolResult},
		{Type: session.EvTurnStart, Seq: 7, RunID: trajectory.RunID, Turn: 1},
		{Type: session.EvToolCall, Seq: 8, RunID: trajectory.RunID, Turn: 1, ToolCall: &calls[2]},
		{Type: session.EvToolCall, Seq: 9, RunID: trajectory.RunID, Turn: 1, ToolCall: &calls[3]},
		{Type: session.EvToolResult, Seq: 10, RunID: trajectory.RunID, Turn: 1, ToolResult: messages[5].ToolResult},
		{Type: session.EvToolResult, Seq: 11, RunID: trajectory.RunID, Turn: 1, ToolResult: messages[6].ToolResult},
		{Type: session.EvTurnStart, Seq: 12, RunID: trajectory.RunID, Turn: 2},
		{Type: session.EvMessageDelta, Seq: 13, RunID: trajectory.RunID, Turn: 2, Text: "done"},
		{Type: session.EvResult, Seq: 14, RunID: trajectory.RunID, Turn: 2, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	for _, event := range sequence {
		if err := events.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	attempts, err := attemptstore.New(filepath.Join(t.TempDir(), "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	ledger := automaticStoreForTest(t, filepath.Join(t.TempDir(), "automatic"), defaultLearningAutomaticConfig())
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
		PlacementProvider: appTestPlacementProvider{root: trajectory.Workspace}, PlacementScope: "test",
	}
	userMemory := memmemory.New()
	observer, ok := buildReflectionObserver(cfg, provider, cfg.Model, userMemory, nil, proposals, coordinator, nil).(*reflectionObserver)
	if !ok {
		t.Fatal("weighted reflection observer was not built")
	}
	registry := regForTest(provider, providerOpenAI, cfg.Model)
	recovery := newAttemptRecoveryLoop(ctx, orderedAttempts, 5*time.Millisecond, func(recoveryCtx context.Context, item learning.AttemptWork) error {
		return recoverAttempt(recoveryCtx, cfg, registry, sources, events, orderedAttempts, proposals, catalogAssets{
			userModelStore:       userMemory,
			reflectionRepository: proposals,
		}, appTestPlacementProvider{}, "legacy-local", item)
	}, nil)
	t.Cleanup(recovery.Close)
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
	if !orderedAttempts.sawRetainedReservation() {
		t.Fatal("durable attempt was created before its global automatic charge was retained")
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
	coordinator.mu.Lock()
	coordinatorStarted := coordinator.started
	coordinator.mu.Unlock()
	if coordinatorStarted {
		t.Fatal("weighted durable attempt entered the legacy process-local coordinator")
	}
}

func TestCloudNativeLearning_Scenario6_NoPrematureGlobalBoundClaim(t *testing.T) {
	capture := func(t *testing.T, ledger learning.AutomaticAdmissionLedger) prompt.Layered {
		t.Helper()
		var captured prompt.Layered
		provider := mockllm.NewWith([]mockllm.Option{
			mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req.System }),
		}, mockllm.TextTurn("ok"))
		cfg := Config{Model: "gpt-5", LearningMode: learning.Auto,
			operatorLearningMode: learning.Auto, operatorLearningSensitivity: learning.Balanced,
			operatorSkillActivationPolicy: learning.SkillActivationValidated}
		factory := sessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider,
			memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
			prompt.RootAssembler{}, catalogAssets{automaticAdmissionLedger: ledger}, nil)
		result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = result.Close() }()
		run := result.Engine.Run(context.Background(), session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Unix(1, 0)), memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
		for range run.Events() {
		}
		return captured
	}

	unwired := capture(t, nil)
	if !strings.Contains(unwired.StablePrefix, learningAutomaticProcessLocalPostureNote) {
		t.Fatalf("unwired StablePrefix = %q, want ADR-0114 process-local limitation", unwired.StablePrefix)
	}
	if strings.Contains(unwired.StablePrefix, learningAutomaticGlobalPostureNote) {
		t.Fatalf("unwired StablePrefix claims global automatic bounds: %q", unwired.StablePrefix)
	}

	ledger := automaticStoreForTest(t, filepath.Join(t.TempDir(), "automatic"), defaultLearningAutomaticConfig())
	durable := capture(t, ledger)
	if !strings.Contains(durable.StablePrefix, learningAutomaticGlobalPostureNote) {
		t.Fatalf("durable-ledger StablePrefix = %q, want global automatic bounds", durable.StablePrefix)
	}
	if strings.Contains(durable.StablePrefix, learningAutomaticProcessLocalPostureNote) {
		t.Fatalf("durable-ledger StablePrefix retains ADR-0114 process-local limitation: %q", durable.StablePrefix)
	}
}
