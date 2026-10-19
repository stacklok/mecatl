package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestManualDreamBuildFailureDiagnosticsAndWire(t *testing.T) {
	const memoryCanary = "private-memory-payload"
	const providerCanary = "Bearer provider-secret https://example.invalid/?token=secret"

	for _, tc := range []struct {
		name           string
		providerErr    error
		wantHTTPStatus bool
	}{
		{name: "hostile", providerErr: errors.New(providerCanary)},
		{name: "provider metadata", providerErr: dreamProviderMetadataError{}, wantHTTPStatus: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			memoryDir := t.TempDir()
			store, err := memory.New(memoryDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"a", "b"} {
				if _, err := store.Remember(ctx, tool.MemoryEntry{Key: key, Value: memoryCanary}, tool.MemoryCurrent{}); err != nil {
					t.Fatal(err)
				}
			}
			diag := &attrCapturingDiag{}
			built, err := buildIsolated(t, ctx, Config{
				Model: "model", Workspace: t.TempDir(), MemoryDir: memoryDir, UserModelDir: t.TempDir(), NoSoul: true,
				Diagnostics: diag, MockProvider: mockllm.New(mockllm.ErrorTurn(tc.providerErr)),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			before := diag.dump()

			_, err = server.NewHarnessServer(built.Service).GenerateDreamPlan(ctx, &mecatlv1.GenerateDreamPlanRequest{Target: "project_memory"})
			st := status.Convert(err)
			if st.Code() != codes.Internal || strings.Contains(st.Message(), providerCanary) || strings.Contains(st.Message(), memoryCanary) {
				t.Fatalf("unsafe wire error: %v", st)
			}
			found := false
			for _, detail := range st.Details() {
				if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "mecatl.stacklok.com" && info.GetReason() == "dream_generate_failed" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing stable ErrorInfo: %v", st)
			}

			logs := strings.TrimPrefix(diag.dump(), before)
			if !strings.Contains(logs, "dream generation failed") || !strings.Contains(logs, "stage planner") {
				t.Fatalf("no actionable stage diagnostic: %s", logs)
			}
			if got := strings.Contains(logs, "http_status 503"); got != tc.wantHTTPStatus {
				t.Fatalf("http status diagnostic = %v, want %v: %s", got, tc.wantHTTPStatus, logs)
			}
			for _, canary := range []string{memoryCanary, providerCanary, "private-code", "private-request-id", "https://", "token=secret", "memory"} {
				if strings.Contains(logs, canary) {
					t.Fatalf("diagnostics leaked %q: %s", canary, logs)
				}
			}
		})
	}
}

func TestManualDreamBuildDeadlineDiagnosticsAndWire(t *testing.T) {
	const canary = "private planner deadline https://example.invalid/?token=secret"
	ctx := context.Background()
	memoryDir := t.TempDir()
	store, err := memory.New(memoryDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := store.Remember(ctx, tool.MemoryEntry{Key: key, Value: "same"}, tool.MemoryCurrent{}); err != nil {
			t.Fatal(err)
		}
	}
	diag := &attrCapturingDiag{}
	built, err := buildIsolated(t, ctx, Config{
		Model: "model", Workspace: t.TempDir(), MemoryDir: memoryDir, UserModelDir: t.TempDir(), NoSoul: true,
		Diagnostics: diag, MockProvider: mockllm.New(mockllm.ErrorTurn(fmt.Errorf("%s: %w", canary, context.DeadlineExceeded))),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	before := diag.dump()

	_, err = server.NewHarnessServer(built.Service).GenerateDreamPlan(ctx, &mecatlv1.GenerateDreamPlanRequest{Target: "project_memory"})
	st := status.Convert(err)
	if st.Code() != codes.DeadlineExceeded || strings.Contains(st.Message(), canary) {
		t.Fatalf("unsafe deadline wire error: %v", st)
	}
	found := false
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "mecatl.stacklok.com" && info.GetReason() == "dream_deadline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing deadline ErrorInfo: %v", st)
	}
	logs := strings.TrimPrefix(diag.dump(), before)
	if !strings.Contains(logs, "stage deadline") || strings.Contains(logs, canary) || strings.Contains(logs, "token=secret") {
		t.Fatalf("unsafe or unclassified deadline diagnostic: %s", logs)
	}
}

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
		if _, err := store.Remember(ctx, entry, tool.MemoryCurrent{}); err != nil {
			t.Fatal(err)
		}
	}
	provider := mockllm.New(mockllm.TextTurn(`{"exact_duplicates":[],"synthesized_replacements":[{"survivor":"a","superseded":["b"],"Value":"reviewed replacement","Description":"reviewed description","reason":"combine complete facts"}]}`))
	built, err := buildIsolated(t, ctx, Config{
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
