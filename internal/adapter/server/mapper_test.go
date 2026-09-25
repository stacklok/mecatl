package server

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

func TestResumableSessionStatusMetrics_Scenario2_GetSessionProjection(t *testing.T) {
	for _, kind := range []session.SessionKind{
		session.SessionKindMain,
		session.SessionKindScheduled,
		session.SessionKindSubagent,
		session.SessionKindParallelBranch,
		session.SessionKindTeamMember,
		session.SessionKindDebug,
	} {
		t.Run(string(kind), func(t *testing.T) {
			sess := session.New(session.SessionID("status-"+string(kind)), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "test"}, session.Limits{}, time.Unix(0, 0))
			sess.Kind = kind
			if err := sess.BeginTurn(); err != nil {
				t.Fatalf("BeginTurn: %v", err)
			}
			if err := sess.RecordUsage(session.Usage{InputTokens: 100, OutputTokens: 25}); err != nil {
				t.Fatalf("RecordUsage: %v", err)
			}
			sess.RecordLatestContextOccupancy(session.ContextOccupancy{InputTokens: 4096, Estimated: true})

			got := toProtoSession(sess, ResolvedModel{ProviderID: "test", ModelID: "model", ContextWindow: 8192}, nil, port.ProviderCapabilities{})
			if got.GetResolvedModel().GetContextWindow() != 8192 {
				t.Fatalf("resolved context window = %d, want 8192", got.GetResolvedModel().GetContextWindow())
			}
			if usage := got.GetTokenUsage()["main"].GetTotal(); usage.GetInputTokens() != 100 || usage.GetOutputTokens() != 25 {
				t.Fatalf("main lifetime usage = %+v, want input/output 100/25", usage)
			}
			if got.GetLatestContextOccupancy() == nil || got.GetLatestContextOccupancy().GetInputTokens() != 4096 || !got.GetLatestContextOccupancy().GetEstimated() {
				t.Fatalf("latest context occupancy = %+v, want 4096 estimated", got.GetLatestContextOccupancy())
			}
		})
	}
}

func TestResumableSessionStatusMetrics_Scenario2_LegacySnapshotCompatibility(t *testing.T) {
	legacyWire, err := proto.Marshal(&mecatlv1.Session{SessionId: "legacy"})
	if err != nil {
		t.Fatalf("marshal legacy Session: %v", err)
	}
	var restored mecatlv1.Session
	if err := proto.Unmarshal(legacyWire, &restored); err != nil {
		t.Fatalf("unmarshal legacy Session: %v", err)
	}
	if restored.GetLatestContextOccupancy() != nil {
		t.Fatalf("legacy latest_context_occupancy = %+v, want absent", restored.GetLatestContextOccupancy())
	}

	field := (&mecatlv1.Session{}).ProtoReflect().Descriptor().Fields().ByNumber(22)
	if field == nil || field.Name() != "latest_context_occupancy" || field.Message() == nil {
		t.Fatalf("Session field 22 = %v, want ContextOccupancy latest_context_occupancy", field)
	}

	currentWire, err := proto.Marshal(&mecatlv1.Session{
		SessionId:              "new",
		LatestContextOccupancy: &mecatlv1.ContextOccupancy{InputTokens: 4096, Estimated: true},
	})
	if err != nil {
		t.Fatalf("marshal current Session: %v", err)
	}
	legacyFile := protodesc.ToFileDescriptorProto(mecatlv1.File_mecatl_v1_harness_proto)
	for _, message := range legacyFile.MessageType {
		if message.GetName() != "Session" {
			continue
		}
		fields := message.Field[:0]
		for _, candidate := range message.Field {
			if candidate.GetNumber() != 22 {
				fields = append(fields, candidate)
			}
		}
		message.Field = fields
	}
	legacyDescriptor, err := protodesc.NewFile(legacyFile, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build legacy Session descriptor: %v", err)
	}
	legacyClient := dynamicpb.NewMessage(legacyDescriptor.Messages().ByName("Session"))
	if err := proto.Unmarshal(currentWire, legacyClient); err != nil {
		t.Fatalf("older client rejected additive field: %v", err)
	}
	if got := legacyClient.Get(legacyClient.Descriptor().Fields().ByName("session_id")).String(); got != "new" {
		t.Fatalf("older client session_id = %q, want new", got)
	}
	if len(legacyClient.GetUnknown()) == 0 {
		t.Fatal("older client did not retain the ignored additive field as unknown")
	}
}

func TestADR_0352_Scenario6_WireAndDebugger(t *testing.T) {
	zero := 0.0
	decision := &session.RoutingDecision{
		Backend: "jev", ClassifierModel: "jev-1.13.0", CandidateCategory: "deep", CandidateModel: "capable",
		Confidence: &zero, MinimumConfidence: &zero, Outcome: "fallback", ConsecutiveMisses: 2, MissLimit: 3, BreakerOpen: false,
	}
	for name, ev := range map[string]session.Event{
		"subagent": {Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{RoutingDecision: decision}},
		"parallel": {Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{RoutingDecision: decision}},
		"team":     {Type: session.EvTeamStart, Team: &session.TeamPayload{Roster: []session.TeamMemberSpec{{Name: "reviewer", RoutingDecision: decision, MemberSessionID: "private-member-id", MemberIncarnation: session.NewIncarnationID()}}}},
	} {
		t.Run(name, func(t *testing.T) {
			pb := toProto(ev)
			wire, err := proto.Marshal(pb)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var round mecatlv1.Event
			if err := proto.Unmarshal(wire, &round); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			jsonWire, err := protojson.Marshal(&round)
			if err != nil {
				t.Fatalf("protojson: %v", err)
			}
			text := string(jsonWire)
			for _, want := range []string{`"routingDecision"`, `"confidence":0`, `"minimumConfidence":0`, `"outcome":"fallback"`} {
				if !strings.Contains(text, want) {
					t.Fatalf("protobuf JSON %s missing %s", text, want)
				}
			}
			if strings.Contains(text, "private-member-id") || strings.Contains(text, "memberIncarnation") {
				t.Fatalf("private team lifetime leaked onto wire: %s", text)
			}
		})
	}

	bad := math.NaN()
	hostile := &session.RoutingDecision{
		Backend: "custom\x00", ClassifierModel: "model\xff\u202e", CandidateCategory: strings.Repeat("x", 1000),
		CandidateModel: "candidate\nsecret\u200b", Confidence: &bad, MinimumConfidence: &bad, Outcome: "invented",
	}
	pb := toProto(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{RoutingDecision: hostile}})
	if _, err := protojson.Marshal(pb); err != nil {
		t.Fatalf("hostile custom routing evidence broke protobuf JSON: %v", err)
	}
	got := pb.GetSubagent().GetRoutingDecision()
	if got.GetBackend() != "" || got.GetOutcome() != "" || got.Confidence != nil || got.MinimumConfidence != nil {
		t.Fatalf("unsafe closed/numeric evidence survived: %+v", got)
	}
	if len([]rune(got.GetCandidateCategory())) > 200 || strings.ContainsAny(got.GetCandidateModel(), "\n\r\x00") || strings.ContainsRune(got.GetCandidateModel(), '\u200b') || strings.ContainsRune(got.GetClassifierModel(), '\u202e') {
		t.Fatalf("unbounded/control-bearing candidate survived: %+v", got)
	}
	for name, invalid := range map[string]float64{"positive infinity": math.Inf(1), "negative infinity": math.Inf(-1), "below range": -0.01, "above range": 1.01} {
		t.Run(name, func(t *testing.T) {
			pb := toProto(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{RoutingDecision: &session.RoutingDecision{Confidence: &invalid, MinimumConfidence: &invalid}}})
			if got := pb.GetSubagent().GetRoutingDecision(); got.Confidence != nil || got.MinimumConfidence != nil {
				t.Fatalf("invalid score survived: %+v", got)
			}
			if _, err := protojson.Marshal(pb); err != nil {
				t.Fatalf("invalid score broke protobuf JSON: %v", err)
			}
		})
	}

	historical := toProto(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{}})
	if historical.GetSubagent().GetRoutingDecision() != nil {
		t.Fatalf("historical absence became an empty decision: %+v", historical.GetSubagent())
	}

	failedLog := &countingEventLog{failCalls: map[int]bool{1: true}}
	diag := &countingDiagnostics{}
	recorder := NewRunEventRecorder(t.Context(), recorderService(failedLog, diag), "routing-session")
	recorder.Observe(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{RoutingDecision: decision}})
	recorder.Observe(session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	recorder.Close()
	if len(failedLog.attempts) != 2 || len(failedLog.recorded) != 1 || failedLog.recorded[0].Type != session.EvResult || diag.warnings != 1 {
		t.Fatalf("append failure changed warn-and-continue behavior: attempts=%d recorded=%+v warnings=%d", len(failedLog.attempts), failedLog.recorded, diag.warnings)
	}
}

func TestADR_0352_Scenario6_RealProducerRelayReloadDebugger(t *testing.T) {
	store := memstore.New()
	log := memstore.NewEventLog()
	child := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("child done")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "capable-model",
	})
	subagent := agent.NewSubagentTool(child,
		agent.WithSubagentStore(store),
		agent.WithSubagentReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }),
		agent.WithSubagentEngineFactory(func(model string) (*agent.Engine, bool) {
			if model != "capable-model" {
				return nil, false
			}
			return child, true
		}),
	)
	catalog := tool.NewCatalog()
	catalog.MustRegister(subagent)
	confidence, minimum := 0.9, 0.5
	parent := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("route-call", "Subagent", json.RawMessage(`{"prompt":"deep review"}`))),
			mockllm.TextTurn("parent done"),
		),
		Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "inherited-model",
		SubagentModelRouter: &agent.SubagentModelRouter{
			Backend: "jev", ClassifierModel: "jev-1.13.0", MinimumConfidence: &minimum,
			Route: func(context.Context, string) agent.ModelRouteResult {
				return agent.ModelRouteResult{Category: "deep", Model: "capable-model", OK: true, Confidence: &confidence}
			},
		},
	})
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	root := session.New("routing-e2e", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	root.Owner = &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	run := parent.Run(t.Context(), root, env, agent.RunRequest{Text: "delegate"})
	recorder := NewRunEventRecorder(t.Context(), recorderService(log, port.NopDiagnostics{}), root.ID)
	var produced *session.RoutingDecision
	var eventTypes []session.EventType
	var toolResult string
	for ev := range run.Events() {
		eventTypes = append(eventTypes, ev.Type)
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			toolResult = ev.ToolResult.Content
		}
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			produced = ev.Subagent.RoutingDecision
		}
		recorder.Observe(ev)
	}
	recorder.Close()
	if produced == nil || produced.CandidateModel != "capable-model" || produced.Outcome != "routed" {
		t.Fatalf("real producer decision = %+v, events=%v, tool_result=%q", produced, eventTypes, toolResult)
	}
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	inspector := sessiondebug.New(root.ID, store, log)
	result, err := inspector.Execute(t.Context(), session.NewToolCall("inspect", sessiondebug.ToolName, json.RawMessage(`{"view":"delegation"}`)), tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, `"candidate_model":"capable-model"`) || !strings.Contains(result.Content, `"actual_model":"capable-model"`) {
		t.Fatalf("producer -> relay -> log -> reload -> debugger chain = %+v", result)
	}
}

func TestIncarnationsAreNotProjectedToNormalClients(t *testing.T) {
	incarnation := session.NewIncarnationID()
	for _, ev := range []session.Event{
		{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ChildID: "child", ChildIncarnation: incarnation}},
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ChildID: "child", ChildIncarnation: incarnation}},
		{Type: session.EvTeamMember, Team: &session.TeamPayload{MemberSessionID: "child", MemberIncarnation: incarnation}},
	} {
		out := toProto(ev).ProtoReflect()
		var payload protoreflect.Message
		for i := 0; i < out.Descriptor().Fields().Len(); i++ {
			field := out.Descriptor().Fields().Get(i)
			if field.Kind() == protoreflect.MessageKind && out.Has(field) {
				payload = out.Get(field).Message()
			}
		}
		if payload != nil && (payload.Descriptor().Fields().ByName("child_incarnation") != nil || payload.Descriptor().Fields().ByName("member_incarnation") != nil) {
			t.Fatalf("event %q exposes internal incarnation on the client wire", ev.Type)
		}
	}
}

// TestToProtoTable round-trips every EventType and each structured submessage
// through toProto, asserting the proto shape matches the domain Event.
func TestToProtoTable(t *testing.T) {
	cases := []struct {
		name   string
		in     session.Event
		assert func(t *testing.T, got *mecatlv1.Event)
	}{
		{
			name: "session.init",
			in:   session.Event{Type: session.EvSessionInit, Seq: 1, Turn: 0},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "session.init" || got.GetSeq() != 1 {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "turn.start",
			in:   session.Event{Type: session.EvTurnStart, Seq: 2, Turn: 3},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "turn.start" || got.GetTurn() != 3 {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "message.delta",
			in:   session.Event{Type: session.EvMessageDelta, Seq: 3, Turn: 1, Text: "hello"},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "message.delta" || got.GetText() != "hello" {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "reasoning.delta",
			in:   session.Event{Type: session.EvReasoningDelta, Seq: 11, Turn: 1, Text: "thinking…"},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "reasoning.delta" || got.GetText() != "thinking…" {
					t.Fatalf("got %+v", got)
				}
				if got.GetTurnEnd() != nil {
					t.Fatalf("reasoning.delta should not carry a turn_end payload: %+v", got)
				}
			},
		},
		{
			name: "turn.end",
			in: session.Event{Type: session.EvTurnEnd, Seq: 12, Turn: 2,
				TurnEnd: &session.TurnEndPayload{DurationMs: 4100, Estimated: true,
					Usage: session.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 800, CacheWriteTokens: 100, ReasoningTokens: 40}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "turn.end" || got.GetTurn() != 2 {
					t.Fatalf("got %+v", got)
				}
				// turn.end carries its per-turn data only in the typed turn_end submessage.
				te := got.GetTurnEnd()
				if te == nil {
					t.Fatalf("turn.end missing turn_end payload: %+v", got)
				}
				if te.GetDurationMs() != 4100 || !te.GetEstimated() {
					t.Fatalf("turn_end = %+v, want duration 4100 and estimated", te)
				}
				u := te.GetUsage()
				if u.GetInputTokens() != 1200 || u.GetOutputTokens() != 340 ||
					u.GetCacheReadTokens() != 800 || u.GetCacheWriteTokens() != 100 ||
					u.GetReasoningTokens() != 40 {
					t.Fatalf("per-turn usage mismatch: %+v", u)
				}
			},
		},
		{
			name: "tool.call",
			in: session.Event{Type: session.EvToolCall, Seq: 4, Turn: 1,
				ToolCall: &session.ToolCall{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tc := got.GetToolCall()
				if tc == nil || tc.GetId() != "c1" || tc.GetName() != "Read" || tc.GetArgs() != `{"path":"a.go"}` {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "tool.result",
			in: session.Event{Type: session.EvToolResult, Seq: 5, Turn: 1,
				ToolResult: &session.ToolResult{CallID: "c1", Content: "body", IsError: true}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tr := got.GetToolResult()
				if tr == nil || tr.GetCallId() != "c1" || tr.GetContent() != "body" || !tr.GetIsError() {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "permission.ask",
			in: session.Event{Type: session.EvPermissionAsk, Seq: 6, Turn: 1,
				Ask: &session.PendingAsk{AskID: "a1", Tool: "Write", Args: json.RawMessage(`{"path":"x"}`), Reason: "needs approval"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				a := got.GetAsk()
				if a == nil || a.GetAskId() != "a1" || a.GetTool() != "Write" ||
					a.GetArgs() != `{"path":"x"}` || a.GetReason() != "needs approval" {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "hook",
			in: session.Event{Type: session.EvHook, Seq: 7, Turn: 1, Text: "blocked-by-policy",
				Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Shell", Decision: session.HookBlocked, CallID: "call-7"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "hook" || got.GetText() != "blocked-by-policy" {
					t.Fatalf("got %+v", got)
				}
				h := got.GetHook()
				if h == nil || h.GetPhase() != "PreToolUse" || h.GetTool() != "Shell" ||
					h.GetDecision() != mecatlv1.HookDecision_HOOK_DECISION_BLOCKED ||
					h.GetCallId() != "call-7" {
					t.Fatalf("hook payload mismatch: %+v", h)
				}
			},
		},
		{
			name: "hook info default",
			in:   session.Event{Type: session.EvHook, Seq: 7, Turn: 1, Text: "ran", Hook: &session.HookPayload{Phase: "Stop"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				h := got.GetHook()
				if h == nil || h.GetDecision() != mecatlv1.HookDecision_HOOK_DECISION_INFO {
					t.Fatalf("empty decision should map to INFO: %+v", h)
				}
				if h.GetCallId() != "" {
					t.Errorf("a non-tool (Stop) hook should carry no call id, got %q", h.GetCallId())
				}
			},
		},
		{
			name: "hook advisory",
			in: session.Event{Type: session.EvHook, Seq: 8, Turn: 1, Text: "guardrail advisory: borderline",
				Hook: &session.HookPayload{Phase: "PostToolUse", Tool: "WebFetch", Decision: session.HookAdvisory, CallID: "call-8"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				h := got.GetHook()
				if h == nil || h.GetDecision() != mecatlv1.HookDecision_HOOK_DECISION_ADVISORY ||
					h.GetPhase() != "PostToolUse" || h.GetTool() != "WebFetch" || h.GetCallId() != "call-8" {
					t.Fatalf("advisory hook payload mismatch: %+v", h)
				}
			},
		},
		{
			name: "subagent.start",
			in: session.Event{Type: session.EvSubagentStart, Seq: 20, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1", Goal: "investigate main.go"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetParentCallId() != "p1" || s.GetChildId() != "subagent-p1" || s.GetGoal() != "investigate main.go" {
					t.Fatalf("subagent.start payload mismatch: %+v", s)
				}
			},
		},
		{
			// Routed-category metadata is BARE metadata (a label + a model id), set on
			// subagent.start only when the opt-in model router classified the delegation
			// (ADR 0031). It must round-trip to the proto fields verbatim — gauntlet #7
			// holds (no child content crosses). The generic Model field (issue #112 / ADR
			// 0035) equals RoutedModel when routed.
			name: "subagent.start routed",
			in: session.Event{Type: session.EvSubagentStart, Seq: 200, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1", Goal: "investigate main.go",
					RoutedCategory: "small", RoutedModel: "openai/gpt-4.1-mini", Model: "openai/gpt-4.1-mini"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetRoutedCategory() != "small" || s.GetRoutedModel() != "openai/gpt-4.1-mini" {
					t.Fatalf("subagent.start routed metadata mismatch: %+v", s)
				}
				if s.GetModel() != "openai/gpt-4.1-mini" || s.GetModel() != s.GetRoutedModel() {
					t.Fatalf("subagent.start Model should equal RoutedModel when routed: %+v", s)
				}
			},
		},
		{
			// The generic Model field (issue #112 / ADR 0035) is set UNCONDITIONALLY —
			// here for the inherited/default case (no router fired, routed fields empty).
			// It must round-trip verbatim; bare metadata, gauntlet #7.
			name: "subagent.start inherited model",
			in: session.Event{Type: session.EvSubagentStart, Seq: 201, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1", Goal: "investigate main.go",
					Model: "openai/gpt-4.5", RoutingReason: session.RoutingReasonRouterDisabled}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetModel() != "openai/gpt-4.5" {
					t.Fatalf("subagent.start inherited Model mismatch: %+v", s)
				}
				if s.GetRoutedCategory() != "" || s.GetRoutedModel() != "" {
					t.Fatalf("subagent.start inherited must have empty routed fields: %+v", s)
				}
				if s.GetRoutingReason() != session.RoutingReasonRouterDisabled {
					t.Fatalf("subagent.start RoutingReason not mapped: %+v", s)
				}
			},
		},
		{
			name: "subagent.tool",
			in: session.Event{Type: session.EvSubagentTool, Seq: 21, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1", ToolName: "Grep", IsError: true, ToolCount: 3}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetToolName() != "Grep" || !s.GetIsError() || s.GetToolCount() != 3 {
					t.Fatalf("subagent.tool payload mismatch: %+v", s)
				}
			},
		},
		{
			// ADR 0079 bounded previews: the tool/message preview fields (Text / Detail /
			// InnerKind) the delegation chokepoint now populates on subagent.tool events
			// round-trip verbatim over the wire. Already redacted upstream (clamped in
			// engine/agent), so the mapper copies them unchanged; inner_kind is a STRING
			// passthrough, mirroring Team.inner_kind.
			name: "subagent.tool with bounded previews",
			in: session.Event{Type: session.EvSubagentTool, Seq: 23, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1",
					ToolName: "Grep", ToolCount: 2, InnerKind: session.EvToolCall,
					Detail: `{"pattern":"foo","path":"main.go"}`}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetInnerKind() != "tool.call" ||
					s.GetDetail() != `{"pattern":"foo","path":"main.go"}` {
					t.Fatalf("subagent.tool preview payload mismatch: %+v", s)
				}
				if s.GetText() != "" {
					t.Fatalf("subagent.tool tool.call preview should carry Detail, not Text: %+v", s)
				}
			},
		},
		{
			name: "subagent.tool message preview",
			in: session.Event{Type: session.EvSubagentTool, Seq: 24, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1",
					ToolCount: 2, InnerKind: session.EvMessageDelta, Text: "scanning src/…"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetInnerKind() != "message.delta" || s.GetText() != "scanning src/…" {
					t.Fatalf("subagent.tool message preview mismatch: %+v", s)
				}
				if s.GetDetail() != "" {
					t.Fatalf("subagent.tool message preview should carry Text, not Detail: %+v", s)
				}
			},
		},
		{
			name: "subagent.end",
			in: session.Event{Type: session.EvSubagentEnd, Seq: 22, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1", ToolCount: 5,
					Usage: session.Usage{InputTokens: 90, OutputTokens: 12, CacheReadTokens: 40, CacheWriteTokens: 8},
					Stop:  session.StopMaxToolCalls, DurationMs: 1234}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetToolCount() != 5 || s.GetStop() != "max_tool_calls" || s.GetDurationMs() != 1234 {
					t.Fatalf("subagent.end payload mismatch: %+v", s)
				}
				u := s.GetUsage()
				if u.GetInputTokens() != 90 || u.GetOutputTokens() != 12 ||
					u.GetCacheReadTokens() != 40 || u.GetCacheWriteTokens() != 8 {
					t.Fatalf("subagent.end usage mismatch: %+v", u)
				}
			},
		},
		{
			// Issue #319: a FAILED child's cause must survive toProtoSubagent, or the TUI
			// (and every gRPC consumer) can only ever show "stop:error" with no WHY.
			name: "subagent.end carries the failure cause",
			in: session.Event{Type: session.EvSubagentEnd, Seq: 23, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1",
					Stop:  session.StopError,
					Cause: "agent: stream: upstream 503 model overloaded"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				s := got.GetSubagent()
				if s == nil || s.GetStop() != "error" {
					t.Fatalf("subagent.end payload mismatch: %+v", s)
				}
				if s.GetCause() != "agent: stream: upstream 503 model overloaded" {
					t.Fatalf("subagent.end cause not mapped: %q", s.GetCause())
				}
			},
		},
		{
			// The negative half: a clean terminal must leave cause empty so a consumer can
			// treat a non-empty cause as "this delegation failed".
			name: "subagent.end clean terminal carries no cause",
			in: session.Event{Type: session.EvSubagentEnd, Seq: 24, Turn: 1,
				Subagent: &session.SubagentPayload{ParentCallID: "p1", ChildID: "subagent-p1",
					Stop: session.StopEndTurn}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if c := got.GetSubagent().GetCause(); c != "" {
					t.Fatalf("clean subagent.end must carry no cause, got %q", c)
				}
			},
		},
		{
			name: "team.start",
			in: session.Event{Type: session.EvTeamStart, Seq: 30, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1",
					Roster: []session.TeamMemberSpec{
						{Name: "lead", Role: "coordinate", Lead: true, Model: "openai/gpt-4.5",
							RoutingReason: session.RoutingReasonAgentDefPinned},
						{Name: "worker", Role: "investigate", Mutating: true,
							RoutedCategory: "large", RoutedModel: "anthropic/claude-opus-4", Model: "anthropic/claude-opus-4"},
					}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tm := got.GetTeam()
				if tm == nil || tm.GetParentCallId() != "p1" || tm.GetTeamId() != "team-p1" {
					t.Fatalf("team.start ids mismatch: %+v", tm)
				}
				r := tm.GetRoster()
				if len(r) != 2 || r[0].GetName() != "lead" || !r[0].GetLead() ||
					r[1].GetName() != "worker" || !r[1].GetMutating() || r[1].GetLead() {
					t.Fatalf("team.start roster mismatch: %+v", r)
				}
				// Routed-category metadata is BARE metadata (a label + a model id), set on the
				// team.start roster entry only when the opt-in model router classified the
				// member (ADR 0031 / ADR 0034). It must round-trip verbatim — gauntlet #7 holds
				// (no member content crosses). The lead was unrouted (both empty).
				if r[0].GetRoutedCategory() != "" || r[0].GetRoutedModel() != "" {
					t.Fatalf("team.start unrouted lead carries routed metadata: %+v", r[0])
				}
				if r[0].GetRoutingReason() != session.RoutingReasonAgentDefPinned {
					t.Fatalf("team.start lead RoutingReason not mapped: %+v", r[0])
				}
				if r[1].GetRoutedCategory() != "large" || r[1].GetRoutedModel() != "anthropic/claude-opus-4" {
					t.Fatalf("team.start routed member metadata mismatch: %+v", r[1])
				}
				// The generic Model field (issue #112 / ADR 0035) round-trips for BOTH
				// members: the routed worker's Model == RoutedModel, and the unrouted lead
				// carries its inherited model with empty routed fields.
				if r[0].GetModel() != "openai/gpt-4.5" {
					t.Fatalf("team.start lead inherited Model mismatch: %+v", r[0])
				}
				if r[1].GetModel() != "anthropic/claude-opus-4" || r[1].GetModel() != r[1].GetRoutedModel() {
					t.Fatalf("team.start routed worker Model should equal RoutedModel: %+v", r[1])
				}
			},
		},
		{
			name: "team.member",
			in: session.Event{Type: session.EvTeamMember, Seq: 31, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Member: "worker",
					MemberSessionID: "team-p1-worker",
					InnerKind:       session.EvToolResult, ToolName: "Read", Detail: "capped body", IsError: true}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tm := got.GetTeam()
				if tm == nil || tm.GetMember() != "worker" || tm.GetInnerKind() != "tool.result" ||
					tm.GetToolName() != "Read" || tm.GetDetail() != "capped body" || !tm.GetIsError() {
					t.Fatalf("team.member payload mismatch: %+v", tm)
				}
				// member_session_id (the CancelChild handle — D16) crosses verbatim.
				if tm.GetMemberSessionId() != "team-p1-worker" {
					t.Fatalf("team.member member_session_id = %q, want team-p1-worker", tm.GetMemberSessionId())
				}
			},
		},
		{
			name: "team.member turn.end context meter",
			in: session.Event{Type: session.EvTeamMember, Seq: 33, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Member: "worker",
					InnerKind:     session.EvTurnEnd,
					Usage:         session.Usage{InputTokens: 40000, OutputTokens: 80},
					ContextUsed:   40000,
					ContextWindow: 200000}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tm := got.GetTeam()
				if tm == nil || tm.GetContextUsed() != 40000 || tm.GetContextWindow() != 200000 {
					t.Fatalf("team.member context-meter fields mismatch: used=%d window=%d",
						tm.GetContextUsed(), tm.GetContextWindow())
				}
			},
		},
		{
			// Issue #331: a failed round's cause must survive toProtoTeam, so the
			// TUI/ACP can surface WHY a member round failed — the Team mirror of the
			// #319 subagent.end cause case.
			name: "team.member result carries the failure cause on StopError",
			in: session.Event{Type: session.EvTeamMember, Seq: 35, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Member: "worker",
					MemberSessionID: "team-p1-worker",
					InnerKind:       session.EvResult, Stop: session.StopError,
					Cause: "agent: stream: upstream 503 model overloaded"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tm := got.GetTeam()
				if tm == nil {
					t.Fatal("team.member result payload missing")
				}
				if tm.GetCause() != "agent: stream: upstream 503 model overloaded" {
					t.Fatalf("team.member result cause not mapped: %q", tm.GetCause())
				}
			},
		},
		{
			// The negative half: a clean per-round result leaves cause empty.
			name: "team.member clean result carries no cause",
			in: session.Event{Type: session.EvTeamMember, Seq: 36, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Member: "worker",
					InnerKind: session.EvResult, Stop: session.StopEndTurn, Text: "done"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if c := got.GetTeam().GetCause(); c != "" {
					t.Fatalf("clean team.member result must carry no cause, got %q", c)
				}
			},
		},
		{
			name: "team.end",
			in: session.Event{Type: session.EvTeamEnd, Seq: 32, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Rounds: 3,
					Stop:  session.StopEndTurn,
					Usage: session.Usage{InputTokens: 50, OutputTokens: 9}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				tm := got.GetTeam()
				if tm == nil || tm.GetRounds() != 3 || tm.GetStop() != "end_turn" {
					t.Fatalf("team.end payload mismatch: %+v", tm)
				}
				if tm.GetUsage().GetInputTokens() != 50 || tm.GetUsage().GetOutputTokens() != 9 {
					t.Fatalf("team.end usage mismatch: %+v", tm.GetUsage())
				}
			},
		},
		{
			name: "team.tasks snapshot",
			in: session.Event{Type: session.EvTeamTasks, Seq: 34, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1",
					Tasks: []session.TeamTaskSnapshot{
						{ID: "task-1", Description: "investigate", State: "completed", Assignee: "scout"},
						{ID: "task-2", Description: "fix", State: "pending", Deps: []string{"task-1"}},
					}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "team.tasks" {
					t.Fatalf("event type = %q, want team.tasks", got.GetType())
				}
				tm := got.GetTeam()
				if tm == nil || tm.GetMember() != "" {
					t.Fatalf("team.tasks must carry no member: %+v", tm)
				}
				tasks := tm.GetTasks()
				if len(tasks) != 2 {
					t.Fatalf("tasks len = %d, want 2: %+v", len(tasks), tasks)
				}
				if tasks[0].GetId() != "task-1" || tasks[0].GetState() != "completed" ||
					tasks[0].GetAssignee() != "scout" {
					t.Errorf("task-1 mapping mismatch: %+v", tasks[0])
				}
				if tasks[1].GetId() != "task-2" || len(tasks[1].GetDeps()) != 1 ||
					tasks[1].GetDeps()[0] != "task-1" {
					t.Errorf("task-2 deps not preserved: %+v", tasks[1])
				}
			},
		},
		{
			name: "team.findings snapshot",
			in: session.Event{Type: session.EvTeamFindings, Seq: 35, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1",
					Findings: []session.TeamFindingSnapshot{
						{Member: "scout", Body: "the cache key omits the tenant id"},
						{Member: "fixer", Body: "patched the key"},
					}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "team.findings" {
					t.Fatalf("event type = %q, want team.findings", got.GetType())
				}
				tm := got.GetTeam()
				if tm == nil || tm.GetMember() != "" {
					t.Fatalf("team.findings must carry no member: %+v", tm)
				}
				findings := tm.GetFindings()
				if len(findings) != 2 {
					t.Fatalf("findings len = %d, want 2: %+v", len(findings), findings)
				}
				if findings[0].GetMember() != "scout" || findings[0].GetBody() != "the cache key omits the tenant id" {
					t.Errorf("finding[0] mapping mismatch: %+v", findings[0])
				}
				if findings[1].GetMember() != "fixer" {
					t.Errorf("finding[1] member mismatch: %+v", findings[1])
				}
			},
		},
		{
			name: "team.end disposition snapshot",
			in: session.Event{Type: session.EvTeamEnd, Seq: 36, Turn: 1,
				Team: &session.TeamPayload{ParentCallID: "p1", TeamID: "team-p1", Rounds: 2,
					Stop: session.StopEndTurn,
					Dispositions: []session.TeamMemberDisposition{
						{Name: "lead", Disposition: "done"},
						{Name: "scout", Disposition: "stopped", Reason: "budget"},
						{Name: "fixer", Disposition: "stopped", Reason: "error", ErrorRounds: 2},
						{Name: "probe", Disposition: "stopped", Reason: "cancelled"},
						// Retried-then-finished (issue #318): done with NO reason, so
						// error_rounds is the only thing on the wire that says the run was
						// not clean. If the mapper dropped the count this member would be
						// byte-identical to the clean lead.
						{Name: "medic", Disposition: "done", ErrorRounds: 1},
					}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				disps := got.GetTeam().GetDispositions()
				if len(disps) != 5 {
					t.Fatalf("dispositions len = %d, want 5: %+v", len(disps), disps)
				}
				// done → stopped=false, reason UNSPECIFIED, zero error rounds.
				if disps[0].GetName() != "lead" || disps[0].GetStopped() ||
					disps[0].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_UNSPECIFIED ||
					disps[0].GetErrorRounds() != 0 {
					t.Errorf("done disposition mismatch: %+v", disps[0])
				}
				if !disps[1].GetStopped() || disps[1].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET {
					t.Errorf("budget disposition mismatch: %+v", disps[1])
				}
				if !disps[2].GetStopped() || disps[2].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_ERROR {
					t.Errorf("error disposition mismatch: %+v", disps[2])
				}
				if !disps[2].GetStopped() || disps[2].GetErrorRounds() != 2 {
					t.Errorf("a benched member's error_rounds must cross: %+v", disps[2])
				}
				if !disps[3].GetStopped() || disps[3].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED {
					t.Errorf("cancelled disposition mismatch: %+v", disps[3])
				}
				if disps[3].GetErrorRounds() != 0 {
					t.Errorf("cancellation is not a run-level error: %+v", disps[3])
				}
				// The retried-then-finished member: done, no reason, error_rounds > 0.
				if disps[4].GetName() != "medic" || disps[4].GetStopped() ||
					disps[4].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_UNSPECIFIED ||
					disps[4].GetErrorRounds() != 1 {
					t.Errorf("retried-then-finished disposition mismatch: %+v", disps[4])
				}
			},
		},
		{
			name: "parallel.start",
			in: session.Event{Type: session.EvParallelStart, Seq: 40, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Join: "judge", BranchCount: 3}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if got.GetType() != "parallel.start" {
					t.Fatalf("type = %q, want parallel.start", got.GetType())
				}
				if p == nil || p.GetParentCallId() != "p1" || p.GetJoin() != "judge" || p.GetBranchCount() != 3 {
					t.Fatalf("parallel.start payload mismatch: %+v", p)
				}
			},
		},
		{
			name: "parallel.branch branch_start",
			in: session.Event{Type: session.EvParallelBranch, Seq: 41, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchStart,
					BranchIndex: 1, ChildID: "parallel-p1-1", BranchLabel: "branch-2", Goal: "explore beta",
					RoutedCategory: "small", RoutedModel: "openai/gpt-4.1-mini", Model: "openai/gpt-4.1-mini"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetKind() != "branch_start" || p.GetBranchIndex() != 1 ||
					p.GetBranchLabel() != "branch-2" || p.GetGoal() != "explore beta" {
					t.Fatalf("parallel branch_start payload mismatch: %+v", p)
				}
				// child_id (the CancelChild handle — D16) crosses verbatim.
				if p.GetChildId() != "parallel-p1-1" {
					t.Fatalf("parallel branch_start child_id = %q, want parallel-p1-1", p.GetChildId())
				}
				// Routed-category metadata is BARE metadata (a label + a model id), set on
				// branch_start only when the opt-in model router classified the branch (ADR
				// 0031 / ADR 0034). It round-trips verbatim — gauntlet #7 holds (no branch
				// content crosses).
				if p.GetRoutedCategory() != "small" || p.GetRoutedModel() != "openai/gpt-4.1-mini" {
					t.Fatalf("parallel branch_start routed metadata mismatch: %+v", p)
				}
				// The generic Model field (issue #112 / ADR 0035) equals RoutedModel when routed.
				if p.GetModel() != "openai/gpt-4.1-mini" || p.GetModel() != p.GetRoutedModel() {
					t.Fatalf("parallel branch_start Model should equal RoutedModel when routed: %+v", p)
				}
			},
		},
		{
			// The generic Model field (issue #112 / ADR 0035) for the inherited/default
			// branch case (no router fired, routed fields empty). Round-trips verbatim.
			name: "parallel.branch branch_start inherited model",
			in: session.Event{Type: session.EvParallelBranch, Seq: 41, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchStart,
					BranchIndex: 0, ChildID: "parallel-p1-0", BranchLabel: "branch-1", Goal: "explore alpha",
					Model: "anthropic/claude-3.5", RoutingReason: session.RoutingReasonTargetUnavailable}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetModel() != "anthropic/claude-3.5" {
					t.Fatalf("parallel branch_start inherited Model mismatch: %+v", p)
				}
				if p.GetRoutedCategory() != "" || p.GetRoutedModel() != "" {
					t.Fatalf("parallel branch_start inherited must have empty routed fields: %+v", p)
				}
				if p.GetRoutingReason() != session.RoutingReasonTargetUnavailable {
					t.Fatalf("parallel branch_start RoutingReason not mapped: %+v", p)
				}
			},
		},
		{
			name: "parallel.branch branch_tool",
			in: session.Event{Type: session.EvParallelBranch, Seq: 42, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchTool,
					BranchIndex: 0, ToolName: "Grep", IsError: true, ToolCount: 2}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetKind() != "branch_tool" || p.GetToolName() != "Grep" ||
					!p.GetIsError() || p.GetToolCount() != 2 {
					t.Fatalf("parallel branch_tool payload mismatch: %+v", p)
				}
			},
		},
		{
			// ADR 0079 bounded previews: the branchTool re-tag now projects Text / Detail /
			// InnerKind on branch_tool events; they round-trip verbatim (already clamped
			// upstream in engine/agent). inner_kind is a STRING passthrough.
			name: "parallel.branch branch_tool with bounded previews",
			in: session.Event{Type: session.EvParallelBranch, Seq: 46, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchTool,
					BranchIndex: 1, ToolName: "Read", ToolCount: 3, InnerKind: session.EvToolResult,
					Detail: "package main …"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetKind() != "branch_tool" || p.GetInnerKind() != "tool.result" ||
					p.GetDetail() != "package main …" {
					t.Fatalf("parallel branch_tool preview payload mismatch: %+v", p)
				}
				if p.GetText() != "" {
					t.Fatalf("parallel branch_tool tool.result preview should carry Detail, not Text: %+v", p)
				}
			},
		},
		{
			name: "parallel.branch branch_tool message preview",
			in: session.Event{Type: session.EvParallelBranch, Seq: 47, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchTool,
					BranchIndex: 1, ToolCount: 3, InnerKind: session.EvResult, Text: "branch summary"}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetInnerKind() != "result" || p.GetText() != "branch summary" {
					t.Fatalf("parallel branch_tool message preview mismatch: %+v", p)
				}
				if p.GetDetail() != "" {
					t.Fatalf("parallel branch_tool result preview should carry Text, not Detail: %+v", p)
				}
			},
		},
		{
			name: "parallel.branch branch_end",
			in: session.Event{Type: session.EvParallelBranch, Seq: 43, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Kind: session.ParallelBranchEnd,
					BranchIndex: 2, ChildID: "parallel-p1-2", ToolCount: 4, Failed: true,
					Stop: session.StopError, DurationMs: 555,
					Usage: session.Usage{InputTokens: 12, OutputTokens: 3}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetKind() != "branch_end" || p.GetBranchIndex() != 2 ||
					!p.GetFailed() ||
					p.GetStop() != "error" || p.GetDurationMs() != 555 || p.GetToolCount() != 4 {
					t.Fatalf("parallel branch_end payload mismatch: %+v", p)
				}
				if p.GetChildId() != "parallel-p1-2" {
					t.Fatalf("parallel branch_end child_id = %q, want parallel-p1-2", p.GetChildId())
				}
				if u := p.GetUsage(); u.GetInputTokens() != 12 || u.GetOutputTokens() != 3 {
					t.Fatalf("parallel branch_end usage mismatch: %+v", u)
				}
			},
		},
		{
			name: "parallel.end winner",
			in: session.Event{Type: session.EvParallelEnd, Seq: 44, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Join: "judge", BranchCount: 3,
					Winner: 1, Stop: session.StopEndTurn,
					Usage: session.Usage{InputTokens: 100, OutputTokens: 20}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if got.GetType() != "parallel.end" {
					t.Fatalf("type = %q, want parallel.end", got.GetType())
				}
				if p == nil || p.GetWinner() != 1 ||
					p.GetJoin() != "judge" || p.GetBranchCount() != 3 || p.GetStop() != "end_turn" {
					t.Fatalf("parallel.end payload mismatch: %+v", p)
				}
				if u := p.GetUsage(); u.GetInputTokens() != 100 || u.GetOutputTokens() != 20 {
					t.Fatalf("parallel.end usage mismatch: %+v", u)
				}
			},
		},
		{
			name: "parallel.end join=all no winner",
			in: session.Event{Type: session.EvParallelEnd, Seq: 45, Turn: 1,
				Parallel: &session.ParallelPayload{ParentCallID: "p1", Join: "all", BranchCount: 2, Winner: -1}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				p := got.GetParallel()
				if p == nil || p.GetWinner() != -1 {
					t.Fatalf("parallel.end (all) should carry Winner=-1, no workspace: %+v", p)
				}
			},
		},
		{
			name: "compaction",
			in:   session.Event{Type: session.EvCompaction, Seq: 8, Turn: 2, Text: "summary"},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				if got.GetType() != "compaction" || got.GetText() != "summary" {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "result",
			in: session.Event{Type: session.EvResult, Seq: 9, Turn: 2,
				Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: "all done",
					Usage: session.Usage{InputTokens: 15, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 1}}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				res := got.GetResult()
				if res == nil || res.GetStop() != "end_turn" || res.GetText() != "all done" {
					t.Fatalf("result mismatch: %+v", got)
				}
				u := res.GetUsage()
				if u.GetInputTokens() != 15 || u.GetOutputTokens() != 5 ||
					u.GetCacheReadTokens() != 3 || u.GetCacheWriteTokens() != 1 {
					t.Fatalf("usage mismatch: %+v", u)
				}
			},
		},
		{
			name: "result permanent error",
			in: session.Event{Type: session.EvResult, Seq: 10, Turn: 2,
				Result: &session.ResultPayload{
					Stop:        session.StopError,
					Error:       "invalid_encrypted_content",
					Disposition: session.RetryDispositionPermanent,
				}},
			assert: func(t *testing.T, got *mecatlv1.Event) {
				res := got.GetResult()
				if res == nil || res.GetStop() != "error" || res.GetError() != "invalid_encrypted_content" {
					t.Fatalf("result mismatch: %+v", got)
				}
				if res.GetRetryDisposition() != mecatlv1.RetryDisposition_RETRY_DISPOSITION_PERMANENT {
					t.Fatalf("retry disposition = %v, want permanent", res.GetRetryDisposition())
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toProto(tc.in)
			if got.GetType() != string(tc.in.Type) {
				t.Fatalf("type = %q, want %q", got.GetType(), tc.in.Type)
			}
			if got.GetSeq() != tc.in.Seq {
				t.Fatalf("seq = %d, want %d", got.GetSeq(), tc.in.Seq)
			}
			tc.assert(t, got)
		})
	}
}

// TestToProtoNoSubmessages confirms a bare event leaves all submessages nil.
func TestToProtoNoSubmessages(t *testing.T) {
	got := toProto(session.Event{Type: session.EvTurnStart})
	if got.GetToolCall() != nil || got.GetToolResult() != nil || got.GetAsk() != nil ||
		got.GetResult() != nil || got.GetTurnEnd() != nil ||
		got.GetSubagent() != nil || got.GetTeam() != nil || got.GetParallel() != nil ||
		got.GetApproval() != nil || got.GetUserPrompt() != nil || got.GetCompactionArchive() != nil {
		t.Fatalf("unexpected submessage on bare event: %+v", got)
	}
}

// TestToProtoLogOnlyPayloads pins the three log-only submessage branches added so
// the StreamSessionEvents replay surfaces them (the live Converse relay skips
// them, but toProto MUST be total — the relay FILTER decides what to SEND, not
// toProto). Each payload is already metadata-only/redacted by construction
// (gauntlet #7), so the mapper adds no redaction: Approval carries NAME+verdict+
// askID+call id+allow-always (never args); UserPrompt carries Text+media Parts;
// CompactionArchive carries the parent's OWN pre-compaction message slice via the
// new ConversationMessage projection (Role/Text/ToolCalls/ToolResult/Reasoning/
// ProviderPhase/ReasoningItemID/Parts).
func TestToProtoLogOnlyPayloads(t *testing.T) {
	// Approval.
	apr := toProto(session.Event{
		Type: session.EvApproval,
		Approval: &session.ApprovalPayload{
			AskID:       "s1:1:c1:r0",
			Verdict:     session.VerdictStringAllowAlways,
			Tool:        "Shell",
			Call:        session.ToolCallID("c1"),
			AllowAlways: true,
		},
	}).GetApproval()
	if apr == nil {
		t.Fatal("approval submessage not projected")
	}
	if apr.GetAskId() != "s1:1:c1:r0" || apr.GetVerdict() != mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS ||
		apr.GetTool() != "Shell" || apr.GetCallId() != "c1" {
		t.Fatalf("approval projected wrong: %+v", apr)
	}

	// UserPrompt with a media part.
	img := session.Content{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte("px")}
	up := toProto(session.Event{
		Type: session.EvUserPrompt,
		UserPrompt: &session.UserPromptPayload{
			Text:  "look",
			Parts: []session.Content{img},
		},
	}).GetUserPrompt()
	if up == nil {
		t.Fatal("user_prompt submessage not projected")
	}
	if up.GetText() != "look" {
		t.Errorf("user_prompt.Text = %q, want look", up.GetText())
	}
	if len(up.GetParts()) != 1 || up.GetParts()[0].GetKind() != mecatlv1.Content_KIND_IMAGE ||
		up.GetParts()[0].GetMimeType() != "image/png" {
		t.Errorf("user_prompt.Parts projected wrong: %+v", up.GetParts())
	}

	// CompactionArchive with a full message slice (assistant w/ tool calls, tool-role
	// w/ result, user w/ media).
	tr := session.NewToolResult("c1", "ok")
	assistantMsg := session.NewAssistantMessage("calling", "reason", []session.ToolCall{
		session.NewToolCall("c1", "Shell", json.RawMessage(`{"cmd":"ls"}`)),
	})
	assistantMsg.ReasoningItemID = "rs_1"
	arch := toProto(session.Event{
		Type: session.EvCompactionArchive,
		CompactionArchive: &session.CompactionArchivePayload{
			Replaced: []session.Message{
				session.NewUserMessageWithParts("hi", []session.Content{img}),
				assistantMsg,
				session.NewToolMessage(tr),
			},
		},
	}).GetCompactionArchive()
	if arch == nil {
		t.Fatal("compaction_archive submessage not projected")
	}
	if len(arch.GetReplaced()) != 3 {
		t.Fatalf("replaced = %d msgs, want 3", len(arch.GetReplaced()))
	}
	if arch.GetReplaced()[0].GetRole() != "user" || arch.GetReplaced()[0].GetText() != "hi" {
		t.Errorf("replaced[0] wrong: %+v", arch.GetReplaced()[0])
	}
	if len(arch.GetReplaced()[0].GetParts()) != 1 {
		t.Errorf("replaced[0].Parts = %d, want 1", len(arch.GetReplaced()[0].GetParts()))
	}
	if arch.GetReplaced()[1].GetRole() != "assistant" || arch.GetReplaced()[1].GetReasoning() != "reason" {
		t.Errorf("replaced[1] wrong: %+v", arch.GetReplaced()[1])
	}
	if arch.GetReplaced()[1].GetReasoningItemId() != "rs_1" {
		t.Errorf("replaced[1].ReasoningItemId = %q, want rs_1 (the reasoning-item id must project alongside Reasoning/ProviderPhase)", arch.GetReplaced()[1].GetReasoningItemId())
	}
	if len(arch.GetReplaced()[1].GetToolCalls()) != 1 ||
		arch.GetReplaced()[1].GetToolCalls()[0].GetName() != "Shell" {
		t.Errorf("replaced[1].ToolCalls wrong: %+v", arch.GetReplaced()[1].GetToolCalls())
	}
	if arch.GetReplaced()[2].GetRole() != "tool" || arch.GetReplaced()[2].GetToolResult() == nil ||
		arch.GetReplaced()[2].GetToolResult().GetCallId() != "c1" {
		t.Errorf("replaced[2] wrong: %+v", arch.GetReplaced()[2])
	}

	// Empty CompactionArchive (a nil/empty Replaced slice) still projects a non-nil
	// submessage (so the replay shows the event, not a bare type-only stub).
	emptyArch := toProto(session.Event{
		Type:              session.EvCompactionArchive,
		CompactionArchive: &session.CompactionArchivePayload{},
	}).GetCompactionArchive()
	if emptyArch == nil {
		t.Fatal("empty compaction_archive must still project a non-nil submessage")
	}
	if len(emptyArch.GetReplaced()) != 0 {
		t.Errorf("empty compaction_archive.Replaced = %d, want 0", len(emptyArch.GetReplaced()))
	}
}

// TestToProtoTeamOutcomeRoundTrip pins the terminal RunTeam outcome mapping
// (issue #36): every agent.TeamOutcome field crosses to the proto TeamOutcome —
// rounds, quiescent, budget_exhausted, the string-passthrough stop ("budget" for a
// non-quiescent budget-stopped team, via agent.TeamStop), the usage total, each
// member disposition (reusing the closed enum, incl. the budget reason), and the
// capped findings.
func TestToProtoTeamOutcomeRoundTrip(t *testing.T) {
	in := agent.TeamOutcome{
		Rounds:          3,
		Quiescent:       false,
		BudgetExhausted: true,
		Usage:           session.Usage{InputTokens: 700, OutputTokens: 50, CacheReadTokens: 10, CacheWriteTokens: 5},
		Members: []agent.MemberOutcome{
			// The lead was RETRIED through one run-level failure and still finished
			// (issue #318): stopped=false, no reason, so ErrorRounds is the only thing
			// distinguishing it on the wire from a member that never failed.
			{Name: "lead", Stopped: false, Disposition: agent.DispositionDone, ErrorRounds: 1},
			{Name: "worker", Stopped: true, Disposition: agent.DispositionStopped, Reason: agent.StopReasonBudget},
		},
		Findings: []session.TeamFindingSnapshot{
			{Member: "worker", Body: "found the leak"},
			{Member: "lead", Body: "confirmed the fix"},
		},
	}
	got := toProtoTeamOutcome(in)
	if got.GetRounds() != 3 {
		t.Errorf("rounds = %d, want 3", got.GetRounds())
	}
	if got.GetQuiescent() {
		t.Error("quiescent = true, want false")
	}
	if !got.GetBudgetExhausted() {
		t.Error("budget_exhausted = false, want true")
	}
	if got.GetStop() != "budget" {
		t.Errorf("stop = %q, want %q (string passthrough of session.StopBudget)", got.GetStop(), "budget")
	}
	if u := got.GetUsage(); u.GetInputTokens() != 700 || u.GetOutputTokens() != 50 ||
		u.GetCacheReadTokens() != 10 || u.GetCacheWriteTokens() != 5 {
		t.Errorf("usage = %+v, want input=700 output=50 cache_read=10 cache_write=5", u)
	}
	ds := got.GetDispositions()
	if len(ds) != 2 {
		t.Fatalf("dispositions = %d, want 2", len(ds))
	}
	if ds[0].GetName() != "lead" || ds[0].GetStopped() ||
		ds[0].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_UNSPECIFIED ||
		ds[0].GetErrorRounds() != 1 {
		t.Errorf("dispositions[0] = %+v, want done lead, UNSPECIFIED reason, error_rounds=1", ds[0])
	}
	if ds[1].GetName() != "worker" || !ds[1].GetStopped() ||
		ds[1].GetReason() != mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET ||
		ds[1].GetErrorRounds() != 0 {
		t.Errorf("dispositions[1] = %+v, want stopped worker, BUDGET reason, error_rounds=0", ds[1])
	}
	// Two findings, asserted positionally: the ledger's append order must survive
	// the mapping verbatim.
	fs := got.GetFindings()
	if len(fs) != 2 {
		t.Fatalf("findings = %d, want 2", len(fs))
	}
	if fs[0].GetMember() != "worker" || fs[0].GetBody() != "found the leak" {
		t.Errorf("findings[0] = %+v, want {worker found the leak}", fs[0])
	}
	if fs[1].GetMember() != "lead" || fs[1].GetBody() != "confirmed the fix" {
		t.Errorf("findings[1] = %+v, want {lead confirmed the fix}", fs[1])
	}
}

// TestToProtoTeamOutcomeQuiescentBudgetExhausted pins the quiescent+exhausted
// edge on the wire seam (issue #36): a team that crossed its budget but STILL
// reached genuine quiescence stops "end_turn" — agent.TeamStop reserves "budget"
// for a NON-quiescent budget stop — while budget_exhausted independently stays
// true. The engine pins this rule for EvTeamEnd; this case pins the exported
// seam the terminal outcome frame rides.
func TestToProtoTeamOutcomeQuiescentBudgetExhausted(t *testing.T) {
	got := toProtoTeamOutcome(agent.TeamOutcome{Quiescent: true, BudgetExhausted: true})
	if got.GetStop() != "end_turn" {
		t.Errorf("stop = %q, want %q (quiescence wins over budget in agent.TeamStop)", got.GetStop(), "end_turn")
	}
	if !got.GetBudgetExhausted() {
		t.Error("budget_exhausted = false, want true (independent of the stop label)")
	}
}

// TestSessionMapping checks the session snapshot mapping including mode and
// limits round-trips.
func TestSessionMapping(t *testing.T) {
	sess := session.New("s1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{MaxTurns: 4, MaxToolCalls: 8, MaxConsecutiveFailures: 2}, time.Unix(1000, 0))
	got := toProtoSession(sess, ResolvedModel{ProviderID: "openai", ModelID: "gpt-x", ContextWindow: 2048}, nil, port.ProviderCapabilities{Image: true})
	if got.GetSessionId() != "s1" || got.GetState() != "idle" {
		t.Fatalf("got %+v", got)
	}
	if !got.GetSessionCapabilities().GetImage() {
		t.Fatal("session image capability was not projected")
	}
	if got.GetMode() != mecatlv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("mode = %v", got.GetMode())
	}
	if got.GetLimits().GetMaxTurns() != 4 || got.GetLimits().GetMaxToolCalls() != 8 {
		t.Fatalf("limits = %+v", got.GetLimits())
	}
	if got.GetCreatedAtUnix() != 1000 {
		t.Fatalf("created_at = %d", got.GetCreatedAtUnix())
	}
	if rm := got.GetResolvedModel(); rm.GetProviderId() != "openai" || rm.GetModelId() != "gpt-x" || rm.GetContextWindow() != 2048 {
		t.Fatalf("resolved_model = %+v", rm)
	}
}

// TestResolvedModelToProtoZeroIsNil checks the zero ResolvedModel maps to nil so
// an unresolved value round-trips to "absent" (older-server-equivalent fallback).
func TestResolvedModelToProtoZeroIsNil(t *testing.T) {
	if resolvedModelToProto(ResolvedModel{}) != nil {
		t.Fatalf("zero ResolvedModel should map to nil proto")
	}
	if got := resolvedModelToProto(ResolvedModel{ModelID: "m"}); got == nil || got.GetModelId() != "m" {
		t.Fatalf("non-zero ResolvedModel = %+v", got)
	}
}

// TestModeRoundTrip checks mode mapping in both directions.
func TestModeRoundTrip(t *testing.T) {
	for _, m := range []session.PermissionMode{session.ModeDefault, session.ModePlan, session.ModeAccept} {
		if got := modeFromProto(modeToProto(m)); got != m {
			t.Fatalf("mode round-trip: %q -> %q", m, got)
		}
	}
	if got := modeFromProto(mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED); got != session.ModeDefault {
		t.Fatalf("unspecified mode -> %q, want default", got)
	}
}

func TestContentFromProto(t *testing.T) {
	parts, err := contentFromProto([]*mecatlv1.Content{
		{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1, 2}},
		{Kind: mecatlv1.Content_KIND_AUDIO, MimeType: "audio/wav", Url: "https://media.example.com/a.wav"},
	})
	if err != nil {
		t.Fatalf("contentFromProto: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].Kind != session.MediaImage || string(parts[0].Data) != string([]byte{1, 2}) {
		t.Fatalf("part 0 = %+v", parts[0])
	}
	if parts[1].Kind != session.MediaAudio || parts[1].URL != "https://media.example.com/a.wav" {
		t.Fatalf("part 1 = %+v", parts[1])
	}
}

func TestContentFromProtoEmpty(t *testing.T) {
	if got, err := contentFromProto(nil); err != nil || got != nil {
		t.Fatalf("contentFromProto(nil) = %v, %v; want nil, nil", got, err)
	}
}

func TestContentFromProtoRejectsUnspecifiedKind(t *testing.T) {
	_, err := contentFromProto([]*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_UNSPECIFIED, MimeType: "image/png", Data: []byte{1}}})
	if err == nil {
		t.Fatal("expected reject for KIND_UNSPECIFIED")
	}
}

func TestContentFromProtoRejectsBothDataAndURL(t *testing.T) {
	_, err := contentFromProto([]*mecatlv1.Content{{
		Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png",
		Data: []byte{1}, Url: "https://x/y.png",
	}})
	if err == nil {
		t.Fatal("expected reject for both data and url set")
	}
}

func TestContentToProtoRoundTrip(t *testing.T) {
	in := []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{9}},
		{Kind: session.MediaAudio, MIMEType: "audio/wav", URL: "https://media.example.com/a.wav"},
	}
	out := contentToProto(in)
	back, err := contentFromProto(out)
	if err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if len(back) != 2 || back[0].Kind != session.MediaImage || back[1].URL != "https://media.example.com/a.wav" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}

func TestContentFromProtoRejectsSSRFURL(t *testing.T) {
	for _, bad := range []string{
		"http://media.example.com/a.png",           // plaintext http
		"https://169.254.169.254/latest/meta-data", // metadata IP
		"https://127.0.0.1/a.png",                  // loopback
		"https://10.0.0.5/a.png",                   // RFC1918
		"https://localhost/a.png",                  // internal name
		"file:///etc/passwd",                       // file scheme
	} {
		_, err := contentFromProto([]*mecatlv1.Content{
			{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Url: bad},
		})
		if err == nil {
			t.Fatalf("contentFromProto with url %q: expected reject, got nil", bad)
		}
	}
}

func TestContentFromProtoRejectsOversizedPart(t *testing.T) {
	_, err := contentFromProto([]*mecatlv1.Content{
		{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: make([]byte, session.MaxMediaBytes+1)},
	})
	if err == nil {
		t.Fatal("expected reject for oversized inline part")
	}
}

func TestContentFromProtoRejectsTooManyParts(t *testing.T) {
	parts := make([]*mecatlv1.Content, session.MaxPromptMediaParts+1)
	for i := range parts {
		parts[i] = &mecatlv1.Content{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1}}
	}
	if _, err := contentFromProto(parts); err == nil {
		t.Fatal("expected reject for too many parts")
	}
}

func TestContentFromProtoRejectsMimeKindMismatch(t *testing.T) {
	_, err := contentFromProto([]*mecatlv1.Content{
		{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "audio/wav", Data: []byte{1}},
	})
	if err == nil {
		t.Fatal("expected reject for image kind with audio mime")
	}
	_, err = contentFromProto([]*mecatlv1.Content{
		{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "", Data: []byte{1}},
	})
	if err == nil {
		t.Fatal("expected reject for empty mime")
	}
}

// TestVerdictFromResumeApproval pins the explicit enum mapping and ensures unknown
// values remain invalid for shared validation rather than consuming the pending ask.
func TestVerdictFromResumeApproval(t *testing.T) {
	cases := []struct {
		name    string
		verdict mecatlv1.ApprovalVerdict
		want    session.ApprovalVerdict
	}{
		{"allow always", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS, session.VerdictAllowAlways},
		{"allow once", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE, session.VerdictAllowOnce},
		{"deny", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY, session.VerdictDeny},
		{"unspecified remains invalid", mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED, invalidTransportApprovalVerdict},
		{"unknown remains invalid", mecatlv1.ApprovalVerdict(99), invalidTransportApprovalVerdict},
		{"large unknown cannot truncate to allow", mecatlv1.ApprovalVerdict(257), invalidTransportApprovalVerdict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdictFromResumeApproval(tc.verdict); got != tc.want {
				t.Fatalf("verdictFromResumeApproval(%v) = %v, want %v", tc.verdict, got, tc.want)
			}
		})
	}
}

func TestApprovalResolutionFromProtoPreservesGuardrailAcknowledgement(t *testing.T) {
	got := approvalResolutionFromProto(&mecatlv1.ResumeApproval{
		AskId: "ask-1", ReviewId: "review-1",
		GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE,
		Verdict:       mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS,
	})
	if got.AskID != "ask-1" || got.ReviewID != "review-1" || got.Kind != session.GuardrailApprovalResultRelease || got.Verdict != session.VerdictAllowAlways {
		t.Fatalf("approval resolution lost contextual acknowledgement: %+v", got)
	}
}

// TestToProtoToolResultWithBlocks maps a session.ToolResult carrying one of each
// block kind to its proto form and back, asserting every ContentBlock field
// round-trips and the structured_content field is derived from the structured
// block. It also confirms a legacy nil-Parts result maps to empty blocks
// (backward compat — a string-only ToolResult stays byte-identical).
func TestToProtoToolResultWithBlocks(t *testing.T) {
	parts := []session.Content{
		session.NewTextBlock("hello"),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3}},
		{BlockKind: session.BlockAudio, Kind: session.MediaAudio, MIMEType: "audio/wav", URL: "https://ex.com/a.wav"},
		session.NewResourceLinkBlock("res://x", "name", "title", "desc", "text/plain", 42, []string{"admin"}),
		mustEmbedded(t, "res://y", "text/plain", "blob-text", nil, []string{"user"}),
		session.NewStructuredContentBlock(`{"k":"v"}`),
	}
	r := session.NewToolResultWithParts("c1", "summary", parts)

	pb := toProtoToolResult(r)

	if pb.GetCallId() != "c1" || pb.GetContent() != "summary" || pb.GetIsError() {
		t.Fatalf("legacy fields mismatch: %+v", pb)
	}
	// structured_content is derived from the BlockStructuredContent block's Text.
	if pb.GetStructuredContent() != `{"k":"v"}` {
		t.Fatalf("structured_content = %q, want {\"k\":\"v\"}", pb.GetStructuredContent())
	}
	blocks := pb.GetBlocks()
	if len(blocks) != len(parts) {
		t.Fatalf("blocks len = %d, want %d", len(blocks), len(parts))
	}

	// Per-block kind + field checks.
	wantKinds := []mecatlv1.ContentBlock_Kind{
		mecatlv1.ContentBlock_KIND_TEXT,
		mecatlv1.ContentBlock_KIND_IMAGE,
		mecatlv1.ContentBlock_KIND_AUDIO,
		mecatlv1.ContentBlock_KIND_RESOURCE_LINK,
		mecatlv1.ContentBlock_KIND_EMBEDDED_RESOURCE,
		mecatlv1.ContentBlock_KIND_STRUCTURED_CONTENT,
	}
	for i, want := range wantKinds {
		if blocks[i].GetKind() != want {
			t.Errorf("block[%d] kind = %v, want %v", i, blocks[i].GetKind(), want)
		}
	}
	if blocks[0].GetText() != "hello" {
		t.Errorf("text block Text = %q, want hello", blocks[0].GetText())
	}
	if blocks[1].GetMimeType() != "image/png" || string(blocks[1].GetData()) != string([]byte{1, 2, 3}) {
		t.Errorf("image block mismatch: %+v", blocks[1])
	}
	if blocks[2].GetMimeType() != "audio/wav" || blocks[2].GetUrl() != "https://ex.com/a.wav" {
		t.Errorf("audio block mismatch: %+v", blocks[2])
	}
	if blocks[3].GetUrl() != "res://x" || blocks[3].GetName() != "name" ||
		blocks[3].GetTitle() != "title" || blocks[3].GetDescription() != "desc" ||
		blocks[3].GetMimeType() != "text/plain" || blocks[3].GetSize() != 42 ||
		len(blocks[3].GetAudience()) != 1 || blocks[3].GetAudience()[0] != "admin" {
		t.Errorf("resource-link block mismatch: %+v", blocks[3])
	}
	if blocks[4].GetUrl() != "res://y" || blocks[4].GetMimeType() != "text/plain" ||
		blocks[4].GetText() != "blob-text" || len(blocks[4].GetAudience()) != 1 ||
		blocks[4].GetAudience()[0] != "user" {
		t.Errorf("embedded-resource block mismatch: %+v", blocks[4])
	}
	if blocks[5].GetText() != `{"k":"v"}` {
		t.Errorf("structured-content block Text = %q", blocks[5].GetText())
	}

	// Round-trip back through the symmetric reverse mapper.
	back := blocksFromProto(blocks)
	if len(back) != len(parts) {
		t.Fatalf("round-trip len = %d, want %d", len(back), len(parts))
	}
	for i, want := range parts {
		got := back[i]
		if got.BlockKind != want.BlockKind {
			t.Errorf("round-trip[%d] BlockKind = %q, want %q", i, got.BlockKind, want.BlockKind)
		}
		if got.Kind != want.Kind {
			t.Errorf("round-trip[%d] Kind = %q, want %q", i, got.Kind, want.Kind)
		}
		if got.MIMEType != want.MIMEType || got.URL != want.URL ||
			got.Text != want.Text || got.Name != want.Name ||
			got.Title != want.Title || got.Description != want.Description ||
			got.Size != want.Size || got.LastModified != want.LastModified {
			t.Errorf("round-trip[%d] scalar mismatch:\n got=%+v\nwant=%+v", i, got, want)
		}
		if string(got.Data) != string(want.Data) {
			t.Errorf("round-trip[%d] Data mismatch: %v vs %v", i, got.Data, want.Data)
		}
		if len(got.Audience) != len(want.Audience) {
			t.Errorf("round-trip[%d] Audience len mismatch", i)
		}
		for j := range want.Audience {
			if j >= len(got.Audience) || got.Audience[j] != want.Audience[j] {
				t.Errorf("round-trip[%d] Audience[%d] mismatch", i, j)
			}
		}
	}

	// Legacy nil-Parts result: backward compat — empty blocks (nil), no structured_content.
	legacy := toProtoToolResult(session.NewToolResult("c2", "body"))
	if legacy.GetBlocks() != nil {
		t.Fatalf("legacy nil-Parts result should map to nil blocks, got %+v", legacy.GetBlocks())
	}
	if legacy.GetStructuredContent() != "" {
		t.Fatalf("legacy nil-Parts result should have empty structured_content, got %q", legacy.GetStructuredContent())
	}
}

func mustEmbedded(t *testing.T, uri, mime, text string, blob []byte, audience []string) session.Content {
	t.Helper()
	c, err := session.NewEmbeddedResourceBlock(uri, mime, text, blob, audience)
	if err != nil {
		t.Fatalf("NewEmbeddedResourceBlock: %v", err)
	}
	return c
}

// badUTF8 is the orphaned-lead-byte sequence from issue #402 (an em dash's E2
// lead byte retained while its continuation bytes are lost) — a value protobuf
// string fields reject at marshal time.
const badUTF8 = "\xe2M-^@M-^T"

// TestToProtoNeverFailsMarshalOnInvalidUTF8 is the READABLE half of the
// protobuf-projection backstop oracle (issue #402): NO domain string, however
// malformed, may make proto marshaling fail. It builds one event of every
// payload kind with badUTF8 injected into the producer-influenced string
// fields, maps each through toProto, and asserts proto.Marshal succeeds AND the
// malformed bytes were repaired to U+FFFD.
//
// Its coverage is exactly what the fixtures below SEED, and no more. It does
// NOT catch a future payload field that someone forgets to run through valid():
// the field would simply never be populated here, and the test would stay
// green. That job belongs to TestToProtoStructuralUTF8Guard, which reflects
// over session.Event and seeds every bare-string field automatically. Keep both
// — this one documents the shape of a real event and is what you read to
// understand the mapping; that one is the guard that actually fails closed.
func TestToProtoNeverFailsMarshalOnInvalidUTF8(t *testing.T) {
	msg := session.Message{
		Role:          session.RoleAssistant,
		Text:          "txt " + badUTF8,
		Reasoning:     "reason " + badUTF8,
		ProviderPhase: "phase " + badUTF8,
		ToolCalls:     []session.ToolCall{session.NewToolCall("c1", "Shell", json.RawMessage(`{"cmd":"`+badUTF8+`"}`))},
	}
	emb := mustEmbedded(t, "https://x/"+badUTF8, "application/octet-stream", "", []byte{0xff, 0xfe}, []string{badUTF8})
	parts := []session.Content{
		session.NewTextBlock("body " + badUTF8),
		session.NewResourceLinkBlock("https://x/"+badUTF8, badUTF8, badUTF8, badUTF8, "text/plain", 1, []string{badUTF8}),
		emb,
	}

	events := map[string]session.Event{
		"message.delta": {Type: session.EvMessageDelta, Text: "delta " + badUTF8},
		"tool.call":     {Type: session.EvToolCall, ToolCall: &msg.ToolCalls[0]},
		"tool.result": {Type: session.EvToolResult, ToolResult: &session.ToolResult{
			CallID: "c1", Content: "out " + badUTF8, Parts: parts,
		}},
		"permission.ask": {Type: session.EvPermissionAsk, Ask: &session.PendingAsk{
			AskID: "a1", Tool: "Shell" + badUTF8, Args: json.RawMessage(`{"x":"` + badUTF8 + `"}`), Reason: "why " + badUTF8,
		}},
		"result": {Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: "final " + badUTF8, Error: "err " + badUTF8}},
		"hook":   {Type: session.EvHook, Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Shell" + badUTF8}},
		"approval": {Type: session.EvApproval, Approval: &session.ApprovalPayload{
			AskID: "a1", Verdict: session.VerdictStringAllowOnce, Tool: "Shell" + badUTF8, Call: "c1",
		}},
		"user_prompt": {Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "ask " + badUTF8}},
		"compaction.archive": {Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{
			Replaced: []session.Message{msg},
		}},
		"subagent": {Type: session.EvSubagentEnd, Subagent: &session.SubagentPayload{
			ParentCallID: "p1", ChildID: "ch1", Goal: "g " + badUTF8, Text: "t " + badUTF8,
			Detail: "d " + badUTF8, Cause: "c " + badUTF8, Stop: session.StopError,
		}},
		"team": {Type: session.EvTeamEnd, Team: &session.TeamPayload{
			ParentCallID: "p1", TeamID: "t1", Member: "m " + badUTF8, Text: "t " + badUTF8, Detail: "d " + badUTF8,
			Roster:       []session.TeamMemberSpec{{Name: "n " + badUTF8, Role: "r " + badUTF8}},
			Tasks:        []session.TeamTaskSnapshot{{ID: "1", Description: "desc " + badUTF8, Assignee: "as " + badUTF8, Deps: []string{badUTF8}}},
			Findings:     []session.TeamFindingSnapshot{{Member: "m " + badUTF8, Body: "b " + badUTF8}},
			Dispositions: []session.TeamMemberDisposition{{Name: "n " + badUTF8, Disposition: "stopped", Reason: "error"}},
		}},
		"parallel": {Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{
			ParentCallID: "p1", Kind: session.ParallelBranchTool, BranchLabel: "bl " + badUTF8, Goal: "g " + badUTF8,
			Text: "t " + badUTF8, Detail: "d " + badUTF8,
		}},
		"schedule": {Type: session.EvScheduleFired, Schedule: &session.SchedulePayload{
			ScheduleName: "s", FireID: "f", SessionID: "sid", Kind: "fired", Err: "e " + badUTF8,
		}},
	}

	for name, ev := range events {
		out := toProto(ev)
		b, err := proto.Marshal(out)
		if err != nil {
			t.Fatalf("%s: proto.Marshal failed (the stream-kill bug): %v", name, err)
		}
		if !bytes.Contains(b, []byte("�")) {
			t.Fatalf("%s: expected U+FFFD repair in marshaled output, got none (%x)", name, b)
		}
	}
}

// TestMapperBackstopNonEventMessages covers the non-event mappers (MCP
// inspection, worktrees, session summaries, team roster/tasks) whose strings
// are MCP-server- or OS-sourced — the most likely NEXT instance of this bug
// class, and not covered by the loop's ToolResult repair at all.
func TestMapperBackstopNonEventMessages(t *testing.T) {
	check := func(name string, m proto.Message) {
		t.Helper()
		if _, err := proto.Marshal(m); err != nil {
			t.Fatalf("%s: proto.Marshal failed: %v", name, err)
		}
	}
	check("McpResource", toProtoMcpResource(mcp.Resource{Server: "s", URI: "u" + badUTF8, Name: "n" + badUTF8, Title: "t" + badUTF8, Description: "d" + badUTF8, MIMEType: "text/plain"}))
	check("McpResourceContents", toProtoMcpResourceContents(mcp.ResourceContents{URI: "u" + badUTF8, MIMEType: "text/plain", Text: "x" + badUTF8, Blob: []byte{0xff}}))
	check("McpPrompt", toProtoMcpPrompt(mcp.Prompt{Server: "s", Name: "n" + badUTF8, Title: "t" + badUTF8, Description: "d" + badUTF8, Arguments: []mcp.PromptArgument{{Name: "a" + badUTF8, Title: "at" + badUTF8, Description: "ad" + badUTF8}}}))
	check("McpPromptMessage", toProtoMcpPromptMessage(mcp.PromptMessage{Role: "user" + badUTF8, Text: "x" + badUTF8}))
	check("McpSource", toProtoMcpSource(source.SourceInfo{Name: "n" + badUTF8, Kind: "k", Group: "g" + badUTF8, Servers: []source.ServerInfo{{Name: "sv" + badUTF8, URL: "u" + badUTF8, Transport: "http", Group: "g" + badUTF8}}, Diagnostics: []string{"d" + badUTF8}}))
	check("Worktree", toProtoScopedWorktrees([]ScopedWorktree{{Selector: "s" + badUTF8, Label: "l" + badUTF8, Branch: "b" + badUTF8, Revision: "h" + badUTF8}})[0])
	check("SessionSummary", toProtoSessionSummary(SessionSummary{SessionID: "s", State: "idle", ModelID: "m", Title: "t" + badUTF8}))
	check("TeamMember", toProtoTeamMember(team.Member{Name: "n" + badUTF8, AgentType: "a" + badUTF8}))
	check("TeamTask", toProtoTeamTask(team.Task{ID: "1", Description: "d" + badUTF8, Assignee: "a" + badUTF8, Deps: []team.TaskID{team.TaskID("x" + badUTF8)}}))
	check("TeamOutcome", toProtoTeamOutcome(agent.TeamOutcome{
		Quiescent: true,
		Members:   []agent.MemberOutcome{{Name: "n" + badUTF8, Reason: agent.StopReasonError}},
		Findings:  []session.TeamFindingSnapshot{{Member: "m" + badUTF8, Body: "b" + badUTF8}},
	}))
}
