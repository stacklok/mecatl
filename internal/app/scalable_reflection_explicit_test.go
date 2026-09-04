package app

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func newExplicitTestObserver(t *testing.T, reflector *automaticCaptureReflector) (*reflectionObserver, *reflectionCoordinator) {
	t.Helper()
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 2, Timeout: time.Second})
	t.Cleanup(coordinator.Close)
	return &reflectionObserver{
		coordinator: coordinator, reflector: reflector, repository: memproposal.New(),
		operatorMemory: memmemory.New(), mode: learning.Review,
	}, coordinator
}

func TestScalableReflectionEvidence_Scenario1_ExplicitLargeTrajectoryMatchesAutomatic(t *testing.T) {
	large := session.NewUserMessageWithParts("", []session.Content{{Data: []byte(strings.Repeat("x", defaultReflectionJobBytes+1))}})
	trajectory := automaticTrajectory("shared-selection", []session.Message{
		large,
		session.NewUserMessage("Please remember that Go files use gofmt"),
	}, learning.MessageSpan{Start: 1, End: 2})

	autoReflector := &automaticCaptureReflector{estimate: 8, called: make(chan learning.Input, 1)}
	automatic, _, _ := automaticTestObserver(t, autoReflector, learning.AlwaysPolicy{})
	if err := automatic.Observe(context.Background(), trajectory); err != nil {
		t.Fatal(err)
	}
	autoInput := <-autoReflector.called

	explicitReflector := &automaticCaptureReflector{called: make(chan learning.Input, 1)}
	explicit, _ := newExplicitTestObserver(t, explicitReflector)
	if _, err := explicit.Reflect(context.Background(), trajectory, false); err != nil {
		t.Fatalf("explicit reflection rejected excluded raw bytes: %v", err)
	}
	explicitInput := <-explicitReflector.called
	if autoInput.Manifest == nil || explicitInput.Manifest == nil || autoInput.Manifest.Digest != explicitInput.Manifest.Digest {
		t.Fatalf("selected evidence differs: automatic=%#v explicit=%#v", autoInput.Manifest, explicitInput.Manifest)
	}
}

func TestScalableReflectionEvidence_Scenario5_ExplicitClosedAbstentionReasons(t *testing.T) {
	for _, tc := range []struct {
		name       string
		trajectory learning.Trajectory
		want       learning.MaterializationReason
		maxBytes   int
	}{
		{"no eligible evidence", automaticTrajectory("empty", []session.Message{session.NewAssistantMessage("", "reasoning", nil)}, learning.MessageSpan{}), learning.MaterializationNoEligibleEvidence, defaultReflectionJobBytes},
		{"mandatory span exceeds bounds", automaticTrajectory("mandatory", []session.Message{session.NewUserMessage("required evidence")}, learning.MessageSpan{Start: 0, End: 1}), learning.MaterializationMandatorySpanExceedsBounds, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			materialized, err := learning.MaterializeEvidence(learning.MaterializationRequest{Trajectory: tc.trajectory, Mandatory: tc.trajectory.Current, Explicit: true, Limits: learning.MaterializationLimits{MaxBytes: tc.maxBytes}})
			if err != nil {
				t.Fatal(err)
			}
			if materialized.Disposition != learning.MaterializationAbstained || materialized.Reason != tc.want {
				t.Fatalf("materialization = (%q, %q), want (abstained, %q)", materialized.Disposition, materialized.Reason, tc.want)
			}
		})
	}

	reflector := &automaticCaptureReflector{called: make(chan learning.Input, 1)}
	observer, coordinator := newExplicitTestObserver(t, reflector)
	receipt, err := observer.Reflect(context.Background(), automaticTrajectory("closed", []session.Message{session.NewAssistantMessage("", "reasoning", nil)}, learning.MessageSpan{}), false)
	if err != nil || !receipt.Abstained {
		t.Fatalf("closed explicit result = %+v, err=%v", receipt, err)
	}
	coordinator.mu.Lock()
	started, receipts := coordinator.started, len(coordinator.receipts)
	coordinator.mu.Unlock()
	if started || receipts != 0 || len(reflector.inputs) != 0 {
		t.Fatalf("abstention reached coordinator: started=%v receipts=%d calls=%d", started, receipts, len(reflector.inputs))
	}
}

func TestADR_0298_MaterializationDispositionReasonAndErrorMatrix(t *testing.T) {
	selected, err := learning.MaterializeEvidence(learning.MaterializationRequest{Trajectory: automaticTrajectory("selected", []session.Message{session.NewUserMessage("remember gofmt")}, learning.MessageSpan{}), Explicit: true})
	if err != nil || selected.Disposition != learning.MaterializationSelected || selected.Reason != learning.MaterializationReasonSelected {
		t.Fatalf("selected = %+v, err=%v", selected, err)
	}
	if _, err := learning.MaterializeEvidence(learning.MaterializationRequest{}); !errors.Is(err, learning.ErrInvalidInput) {
		t.Fatalf("invalid request error = %v", err)
	}
}

func TestADR_0298_NonMaterializationFaultsRetainTypedMappings(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		got := explicitReflectionServiceError(want)
		if !errors.Is(got, want) {
			t.Fatalf("mapping %v = %v", want, got)
		}
	}
}

func TestScalableReflectionEvidence_Scenario7_ExplicitCancellationStopsMaterialization(t *testing.T) {
	reflector := &automaticCaptureReflector{called: make(chan learning.Input, 1)}
	observer, coordinator := newExplicitTestObserver(t, reflector)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := observer.Reflect(ctx, automaticTrajectory("cancelled", []session.Message{session.NewUserMessage("remember cancellation")}, learning.MessageSpan{}), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	coordinator.mu.Lock()
	started := coordinator.started
	coordinator.mu.Unlock()
	if started || len(reflector.inputs) != 0 {
		t.Fatalf("cancelled materialization admitted work: started=%v calls=%d", started, len(reflector.inputs))
	}
}

func TestScalableReflectionEvidence_Scenario7_BuiltCloseCancelsAndJoinsMaterialization(t *testing.T) {
	gate := newMaterializationLifecycle()
	op, err := gate.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	built := &Built{Close: gate.close}
	closed := make(chan struct{})
	go func() { built.Close(); close(closed) }()
	select {
	case <-op.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel active materialization")
	}
	select {
	case <-closed:
		t.Fatal("close returned before active materialization joined")
	default:
	}
	op.leave()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join materialization")
	}
}

func TestADR_0298_MaterializationCancelCloseRaceAndNoPerJobGoroutine(t *testing.T) {
	gate := newMaterializationLifecycle()
	before := runtime.NumGoroutine()
	const jobs = 64
	ops := make([]*materializationOperation, 0, jobs)
	for range jobs {
		op, err := gate.enter(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	if delta := runtime.NumGoroutine() - before; delta > 1 {
		t.Fatalf("enter started per-job goroutines: delta=%d", delta)
	}
	done := make(chan struct{})
	go func() { gate.close(); close(done) }()
	select {
	case <-ops[0].Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel operations")
	}
	for _, op := range ops {
		if !errors.Is(op.Err(), errReflectionMaterializationClosed) {
			t.Fatalf("operation error = %v", op.Err())
		}
		op.leave()
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel/close race did not join")
	}
	if _, err := gate.enter(context.Background()); !errors.Is(err, errReflectionMaterializationClosed) {
		t.Fatalf("post-close enter error = %v", err)
	}
}

func TestScalableReflectionEvidence_Scenario8_ExplicitUsesPersistedProviderModel(t *testing.T) {
	const persistedModel = "persisted-model"
	requests := make(chan port.LLMRequest, 1)
	provider := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests <- req })},
		mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`),
	)
	cfg := Config{Model: persistedModel, LearningMode: learning.Review}
	observer := buildExplicitReflectionObserver(cfg, provider, persistedModel, memmemory.New(), nil, memproposal.New(), nil)
	trajectory := automaticTrajectory("persisted-routing", []session.Message{session.NewUserMessage("remember model routing")}, learning.MessageSpan{})
	if _, err := observer.Reflect(context.Background(), trajectory, false); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		if req.Model != persistedModel {
			t.Fatalf("reflection model = %q, want %q", req.Model, persistedModel)
		}
	case <-time.After(time.Second):
		t.Fatal("selected provider was not called")
	}
}

func TestScalableReflectionEvidence_Scenario8_OffModeExplicitOnly(t *testing.T) {
	cfg := Config{Model: "model", LearningMode: learning.Off}
	provider := mockllm.New(mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
	if automatic := buildReflectionObserver(cfg, provider, cfg.Model, memmemory.New(), nil, memproposal.New(), nil, nil); automatic != nil {
		t.Fatal("off mode installed automatic reflection")
	}
	explicit := buildExplicitReflectionObserver(cfg, provider, cfg.Model, memmemory.New(), nil, memproposal.New(), nil)
	if explicit == nil {
		t.Fatal("off mode did not install explicit reflection")
	}
	trajectory := automaticTrajectory("off-explicit", []session.Message{session.NewUserMessage("remember explicit only")}, learning.MessageSpan{})
	if _, err := explicit.Reflect(context.Background(), trajectory, false); err != nil {
		t.Fatal(err)
	}
	if calls := provider.Calls(); calls != 1 {
		t.Fatalf("off-mode explicit provider calls = %d, want 1", calls)
	}
}
