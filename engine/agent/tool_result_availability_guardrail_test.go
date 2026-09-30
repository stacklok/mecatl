package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	t.Run("PostToolUse UTF-8 repair", testAvailabilityAfterUTF8Repair)
	t.Run("stale principal revision", testAvailabilityAfterStalePrincipalRevision)
}

func testAvailabilityAfterUTF8Repair(t *testing.T) {
	recorder := &resultRecorder{}
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(c.ID, "result "+invalidUTF8), nil
	}}
	sess := newSession(t, session.Limits{})
	events := drain(agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, ToolCallRecorder: recorder}).Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"}))

	available, canonical := availabilityAndCanonical(t, events, "one")
	for _, result := range []session.ToolResult{available, canonical, recorder.results[0]} {
		if !utf8.ValidString(result.Content) || !strings.ContainsRune(result.Content, '�') || strings.Contains(result.Content, invalidUTF8) || !strings.Contains(result.Content, "result ") {
			t.Fatalf("result was not repaired effective payload: %+v", result)
		}
	}
	if !reflect.DeepEqual(available, canonical) || !reflect.DeepEqual(available, recorder.results[0]) {
		t.Fatalf("availability/canonical/recorder differ: available=%+v canonical=%+v recorder=%+v", available, canonical, recorder.results)
	}
	for _, message := range sess.Conversation.Messages {
		if message.ToolResult != nil && (!utf8.ValidString(message.ToolResult.Content) || !reflect.DeepEqual(*message.ToolResult, available)) {
			t.Fatalf("model history did not receive repaired effective payload: %+v", message)
		}
	}
}

func testAvailabilityAfterStalePrincipalRevision(t *testing.T) {
	const held = "PRIVATE_STALE_PRINCIPAL"
	reviewRelease := make(chan struct{})
	reviewer := &inboundReviewer{entered: make(chan session.ToolCallID, 1), release: reviewRelease, assessment: agent.ReviewProhibited}
	recorder := &resultRecorder{}
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(c.ID, held), nil
	}}
	sess := newSession(t, session.Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, ToolReviewer: reviewer, ToolCallRecorder: recorder, Interactive: true}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	select {
	case <-reviewer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("inbound review did not start")
	}
	agent.RefreshReviewTasksForTest(run, []session.Message{{Role: session.RoleUser, Text: "changed principal task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	close(reviewRelease)
	events := drain(run)

	available, canonical := availabilityAndCanonical(t, events, "one")
	if !available.IsError || !strings.Contains(available.Content, "root instructions changed during review") || !reflect.DeepEqual(available, canonical) || len(recorder.results) != 1 || !reflect.DeepEqual(available, recorder.results[0]) {
		t.Fatalf("stale revision must produce only the synthetic safe result: available=%+v canonical=%+v recorder=%+v", available, canonical, recorder.results)
	}
	for _, event := range events {
		if strings.Contains(event.Text, held) || event.ToolResult != nil && strings.Contains(fmt.Sprint(*event.ToolResult), held) {
			t.Fatalf("stale principal held bytes escaped: %+v", event)
		}
	}
	for _, message := range sess.Conversation.Messages {
		if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, held) {
			t.Fatalf("model history saw stale principal held result: %+v", message)
		}
	}
}

func TestADR_0370_Scenario2_StaleInboundSuppressesPostHookAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
		stale    bool
	}{
		{name: "read batch stale", readOnly: true, stale: true},
		{name: "serial stale", stale: true},
		{name: "serial released", stale: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const private = "POST_HOOK_PRIVATE_CONTENT"
			release := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			reviewer := &inboundReviewer{entered: make(chan session.ToolCallID, 1), release: release, assessment: agent.ReviewAcceptable}
			name := "Write"
			if tc.readOnly {
				name = "Read"
			}
			instrument := &fakeTool{name: name, readOnly: tc.readOnly, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(call.ID, "tool returned "+private), nil
			}}
			hook := &countingPostHook{message: "hook saw " + private}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", name, `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, instrument), Policy: staticAllowPolicy{}, Hooks: hook, ToolReviewer: reviewer}).Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
			select {
			case <-reviewer.entered:
			case <-ctx.Done():
				t.Fatal("inbound review did not begin")
			}
			if tc.stale {
				agent.RefreshReviewTasksForTest(run, []session.Message{{Role: session.RoleUser, Text: "new root task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
			}
			close(release)
			events := drain(run)
			available, canonical := availabilityAndCanonical(t, events, "one")
			if !reflect.DeepEqual(available, canonical) || available.IsError != tc.stale {
				t.Fatalf("unexpected effective result: available=%+v canonical=%+v", available, canonical)
			}
			annotations := 0
			for _, ev := range events {
				if strings.Contains(ev.Text, private) {
					annotations++
				}
			}
			wantAnnotations := 1
			if tc.stale {
				wantAnnotations = 0
				if !strings.Contains(canonical.Content, "root instructions changed during review") || strings.Contains(canonical.Content, private) {
					t.Fatalf("stale review did not withhold original result: %+v", canonical)
				}
			} else if !strings.Contains(canonical.Content, private) {
				t.Fatalf("released result lost original content: %+v", canonical)
			}
			if annotations != wantAnnotations {
				t.Fatalf("PostToolUse annotations = %d, want %d", annotations, wantAnnotations)
			}
		})
	}
}

func availabilityAndCanonical(t *testing.T, events []session.Event, callID session.ToolCallID) (session.ToolResult, session.ToolResult) {
	t.Helper()
	var available, canonical *session.ToolResult
	for _, event := range events {
		if event.ToolResult == nil || event.ToolResult.CallID != callID {
			continue
		}
		switch event.Type {
		case session.EvToolResultAvailable:
			available = event.ToolResult
		case session.EvToolResult:
			canonical = event.ToolResult
		}
	}
	if available == nil || canonical == nil {
		t.Fatalf("missing availability or canonical result for %q: %v", callID, typesOf(events))
	}
	return *available, *canonical
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

func TestADR_0370_Scenario2_ExactReleasedAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict session.ApprovalVerdict
		want    bool
	}{
		{"release", session.VerdictAllowOnce, true},
		{"deny", session.VerdictDeny, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const private = "PRIVATE_HELD_RESULT"
			var executions int
			hooks := &countingPostHook{}
			reviewer := &inboundReviewer{}
			recorder := &resultRecorder{}
			payload := session.NewToolResultWithParts("one", private, []session.Content{session.NewTextBlock("PRIVATE_TYPED"), session.NewStructuredContentBlock(`{"private":true}`)})
			read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, _ session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executions++
				return payload, nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sess := newSession(t, session.Limits{})
			policy := &noLearnPolicy{}
			run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("one", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: policy, Hooks: hooks, ToolReviewer: reviewer, ToolCallRecorder: recorder, Interactive: true}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
			var events []session.Event
			var asks int
			for {
				select {
				case ev, ok := <-run.Events():
					if !ok {
						goto finished
					}
					events = append(events, ev)
					if ev.Type == session.EvPermissionAsk {
						asks++
						if ev.Ask.Guardrail == nil || ev.Ask.Guardrail.Kind != session.GuardrailApprovalResultRelease {
							t.Fatalf("unexpected ask: %+v", ev.Ask)
						}
						if err := resolveScoped(t, run, ev.Ask, tc.verdict); err != nil {
							t.Fatal(err)
						}
					}
				case <-ctx.Done():
					t.Fatal("release decision did not finish")
				}
			}
		finished:
			var available, canonical []session.ToolResult
			for _, ev := range events {
				if ev.ToolResult == nil {
					continue
				}
				switch ev.Type {
				case session.EvToolResultAvailable:
					available = append(available, *ev.ToolResult)
				case session.EvToolResult:
					canonical = append(canonical, *ev.ToolResult)
				}
				if !tc.want && strings.Contains(fmt.Sprint(*ev.ToolResult), "PRIVATE_") {
					t.Fatalf("denied bytes escaped: %+v", ev)
				}
			}
			if asks != 1 || executions != 1 || hooks.posts != 1 || reviewer.calls != 1 || policy.learns != 0 || len(available) != 1 || len(canonical) != 1 || len(recorder.results) != 1 || !reflect.DeepEqual(available[0], canonical[0]) || !reflect.DeepEqual(available[0], recorder.results[0]) {
				t.Fatalf("asks=%d executions=%d posts=%d reviews=%d learns=%d available=%+v canonical=%+v recorded=%+v", asks, executions, hooks.posts, reviewer.calls, policy.learns, available, canonical, recorder.results)
			}
			if tc.want && !reflect.DeepEqual(available[0], payload) {
				t.Fatalf("released payload = %+v, want %+v", available[0], payload)
			}
			if !tc.want && (!available[0].IsError || !strings.Contains(available[0].Content, "tool result withheld by contextual guardrail") || len(available[0].Parts) != 0) {
				t.Fatalf("denial must contain only synthetic withholding: %+v", available[0])
			}
		})
	}
}

func TestADR_0370_Scenario2_CanonicalCancellationReplacement(t *testing.T) {
	const private = "PRIVATE_HELD_RESULT"
	read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		if c.ID == "held" {
			return session.NewToolResult(c.ID, private), nil
		}
		return session.NewToolResult(c.ID, "public success"), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess := newSession(t, session.Limits{})
	run := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("held", "Read", `{}`), toolCall("clean", "Read", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, read), Policy: staticAllowPolicy{}, ToolReviewer: selectiveAvailabilityReviewer{}, Interactive: true}).Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	var events []session.Event
	for {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				t.Fatal("closed before held release ask")
			}
			events = append(events, ev)
			if ev.Type == session.EvPermissionAsk {
				if ev.Ask.Call != "held" {
					t.Fatalf("unexpected ask: %+v", ev.Ask)
				}
				goto cancelRun
			}
		case <-ctx.Done():
			t.Fatal("held release ask timed out")
		}
	}
cancelRun:
	cleanAvailable := 0
	for _, ev := range events {
		if ev.Type == session.EvToolResultAvailable && ev.ToolResult.CallID == "clean" {
			cleanAvailable++
		}
		if ev.Type == session.EvToolResult || ev.Type == session.EvToolResultAvailable && ev.ToolResult.CallID == "held" || ev.ToolResult != nil && strings.Contains(fmt.Sprint(*ev.ToolResult), private) {
			t.Fatalf("held bytes or canonical escaped before cancellation: type=%s result=%+v events=%v", ev.Type, ev.ToolResult, typesOf(events))
		}
	}
	if cleanAvailable != 1 {
		t.Fatalf("clean sibling must be available before cancellation, got %d", cleanAvailable)
	}
	run.Cancel()
	for ev := range run.Events() {
		events = append(events, ev)
	}
	available := map[session.ToolCallID][]session.ToolResult{}
	canonical := map[session.ToolCallID][]session.ToolResult{}
	var order []session.ToolCallID
	for _, ev := range events {
		if ev.ToolResult == nil {
			continue
		}
		if strings.Contains(fmt.Sprint(*ev.ToolResult), private) {
			t.Fatalf("cancelled held bytes escaped: %+v", ev)
		}
		switch ev.Type {
		case session.EvToolResultAvailable:
			available[ev.ToolResult.CallID] = append(available[ev.ToolResult.CallID], *ev.ToolResult)
		case session.EvToolResult:
			canonical[ev.ToolResult.CallID] = append(canonical[ev.ToolResult.CallID], *ev.ToolResult)
			order = append(order, ev.ToolResult.CallID)
			if ev.ToolResult.CallID == "held" && len(available["held"]) != 1 {
				t.Fatal("held canonical error arrived before safe availability")
			}
		}
	}
	if len(canonical["held"]) != 1 || len(canonical["clean"]) != 1 || len(available["clean"]) != 1 || len(available["held"]) != 1 || available["clean"][0].Content != "public success" || !available["held"][0].IsError || !reflect.DeepEqual(available["held"][0], canonical["held"][0]) || !canonical["clean"][0].IsError || canonical["clean"][0].Content == available["clean"][0].Content || !strings.Contains(canonical["held"][0].Content, "release decision was cancelled") || len(canonical["held"][0].Parts) != 0 || !reflect.DeepEqual(order, []session.ToolCallID{"held", "clean"}) {
		t.Fatalf("available=%+v canonical=%+v order=%v", available, canonical, order)
	}

	// A sibling still running at cancellation has no prior availability either.
	batch, _, started, gates := availabilityBatch(t)
	awaitAvailabilityBatchStart(t, started)
	close(gates["fast"])
	batchEvents := awaitBatchEvent(t, batch, session.EvToolResultAvailable, "fast")
	batch.Cancel()
	for ev := range batch.Events() {
		batchEvents = append(batchEvents, ev)
	}
	seen := map[session.ToolCallID]int{}
	var slowAvailable session.ToolResult
	var slowCanonical session.ToolResult
	var fastCanonical session.ToolResult
	for _, ev := range batchEvents {
		if ev.ToolResult == nil {
			continue
		}
		switch ev.Type {
		case session.EvToolResultAvailable:
			seen[ev.ToolResult.CallID]++
			if ev.ToolResult.CallID == "slow" {
				slowAvailable = *ev.ToolResult
			}
		case session.EvToolResult:
			if ev.ToolResult.CallID == "slow" {
				if seen["slow"] != 1 {
					t.Fatal("slow canonical result arrived before synthetic availability")
				}
				slowCanonical = *ev.ToolResult
			} else {
				fastCanonical = *ev.ToolResult
			}
		}
	}
	if seen["fast"] != 1 || seen["slow"] != 1 || !slowAvailable.IsError || !reflect.DeepEqual(slowAvailable, slowCanonical) || !fastCanonical.IsError {
		t.Fatalf("running sibling cancellation: availability=%v slow=%+v/%+v fast=%+v", seen, slowAvailable, slowCanonical, fastCanonical)
	}
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
