package agent

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// RunAcceptedCallsForTest drives the real dispatcher with explicit canonical
// domain fixtures, allowing exact durable-key collision tests without replacing
// the production identity generator or predicting an LLM acceptance's IDs.
func RunAcceptedCallsForTest(ctx context.Context, e *Engine, sess *session.Session, env tool.Environment, calls []session.ToolCall) *Run {
	return e.startRun(ctx, sess, RunRequest{}, session.Usage{}, func(ctx context.Context, r *Run) {
		e.emit(r, session.Event{Type: session.EvSessionInit})
		if err := sess.BeginTurn(); err != nil {
			e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, err, false)
			return
		}
		if err := sess.RecordAssistant(session.NewAssistantMessage("", calls)); err != nil {
			e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, err, false)
			return
		}
		if !e.dispatchTurn(ctx, r, sess, env, 0, calls, "", session.Usage{}) {
			e.terminate(ctx, r, sess, session.StopEndTurn, "", session.Usage{}, nil, false)
		}
	})
}

func acceptedCallIDs(sess *session.Session) []session.ToolCallID {
	var ids []session.ToolCallID
	for _, m := range sess.Conversation.Messages {
		for _, call := range m.ToolCalls {
			ids = append(ids, call.ID)
		}
	}
	return ids
}

func mustBuildRequest(t *testing.T, e *Engine, r *Run, sess *session.Session, env tool.Environment) port.LLMRequest {
	t.Helper()
	req, err := e.buildRequest(t.Context(), r, sess, env)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestAskIDDiscriminatorReconstructableAcrossRuns exercises two independent
// startRun -> dispatch -> authorize chains over copies of ONE accepted persisted
// invocation. Repeating a model response instead would mint different invocations
// and correctly yield different ask IDs, not reconstruct the stored invocation.
func TestAskIDDiscriminatorReconstructableAcrossRuns(t *testing.T) {
	const discriminator = "run-42"
	env := memEnv("/ws")
	base := session.New("s-reconstruct", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	if err := base.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall(session.NewToolCallID(), "Act", []byte(`{}`))
	message := session.NewAssistantMessage("", []session.ToolCall{call})
	message.Replay = &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: call.ID, PairingID: "provider-key"}}}
	if err := base.RecordAssistant(message); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessnap.Of(base)
	if err != nil {
		t.Fatal(err)
	}
	mint := func() session.PendingAsk {
		sess, err := snapshot.Restore()
		if err != nil {
			t.Fatal(err)
		}
		catalog := tool.NewCatalog()
		executions := 0
		catalog.MustRegister(revisionActionTool{executions: &executions})
		e := NewEngine(Deps{Catalog: catalog, Policy: permpolicy.NewPolicy(nil, permstore.New())})
		run := e.startRun(t.Context(), sess, RunRequest{AskIDDiscriminator: discriminator}, session.Usage{}, func(ctx context.Context, r *Run) {
			e.dispatch(ctx, r, sess, env, 0, sess.Conversation.Messages[0].ToolCalls)
		})
		var ask *session.PendingAsk
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				approval := *ev.Ask
				ask = &approval
				run.Cancel()
			}
		}
		if ask == nil || ask.Call != call.ID || executions != 0 {
			t.Fatalf("ask=%+v executions=%d", ask, executions)
		}
		return *ask
	}
	first, second := mint(), mint()
	if first.AskID != second.AskID || !strings.HasSuffix(first.AskID, ":"+discriminator) {
		t.Fatalf("same persisted invocation must reconstruct the ask: %q vs %q", first.AskID, second.AskID)
	}
}

func TestCanonicalLLMHistoryProjectsThroughRequestAndSummarizer(t *testing.T) {
	env := memEnv("/ws")
	sess := session.New("projection", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	id := session.NewToolCallID()
	replay := &session.ProviderReplay{Format: "private-test", Version: 17, Data: []byte{0, 255, 1}}
	message := session.NewAssistantMessage("working", []session.ToolCall{session.NewToolCall(id, "Read", []byte(`{}`))})
	message.Replay = &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: id, PairingID: "exact-provider-key", ItemID: "exact-item-key"}}, Provider: replay}
	if err := sess.RecordAssistant(message); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordToolResults([]session.ToolResult{session.NewToolResult(id, "body")}); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(Deps{Catalog: tool.NewCatalog()})
	run := &Run{diag: e.deps.Diagnostics}
	req := mustBuildRequest(t, e, run, sess, env)
	if req.Messages[0].ToolCalls[0].ID != "exact-provider-key" || req.Messages[0].ToolCalls[0].ItemID != "exact-item-key" || req.Messages[1].ToolResult.CallID != "exact-provider-key" || !reflect.DeepEqual(req.Messages[0].Replay, replay) {
		t.Fatalf("request did not resolve occurrence carrier: %+v", req.Messages)
	}
	var summaryReq port.LLMRequest
	compactor := CascadeCompactor{LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { summaryReq = req })}, mockllm.TextTurn("summary"))}
	if _, _, err := compactor.summarize(t.Context(), nil, sess.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summaryReq.Messages[:2], req.Messages) {
		t.Fatalf("summarizer changed the canonical projection: %+v", summaryReq.Messages)
	}
	req.Messages[0].Replay.Data[0] = 8
	req.Messages[0].ToolCalls[0].Args[0] = '!'
	stored := sess.Conversation.Messages[0]
	if !bytes.Equal(stored.Replay.Provider.Data, replay.Data) || string(stored.ToolCalls[0].Args) != `{}` {
		t.Fatal("provider request aliases canonical message storage")
	}
}

func TestCanonicalLLMHistoryRejectsInvalidRequestBeforeProvider(t *testing.T) {
	env := memEnv("/ws")
	sess := session.New("orphan-projection", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	// Simulate corrupt restored state; live aggregate ingress rejects orphans.
	sess.Conversation.Append(session.NewToolMessage(session.NewToolResult(session.NewToolCallID(), "orphan")))
	if err := sess.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	e := NewEngine(Deps{Catalog: tool.NewCatalog(), LLM: llm})
	var terminal *session.ResultPayload
	for ev := range e.Run(t.Context(), sess, env, RunRequest{Text: "go"}).Events() {
		if ev.Result != nil {
			terminal = ev.Result
		}
	}
	if llm.Calls() != 0 || terminal == nil || terminal.Stop != session.StopError || !strings.Contains(terminal.Error, "project history") {
		t.Fatalf("invalid projection reached provider: calls=%d result=%+v", llm.Calls(), terminal)
	}
}
