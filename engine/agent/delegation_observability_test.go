package agent_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// collectSubagent filters the parent run's emitted events down to the subagent.*
// family, preserving order.
func collectSubagent(evs []session.Event) []*session.SubagentPayload {
	var out []*session.SubagentPayload
	for _, ev := range evs {
		switch ev.Type {
		case session.EvSubagentStart, session.EvSubagentTool, session.EvSubagentEnd:
			out = append(out, ev.Subagent)
		}
	}
	return out
}

// newObservedSubagentRun drives a REAL Subagent call whose child reads once (args
// carry canaryArgs, the result body carries canaryBody) and then emits a final text
// (canaryText), draining the parent run's events. The canaries are long (> the
// clampPreview 200-rune cap) and control-byte-bearing so verbatim carriage is
// impossible and the scrub is what the assertions prove.
func newObservedSubagentRun(t *testing.T) ([]session.Event, *session.Session) {
	t.Helper()
	// The head exceeds the 200-rune clampPreview cap, so the head itself is cut and
	// the tail (and everything past it) can never survive — verbatim carriage is
	// impossible by construction.
	head := "CANARY-" + strings.Repeat("h", 220) + "-"
	tail := "-TAIL-" + strings.Repeat("y", 600)
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, head+"BODY"+tail+"\x1b[31m"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk(head+"MSG"+tail+"\x1b[7m"),
			mockllm.ToolCallChunk(toolCall("k1", "Read", `{"path":"`+head+"ARGS"+tail+"\x1b[1m"+`"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 3, OutputTokens: 1}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("final answer "+head+"FINAL"+tail+"\x1b[0m"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, childRead))
	task := agent.NewSubagentTool(childEngine)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	return drain(r), sess
}

// assertNoVerbatimCanary scans EVERY string-kinded field of payload by reflection and
// fails if any canary tail (the part clamping removes) or any control byte survives.
func assertNoVerbatimCanary(t *testing.T, payload any) {
	t.Helper()
	rv := reflect.ValueOf(payload).Elem()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if f.Kind() != reflect.String {
			continue
		}
		s := f.String()
		if strings.Contains(s, "-TAIL-") {
			t.Fatalf("canary leaked UNBOUNDED into %s.%s: %q", rv.Type().Name(), rv.Type().Field(i).Name, s)
		}
		for _, c := range s {
			if c < 0x20 || (c >= 0x7f && c <= 0x9f) {
				t.Fatalf("control byte leaked into %s.%s: %q", rv.Type().Name(), rv.Type().Field(i).Name, s)
			}
		}
	}
}

// TestDelegationObservability_Scenario2_SubagentProjectsBoundedPreviews (AC2.1): a
// running Subagent emits subagent.tool events whose Detail is a clamped,
// control-byte-scrubbed preview of the child tool call's args (and the tool result's
// body), and whose Text carries the child's capped message text — each tagged with
// the InnerKind it was projected from.
func TestDelegationObservability_Scenario2_SubagentProjectsBoundedPreviews(t *testing.T) {
	evs, _ := newObservedSubagentRun(t)
	ps := collectSubagent(evs)

	var callArgs, callBody, msgText, resultText *session.SubagentPayload
	for _, p := range ps {
		if p == nil {
			continue
		}
		switch p.InnerKind {
		case session.EvToolCall:
			callArgs = p
		case session.EvToolResult:
			callBody = p
		case session.EvMessageDelta:
			if msgText == nil {
				msgText = p
			}
		case session.EvResult:
			resultText = p
		}
	}
	if callArgs == nil || callBody == nil || msgText == nil || resultText == nil {
		t.Fatalf("want tool.call + tool.result + message.delta + result projections; got %+v", ps)
	}
	if !strings.Contains(callArgs.Detail, "CANARY-hhh") {
		t.Fatalf("tool.call Detail = %q, want the clamped args preview", callArgs.Detail)
	}
	if callArgs.ToolName != "Read" {
		t.Fatalf("tool.call ToolName = %q, want Read", callArgs.ToolName)
	}
	if !strings.Contains(callBody.Detail, "CANARY-hhh") {
		t.Fatalf("tool.result Detail = %q, want the clamped body preview", callBody.Detail)
	}
	if !strings.Contains(msgText.Text, "CANARY-hhh") {
		t.Fatalf("message.delta Text = %q, want the clamped message preview", msgText.Text)
	}
	if !strings.Contains(resultText.Text, "CANARY-hhh") {
		t.Fatalf("result Text = %q, want the clamped terminal text preview", resultText.Text)
	}
	// Every projection is bounded + scrubbed (the tails and control bytes are gone).
	for _, p := range ps {
		if p != nil {
			assertNoVerbatimCanary(t, p)
		}
	}
}

// TestDelegationObservability_Scenario2_ParallelInheritsBoundedPreviews (AC2.2): a
// Parallel branch emits parallel.branch tool events carrying the same bounded
// previews via the branchTool re-tag closure.
func TestDelegationObservability_Scenario2_ParallelInheritsBoundedPreviews(t *testing.T) {
	head := "BRANCHPREVIEW-" + strings.Repeat("h", 220) + "-"
	tail := "-TAIL-" + strings.Repeat("w", 600)
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, head+"BODY"+tail+"\x1b[31m"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("k1", "Read", `{"path":"`+head+"ARGS"+tail+"\x1b[5m"+`"}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("branch said "+head+"MSG"+tail),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, childRead))
	parallel := agent.NewParallelTool(childEngine, &memForker{})
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["one","two"]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, parallel)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	var sawArgs, sawBody, sawText bool
	for _, p := range collectParallel(evs) {
		if p == nil || p.Kind != session.ParallelBranchTool {
			continue
		}
		switch p.InnerKind {
		case session.EvToolCall:
			if strings.Contains(p.Detail, "BRANCHPREVIEW-hhh") && !strings.Contains(p.Detail, tail) {
				sawArgs = true
			}
		case session.EvToolResult:
			if strings.Contains(p.Detail, "BRANCHPREVIEW-hhh") && !strings.Contains(p.Detail, tail) {
				sawBody = true
			}
		case session.EvMessageDelta, session.EvResult:
			if strings.Contains(p.Text, "BRANCHPREVIEW-hhh") && !strings.Contains(p.Text, tail) {
				sawText = true
			}
		}
		assertNoVerbatimCanary(t, p)
	}
	if !sawArgs || !sawBody || !sawText {
		t.Fatalf("parallel.branch previews incomplete: args=%v body=%v text=%v", sawArgs, sawBody, sawText)
	}
}

// TestInvariant_delegation_previews_bounded_scrubbed (AC2.3): a raw canary in a
// child's tool args, result body, or message text never appears VERBATIM in any
// projected SubagentPayload/ParallelPayload string field; only its clamped, scrubbed
// form may appear. (The Subagent half; the Parallel half is
// TestParallelNoContentLeakBehavioral.)
func TestInvariant_delegation_previews_bounded_scrubbed(t *testing.T) {
	evs, _ := newObservedSubagentRun(t)
	saw := false
	for _, p := range collectSubagent(evs) {
		if p == nil {
			continue
		}
		saw = true
		assertNoVerbatimCanary(t, p)
	}
	if !saw {
		t.Fatal("no subagent.* events emitted; the guard did not exercise")
	}
}

// TestInvariant_delegation_permission_ask_dropped (AC2.4): a child's permission.ask
// event is NEVER projected to the parent stream — the drop discipline survives the
// ADR 0079 widening. A child parked on an ask is auto-denied, and no subagent.tool
// projection carries InnerKind == EvPermissionAsk.
func TestInvariant_delegation_permission_ask_dropped(t *testing.T) {
	// The child asks for a mutating tool; the headless child contract auto-denies.
	childWrite := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "written"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("k1", "Write", `{"path":"x","content":"SECRET-ASK-REASON-TAIL-`+strings.Repeat("q", 600)+`"}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child finished"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, childWrite))
	task := agent.NewSubagentTool(childEngine)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatalf("a child permission.ask reached the parent stream: %+v", ev.Ask)
		}
	}
	for _, p := range collectSubagent(evs) {
		if p == nil {
			continue
		}
		if p.InnerKind == session.EvPermissionAsk {
			t.Fatalf("a permission.ask was projected as a subagent.tool preview: %+v", p)
		}
	}
}

// TestInvariant_gauntlet7_no_child_content_in_parent_conversation (AC2.5): under the
// widened projection, no INTERMEDIATE child content (the child's message deltas, its
// tool args, its tool result bodies) enters the parent Conversation — the previews
// ride the EVENT STREAM only. The child's final summary is the ONE allowed channel
// (it folds back as the Subagent tool result, by design); it is deliberately NOT
// asserted against here.
func TestInvariant_gauntlet7_no_child_content_in_parent_conversation(t *testing.T) {
	evs, sess := newObservedSubagentRun(t)
	for _, msg := range sess.Conversation.Messages {
		// The child's intermediate message text ("CANARY-hhh…-MSG") never becomes a
		// parent message. The final summary is allowed (it is the tool result), so
		// the assertion keys on the intermediate-only MSG marker.
		if strings.Contains(msg.Text, "-MSG") {
			t.Fatalf("child message text leaked into parent Conversation: %q", msg.Text)
		}
		for _, tc := range msg.ToolCalls {
			// The child's own tool calls (a "Read" call) never appear in the parent's
			// history — the parent only calls Subagent.
			if tc.Name == "Read" {
				t.Fatalf("child tool call leaked into parent Conversation: %+v", tc)
			}
		}
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, "-BODY") {
			t.Fatalf("child tool result body leaked into parent Conversation: %q", msg.ToolResult.Content)
		}
	}
	// The projection DID fire on the event stream (the guard is non-vacuous).
	saw := false
	for _, p := range collectSubagent(evs) {
		if p != nil && p.Detail != "" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("no preview projections emitted; the conversation guard did not exercise")
	}
}

// TestInvariant_subagent_payload_previews_bounded (AC2.6): the structural sentinel
// for SubagentPayload, mirroring TestParallelPayloadHasNoContentFields — the field
// set is exactly the documented metadata + bounded-preview allow-list, and raw
// content fields (Args/Content/Summary/FailReason) remain banned. Content fields may
// exist ONLY as the clampPreview-fed previews (Text/Detail, ADR 0079).
func TestInvariant_subagent_payload_previews_bounded(t *testing.T) {
	allowed := map[string]bool{
		"ParentCallID": true, "ChildID": true, "Goal": true, "Background": true,
		"RoutedCategory": true, "RoutedModel": true, "Model": true,
		// RoutingReason (issue #397) is the bare-metadata REASON the child was not
		// routed (a session.RoutingReason* gate const or a bounded harness/composition
		// miss string), EMPTY on a routed hit — never the task prompt or classifier
		// output. Same gauntlet-#7 footing as RoutedCategory/Model.
		"RoutingReason": true,
		"ToolName":      true, "IsError": true, "ToolCount": true,
		"Usage": true, "Stop": true, "DurationMs": true,
		// Text / Detail / InnerKind (ADR 0079) are the BOUNDED PREVIEW fields, fed
		// ONLY through clampPreview at the single drainChildObserved chokepoint;
		// client-only, never the parent's Conversation. The behavioral guards
		// (TestInvariant_delegation_previews_bounded_scrubbed) prove the raw body
		// never crosses.
		"Text": true, "Detail": true, "InnerKind": true,
		// Cause (issue #319) is the child run's FAILURE DETAIL — the loop's
		// ResultPayload.Error, a harness/provider error string, NOT child-authored
		// model output. Set on EvSubagentEnd only, clamped at the emit sites to
		// maxSubagentCausePreview, client-only like Stop/Usage. It is metadata about
		// HOW the delegation failed, on the same gauntlet-#7 footing as Stop.
		"Cause": true,
	}
	rt := reflect.TypeOf(session.SubagentPayload{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !allowed[f.Name] {
			t.Fatalf("SubagentPayload grew an unexpected field %q (%s): a new field MUST be reviewed "+
				"against gauntlet #7 — child content may cross only as a clampPreview-bounded, "+
				"client-only preview (ADR 0079). If legitimate, add it to the allow-list with a "+
				"justification.", f.Name, f.Type)
		}
	}
	for _, banned := range []string{"Args", "Content", "Summary", "FailReason"} {
		if _, ok := rt.FieldByName(banned); ok {
			t.Fatalf("SubagentPayload must not carry a raw content field %q (gauntlet #7)", banned)
		}
	}
}
