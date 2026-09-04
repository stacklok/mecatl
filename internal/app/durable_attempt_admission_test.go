package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
)

func durableAdmissionTrajectory(runID string) learning.Trajectory {
	messages := []session.Message{session.NewUserMessage("Create a skill from this verified workflow")}
	trajectory := learning.NewTrajectory("source-session", "/workspace", session.StopEndTurn, session.Usage{}, messages)
	trajectory.RunID = runID
	trajectory.Kind = session.SessionKindMain
	trajectory.Current = learning.MessageSpan{Start: 0, End: len(messages)}
	trajectory.Principal = &session.Principal{Issuer: "issuer", Subject: "caller"}
	return trajectory
}

func persistedAdmissionSource(t *testing.T, trajectory learning.Trajectory) *memstore.Store {
	t.Helper()
	store := memstore.New()
	source := session.New(trajectory.SessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	source.BeginRun(trajectory.RunID)
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestADR_0259_QueuedAttemptRequiresDurableRunIDAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	attempts, err := attemptstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	reflector := &testReflector{start: make(chan session.SessionID, 2), release: release}
	coordinator := newReflectionCoordinator(ctx, reflectionCoordinatorConfig{Workers: 1, Capacity: 2, Timeout: time.Second})
	t.Cleanup(func() {
		close(release)
		coordinator.Close()
	})
	trajectory := durableAdmissionTrajectory("run_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	observer := &reflectionObserver{
		coordinator: coordinator,
		reflector:   reflector,
		repository:  memproposal.New(),
		attempts:    attempts,
		sourceStore: persistedAdmissionSource(t, trajectory),
		mode:        learning.Review,
		policy:      learning.AlwaysPolicy{},
		sensitivity: learning.Balanced,
	}

	first, err := observer.submit(ctx, trajectory, nil, true, false)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	if first.Disposition != reflectionQueued || first.ID == "" {
		t.Fatalf("first receipt = %+v, want durable queued attempt", first)
	}

	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(trajectory.Principal))
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := attemptstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reloaded.Get(ctx, partition, learning.AttemptID(first.ID))
	if err != nil || !found {
		t.Fatalf("second-process Get() found=%v err=%v", found, err)
	}
	if record.State != learning.AttemptQueued && record.State != learning.AttemptRunning && !record.State.Terminal() {
		t.Fatalf("reloaded state = %q, want queued, running, or terminal", record.State)
	}
	if record.Provenance.Source.SessionID != trajectory.SessionID || string(record.Provenance.Source.RunID) != trajectory.RunID {
		t.Fatalf("source binding = %+v, want session=%q run=%q", record.Provenance.Source, trajectory.SessionID, trajectory.RunID)
	}
	expectedInput := learning.NewInput(trajectory, nil, []learning.Signal{{Kind: learning.SignalHostRequested}}, nil)
	expectedDigest, err := automaticTrajectoryDigest(expectedInput)
	if err != nil {
		t.Fatal(err)
	}
	if record.Provenance.Source.CanonicalDigest != learning.CanonicalDigest(expectedDigest) {
		t.Fatalf("canonical digest = %q, want exact %q", record.Provenance.Source.CanonicalDigest, expectedDigest)
	}
	promptRef, err := learning.MessageEvidenceRef(expectedInput, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Provenance.CurrentPrompt.Ordinal != 0 || record.Provenance.CurrentPrompt.Digest != learning.CanonicalDigest(promptRef.Digest) {
		t.Fatalf("current prompt binding = %+v, want ordinal 0 digest %q", record.Provenance.CurrentPrompt, promptRef.Digest)
	}

	duplicate, err := observer.submit(ctx, trajectory, nil, true, false)
	if err != nil {
		t.Fatalf("duplicate admission: %v", err)
	}
	if duplicate.ID != first.ID || (duplicate.Disposition != reflectionDuplicate && duplicate.Disposition != reflectionQueued) {
		t.Fatalf("duplicate receipt = %+v, want existing attempt %q", duplicate, first.ID)
	}
	page, err := reloaded.List(ctx, partition, learning.AttemptList{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != learning.AttemptID(first.ID) {
		t.Fatalf("durable attempts = %+v, want exactly existing %q", page.Records, first.ID)
	}

	for _, runID := range []string{"", "embedded-run-id", "run_unpersistedbutplausibleid"} {
		t.Run("reject_"+runID, func(t *testing.T) {
			repo := memattempt.New(wallclock.Clock{})
			candidate := *observer
			candidate.attempts = repo
			receipt, err := candidate.submit(ctx, durableAdmissionTrajectory(runID), nil, true, true)
			if err == nil {
				t.Fatalf("run ID %q admitted with receipt %+v", runID, receipt)
			}
			part, partErr := learning.DeriveAttemptPartition(reflectionPrincipal(candidateOwner(runID)))
			if partErr != nil {
				t.Fatal(partErr)
			}
			page, listErr := repo.List(ctx, part, learning.AttemptList{})
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(page.Records) != 0 || receipt.Disposition == reflectionQueued {
				t.Fatalf("run ID %q created/queued work: receipt=%+v records=%+v", runID, receipt, page.Records)
			}
		})
	}
}

func candidateOwner(runID string) *session.Principal {
	return durableAdmissionTrajectory(runID).Principal
}

func TestCloudNativeLearning_Scenario2_NonAdmittedWorkIsNotDurable(t *testing.T) {
	ctx := context.Background()
	repo := memattempt.New(wallclock.Clock{})
	var activities []learning.Activity
	coordinator := newReflectionCoordinator(ctx, reflectionCoordinatorConfig{Workers: 1})
	t.Cleanup(coordinator.Close)
	reflector := &testReflector{}
	observer := &reflectionObserver{
		coordinator: coordinator,
		reflector:   reflector,
		repository:  memproposal.New(),
		attempts:    repo,
		mode:        learning.Review,
		policy:      learning.NeverPolicy{},
		sensitivity: learning.Balanced,
		metrics:     func(activity learning.Activity) { activities = append(activities, activity) },
	}
	trajectory := durableAdmissionTrajectory("run_aaaaaaaaaaaaaaaaaaaaaaaaaa")

	receipt, err := observer.submit(ctx, trajectory, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != "" {
		t.Fatalf("non-admitted receipt = %+v, want immediate skipped status", receipt)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(trajectory.Principal))
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.List(ctx, partition, learning.AttemptList{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 0 {
		t.Fatalf("non-admitted completion created durable attempts: %+v", page.Records)
	}
	if len(reflector.calls) != 0 {
		t.Fatalf("non-admitted completion invoked reflector %d times", len(reflector.calls))
	}
	if len(activities) != 1 || activities[0].Kind != learning.ActivitySkipped || activities[0].Reason != learning.ReasonPolicyNever {
		t.Fatalf("content-free immediate activities = %+v, want one policy skip", activities)
	}
}
