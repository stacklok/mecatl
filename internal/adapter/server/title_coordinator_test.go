package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type titleGeneratorFunc func(context.Context, []string) TitleGenerationResult

func (f titleGeneratorFunc) Generate(ctx context.Context, sources []string) TitleGenerationResult {
	return f(ctx, sources)
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
	svc := titleCoordinatorService(t, store, titleGeneratorFunc(func(ctx context.Context, _ []string) TitleGenerationResult {
		<-ctx.Done()
		close(stopped)
		return TitleGenerationResult{Outcome: session.TitleAttemptInterrupted, Err: ctx.Err()}
	}))
	sess := pendingTitleSession(t, store, "shutdown")
	svc.submitTitleGeneration(sess.ID)
	svc.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Close did not join title worker")
	}
}

func titleCoordinatorService(t *testing.T, store *memstore.Store, generator SessionTitleGenerator) *Service {
	t.Helper()
	svc, err := NewService(Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}), Store: store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }, TitleGenerator: generator, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func pendingTitleSession(t *testing.T, store *memstore.Store, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
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
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		TitleGenerator: generator, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sess := session.New("title-outside-run", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	sess.SetTitleGeneration(session.TitleGenerationPending)
	sess.RecordTitleSourcePrompt("fix the title coordinator")
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	usageBefore := sess.Usage
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
			if loaded.Usage != usageBefore {
				t.Fatalf("main usage changed: got %#v, want %#v", loaded.Usage, usageBefore)
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
