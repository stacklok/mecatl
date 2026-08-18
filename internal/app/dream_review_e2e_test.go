package app

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestManualDreamBufconnEndToEnd(t *testing.T) {
	ctx := context.Background()
	memoryDir := t.TempDir()
	store, err := memory.New(memoryDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []tool.MemoryEntry{
		{Key: "a", Value: "old survivor", Description: "old description"},
		{Key: "b", Value: "source", Description: "source description"},
	} {
		if err := store.RememberEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	provider := mockllm.New(mockllm.TextTurn(`{"exact_duplicates":[],"synthesized_replacements":[{"survivor":"a","superseded":["b"],"Value":"reviewed replacement","Description":"reviewed description","reason":"combine complete facts"}]}`))
	built, err := Build(ctx, Config{
		Model: "model", Workspace: t.TempDir(), MemoryDir: memoryDir, UserModelDir: t.TempDir(), NoSoul: true,
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenAI {
				return provider
			}
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(built.Service))
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()
	conn, err := grpc.NewClient("passthrough:///dream-bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := mecatlv1.NewHarnessServiceClient(conn)

	generated, err := client.GenerateDreamPlan(ctx, &mecatlv1.GenerateDreamPlanRequest{Target: "project_memory"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("manual two-entry provider calls = %d, want 1", provider.Calls())
	}
	plan := generated.GetPlan()
	if plan.GetPlannedSourceCount() != 1 || len(plan.GetOperations()) != 1 || plan.GetOperations()[0].GetReplacement().GetValue() != "reviewed replacement" {
		t.Fatalf("review over gRPC = %+v", plan)
	}
	if source, found, err := store.Recall(ctx, "b"); err != nil || !found || source.Value != "source" {
		t.Fatalf("generation mutated source: found=%v source=%+v err=%v", found, source, err)
	}

	decided, err := client.DecideDreamPlan(ctx, &mecatlv1.DecideDreamPlanRequest{PlanId: plan.GetId(), Decision: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	receipt := decided.GetReceipt()
	if receipt.GetPlannedSourceCount() != 1 || receipt.GetAppliedSourceCount() != 1 || receipt.GetConflictedSourceCount()+receipt.GetSkippedSourceCount()+receipt.GetFailedSourceCount() != 0 {
		t.Fatalf("receipt over gRPC = %+v", receipt)
	}
	survivor, found, err := store.Inspect(ctx, "a")
	if err != nil || !found || survivor.Current.Value != plan.GetOperations()[0].GetReplacement().GetValue() || len(survivor.Revisions) != 2 {
		t.Fatalf("survivor after apply: found=%v record=%+v err=%v", found, survivor, err)
	}
	source, found, err := store.Inspect(ctx, "b")
	if err != nil || !found || source.Current.Status != tool.MemoryStatusDeleted || len(source.Revisions) != 2 {
		t.Fatalf("source tombstone after apply: found=%v record=%+v err=%v", found, source, err)
	}
}
