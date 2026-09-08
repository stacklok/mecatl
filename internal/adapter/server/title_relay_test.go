package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestSessionTitleGeneration_PromptIngressPublishesAfterSuccessfulPersistOnce(t *testing.T) {
	store := memstore.New()
	events := memstore.NewEventLog()
	svc, err := NewService(Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, EventLog: events, SharedEngineRoot: "/ws", PlacementProvider: titlePlacementProvider{}, PlacementScope: "test",
		TitleGenerationEligible: func(ProviderSelector) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before := titleEventCount(t, events, sess.ID)
	live, unsubscribe, err := svc.Subscribe(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	run, err := svc.StartRun(context.Background(), sess.ID, "name this session")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	persisted, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sources := persisted.TitleSourcePrompts(); len(sources) != 1 || sources[0] != "name this session" {
		t.Fatalf("persisted title sources = %#v, want prompt ingress metadata", sources)
	}

	select {
	case event := <-live:
		if event.Type != session.EvSessionTitle || event.Title == nil || event.Title.Revision != persisted.TitleRevision {
			t.Fatalf("live title event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing post-save title event")
	}
	if got, want := titleEventCount(t, events, sess.ID), before+1; got != want {
		t.Fatalf("durable title events = %d, want %d", got, want)
	}

	svc.Persist(context.Background(), sess.ID)
	select {
	case event := <-live:
		t.Fatalf("duplicate title event after unchanged persist: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
	if got, want := titleEventCount(t, events, sess.ID), before+1; got != want {
		t.Fatalf("durable title events after unchanged persist = %d, want %d", got, want)
	}
}

func TestSessionTitleGeneration_PromptIngressSaveFailureDoesNotPublish(t *testing.T) {
	store := &titleFailSaveStore{Store: memstore.New()}
	events := memstore.NewEventLog()
	svc, err := NewService(Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, EventLog: events, SharedEngineRoot: "/ws", PlacementProvider: titlePlacementProvider{}, PlacementScope: "test",
		TitleGenerationEligible: func(ProviderSelector) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before := titleEventCount(t, events, sess.ID)
	live, unsubscribe, err := svc.Subscribe(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	run, err := svc.StartRun(context.Background(), sess.ID, "name this session")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	store.fail = true
	svc.Persist(context.Background(), sess.ID)

	select {
	case event := <-live:
		t.Fatalf("title event after failed save: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
	if got := titleEventCount(t, events, sess.ID); got != before {
		t.Fatalf("durable title events after failed save = %d, want %d", got, before)
	}
}

func titleEventCount(t *testing.T, events *memstore.EventLog, id session.SessionID) int {
	t.Helper()
	count := 0
	for event, err := range events.Read(context.Background(), id) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == session.EvSessionTitle {
			count++
		}
	}
	return count
}

type titleFailSaveStore struct {
	*memstore.Store
	fail bool
}

func (s *titleFailSaveStore) Save(ctx context.Context, sess *session.Session) error {
	if s.fail {
		return fmt.Errorf("save failed")
	}
	return s.Store.Save(ctx, sess)
}

func TestSessionTitleGeneration_NormalTransportRunsPersistAndSubmit(t *testing.T) {
	for _, transport := range []string{"grpc", "http"} {
		t.Run(transport, func(t *testing.T) {
			store := memstore.New()
			svc := titleTransportService(t, store)

			var id string
			switch transport {
			case "grpc":
				client, cleanup := dialTitleGRPC(t, svc)
				defer cleanup()
				created, err := client.CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{})
				if err != nil {
					t.Fatal(err)
				}
				id = created.GetSessionId()
				stream, err := client.Converse(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: id, Text: "name this"}}}); err != nil {
					t.Fatal(err)
				}
				_ = stream.CloseSend()
				drainTitleConverse(t, stream)
			case "http":
				h := httptest.NewServer(NewHTTPHandler(svc))
				defer h.Close()
				resp, err := http.Post(h.URL+"/v1/sessions", "application/json", strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				var created struct {
					SessionID string `json:"session_id"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				id = created.SessionID
				resp, err = http.Post(h.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"name this"}`))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}
			waitForGeneratedTitle(t, store, session.SessionID(id))
		})
	}
}

func TestSessionTitleGeneration_ReconcilesStrandedCompletedSnapshot(t *testing.T) {
	store := memstore.New()
	stranded := pendingTitleSession(t, store, "stranded-title")
	if err := stranded.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := stranded.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), stranded); err != nil {
		t.Fatal(err)
	}
	titleCoordinatorService(t, store, titleGeneratorFunc(func(_ context.Context, _ []string) TitleGenerationResult {
		return TitleGenerationResult{Title: "reconciled", Outcome: session.TitleAttemptSucceeded}
	}))
	waitForGeneratedTitle(t, store, stranded.ID)
}

func TestSessionTitleGeneration_ReconciliationSkipsCrashUnknownClaim(t *testing.T) {
	store := memstore.New()
	claimed := pendingTitleSession(t, store, "claimed-title")
	if err := claimed.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := claimed.Complete(); err != nil {
		t.Fatal(err)
	}
	claimed.RecordTitleAttempt(session.TitleAttempt{ID: "unknown"})
	if err := store.Save(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	titleCoordinatorService(t, store, titleGeneratorFunc(func(_ context.Context, _ []string) TitleGenerationResult {
		calls.Add(1)
		return TitleGenerationResult{Title: "must not run", Outcome: session.TitleAttemptSucceeded}
	}))
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reconciliation retried crash-unknown title claim %d times", calls.Load())
	}
}

func titleTransportService(t *testing.T, store *memstore.Store) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, SharedEngineRoot: "/ws", PlacementProvider: titleTestPlacementProvider(), PlacementScope: "test",
		TitleGenerationEligible: func(ProviderSelector) bool { return true },
		TitleGenerator: titleGeneratorFunc(func(_ context.Context, _ []string) TitleGenerationResult {
			return TitleGenerationResult{Title: "generated", Outcome: session.TitleAttemptSucceeded}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func waitForGeneratedTitle(t *testing.T, store *memstore.Store, id session.SessionID) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		sess, err := store.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if sess.TitleGeneration == session.TitleGenerationGenerated {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("title was not generated: %#v", sess)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
