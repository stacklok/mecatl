package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

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
				created, err := client.CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
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
				resp, err := http.Post(h.URL+"/v1/sessions", "application/json", strings.NewReader(`{"workspace":"/ws"}`))
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
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
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
