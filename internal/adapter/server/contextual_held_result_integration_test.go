package server_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type heldResultReviewer struct{}

type heldResultAllowPolicy struct{}

func (heldResultAllowPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}

func (heldResultAllowPolicy) Learn(session.SessionID, session.ToolCall) {}

func (heldResultReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	return job == agent.ReviewJobInbound, true
}

func (heldResultReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, nil
}

type heldResultRecorder struct {
	mu      sync.Mutex
	results []session.ToolResult
}

func (r *heldResultRecorder) ToolCall(_ session.SessionID, _ session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *heldResultRecorder) snapshot() []session.ToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.ToolResult(nil), r.results...)
}

func newHeldResultService(t *testing.T, secret string) (*server.Service, *memstore.Store, *memstore.EventLog, *heldResultRecorder, *scriptTool) {
	t.Helper()
	store := memstore.New()
	log := memstore.NewEventLog()
	recorder := &heldResultRecorder{}
	read := &scriptTool{name: "Read", readOnly: true, content: secret}
	cat := tool.NewCatalog()
	cat.MustRegister(read)
	llm := mockllm.New(mockllm.ToolCallTurn(call("read-1", "Read", `{"path":"a"}`)), mockllm.TextTurn("done"))
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: cat, Policy: heldResultAllowPolicy{}, ToolReviewer: heldResultReviewer{}, ToolCallRecorder: recorder, Interactive: true,
	})
	svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, EventLog: log, DefaultCapabilities: llm.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, store, log, recorder, read
}

func assertNoHeldSentinel(t *testing.T, secret string, store *memstore.Store, log *memstore.EventLog, recorder *heldResultRecorder, id session.SessionID, wire []*mecatlv1.ConverseResponse) {
	t.Helper()
	loaded, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range loaded.Conversation.Messages {
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, secret) {
			t.Fatalf("held sentinel reached SessionStore: %+v", msg.ToolResult)
		}
	}
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, secret) {
			t.Fatalf("held sentinel reached EventLog: %+v", ev.ToolResult)
		}
	}
	for _, result := range recorder.snapshot() {
		if strings.Contains(result.Content, secret) {
			t.Fatalf("held sentinel reached ToolCallRecorder: %+v", result)
		}
	}
	for _, response := range wire {
		raw, err := protojson.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("held sentinel reached gRPC stream before release: %s", raw)
		}
	}
}

func TestGRPCHeldResultIsPrivateUntilExactRelease(t *testing.T) {
	const secret = "HELD_RESULT_SENTINEL"
	svc, store, log, recorder, read := newHeldResultService(t, secret)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID(created.GetSessionId())
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "read"}}}); err != nil {
		t.Fatal(err)
	}
	var wire []*mecatlv1.ConverseResponse
	for {
		response, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wire = append(wire, response)
		ask := response.GetEvent().GetAsk()
		if ask == nil {
			continue
		}
		if ask.GetGuardrail().GetKind() != mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE {
			t.Fatalf("ask kind = %v", ask.GetGuardrail().GetKind())
		}
		assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
		refused := []*mecatlv1.ResumeApproval{
			{AskId: ask.GetAskId(), Allow: true},
			{AskId: ask.GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE, ReviewId: ask.GetGuardrail().GetReviewId(), GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_ACTION},
			{AskId: ask.GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS, ReviewId: ask.GetGuardrail().GetReviewId(), GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE},
		}
		for _, frame := range refused {
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: frame}}); err != nil {
				t.Fatal(err)
			}
			notice, err := stream.Recv()
			if err != nil || notice.GetEvent().GetType() != "control.refused" || !strings.Contains(notice.GetEvent().GetText(), "result release") {
				t.Fatalf("refusal=%+v err=%v", notice, err)
			}
			wire = append(wire, notice)
			assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: ask.GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE, ReviewId: ask.GetGuardrail().GetReviewId(), GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE}}}); err != nil {
			t.Fatal(err)
		}
		break
	}
	_ = stream.CloseSend()
	wireResults := 0
	for {
		response, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wire = append(wire, response)
		if strings.Contains(response.GetEvent().GetToolResult().GetContent(), secret) {
			wireResults++
		}
	}
	loaded, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	storedResults := 0
	for _, msg := range loaded.Conversation.Messages {
		if msg.ToolResult != nil && msg.ToolResult.Content == secret {
			storedResults++
		}
	}
	loggedResults := 0
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.ToolResult != nil && ev.ToolResult.Content == secret {
			loggedResults++
		}
	}
	recorded := recorder.snapshot()
	if read.runs() != 1 || loaded.Counters.ToolCalls != 1 || wireResults != 1 || storedResults != 1 || loggedResults != 1 || len(recorded) != 1 || recorded[0].Content != secret {
		t.Fatalf("runs=%d toolCalls=%d wire=%d store=%d log=%d recorder=%+v", read.runs(), loaded.Counters.ToolCalls, wireResults, storedResults, loggedResults, recorded)
	}
}

func TestGRPCHeldResultDenyDestroysPrivatePayload(t *testing.T) {
	const secret = "DENIED_HELD_SENTINEL"
	svc, store, log, recorder, read := newHeldResultService(t, secret)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID(created.GetSessionId())
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "read"}}}); err != nil {
		t.Fatal(err)
	}
	var wire []*mecatlv1.ConverseResponse
	for {
		response, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wire = append(wire, response)
		if ask := response.GetEvent().GetAsk(); ask != nil {
			assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: ask.GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY, ReviewId: ask.GetGuardrail().GetReviewId(), GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE}}}); err != nil {
				t.Fatal(err)
			}
			_ = stream.CloseSend()
			break
		}
	}
	for {
		response, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wire = append(wire, response)
	}
	assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
	if read.runs() != 1 {
		t.Fatalf("tool runs=%d, want one produced then withheld", read.runs())
	}
}

func TestGRPCHeldResultDisconnectDestroysPrivatePayload(t *testing.T) {
	const secret = "DISCONNECTED_HELD_SENTINEL"
	svc, store, log, recorder, read := newHeldResultService(t, secret)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	created, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID(created.GetSessionId())
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "read"}}}); err != nil {
		t.Fatal(err)
	}
	var wire []*mecatlv1.ConverseResponse
	for {
		response, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		wire = append(wire, response)
		if response.GetEvent().GetAsk() != nil {
			assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
			cancel()
			break
		}
	}
	for {
		_, recvErr := stream.Recv()
		if recvErr != nil {
			break
		}
	}
	assertNoHeldSentinel(t, secret, store, log, recorder, id, wire)
	if read.runs() != 1 {
		t.Fatalf("tool runs=%d, want one produced then destroyed", read.runs())
	}
}

func TestServiceRestartCannotReleaseOrRerunLostHeldResult(t *testing.T) {
	const secret = "RESTART_LOST_HELD_SENTINEL"
	svc, store, _, _, _ := newHeldResultService(t, secret)
	client, cleanup := dialGRPC(t, svc)
	ctx, cancel := context.WithCancel(context.Background())
	created, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID(created.GetSessionId())
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "read"}}}); err != nil {
		t.Fatal(err)
	}
	var askID string
	for askID == "" {
		response, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if ask := response.GetEvent().GetAsk(); ask != nil {
			askID = ask.GetAskId()
		}
	}
	parked, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if parked.State != session.StateAwaiting {
		t.Fatalf("persisted state=%q, want awaiting", parked.State)
	}
	cancel()
	for {
		_, recvErr := stream.Recv()
		if recvErr != nil {
			break
		}
	}
	cleanup()

	restartedStore := memstore.New()
	if err := restartedStore.Save(context.Background(), parked); err != nil {
		t.Fatal(err)
	}
	freshTool := &scriptTool{name: "Read", readOnly: true, content: secret}
	freshCatalog := tool.NewCatalog()
	freshCatalog.MustRegister(freshTool)
	freshLLM := mockllm.New(mockllm.TextTurn("done after restart"))
	freshEngine := agent.NewEngine(agent.Deps{LLM: freshLLM, Catalog: freshCatalog, Policy: heldResultAllowPolicy{}, ToolReviewer: heldResultReviewer{}, Interactive: true})
	restarted, err := newPlacementTestService(server.Config{Engine: freshEngine, Store: restartedStore, DefaultCapabilities: freshLLM.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	resumed, err := restarted.ApproveRun(context.Background(), id, askID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatal(err)
	}
	if resumed == nil {
		t.Fatal("restart approval did not produce a fail-closed continuation run")
	}
	var leaked bool
	for ev := range resumed.Events() {
		leaked = leaked || (ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, secret))
	}
	restarted.FinishRun(id, resumed)
	if leaked || freshTool.runs() != 0 {
		t.Fatalf("lost held result leaked=%v reruns=%d", leaked, freshTool.runs())
	}
	loaded, err := restartedStore.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range loaded.Conversation.Messages {
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, secret) {
			t.Fatalf("lost held result reached restarted store: %+v", msg.ToolResult)
		}
	}
}

var _ port.ToolCallRecorder = (*heldResultRecorder)(nil)
