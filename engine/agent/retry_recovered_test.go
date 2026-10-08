package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type retryDeliveryQueue struct {
	notes []port.DeliveryNote
}

func (q *retryDeliveryQueue) Enqueue(_ context.Context, id session.SessionID, text string) (port.DeliveryNote, error) {
	n := port.DeliveryNote{Seq: uint64(len(q.notes) + 1), SessionID: id, Text: text, EnqueuedAt: time.Now()}
	q.notes = append(q.notes, n)
	return n, nil
}
func (q *retryDeliveryQueue) Pending(context.Context, session.SessionID) ([]port.DeliveryNote, error) {
	return append([]port.DeliveryNote(nil), q.notes...), nil
}
func (q *retryDeliveryQueue) MarkDelivered(_ context.Context, _ session.SessionID, seq uint64) error {
	for i, n := range q.notes {
		if n.Seq == seq {
			q.notes = append(q.notes[:i], q.notes[i+1:]...)
			break
		}
	}
	return nil
}

func TestRetryFailedStepSkipsOnlyFirstBoundaryInjections(t *testing.T) {
	queue := &retryDeliveryQueue{}
	_, _ = queue.Enqueue(context.Background(), "retry-boundary", "PENDING DELIVERY")
	var requests []port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		clone := req
		clone.Messages = session.CloneMessages(req.Messages)
		requests = append(requests, clone)
	})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: []byte(`{"path":"a"}`)}),
		mockllm.TextTurn("done"),
	)
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: func(context.Context, session.ToolCall, tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult("read", "body"), nil
	}})
	eng := newEngine(agent.Deps{LLM: llm, Catalog: cat, DeliveryQueue: queue})
	sess := newSession(t, session.Limits{})
	sess.ID = "retry-boundary"
	if err := sess.RecordUserPrompt("original", nil); err != nil {
		t.Fatal(err)
	}
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
	events := drain(eng.RetryFailedStep(context.Background(), sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)))
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2; result=%+v", len(requests), lastResult(t, events))
	}
	for _, m := range requests[0].Messages {
		if strings.Contains(m.Text, "PENDING DELIVERY") {
			t.Fatalf("first failed-step retry request included boundary delivery: %+v", requests[0].Messages)
		}
	}
	found := false
	for _, m := range requests[1].Messages {
		found = found || strings.Contains(m.Text, "PENDING DELIVERY")
	}
	if !found {
		t.Fatalf("second request did not resume boundary delivery: %+v", requests[1].Messages)
	}
}

func TestRetryFailedStepVisibleNoticeSupersedesPartialOutput(t *testing.T) {
	eng := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("replacement")), Catalog: tool.NewCatalog()})
	sess := newSession(t, session.Limits{})
	if err := sess.RecordUserPrompt("original", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}); err != nil {
		t.Fatal(err)
	}
	if err := sess.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	events := drain(eng.RetryFailedStep(context.Background(), sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)))
	if len(events) < 2 || events[1].Type != session.EvModelRetry {
		t.Fatalf("opening events = %+v", events)
	}
	text := strings.ToLower(events[1].Text)
	if !strings.Contains(text, "partial model output failed") || !strings.Contains(text, "superseded") {
		t.Fatalf("visible retry notice = %q", events[1].Text)
	}
}

func TestRetryFailedStepRepeatsOnlyFailedModelStep(t *testing.T) {
	call := session.ToolCall{ID: "read-once", Name: "Read", Args: []byte(`{"path":"a"}`)}
	firstTurn := mockllm.ToolCallTurn(call)
	for i := range firstTurn.Chunks {
		if firstTurn.Chunks[i].Kind == port.ChunkUsage {
			firstTurn.Chunks[i].Usage = &session.Usage{InputTokens: 3, OutputTokens: 2}
		}
	}
	retryTurn := mockllm.TextTurn("recovered answer")
	for i := range retryTurn.Chunks {
		if retryTurn.Chunks[i].Kind == port.ChunkUsage {
			retryTurn.Chunks[i].Usage = &session.Usage{InputTokens: 4, OutputTokens: 3}
		}
	}
	llm := mockllm.New(firstTurn, mockllm.ErrorTurn(&typedFailureError{
		disposition: session.RetryDispositionRetryable,
		progress:    session.StreamProgressPrecommit,
	}), retryTurn)
	executions := 0
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(in.ID, "earlier result"), nil
	}})
	hooks := newRecordingHooks(nil)
	eng := newEngine(agent.Deps{LLM: llm, Catalog: cat, Hooks: hooks})
	sess := newSession(t, session.Limits{})
	env := agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)

	firstEvents := drain(eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "one prompt"}))
	if got := lastResult(t, firstEvents).Stop; got != session.StopError {
		t.Fatalf("first stop = %q, want error", got)
	}
	if executions != 1 {
		t.Fatalf("tool executions before retry = %d, want 1", executions)
	}
	if got := sess.UsageFor(session.UsageKindMain).TotalTokens(); got != 5 {
		t.Fatalf("cumulative usage before retry = %d, want 5", got)
	}
	if err := sess.PrepareFailedStepRetry(); err != nil {
		t.Fatalf("PrepareFailedStepRetry: %v", err)
	}

	retryEvents := drain(eng.RetryFailedStep(context.Background(), sess, env))
	if len(retryEvents) < 2 || retryEvents[0].Type != session.EvSessionInit || retryEvents[1].Type != session.EvModelRetry {
		t.Fatalf("retry opening events = %+v, want session.init then model.retry", retryEvents)
	}
	if strings.Contains(strings.ToLower(retryEvents[1].Text), "visible") || strings.Contains(strings.ToLower(retryEvents[1].Text), "superseded") {
		t.Fatalf("precommit retry notice claims visible output: %q", retryEvents[1].Text)
	}
	result := lastResult(t, retryEvents)
	if result.Usage.TotalTokens() != 7 {
		t.Fatalf("retry result usage = %d, want per-run 7", result.Usage.TotalTokens())
	}
	if sess.UsageFor(session.UsageKindMain).TotalTokens() != 12 {
		t.Fatalf("cumulative usage after retry = %d, want 12", sess.UsageFor(session.UsageKindMain).TotalTokens())
	}
	if executions != 1 {
		t.Fatalf("tool executions after retry = %d, want unchanged 1", executions)
	}
	if hooks.count(governance.PhaseSessionStart) != 1 || hooks.count(governance.PhaseUserPromptSubmit) != 1 {
		t.Fatalf("run-start hooks reran: phases=%v", hooks.recorded())
	}
	userMessages := 0
	toolResults := 0
	for _, msg := range sess.Conversation.Messages {
		if msg.Role == session.RoleUser {
			userMessages++
		}
		if msg.ToolResult != nil && msg.ToolResult.CallID == call.ID {
			toolResults++
		}
	}
	if userMessages != 1 || toolResults != 1 {
		t.Fatalf("conversation user messages=%d tool results=%d, want 1/1", userMessages, toolResults)
	}
	userEvents := 0
	for _, ev := range append(firstEvents, retryEvents...) {
		if ev.Type == session.EvUserPrompt {
			userEvents++
		}
	}
	if userEvents != 1 {
		t.Fatalf("EvUserPrompt count = %d, want 1", userEvents)
	}
}

func TestRetryFailedStepPreTurnBudgetDefersWithoutProviderCall(t *testing.T) {
	calls := 0
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { calls++ })}, mockllm.TextTurn("must not run"))
	eng := newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), MaxRunTokens: 1})
	sess := newSession(t, session.Limits{})
	if err := sess.RecordUserPrompt("original", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUsage(session.Usage{InputTokens: 1}); err != nil {
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

	events := drain(eng.RetryFailedStep(context.Background(), sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)))
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	if got := lastResult(t, events).Stop; got != session.StopBudget {
		t.Fatalf("stop = %q, want budget", got)
	}
	if _, pending := sess.FailedStepRetryPending(); !pending || sess.State != session.StateIdle {
		t.Fatalf("deferred retry state=%s pending=%v, want idle+pending", sess.State, pending)
	}
	if err := sess.RecordUserPrompt("queued", nil); err == nil {
		t.Fatal("deferred retry released prompt entry")
	}
}

func TestRetryFailedStepCancellationExplicitlyClearsIntent(t *testing.T) {
	eng := newEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()})
	sess := newSession(t, session.Limits{})
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := drain(eng.RetryFailedStep(ctx, sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)))
	if got := lastResult(t, events).Stop; got != session.StopCancelled {
		t.Fatalf("stop=%q", got)
	}
	if _, pending := sess.FailedStepRetryPending(); pending || sess.State != session.StateCancelled {
		t.Fatalf("cancel state=%s pending=%v", sess.State, pending)
	}
}
