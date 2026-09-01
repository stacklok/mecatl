package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type projectionCapturingReflector struct {
	rawCalls    int
	projections []learning.Projection
}

func (r *projectionCapturingReflector) Reflect(context.Context, learning.Input) (learning.Outcome, error) {
	r.rawCalls++
	return learning.Outcome{Kind: learning.OutcomeAbstained}, nil
}

func (r *projectionCapturingReflector) ReflectProjection(_ context.Context, projection learning.Projection) (learning.Outcome, error) {
	r.projections = append(r.projections, projection)
	return learning.Outcome{Kind: learning.OutcomeAbstained}, nil
}

func (*projectionCapturingReflector) RequestTokenEstimate(learning.Input) (int, error) {
	return 1, nil
}

func TestADR_0259_DurableExecutionReloadsPersistedRunEvidence(t *testing.T) {
	ctx := context.Background()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	const runID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	const persistedPrompt = "Create a skill from the persisted workflow"

	sessions := memstore.New()
	source := session.New("source-first-process", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
	if err := source.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	source.BeginRun(runID)
	if err := source.RecordUserPrompt(persistedPrompt, nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordAssistant(session.NewAssistantMessage("persisted result", "private reasoning", nil)); err != nil {
		t.Fatal(err)
	}
	if err := source.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(ctx, source); err != nil {
		t.Fatal(err)
	}

	trajectory := learning.NewTrajectory(source.ID, source.Workspace, session.StopEndTurn, session.Usage{}, source.Conversation.Messages)
	trajectory.Principal = owner
	trajectory.RunID = runID
	trajectory.Kind = session.SessionKindMain
	trajectory.Counters = source.Counters
	trajectory.Current = learning.MessageSpan{Start: 0, End: len(trajectory.Messages)}

	attempts := memattempt.New(wallclock.Clock{})
	reflector := &projectionCapturingReflector{}
	coordinator := newReflectionCoordinator(ctx, reflectionCoordinatorConfig{Workers: 1, Timeout: time.Second})
	t.Cleanup(coordinator.Close)
	observer := &reflectionObserver{
		coordinator: coordinator,
		reflector:   reflector,
		repository:  memproposal.New(),
		attempts:    attempts,
		sourceStore: sessions,
		mode:        learning.Review,
		sensitivity: learning.Balanced,
	}

	receipt, err := observer.submit(ctx, trajectory, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != reflectionQueued || reflector.rawCalls != 0 {
		t.Fatalf("durable admission executed retained input: receipt=%+v raw_calls=%d", receipt, reflector.rawCalls)
	}
	coordinator.mu.Lock()
	coordinatorStarted := coordinator.started
	coordinator.mu.Unlock()
	if coordinatorStarted {
		t.Fatal("durable admission entered the legacy process-local coordinator")
	}

	// A caller retaining the original slice can mutate it after admission. Durable
	// execution must not observe this process-local copy.
	trajectory.Messages[0].Text = "MUTATED RETAINED INPUT MUST NOT EXECUTE"

	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := attempts.Get(ctx, partition, learning.AttemptID(receipt.ID))
	if err != nil || !found {
		t.Fatalf("durable attempt found=%v err=%v", found, err)
	}
	log := memstore.NewEventLog()
	loader := newLearningEvidenceLoader(sessions, log)
	if projection, failure := loader.Load(ctx, partition, record); failure != learning.FailureEvidenceUnavailable || len(projection.Messages) != 0 {
		t.Fatalf("reflection ran without terminal persisted event sequence: failure=%q projection=%+v", failure, projection)
	}
	if _, _, failure, loadErr := loader.loadForExecution(ctx, partition, record); failure != learning.FailureNone || !errors.Is(loadErr, errLearningEvidenceNotReady) {
		t.Fatalf("incomplete terminal evidence failure=%q err=%v; want retryable not-ready", failure, loadErr)
	}

	for _, event := range []session.Event{
		{Type: session.EvUserPrompt, Seq: 1, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: persistedPrompt}},
		{Type: session.EvTurnStart, Seq: 2, RunID: runID},
		{Type: session.EvMessageDelta, Seq: 3, RunID: runID, Text: "persisted result"},
		{Type: session.EvResult, Seq: 4, RunID: runID, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	} {
		if err := log.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	outcome, failure, err := reflectAttemptEvidence(ctx, loader, reflector, partition, record)
	if err != nil || failure != learning.FailureNone || outcome.Kind != learning.OutcomeAbstained {
		t.Fatalf("persisted reflection outcome=%+v failure=%q err=%v", outcome, failure, err)
	}
	if reflector.rawCalls != 0 || len(reflector.projections) != 1 {
		t.Fatalf("execution paths raw=%d canonical=%d", reflector.rawCalls, len(reflector.projections))
	}
	projectionText := ""
	for _, message := range reflector.projections[0].Messages {
		projectionText += message.Text
	}
	if !strings.Contains(projectionText, persistedPrompt) || strings.Contains(projectionText, "MUTATED RETAINED INPUT") {
		t.Fatalf("reflection projection did not come exclusively from persisted evidence: %q", projectionText)
	}
}
