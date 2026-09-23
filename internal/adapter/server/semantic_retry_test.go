package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type retryFailure struct {
	d session.RetryDisposition
	p session.StreamProgress
}

func (*retryFailure) Error() string                                { return "provider failed" }
func (e *retryFailure) RetryDisposition() session.RetryDisposition { return e.d }
func (e *retryFailure) StreamProgress() session.StreamProgress     { return e.p }

func sseTerminalResultJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var terminal map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("decode SSE event %q: %v", data, err)
		}
		if event["type"] == string(session.EvResult) {
			var resultOK bool
			terminal, resultOK = event["result"].(map[string]any)
			if !resultOK {
				t.Fatalf("terminal result payload = %#v", event["result"])
			}
		}
	}
	if terminal == nil {
		t.Fatalf("SSE body has no terminal result: %s", body)
	}
	return terminal
}

type blockingRetryLLM struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingRetryLLM) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }
func (p *blockingRetryLLM) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func failedSession(t *testing.T, llm *mockllm.Provider, id string) (*server.Service, session.SessionID) {
	t.Helper()
	svc := newService(t, llm, nil)
	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(session.SessionID(id)))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "one prompt")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run)
	return svc, sess.ID
}

func TestStartRunAwaitsContextWindowBeforePromptAndInference(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	entered := make(chan struct{})
	release := make(chan struct{}, 1)
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()
	var inference atomic.Int32
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { inference.Add(1) })}, mockllm.TextTurn("done"))
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "selected"})
	svc, err := newPlacementTestService(server.Config{
		Engine:               eng,
		Store:                store,
		DefaultResolvedModel: server.ResolvedModel{ProviderID: "provider", ModelID: "selected"},
		AwaitContextWindow: func(context.Context, string, string) error {
			close(entered)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		run *agent.Run
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		run, runErr := svc.StartRun(ctx, sess.ID, "must wait")
		resultCh <- result{run: run, err: runErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for admission callback")
	}
	blocked, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(blocked.Conversation.Messages); got != 0 || inference.Load() != 0 {
		t.Fatalf("work started before admission release: messages=%d inference=%d", got, inference.Load())
	}
	release <- struct{}{}
	var got result
	select {
	case got = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for admitted run")
	}
	if got.err != nil {
		t.Fatalf("StartRun: %v", got.err)
	}
	if text := drainServerRun(got.run); text != "done" {
		t.Fatalf("run result = %q, want done", text)
	}
	svc.FinishRun(sess.ID, got.run)
}

func TestRetryFailedRunEligibility(t *testing.T) {
	cases := []struct {
		name string
		turn mockllm.Turn
	}{
		{"permanent", mockllm.ErrorTurn(&retryFailure{session.RetryDispositionPermanent, session.StreamProgressPrecommit})},
		{"unknown", mockllm.ErrorTurn(errors.New("unknown"))},
		{"complete", mockllm.EmptyTurnWithStop(session.StopError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, id := failedSession(t, mockllm.New(tc.turn), "case-"+tc.name)
			if _, err := svc.RetryFailedRun(context.Background(), id); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
				t.Fatalf("RetryFailedRun = %v, want ErrFailedStepRetryIneligible", err)
			}
		})
	}

	svc := newService(t, mockllm.New(), nil)
	idle, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("nonfailed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RetryFailedRun(context.Background(), idle.ID); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
		t.Fatalf("nonfailed retry = %v", err)
	}

	blocking := &blockingRetryLLM{started: make(chan struct{}), release: make(chan struct{})}
	activeStore := memstore.New()
	activeEngine := agent.NewEngine(agent.Deps{LLM: blocking, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "active-model", MaxNoProgressNudges: -1})
	activeSvc, err := newPlacementTestService(server.Config{Engine: activeEngine, Store: activeStore})
	if err != nil {
		t.Fatal(err)
	}
	active, err := activeSvc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	activeRun, err := activeSvc.StartRun(context.Background(), active.ID, "active")
	if err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	activeSvc.Persist(context.Background(), active.ID)
	if _, err := activeSvc.RetryFailedRun(context.Background(), active.ID); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
		t.Fatalf("active retry = %v", err)
	}
	close(blocking.release)
	for range activeRun.Events() {
	}
	activeSvc.FinishRun(active.ID, activeRun)

	childSvc := newService(t, mockllm.New(), nil)
	child, err := childSvc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(agent.SubagentSessionPrefix+"child"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childSvc.RetryFailedRun(context.Background(), child.ID); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
		t.Fatalf("delegation child retry = %v", err)
	}

	scheduledSvc := newService(t, mockllm.New(), nil)
	scheduled, err := scheduledSvc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID("sched--retry"), server.WithScheduledRelationship("job", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduledSvc.RetryFailedRun(context.Background(), scheduled.ID); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
		t.Fatalf("scheduled retry = %v", err)
	}
}

func TestRetryFailedRunRehydratesPersistedSelector(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	selector := server.ProviderSelector{ProviderID: "provider-a", ModelID: "model-a"}
	factory := func(turn mockllm.Turn, seen *atomic.Value) server.SessionEngineFactory {
		return func(_ context.Context, got server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			seen.Store(got)
			return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(turn), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: got.ModelID}), ProviderID: got.ProviderID, ModelID: got.ModelID}, nil
		}
	}
	shared := func() *agent.Engine {
		return agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("wrong shared model")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "shared"})
	}
	var firstSeen atomic.Value
	svc1, err := newPlacementTestService(server.Config{Engine: shared(), Store: store, SessionEngine: factory(mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressPrecommit}), &firstSeen)})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc1.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, selector)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := svc1.StartRun(ctx, sess.ID, "one prompt")
	if err != nil {
		t.Fatal(err)
	}
	for range failed.Events() {
	}
	svc1.Persist(ctx, sess.ID)
	svc1.FinishRun(sess.ID, failed)

	// A restart-time engine resolution failure must not consume the durable
	// eligibility facts.
	failingFactory := func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{}, errors.New("factory unavailable")
	}
	failedSetupSvc, err := newPlacementTestService(server.Config{Engine: shared(), Store: store, SessionEngine: failingFactory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failedSetupSvc.RetryFailedRun(ctx, sess.ID); err == nil {
		t.Fatal("RetryFailedRun with failed engine resolution succeeded")
	}
	stillFailed, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := stillFailed.FailureMetadata()
	if stillFailed.State != session.StateFailed || metadata.Disposition != session.RetryDispositionRetryable || metadata.Progress != session.StreamProgressPrecommit {
		t.Fatalf("setup failure consumed eligibility: state=%s metadata=%+v", stillFailed.State, metadata)
	}

	var retrySeen atomic.Value
	var recoveredBeforeProvider atomic.Bool
	retryFactory := func(_ context.Context, got server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		retrySeen.Store(got)
		llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) {
			persisted, loadErr := store.Load(context.Background(), sess.ID)
			if loadErr == nil && persisted.State == session.StateIdle {
				metadata, pending := persisted.FailedStepRetryPending()
				recoveredBeforeProvider.Store(pending && metadata.Disposition == session.RetryDispositionRetryable && metadata.Progress == session.StreamProgressPrecommit)
			}
		})}, mockllm.TextTurn("same selector retried"))
		return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: got.ModelID}), ProviderID: got.ProviderID, ModelID: got.ModelID}, nil
	}
	admissionErr := errors.New("context window unavailable")
	rejectAdmission := true
	var admittedProvider, admittedModel string
	awaitWindow := func(_ context.Context, providerID, modelID string) error {
		admittedProvider, admittedModel = providerID, modelID
		if rejectAdmission {
			return admissionErr
		}
		return nil
	}
	svc2, err := newPlacementTestService(server.Config{Engine: shared(), Store: store, SessionEngine: retryFactory, AwaitContextWindow: awaitWindow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.RetryFailedRun(ctx, sess.ID); !errors.Is(err, admissionErr) {
		t.Fatalf("RetryFailedRun admission failure = %v, want %v", err, admissionErr)
	}
	stillFailed, err = store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata = stillFailed.FailureMetadata()
	if stillFailed.State != session.StateFailed || metadata.Disposition != session.RetryDispositionRetryable || metadata.Progress != session.StreamProgressPrecommit {
		t.Fatalf("admission failure consumed eligibility: state=%s metadata=%+v", stillFailed.State, metadata)
	}
	if admittedProvider != selector.ProviderID || admittedModel != selector.ModelID {
		t.Fatalf("admission identity = %q/%q, want %q/%q", admittedProvider, admittedModel, selector.ProviderID, selector.ModelID)
	}
	rejectAdmission = false
	run, err := svc2.RetryFailedRun(ctx, sess.ID)
	if err != nil {
		t.Fatalf("RetryFailedRun after restart: %v", err)
	}
	if got := drainServerRun(run); got != "same selector retried" {
		t.Fatalf("retry reply = %q", got)
	}
	svc2.FinishRun(sess.ID, run)
	if got := retrySeen.Load(); got != selector {
		t.Fatalf("rehydrated selector = %#v, want %#v", got, selector)
	}
	if !recoveredBeforeProvider.Load() {
		t.Fatal("prepared retry intent was not durable before provider entry")
	}
}

func TestRetryFailedRunAdmitsVisibleAndSecondFailureGovernsNextRetry(t *testing.T) {
	ctx := context.Background()
	llm := mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressVisible}),
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionPermanent, session.StreamProgressVisible}),
	)
	svc, id := failedSession(t, llm, "visible-retry")
	run, err := svc.RetryFailedRun(ctx, id)
	if err != nil {
		t.Fatalf("visible retry: %v", err)
	}
	for range run.Events() {
	}
	svc.Persist(ctx, id)
	svc.FinishRun(id, run)

	got, err := svc.GetSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	metadata := got.FailureMetadata()
	if got.State != session.StateFailed || metadata.Disposition != session.RetryDispositionPermanent || metadata.Progress != session.StreamProgressVisible {
		t.Fatalf("second failure = state=%s metadata=%+v", got.State, metadata)
	}
	if _, err := svc.RetryFailedRun(ctx, id); !errors.Is(err, server.ErrFailedStepRetryIneligible) {
		t.Fatalf("retry after permanent second failure = %v", err)
	}
}

func TestRetryPendingRestartBlocksPromptAndRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	sess := session.New("retry-crash-window", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressPrecommit}); err != nil {
		t.Fatal(err)
	}
	if err := sess.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after launch persisted a running aggregate. Abandon during
	// restart recovery must retain the prepared intent.
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("retried after crash"))
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "retry"})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartRun(ctx, sess.ID, "new prompt must not skip retry"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("StartRun pending retry = %v", err)
	}
	run, err := svc.RetryFailedRun(ctx, sess.ID)
	if err != nil {
		t.Fatalf("RetryFailedRun pending restart: %v", err)
	}
	if got := drainServerRun(run); got != "retried after crash" {
		t.Fatalf("retry result = %q", got)
	}
	svc.Persist(ctx, sess.ID)
	svc.FinishRun(sess.ID, run)
}

func TestGRPCRetryEmitsAndPersistsModelRetry(t *testing.T) {
	store := memstore.New()
	log := memstore.NewEventLog()
	llm := mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressVisible}),
		mockllm.TextTurn("replacement"),
	)
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "retry"})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := svc.StartRun(context.Background(), sess.ID, "one prompt")
	if err != nil {
		t.Fatal(err)
	}
	for range failed.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, failed)

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: string(sess.ID)}}}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	var wireRetry *mecatlv1.ModelRetry
	var wireText string
	for {
		msg, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if ev := msg.GetEvent(); ev.GetType() == string(session.EvModelRetry) {
			wireText = ev.GetText()
			wireRetry = ev.GetModelRetry()
		}
	}
	if !strings.Contains(strings.ToLower(wireText), "superseded") {
		t.Fatalf("wire model.retry text = %q", wireText)
	}
	if wireRetry == nil ||
		wireRetry.GetRetryDisposition() != mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE ||
		wireRetry.GetStreamProgress() != mecatlv1.StreamProgress_STREAM_PROGRESS_VISIBLE {
		t.Fatalf("wire model.retry metadata = %+v, want retryable/visible", wireRetry)
	}
	found := false
	for ev, readErr := range log.Read(context.Background(), sess.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		found = found || ev.Type == session.EvModelRetry
	}
	if !found {
		t.Fatal("event log did not persist model.retry")
	}
}

func TestGRPCConverseRetryStartAndIneligibleStatus(t *testing.T) {
	svc, id := failedSession(t, mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressVisible}),
		mockllm.TextTurn("retried"),
	), "grpc-retry")
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: string(id)}}}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	var text string
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetEvent().GetResult() != nil {
			text = msg.GetEvent().GetResult().GetText()
		}
	}
	if text != "retried" {
		t.Fatalf("result text = %q", text)
	}

	stream, _ = client.Converse(ctx)
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: string(id)}}}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second retry status = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestGRPCConverseRetrySupportsApprovalControl(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	llm := mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressPrecommit}),
		mockllm.ToolCallTurn(call("retry-call", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("approved retry"),
	)
	svc := newService(t, llm, nil, read)
	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("grpc-control-retry"))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := svc.StartRun(context.Background(), sess.ID, "one prompt")
	if err != nil {
		t.Fatal(err)
	}
	for range failed.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, failed)

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: string(sess.ID)}}}); err != nil {
		t.Fatal(err)
	}
	var text string
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ev := msg.GetEvent()
		if ev.GetAsk() != nil {
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: ev.GetAsk().GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}}}); err != nil {
				t.Fatal(err)
			}
		}
		if ev.GetResult() != nil {
			text = ev.GetResult().GetText()
		}
	}
	if text != "approved retry" {
		t.Fatalf("result text = %q", text)
	}
}

func TestHTTPSSEResultTypedMetadataEncoding(t *testing.T) {
	svc := newService(t, mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressVisible}),
	), nil)
	h := httptest.NewServer(server.NewHTTPHandler(svc))
	defer h.Close()
	id := createHTTPSession(t, h)
	resp, err := http.Post(h.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"fail"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	terminal := sseTerminalResultJSON(t, body)
	if got, ok := terminal["retry_disposition"]; !ok || got != float64(mecatlv1.RetryDisposition_RETRY_DISPOSITION_RETRYABLE) {
		t.Fatalf("retry_disposition = %#v (present=%v), want numeric RETRYABLE", got, ok)
	}
	if got, ok := terminal["stream_progress"]; !ok || got != float64(mecatlv1.StreamProgress_STREAM_PROGRESS_VISIBLE) {
		t.Fatalf("stream_progress = %#v (present=%v), want numeric VISIBLE", got, ok)
	}

	// An older-server fixture uses the same encoding/json DTO shape but leaves the
	// optional pointers nil. Its transport JSON must preserve absence rather than
	// manufacturing UNSPECIFIED or UNKNOWN.
	legacy, err := json.Marshal(&mecatlv1.Event{Type: string(session.EvResult), Result: &mecatlv1.Result{Stop: string(session.StopError)}})
	if err != nil {
		t.Fatal(err)
	}
	legacyResult := sseTerminalResultJSON(t, append([]byte("data: "), append(legacy, '\n')...))
	if _, ok := legacyResult["retry_disposition"]; ok {
		t.Fatalf("legacy retry_disposition unexpectedly present: %#v", legacyResult)
	}
	if _, ok := legacyResult["stream_progress"]; ok {
		t.Fatalf("legacy stream_progress unexpectedly present: %#v", legacyResult)
	}
}

func TestHTTPRetryStreamsSSEAndMapsConflict(t *testing.T) {
	svc, id := failedSession(t, mockllm.New(
		mockllm.ErrorTurn(&retryFailure{session.RetryDispositionRetryable, session.StreamProgressVisible}),
		mockllm.TextTurn("http retried"),
	), "http-retry")
	h := httptest.NewServer(server.NewHTTPHandler(svc))
	defer h.Close()
	resp, err := http.Post(h.URL+"/v1/sessions/"+string(id)+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "http retried") {
		t.Fatalf("retry response status=%d body=%s", resp.StatusCode, body)
	}
	terminal := sseTerminalResultJSON(t, body)
	if got, ok := terminal["retry_disposition"]; !ok || got != float64(mecatlv1.RetryDisposition_RETRY_DISPOSITION_UNKNOWN) {
		t.Fatalf("terminal retry_disposition = %#v (present=%v), want numeric explicit UNKNOWN", got, ok)
	}
	if got, ok := terminal["stream_progress"]; !ok || got != float64(mecatlv1.StreamProgress_STREAM_PROGRESS_COMPLETE) {
		t.Fatalf("terminal stream_progress = %#v (present=%v), want numeric explicit COMPLETE", got, ok)
	}
	resp, err = http.Post(h.URL+"/v1/sessions/"+string(id)+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second retry status = %d, want 409", resp.StatusCode)
	}
}
