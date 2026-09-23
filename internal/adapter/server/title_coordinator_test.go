package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type titleGeneratorFunc func(context.Context, []string) TitleGenerationResult

func (f titleGeneratorFunc) Generate(ctx context.Context, sources []string) TitleGenerationResult {
	return f(ctx, sources)
}

func TestTitleCoordinatorCommitDoesNotOverwriteConversationSavedByActiveRun(t *testing.T) {
	base := memstore.New()
	store := &titleCommitBarrierStore{
		Store:   base,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	var requestsMu sync.Mutex
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		requestsMu.Lock()
		requests = append(requests, req)
		firstRequest := len(requests) == 1
		requestsMu.Unlock()
		if firstRequest {
			close(providerStarted)
			<-releaseProvider
		}
	})}, mockllm.TextTurn("done"), mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{
		LLM: provider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Store: store,
	})
	svc, err := NewService(Config{
		Engine: engine, Store: store, SharedEngineRoot: "/ws", PlacementProvider: titlePlacementProvider{}, PlacementScope: "test",
		TitleGenerator: titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult { return TitleGenerationResult{} }),
		Now:            func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	sess := pendingTitleSession(t, store, "title-chat-save-race")

	_, attemptID, _, claimed, _ := svc.titleCoordinator.claim(sess.ID)
	if !claimed {
		t.Fatal("title attempt was not claimed")
	}

	const latestPrompt = "context written by the active chat run"
	run, err := svc.StartRun(context.Background(), sess.ID, latestPrompt)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("chat run did not reach the provider")
	}

	commitDone := make(chan struct{})
	go func() {
		defer close(commitDone)
		svc.titleCoordinator.commit(sess.ID, attemptID, TitleGenerationResult{
			Title:   "Generated title",
			Outcome: session.TitleAttemptSucceeded,
		})
	}()

	close(releaseProvider)
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("title commit did not reach its snapshot save")
	}
	close(store.release)
	select {
	case <-commitDone:
	case <-time.After(time.Second):
		t.Fatal("title commit did not finish")
	}

	got, err := base.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range got.Conversation.Messages {
		if message.Text == latestPrompt {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("title commit overwrote the newer chat snapshot: conversation = %#v", got.Conversation.Messages)
	}

	followUp, err := svc.StartRun(context.Background(), sess.ID, "does the model receive prior context?")
	if err != nil {
		t.Fatal(err)
	}
	for range followUp.Events() {
	}
	svc.FinishRun(sess.ID, followUp)

	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(requests))
	}
	for _, message := range requests[1].Messages {
		if message.Text == latestPrompt {
			return
		}
	}
	t.Fatalf("follow-up request lost prior prompt: messages = %#v", requests[1].Messages)
}

type titleCommitBarrierStore struct {
	*memstore.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *titleCommitBarrierStore) Save(ctx context.Context, sess *session.Session) error {
	if sess.TitleGeneration == session.TitleGenerationGenerated {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.Save(ctx, sess)
}

func TestTitleCoordinatorLifecycleUpdatesAdvanceRevision(t *testing.T) {
	store := memstore.New()
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult { return TitleGenerationResult{} }))
	sess := pendingTitleSession(t, store, "revision")

	_, attemptID, _, claimed, _ := svc.titleCoordinator.claim(sess.ID)
	if !claimed || attemptID == "" {
		t.Fatalf("claim = %q, %t", attemptID, claimed)
	}
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil || loaded.TitleRevision != 3 {
		t.Fatalf("claimed revision = %d, err = %v; want 3", loaded.TitleRevision, err)
	}

	svc.titleCoordinator.commit(sess.ID, attemptID, TitleGenerationResult{Outcome: session.TitleAttemptDeferred})
	loaded, err = store.Load(context.Background(), sess.ID)
	if err != nil || loaded.TitleRevision != 4 {
		t.Fatalf("completed revision = %d, err = %v; want 4", loaded.TitleRevision, err)
	}

	loaded.SetTitleGeneration(session.TitleGenerationPending)
	loaded.ApplyTitleGeneration(session.TitleGenerationPending, append(loaded.TitleAttempts(), session.TitleAttempt{ID: "interrupted"}))
	if err := store.Save(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	_, _, _, claimed, _ = svc.titleCoordinator.claim(sess.ID)
	if claimed {
		t.Fatal("incomplete attempt was claimed")
	}
	loaded, err = store.Load(context.Background(), sess.ID)
	if err != nil || loaded.TitleGeneration != session.TitleGenerationExhausted || loaded.TitleRevision != 6 {
		t.Fatalf("interrupted revision = %d, generation = %q, err = %v; want 6/exhausted", loaded.TitleRevision, loaded.TitleGeneration, err)
	}
}

func titleTestPlacementProvider() PlacementProvider {
	return titlePlacementProvider{}
}

func TestSessionTitleGeneration_Scenario4_TwoPhaseAttemptAdmission(t *testing.T) {
	store := memstore.New()
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	generator := titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		<-finish
		return TitleGenerationResult{Title: "automatic", Outcome: session.TitleAttemptSucceeded}
	})
	svc := titleCoordinatorService(t, store, generator)
	sess := pendingTitleSession(t, store, "two-phase")
	svc.submitTitleGeneration(sess.ID)
	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator did not start")
	}
	if _, err := svc.RenameSession(context.Background(), sess.ID, "operator"); err != nil {
		t.Fatalf("RenameSession while generator streams: %v", err)
	}
	close(finish)
}

func TestSessionTitleGeneration_Scenario4_OperatorTitleWinsRace(t *testing.T) {
	store := memstore.New()
	started, finish := make(chan struct{}, 1), make(chan struct{})
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		<-finish
		return TitleGenerationResult{Title: "automatic", Outcome: session.TitleAttemptSucceeded}
	}))
	sess := pendingTitleSession(t, store, "operator-wins")
	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator did not start")
	}
	if _, err := svc.RenameSession(context.Background(), sess.ID, "operator title"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	close(finish)
	time.Sleep(10 * time.Millisecond)
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil || loaded.Title != "operator title" || loaded.TitleProvenance != session.TitleProvenanceOperator {
		t.Fatalf("late automatic result overwrote operator title: %#v, %v", loaded, err)
	}
}

func TestSessionTitleGeneration_Scenario4_InterruptionAndRetryPolicy(t *testing.T) {
	store := memstore.New()
	var calls atomic.Int32
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		calls.Add(1)
		return TitleGenerationResult{Outcome: session.TitleAttemptInterrupted, Err: context.Canceled}
	}))
	sess := pendingTitleSession(t, store, "interrupted")
	svc.submitTitleGeneration(sess.ID)
	deadline := time.After(time.Second)
	for {
		loaded, err := store.Load(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.TitleGeneration == session.TitleGenerationExhausted {
			if calls.Load() != 1 || loaded.TitleAttempts()[0].Outcome != session.TitleAttemptInterrupted {
				t.Fatalf("interrupted attempt = %#v, calls=%d", loaded.TitleAttempts(), calls.Load())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("interruption did not become terminal")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestSessionTitleGeneration_Scenario4_CoordinatorShutdownAndInventory(t *testing.T) {
	store := memstore.New()
	stopped := make(chan struct{})
	started := make(chan struct{}, 1)
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(ctx context.Context, _ []string) TitleGenerationResult {
		started <- struct{}{}
		<-ctx.Done()
		close(stopped)
		return TitleGenerationResult{Outcome: session.TitleAttemptInterrupted, Err: ctx.Err()}
	}))
	sess := pendingTitleSession(t, store, "shutdown")
	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator did not start")
	}
	svc.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Close did not join title worker")
	}
}

func TestSessionTitleGeneration_Scenario4_CloseJoinsPendingRetry(t *testing.T) {
	store := memstore.New()
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		return TitleGenerationResult{Outcome: session.TitleAttemptFailed, Retryable: true}
	}))
	sess := pendingTitleSession(t, store, "retry-close")
	svc.submitTitleGeneration(sess.ID)
	deadline := time.After(time.Second)
	for {
		loaded, err := store.Load(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.TitleAttempts()) == 1 && loaded.TitleAttempts()[0].Outcome == session.TitleAttemptFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("retry was not scheduled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	joined := make(chan struct{})
	go func() {
		svc.titleCoordinator.retryWG.Wait()
		close(joined)
	}()
	select {
	case <-joined:
		t.Fatal("retry was not tracked before shutdown")
	case <-time.After(10 * time.Millisecond):
	}
	svc.Close()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("Close did not join pending retry")
	}
}

func TestSessionTitleGeneration_Scenario4_StartupReconcilesPendingSession(t *testing.T) {
	backing := memstore.New()
	id := session.SessionID("startup-reconcile")
	pendingTitleSession(t, backing, id)
	store := titlePagerStore{Store: backing, id: id}
	started := make(chan struct{}, 1)
	titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		return TitleGenerationResult{Title: "reconciled", Outcome: session.TitleAttemptSucceeded}
	}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup reconciliation did not submit pending title")
	}
}

func TestSessionTitleGeneration_Scenario4_ReconciliationUsesOneCappedPageBestEffort(t *testing.T) {
	backing := memstore.New()
	included := pendingTitleSession(t, backing, "reconcile-included")
	excluded := pendingTitleSession(t, backing, "reconcile-excluded")
	pager := &titleReconcilePagerStore{Store: backing, included: included.ID, excluded: excluded.ID}
	started := make(chan struct{}, 1)
	titleCoordinatorService(t, pager, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		return TitleGenerationResult{Title: "reconciled", Outcome: session.TitleAttemptSucceeded}
	}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup reconciliation did not submit the first page candidate")
	}
	if got := pager.calls.Load(); got != 1 {
		t.Fatalf("metadata page calls = %d, want 1", got)
	}
	if got := pager.request; got.Limit != titleReconcileLimit || got.Cursor != nil {
		t.Fatalf("metadata request = %#v, want one initial capped page", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got := pager.calls.Load(); got != 1 {
		t.Fatalf("metadata page calls after next cursor = %d, want 1", got)
	}
	loaded, err := backing.Load(context.Background(), excluded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TitleGeneration != session.TitleGenerationPending {
		t.Fatalf("later-page session was reconciled: %#v", loaded)
	}
}

func TestSessionTitleGeneration_Scenario4_DeduplicatesThroughGeneration(t *testing.T) {
	store := memstore.New()
	started, finish := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		calls.Add(1)
		started <- struct{}{}
		<-finish
		return TitleGenerationResult{Title: "deduplicated", Outcome: session.TitleAttemptSucceeded}
	}))
	sess := pendingTitleSession(t, store, "active-dedup")
	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator did not start")
	}
	svc.submitTitleGeneration(sess.ID)
	if got, err := store.Load(context.Background(), sess.ID); err != nil || got.TitleGeneration != session.TitleGenerationPending || len(got.TitleAttempts()) != 1 || got.TitleAttempts()[0].Outcome != "" {
		t.Fatalf("duplicate submission changed live attempt: %#v, %v", got, err)
	}
	close(finish)
	deadline := time.After(time.Second)
	for calls.Load() != 1 {
		select {
		case <-deadline:
			t.Fatalf("generator calls = %d, want 1", calls.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestSessionTitleGeneration_Scenario4_AttemptIDsContinueAfterRestart(t *testing.T) {
	store := memstore.New()
	sess := pendingTitleSession(t, store, "attempt-id-restart")
	sess.RecordTitleAttempt(session.TitleAttempt{ID: "title-1", Outcome: session.TitleAttemptFailed})
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		return TitleGenerationResult{Outcome: session.TitleAttemptInterrupted, Err: context.Canceled}
	}))
	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator did not start")
	}
	deadline := time.After(time.Second)
	for {
		loaded, err := store.Load(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		attempts := loaded.TitleAttempts()
		if len(attempts) == 2 && attempts[1].Outcome != "" {
			if attempts[1].ID != "title-2" {
				t.Fatalf("new attempt ID = %q, want title-2", attempts[1].ID)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("attempt was not committed: %#v", attempts)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type titlePlacementProvider struct{}

func (titlePlacementProvider) Bind(_ context.Context, _ PlacementBindRequest) (PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)}, nil
}

func (titlePlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(req.Ref.ID), memledger.New(), nil)}, nil
}

type titlePagerStore struct {
	*memstore.Store
	id session.SessionID
}

func (s titlePagerStore) PageSessionMetadata(_ context.Context, _ port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	return port.SessionMetadataPage{Sessions: []port.SessionDiscoveryMeta{{ID: s.id, State: session.StateCompleted}}}, nil
}

type titleReconcilePagerStore struct {
	*memstore.Store
	included session.SessionID
	excluded session.SessionID
	calls    atomic.Int32
	request  port.SessionMetadataPageRequest
}

func (s *titleReconcilePagerStore) PageSessionMetadata(_ context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	s.calls.Add(1)
	s.request = request
	return port.SessionMetadataPage{
		Sessions:   []port.SessionDiscoveryMeta{{ID: s.included, State: session.StateCompleted}},
		NextCursor: &port.SessionMetadataCursor{Continuation: "more"},
	}, nil
}

func titleCoordinatorService(t *testing.T, store port.SessionStore, generator SessionTitleGenerator) *Service {
	t.Helper()
	svc, err := NewService(Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}), Store: store, PlacementProvider: titlePlacementProvider{}, PlacementScope: "test", TitleGenerator: generator, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSessionTitleGenerationDiagnosticsAreLifecycleSafe(t *testing.T) {
	store := memstore.New()
	diagnostics := newTitleDiagnostics()
	generator := titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		return TitleGenerationResult{
			Outcome:    session.TitleAttemptFailed,
			Err:        context.DeadlineExceeded,
			Usage:      session.Usage{InputTokens: 7},
			ProviderID: "provider-a", ModelID: "model-a",
			FailureClass: titleFailureDeadline, FailureStage: titleStageEstablishment,
		}
	})
	svc, err := NewService(Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}), Store: store, PlacementProvider: titlePlacementProvider{}, PlacementScope: "test", TitleGenerator: generator, Diagnostics: diagnostics, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess := pendingTitleSession(t, store, "diagnostic-safe")
	sess.RecordTitleSourcePrompt("source containing SECRET_PROMPT")
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	svc.submitTitleGeneration(sess.ID)
	deadline := time.After(time.Second)
	for !diagnostics.contains("session title generator completed") {
		select {
		case <-deadline:
			t.Fatal("missing completion diagnostic")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	joined := diagnostics.joined()
	for _, want := range []string{"sessiondiagnostic-safe", "outcomefailed", "failure_classdeadline", "failure_stagestream_establishment", "input_tokens7", "providerprovider-a", "modelmodel-a"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"SECRET_PROMPT", "context deadline exceeded", `"title"`} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("diagnostics leaked %q: %s", forbidden, joined)
		}
	}
}

type titleDiagnosticState struct {
	mu      sync.Mutex
	records []string
}
type titleDiagnostics struct {
	state *titleDiagnosticState
	bound []any
}

func newTitleDiagnostics() *titleDiagnostics {
	return &titleDiagnostics{state: &titleDiagnosticState{}}
}
func (d *titleDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	d.state.records = append(d.state.records, message+" "+fmt.Sprint(append(d.bound, args...)...))
}
func (d *titleDiagnostics) With(args ...any) port.Diagnostics {
	return &titleDiagnostics{state: d.state, bound: append(append([]any(nil), d.bound...), args...)}
}
func (d *titleDiagnostics) contains(message string) bool {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	for _, got := range d.state.records {
		if strings.HasPrefix(got, message+" ") {
			return true
		}
	}
	return false
}
func (d *titleDiagnostics) joined() string {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	return strings.Join(d.state.records, "\n")
}

func pendingTitleSession(t *testing.T, store port.SessionStore, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	sess.SetTitleGeneration(session.TitleGenerationPending)
	sess.RecordTitleSourcePrompt("source")
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSessionTitleGeneration_Scenario2_AutomaticWorkIsOutsideChatRun(t *testing.T) {
	store := memstore.New()
	started := make(chan struct{}, 1)
	generator := titleGeneratorFunc(func(context.Context, []string) TitleGenerationResult {
		started <- struct{}{}
		return TitleGenerationResult{Title: "Generated title", Outcome: session.TitleAttemptSucceeded, ProviderID: "title-provider", ModelID: "title-model", Usage: session.Usage{InputTokens: 3, OutputTokens: 2}}
	})
	svc, err := NewService(Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("main reply")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, PlacementProvider: titlePlacementProvider{}, PlacementScope: "test",
		TitleGenerator: generator, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sess := session.New("title-outside-run", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	sess.SetTitleGeneration(session.TitleGenerationPending)
	sess.RecordTitleSourcePrompt("fix the title coordinator")
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	usageBefore := sess.UsageFor(session.UsageKindMain)
	messagesBefore := append([]session.Message(nil), sess.Conversation.Messages...)

	svc.submitTitleGeneration(sess.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("title generator was not run")
	}
	deadline := time.After(time.Second)
	for {
		loaded, loadErr := store.Load(context.Background(), sess.ID)
		if loadErr != nil {
			t.Fatalf("Load: %v", loadErr)
		}
		if loaded.TitleGeneration == session.TitleGenerationGenerated {
			if got := loaded.UsageFor(session.UsageKindMain); got != usageBefore {
				t.Fatalf("main usage changed: got %#v, want %#v", loaded.UsageFor(session.UsageKindMain), usageBefore)
			}
			if len(loaded.Conversation.Messages) != len(messagesBefore) {
				t.Fatalf("automatic title generation added conversation messages: %#v", loaded.Conversation.Messages)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("generated title was not durably committed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
