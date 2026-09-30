package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type availabilityRewriteHook struct{}

func (availabilityRewriteHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePostToolUse {
		return governance.HookOutcome{Mutated: []byte(`{"content":"HELD_EFFECTIVE","is_error":false}`)}, nil
	}
	return governance.HookOutcome{}, nil
}

func TestADR_0370_Scenario2_AvailabilityAfterEffectiveRelease(t *testing.T) {
	t.Run("released effective payload", testAvailabilityReleasedEffectivePayload)
	t.Run("unattended hold", testAvailabilityUnattendedHold)
}

func testAvailabilityReleasedEffectivePayload(t *testing.T) {
	const raw = "RAW_NEVER_DISPLAY"
	reviewer := &inboundReviewer{}
	recorder := &resultRecorder{}
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(c.ID, raw), nil
	}}
	sess := newSession(t, session.Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, Hooks: availabilityRewriteHook{}, ToolReviewer: reviewer, ToolCallRecorder: recorder, Interactive: true}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	var events []session.Event
	var ask *session.PendingAsk
	for ask == nil {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				t.Fatal("closed before release ask")
			}
			events = append(events, ev)
			if ev.Type == session.EvPermissionAsk {
				ask = ev.Ask
			}
		case <-ctx.Done():
			t.Fatal("release ask timed out")
		}
	}
	if ask.Guardrail == nil || ask.Guardrail.Kind != session.GuardrailApprovalResultRelease {
		t.Fatalf("wrong ask: %+v", ask)
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResultAvailable || ev.Type == session.EvToolResult || strings.Contains(ev.Text, raw) || strings.Contains(ev.Text, "HELD_EFFECTIVE") || (ev.ToolResult != nil && (strings.Contains(fmt.Sprint(*ev.ToolResult), raw) || strings.Contains(fmt.Sprint(*ev.ToolResult), "HELD_EFFECTIVE"))) {
			t.Fatalf("held result escaped before release: %+v", ev)
		}
		if ev.Hook != nil && ev.Hook.CallID == "one" && ev.Hook.Guardrail == nil && ev.Hook.Phase == string(governance.PhasePostToolUse) {
			t.Fatalf("held PostToolUse annotation escaped: %+v", ev)
		}
	}
	recorder.mu.Lock()
	recorded := len(recorder.results)
	recorder.mu.Unlock()
	if recorded != 0 {
		t.Fatalf("recorder saw held result before release: %d", recorded)
	}
	for _, msg := range sess.Conversation.Messages {
		if msg.ToolResult != nil {
			t.Fatalf("model history saw held result: %+v", msg)
		}
	}
	if err := resolveScoped(t, run, ask, session.VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	for ev := range run.Events() {
		events = append(events, ev)
	}
	var available, canonical *session.ToolResult
	var inboundIndex, availableIndex, canonicalIndex = -1, -1, -1
	for i, ev := range events {
		if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.CallID == "one" {
			inboundIndex = i
		}
		if ev.Type == session.EvToolResultAvailable {
			available = ev.ToolResult
			availableIndex = i
		}
		if ev.Type == session.EvToolResult {
			canonical = ev.ToolResult
			canonicalIndex = i
		}
		if strings.Contains(ev.Text, raw) || (ev.ToolResult != nil && strings.Contains(fmt.Sprint(*ev.ToolResult), raw)) {
			t.Fatalf("raw pre-hook result escaped: %+v", ev)
		}
	}
	if inboundIndex < 0 || availableIndex <= inboundIndex || canonicalIndex <= availableIndex || available == nil || available.Content != "HELD_EFFECTIVE" || !reflect.DeepEqual(available, canonical) {
		t.Fatalf("inbound=%d available=%d canonical=%d payloads=%+v/%+v", inboundIndex, availableIndex, canonicalIndex, available, canonical)
	}
	if len(recorder.results) != 1 || !reflect.DeepEqual(recorder.results[0], *available) {
		t.Fatalf("recorder=%+v", recorder.results)
	}
}

func testAvailabilityUnattendedHold(t *testing.T) {
	const held = "PRIVATE_UNATTENDED"
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(c.ID, held), nil
	}}
	recorder := &resultRecorder{}
	sess := newSession(t, session.Limits{})
	run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, ToolReviewer: &inboundReviewer{}, ToolCallRecorder: recorder}).Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	var available, canonical *session.ToolResult
	var reviewIndex, availabilityIndex, canonicalIndex = -1, -1, -1
	for i, ev := range drain(run) {
		if ev.Hook != nil && ev.Hook.Guardrail != nil {
			reviewIndex = i
		}
		if ev.Type == session.EvToolResultAvailable {
			available, availabilityIndex = ev.ToolResult, i
		}
		if ev.Type == session.EvToolResult {
			canonical, canonicalIndex = ev.ToolResult, i
		}
		if ev.Type == session.EvPermissionAsk || strings.Contains(ev.Text, held) || ev.ToolResult != nil && strings.Contains(fmt.Sprint(*ev.ToolResult), held) {
			t.Fatalf("unattended hold leaked: %+v", ev)
		}
	}
	if reviewIndex < 0 || availabilityIndex <= reviewIndex || canonicalIndex <= availabilityIndex || available == nil || !available.IsError || !strings.Contains(available.Content, "withheld") || !reflect.DeepEqual(available, canonical) || len(recorder.results) != 1 || !reflect.DeepEqual(recorder.results[0], *available) {
		t.Fatalf("review=%d available=%d canonical=%d results=%+v/%+v recorder=%+v", reviewIndex, availabilityIndex, canonicalIndex, available, canonical, recorder.results)
	}
	for _, msg := range sess.Conversation.Messages {
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, held) {
			t.Fatalf("model saw held result: %+v", msg)
		}
	}
}

type selectiveAvailabilityReviewer struct{}

func (selectiveAvailabilityReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	return job == agent.ReviewJobInbound, true
}
func (selectiveAvailabilityReviewer) Review(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	if req.EffectiveCall.ID == "clean" {
		return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, nil
	}
	return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, nil
}

func TestADR_0370_Scenario2_CleanSiblingBypassesHeldPresentation(t *testing.T) {
	started := make(chan session.ToolCallID, 3)
	gates := map[session.ToolCallID]chan struct{}{"held-one": make(chan struct{}), "clean": make(chan struct{}), "held-two": make(chan struct{})}
	read := &fakeTool{name: "Read", readOnly: true, exec: func(ctx context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		started <- c.ID
		select {
		case <-gates[c.ID]:
			return session.NewToolResult(c.ID, "payload-"+string(c.ID)), nil
		case <-ctx.Done():
			return session.NewToolError(c.ID, "cancelled"), nil
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		for _, g := range gates {
			select {
			case <-g:
			default:
				close(g)
			}
		}
	}()
	sess := newSession(t, session.Limits{})
	run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("held-one", "Read", `{}`), toolCall("clean", "Read", `{}`), toolCall("held-two", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, ToolReviewer: selectiveAvailabilityReviewer{}, Interactive: true}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	for range 3 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("siblings failed to start")
		}
	}
	close(gates["clean"])
	events := awaitBatchEvent(t, run, session.EvToolResultAvailable, "clean")
	for _, ev := range events {
		if ev.Type == session.EvPermissionAsk || ev.Type == session.EvToolResult || ev.Type == session.EvToolResultAvailable && ev.ToolResult.CallID != "clean" {
			t.Fatalf("premature sibling decision: %+v", ev)
		}
	}
	close(gates["held-one"])
	close(gates["held-two"])
	var asks, canonical []session.ToolCallID
	available := map[session.ToolCallID]int{"clean": 1}
	for ev := range run.Events() {
		switch ev.Type {
		case session.EvPermissionAsk:
			asks = append(asks, ev.Ask.Call)
			if len(asks) == 1 && ev.Ask.Call != "held-one" {
				t.Fatalf("first release ask: %+v", ev.Ask)
			}
			if len(asks) == 2 && ev.Ask.Call != "held-two" {
				t.Fatalf("second release ask: %+v", ev.Ask)
			}
			if err := resolveScoped(t, run, ev.Ask, session.VerdictAllowOnce); err != nil {
				t.Fatal(err)
			}
		case session.EvToolResultAvailable:
			available[ev.ToolResult.CallID]++
			if ev.ToolResult.CallID == "held-two" && len(asks) < 2 {
				t.Fatal("second held result available before its release")
			}
		case session.EvToolResult:
			canonical = append(canonical, ev.ToolResult.CallID)
		}
	}
	if !reflect.DeepEqual(asks, []session.ToolCallID{"held-one", "held-two"}) || !reflect.DeepEqual(canonical, []session.ToolCallID{"held-one", "clean", "held-two"}) || available["held-one"] != 1 || available["held-two"] != 1 || available["clean"] != 1 {
		t.Fatalf("asks=%v canonical=%v available=%v", asks, canonical, available)
	}
}
