package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type queuedDebugProvider struct {
	mu    sync.Mutex
	turns []mockllm.Turn
}

func (p *queuedDebugProvider) enqueue(turns ...mockllm.Turn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns = append(p.turns, turns...)
}

func (*queuedDebugProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *queuedDebugProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	var turn mockllm.Turn
	if len(p.turns) > 0 {
		turn, p.turns = p.turns[0], p.turns[1:]
	}
	p.mu.Unlock()
	return func(yield func(port.Chunk, error) bool) {
		for _, chunk := range turn.Chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(chunk, nil) {
				return
			}
		}
		if turn.Err != nil {
			yield(port.Chunk{}, turn.Err)
		}
	}, nil
}

func debugAcceptanceGRPC(t *testing.T, svc *server.Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(svc))
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///debug-acceptance",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		grpcServer.Stop()
		t.Fatal(err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() { _ = conn.Close(); grpcServer.Stop() }
}

func converseDebugAcceptance(t *testing.T, client mecatlv1.HarnessServiceClient, id, prompt string, verdict mecatlv1.ApprovalVerdict) (map[string]string, int) {
	t.Helper()
	stream, err := client.Converse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: id, Text: prompt}}}); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	asks := 0
	for {
		response, err := stream.Recv()
		if err == io.EOF {
			return results, asks
		}
		if err != nil {
			t.Fatal(err)
		}
		ev := response.GetEvent()
		if result := ev.GetToolResult(); result != nil {
			results[result.GetCallId()] = result.GetContent()
		}
		if ask := ev.GetAsk(); ask != nil {
			asks++
			if verdict == mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED {
				t.Fatalf("unexpected approval ask for %s", ask.GetTool())
			}
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: ask.GetAskId(), Verdict: verdict}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestSessionDebuggerCrossBoundaryAcceptance is the offline assembled-feature gate.
// It drives target tool activity through Build and HTTP, restarts over the durable
// store, then drives target-bound evidence and selected streaming-HTTP MCP through
// Build, Service, gRPC, the agent loop, and the provider boundary.
func TestSessionDebuggerCrossBoundaryAcceptance(t *testing.T) {
	ctx := context.Background()
	storeDir, workspace := t.TempDir(), t.TempDir()

	targetProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("glob-1", "Glob", json.RawMessage(`{"pattern":"*.go"}`))),
		mockllm.TextTurn("target complete"),
	)
	built1, err := buildIsolated(t, ctx, Config{Workspace: workspace, StoreDir: storeDir, NoSoul: true, MockProvider: targetProvider})
	if err != nil {
		t.Fatalf("target Build: %v", err)
	}
	target, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.NewHTTPHandler(built1.Service))
	promptOverHTTP(t, httpServer.URL, string(target.ID), "inspect Go files", func(sseEvent) {})
	httpServer.Close()
	built1.Close()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	target, err = store.Load(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Reopen(); err != nil {
		t.Fatal(err)
	}
	if err := target.RecordUserPrompt("run team", nil); err != nil {
		t.Fatal(err)
	}
	if err := target.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := target.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall("team-call", "Team", json.RawMessage(`{}`))})); err != nil {
		t.Fatal(err)
	}
	if err := target.RecordToolResults([]session.ToolResult{session.NewToolResult("team-call", "done")}); err != nil {
		t.Fatal(err)
	}
	if err := target.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, target); err != nil {
		t.Fatal(err)
	}

	retained, err := session.NewSubagent("subagent-retained", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation(), "sub-call")
	if err != nil {
		t.Fatal(err)
	}
	if err := retained.SeedHistory([]session.Message{{Role: session.RoleUser, Text: "investigate failure"}, {Role: session.RoleAssistant, Text: "retained child finding"}}); err != nil {
		t.Fatal(err)
	}
	pruned, err := session.NewParallelBranch("parallel-pruned", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(3, 0), target.ID, target.Incarnation(), "parallel-call", 0)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := session.NewScheduled("sched--related", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(4, 0), "nightly", target.ID, target.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	team, err := session.NewTeamMember("team-related-member", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(5, 0), "team-1", "reviewer", target.ID, target.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	teamRelationship := team.Relationship
	teamRelationship.CallID = "team-call"
	if err := team.RestoreSessionMetadata(session.SessionKindTeamMember, teamRelationship); err != nil {
		t.Fatal(err)
	}
	unrelated := session.New("unrelated-secret", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(6, 0))
	for _, s := range []*session.Session{retained, pruned, scheduled, team, unrelated} {
		if err := store.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete(ctx, pruned.ID); err != nil {
		t.Fatal(err)
	}
	archive := []session.Message{{Role: session.RoleUser, Text: "pre-compaction request"}, {Role: session.RoleAssistant, Text: "pre-compaction answer"}}
	manifest := session.RequestManifestPayload{Provider: "mock", Model: "acceptance", ToolNames: []string{"Glob", "Subagent", "Parallel", "Team"}, MessageCount: 2, MessageBytes: 42}
	network := session.NetworkAttemptPayload{SessionID: target.ID, RunSerial: 1, Turn: 1, Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "timeout", ElapsedMs: 25, BackoffMs: 10}
	events := []session.Event{
		{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: archive}},
		{Type: session.EvRequestManifest, RunID: "target-run", Turn: 1, RequestManifest: &manifest},
		{Type: session.EvNetworkAttempt, RunID: "target-run", Turn: 1, NetworkAttempt: &network},
		{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "sub-call", ChildID: string(retained.ID), ChildIncarnation: retained.Incarnation()}},
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ParentCallID: "parallel-call", ChildID: string(pruned.ID), ChildIncarnation: pruned.Incarnation(), BranchIndex: 0}},
		{Type: session.EvTeamMember, Team: &session.TeamPayload{ParentCallID: "team-call", TeamID: "team-1", Member: "reviewer", MemberSessionID: string(team.ID), MemberIncarnation: team.Incarnation()}},
		{Type: session.EvScheduleFired, Schedule: &session.SchedulePayload{Kind: "fired", ScheduleName: "nightly", SessionID: scheduled.ID}},
	}
	for _, ev := range events {
		if err := store.Append(ctx, target.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Load(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)

	mcpURL, publishCalls := newDebugPublishMCPServer(t)
	debugProvider := &queuedDebugProvider{}
	debugProvider.enqueue(
		mockllm.ToolCallTurn(
			session.NewToolCall("status", "InspectSession", json.RawMessage(`{"view":"status"}`)),
			session.NewToolCall("related", "InspectSession", json.RawMessage(`{"view":"related"}`)),
			session.NewToolCall("delegation", "InspectSession", json.RawMessage(`{"view":"delegation"}`)),
			session.NewToolCall("history", "InspectSession", json.RawMessage(`{"view":"history"}`)),
			session.NewToolCall("manifest", "InspectSession", json.RawMessage(`{"view":"manifest"}`)),
			session.NewToolCall("network", "InspectSession", json.RawMessage(`{"view":"network"}`)),
		),
		mockllm.TextTurn("diagnosis and issue draft ready"),
	)
	built2, err := buildIsolated(t, ctx, Config{Workspace: workspace, StoreDir: storeDir, NoSoul: true, MockProvider: debugProvider, MCPServers: []mcp.ServerConfig{{Name: "github", URL: mcpURL}}, Posture: PostureYolo, PostureFlagSet: true, Interactive: true})
	if err != nil {
		t.Fatalf("restart Build: %v", err)
	}
	defer built2.Close()
	client, closeGRPC := debugAcceptanceGRPC(t, built2.Service)
	defer closeGRPC()
	created, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Profile: string(server.ProfileNoFS), DebugTargetSessionId: string(target.ID), DebugMcpServers: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	debugID := created.GetSessionId()
	root, _ := converseDebugAcceptance(t, client, debugID, "Diagnose and draft a GitHub issue; do not publish it.", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED)
	for call, wants := range map[string][]string{
		"status":  {`"latest_run_counters"`, `"lifetime_event_log"`},
		"related": {`"subagent"`, `"parallel"`, `"team"`, `"schedule"`, `"pruned"`},
		// delegation rows are proof-gated to currently-RETAINED direct lineage
		// (ADR 0299): the pruned parallel branch and the schedule kind (not yet
		// wired to a lineage-provable join) are correctly absent here, unlike
		// "related" above, which reports every direct edge including pruned ones.
		"delegation": {`"type":"subagent"`, `"type":"team"`},
		"history":    {`"compaction_archive"`, `"history_handle"`},
		"manifest":   {`"provider":"mock"`, `"model":"acceptance"`, `"message_count":2`, `"message_bytes":42`, `"Glob"`},
		"network":    {`"failure_class":"timeout"`, `"successful_attempts_timed":false`},
	} {
		for _, want := range wants {
			if !strings.Contains(root[call], want) {
				t.Fatalf("%s evidence missing %q: %s", call, want, root[call])
			}
		}
	}
	handleMatch := regexp.MustCompile(`"scope_handle":"([^"]+)","kind":"subagent"`).FindStringSubmatch(root["related"])
	if len(handleMatch) != 2 {
		t.Fatalf("retained child handle missing: %s", root["related"])
	}
	historyMatch := regexp.MustCompile(`"history_handle":"([^"]+)"`).FindStringSubmatch(root["history"])
	if len(historyMatch) != 2 {
		t.Fatalf("history handle missing: %s", root["history"])
	}
	debugProvider.enqueue(
		mockllm.ToolCallTurn(
			session.NewToolCall("child", "InspectSession", json.RawMessage(fmt.Sprintf(`{"view":"transcript","scope_handle":%q}`, handleMatch[1]))),
			session.NewToolCall("history-page", "InspectSession", json.RawMessage(fmt.Sprintf(`{"view":"history","history_handle":%q,"limit":1}`, historyMatch[1]))),
			session.NewToolCall("probe", "InspectSession", json.RawMessage(`{"view":"status","scope_handle":"unrelated-secret"}`)),
		),
		mockllm.TextTurn("child evidence checked"),
	)
	scoped, _ := converseDebugAcceptance(t, client, debugID, "Inspect the retained child and verify isolation.", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED)
	if !strings.Contains(scoped["child"], "retained child finding") || !strings.Contains(scoped["history-page"], `"view":"transcript"`) || !strings.Contains(scoped["probe"], "scope handle is unsupported, malformed, or stale") {
		t.Fatalf("scoped evidence/isolation failed: %+v", scoped)
	}

	debugProvider.enqueue(mockllm.TextTurn("Draft only: GitHub issue body"))
	_, _ = converseDebugAcceptance(t, client, debugID, "Prepare the final issue draft, but do not publish.", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED)
	if got := atomic.LoadInt32(publishCalls); got != 0 {
		t.Fatalf("remote mutations before publication approval = %d", got)
	}
	debugProvider.enqueue(mockllm.ToolCallTurn(session.NewToolCall("publish-1", "mcp__github__echo", json.RawMessage(`{"text":"publish issue"}`))), mockllm.TextTurn("published once"))
	_, asks := converseDebugAcceptance(t, client, debugID, "Publish the current issue draft now.", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE)
	if asks != 1 {
		t.Fatalf("first remote mutation approval asks = %d, want 1", asks)
	}
	if got := atomic.LoadInt32(publishCalls); got != 1 {
		t.Fatalf("approved remote mutations = %d, want 1", got)
	}
	debugProvider.enqueue(mockllm.ToolCallTurn(session.NewToolCall("publish-2", "mcp__github__echo", json.RawMessage(`{"text":"publish follow-up"}`))), mockllm.TextTurn("follow-up denied"))
	_, asks = converseDebugAcceptance(t, client, debugID, "Publish a follow-up.", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY)
	if asks != 1 {
		t.Fatalf("second remote mutation approval asks = %d, want 1", asks)
	}
	if got := atomic.LoadInt32(publishCalls); got != 1 {
		t.Fatalf("second remote mutation bypassed fresh approval: calls=%d", got)
	}

	after, err := store.Load(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("debug evidence or MCP journey mutated the target snapshot")
	}
}
