package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type gapMarkedLearningEventLog struct{ port.EventLog }

func (gapMarkedLearningEventLog) LearningEvidenceGap(context.Context, session.SessionID, learning.DurableRunID) (bool, error) {
	return true, nil
}

func TestADR_0254_WorkerSourceAuthorityFailsClosedWithoutIdentityOracle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	const runID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"

	store := memstore.New()
	source := session.New("source", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
	if err := source.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := source.SeedHistory([]session.Message{
		session.NewUserMessage("historical request"),
		session.NewAssistantMessage("historical answer", "", nil),
	}); err != nil {
		t.Fatal(err)
	}
	source.BeginRun(runID)
	if err := source.RecordUserPrompt("Create a skill from this workflow", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordAssistant(session.NewAssistantMessage("done", "private reasoning", nil)); err != nil {
		t.Fatal(err)
	}
	usage := session.Usage{InputTokens: 10, OutputTokens: 2}
	if err := source.RecordUsage(usage); err != nil {
		t.Fatal(err)
	}
	if err := source.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}

	events := memstore.NewEventLog()
	for _, event := range []session.Event{
		{Type: session.EvUserPrompt, Seq: 1, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: "Create a skill from this workflow"}},
		{Type: session.EvTurnStart, Seq: 2, RunID: runID},
		{Type: session.EvMessageDelta, Seq: 3, RunID: runID, Text: "done"},
		{Type: session.EvResult, Seq: 4, RunID: runID, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: usage}},
	} {
		if err := events.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}

	trajectory := learning.NewTrajectory(source.ID, source.Workspace, session.StopEndTurn, usage, source.Conversation.Messages)
	trajectory.RunID = runID
	trajectory.Kind = source.Kind
	trajectory.Counters = source.Counters
	trajectory.Current = learning.MessageSpan{Start: 2, End: len(trajectory.Messages)}
	input := learning.NewInput(trajectory, nil, []learning.Signal{{Kind: learning.SignalHostRequested}}, nil)
	digest, err := automaticTrajectoryDigest("", input)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := currentPromptBinding(input, learning.AdmissionHostRequested)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, learning.AttemptSource{
		SessionID: source.ID, RunID: runID, CanonicalDigest: learning.CanonicalDigest(digest),
	}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	record := learning.AttemptRecord{Provenance: provenance}

	projection, code := newLearningEvidenceLoader(store, events).Load(ctx, partition, record)
	if code != learning.FailureNone {
		t.Fatalf("valid exact source code = %q", code)
	}
	if len(projection.Messages) != len(source.Conversation.Messages) || len(projection.Events) != 4 {
		t.Fatalf("projection shape = %d messages, %d events", len(projection.Messages), len(projection.Events))
	}
	if projection.Messages[3].Text != "done" || projection.Messages[3].Text == source.Conversation.Messages[3].Reasoning {
		t.Fatalf("canonical projection leaked or lost fields: %+v", projection.Messages[3])
	}

	assertUnavailable := func(name string, loader *learningEvidenceLoader, p learning.AttemptPartition, attempt learning.AttemptRecord) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Helper()
			got, failure := loader.Load(session.WithPrincipal(ctx, &session.Principal{Issuer: "system", Subject: "root", GrantType: session.GrantTypeSystem}), p, attempt)
			if failure != learning.FailureEvidenceUnavailable {
				t.Fatalf("failure = %q, want %q", failure, learning.FailureEvidenceUnavailable)
			}
			if len(got.Messages) != 0 || len(got.Events) != 0 {
				t.Fatalf("unavailable source returned evidence: %+v", got)
			}
		})
	}

	assertUnavailable("missing identity oracle", newLearningEvidenceLoader(nil, events), partition, record)
	missingStore := memstore.New()
	assertUnavailable("missing source", newLearningEvidenceLoader(missingStore, events), partition, record)
	foreign, err := learning.DeriveAttemptPartition(reflectionPrincipal(&session.Principal{Issuer: "issuer", Subject: "bob"}))
	if err != nil {
		t.Fatal(err)
	}
	assertUnavailable("foreign owner despite system principal", newLearningEvidenceLoader(store, events), foreign, record)

	runMismatch := record
	runMismatch.Provenance.Source.RunID = "run_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	assertUnavailable("run mismatch", newLearningEvidenceLoader(store, events), partition, runMismatch)
	digestMismatch := record
	digestMismatch.Provenance.Source.CanonicalDigest = learning.CanonicalDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	assertUnavailable("digest mismatch", newLearningEvidenceLoader(store, events), partition, digestMismatch)

	assertUnavailable("gap marked", newLearningEvidenceLoader(store, gapMarkedLearningEventLog{EventLog: events}), partition, record)

	outOfOrder := memstore.NewEventLog()
	for _, event := range []session.Event{
		{Type: session.EvUserPrompt, Seq: 2, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: "Create a skill from this workflow"}},
		{Type: session.EvResult, Seq: 1, RunID: runID, Result: &session.ResultPayload{Stop: session.StopEndTurn, Usage: usage}},
	} {
		if err := outOfOrder.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	assertUnavailable("invalid event ordering", newLearningEvidenceLoader(store, outOfOrder), partition, record)

	compactedStore := memstore.New()
	compacted := session.New("compacted", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
	if err := compacted.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := compacted.SeedHistory([]session.Message{
		session.NewUserMessage(session.CompactionSummaryMarker + " old turns"),
	}); err != nil {
		t.Fatal(err)
	}
	compacted.BeginRun(runID)
	if err := compacted.RecordUserPrompt("Create a skill from this workflow", nil); err != nil {
		t.Fatal(err)
	}
	if err := compacted.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := compacted.RecordAssistant(session.NewAssistantMessage("done", "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := compacted.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := compactedStore.Save(ctx, compacted); err != nil {
		t.Fatal(err)
	}
	compactedTrajectory := learning.NewTrajectory(compacted.ID, compacted.Workspace, session.StopEndTurn, session.Usage{}, compacted.Conversation.Messages)
	compactedTrajectory.RunID = runID
	compactedTrajectory.Kind = compacted.Kind
	compactedTrajectory.Counters = compacted.Counters
	compactedTrajectory.Current = learning.MessageSpan{Start: 1, End: 3}
	compactedInput := learning.NewInput(compactedTrajectory, nil, []learning.Signal{{Kind: learning.SignalHostRequested}}, nil)
	compactedDigest, err := automaticTrajectoryDigest("", compactedInput)
	if err != nil {
		t.Fatal(err)
	}
	compactedPrompt, err := currentPromptBinding(compactedInput, learning.AdmissionHostRequested)
	if err != nil {
		t.Fatal(err)
	}
	compactedProvenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, learning.AttemptSource{SessionID: compacted.ID, RunID: runID, CanonicalDigest: learning.CanonicalDigest(compactedDigest)}, compactedPrompt)
	if err != nil {
		t.Fatal(err)
	}
	compactedEvents := memstore.NewEventLog()
	for _, event := range []session.Event{
		{Type: session.EvUserPrompt, Seq: 1, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: "Create a skill from this workflow"}},
		{Type: session.EvTurnStart, Seq: 2, RunID: runID},
		{Type: session.EvMessageDelta, Seq: 3, RunID: runID, Text: "done"},
		{Type: session.EvResult, Seq: 4, RunID: runID, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	} {
		if err := compactedEvents.Append(ctx, compacted.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	assertUnavailable("compacted without recoverable archive", newLearningEvidenceLoader(compactedStore, compactedEvents), partition, learning.AttemptRecord{Provenance: compactedProvenance})

	archiveEvents := memstore.NewEventLog()
	archiveHistory := []session.Message{
		session.NewUserMessage("archived private request"),
		session.NewAssistantMessage("archived private answer", "", nil),
		session.NewUserMessage("Create a skill from this workflow"),
	}
	for _, event := range []session.Event{
		{Type: session.EvUserPrompt, Seq: 1, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: "Create a skill from this workflow"}},
		{Type: session.EvCompaction, Seq: 2, RunID: runID},
		{Type: session.EvCompactionArchive, Seq: 3, RunID: runID, CompactionArchive: &session.CompactionArchivePayload{Replaced: archiveHistory}},
		{Type: session.EvTurnStart, Seq: 4, RunID: runID},
		{Type: session.EvMessageDelta, Seq: 5, RunID: runID, Text: "done"},
		{Type: session.EvResult, Seq: 6, RunID: runID, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	} {
		if err := archiveEvents.Append(ctx, compacted.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	archivedProjection, archivedCode := newLearningEvidenceLoader(compactedStore, archiveEvents).Load(ctx, partition, learning.AttemptRecord{Provenance: compactedProvenance})
	if archivedCode != learning.FailureNone || len(archivedProjection.Events) != 6 {
		t.Fatalf("recoverable archive result = code %q projection %+v", archivedCode, archivedProjection)
	}
	if archivedProjection.Events[2].Text != "" {
		t.Fatalf("raw archive crossed canonical boundary: %+v", archivedProjection.Events[2])
	}
}
