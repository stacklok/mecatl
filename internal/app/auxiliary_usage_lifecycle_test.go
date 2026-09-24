package app

import (
	"context"
	"errors"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// This uses the extracted result seam called directly by Build's ReflectSession
// closure. A full Build would add unrelated provider, catalog, placement, and
// background-worker setup without getting closer to the lifecycle-error branch.
func TestExplicitReflectionLifecycleErrorRetainsUsageThroughService(t *testing.T) {
	usage := session.Usage{InputTokens: 12, OutputTokens: 4}
	aux := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindReflection: {
			Total:  usage,
			Models: map[string]session.Usage{"reflection-provider/reflection-model": usage},
		},
	}}
	store := memstore.New()
	id := session.SessionID("reflection-lifecycle-usage")
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "test"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	reflector := func(context.Context, *session.Session) (server.ReflectionReceipt, error) {
		lifecycle := newMaterializationLifecycle()
		op, err := lifecycle.enter(context.Background())
		if err != nil {
			return server.ReflectionReceipt{}, err
		}
		closed := make(chan struct{})
		go func() {
			lifecycle.close()
			close(closed)
		}()
		<-op.Done()
		receipt, resultErr := explicitReflectionServiceResult(reflectionReceipt{
			Disposition: reflectionCompleted,
			Usage:       aux,
		}, nil, op.Err())
		op.leave()
		<-closed
		return receipt, resultErr
	}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: childPermPolicy(Config{})})
	svc, err := newTestServerService(server.Config{
		Engine: engine, Store: store, ReflectSession: reflector, Diagnostics: port.NopDiagnostics{},
		MutationCapability: server.NewSessionMutationCapability(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	if _, err := svc.ReflectSession(t.Context(), id); !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("ReflectSession error = %v, want lifecycle unavailable", err)
	}
	persisted, err := store.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	bucket := persisted.TokenUsageSnapshot()[session.UsageKindReflection]
	if bucket.Total != usage || bucket.Models["reflection-provider/reflection-model"] != usage {
		t.Fatalf("persisted lifecycle-error usage = %#v, want exact reflection attribution %+v", bucket, usage)
	}
	projected, err := server.NewHarnessServer(svc).GetSession(t.Context(), &mecatlv1.GetSessionRequest{SessionId: string(id)})
	if err != nil {
		t.Fatal(err)
	}
	got := projected.GetSession().GetTokenUsage()[string(session.UsageKindReflection)].GetModels()["reflection-provider/reflection-model"]
	if got.GetInputTokens() != int64(usage.InputTokens) || got.GetOutputTokens() != int64(usage.OutputTokens) {
		t.Fatalf("projected lifecycle-error usage = %+v, want %+v", got, usage)
	}
}
