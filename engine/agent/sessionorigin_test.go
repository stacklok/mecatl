package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestFireDelivery_Scenario1_CreateCapturesOriginSession pins AC1.1:
// A schedule created via the in-chat Schedule create verb persists the calling
// session's id in Spec.OriginSessionID, bound via the per-run wrapper.
func TestFireDelivery_Scenario1_CreateCapturesOriginSession(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()
	wrapper := agent.NewSessionOriginScheduleManager(mgr)

	// Bind a session origin; the wrapper's CreateSchedule should stamp it.
	wrapper.BindSessionOrigin(session.SessionID("s1"))

	tl := agent.NewScheduleTool(wrapper)
	res, err := tl.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}

	if len(mgr.created) != 1 {
		t.Fatalf("created %d specs, want 1", len(mgr.created))
	}
	if mgr.created[0].OriginSessionID != "s1" {
		t.Fatalf("OriginSessionID = %q, want %q", mgr.created[0].OriginSessionID, "s1")
	}

	// Re-bind to a different session; the next create should carry the new id.
	wrapper.BindSessionOrigin(session.SessionID("s2"))
	res, err = tl.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"daily","prompt":"check ci","cron":"0 9 * * *","workspace":"/repo"}`),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	if len(mgr.created) != 2 || mgr.created[1].OriginSessionID != "s2" {
		t.Fatalf("second OriginSessionID = %q, want %q", mgr.created[1].OriginSessionID, "s2")
	}

	// Unbound wrapper (no BindSessionOrigin) stamps empty OriginSessionID.
	mgr2 := newStubScheduleManager()
	wrapper2 := agent.NewSessionOriginScheduleManager(mgr2)
	tl2 := agent.NewScheduleTool(wrapper2)
	res, err = tl2.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"orphan","prompt":"x","cron":"@every 1h","workspace":"/r"}`),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unbound create = error %q", res.Content)
	}
	if len(mgr2.created) != 1 || mgr2.created[0].OriginSessionID != "" {
		t.Fatalf("unbound OriginSessionID = %q, want empty", mgr2.created[0].OriginSessionID)
	}
}

// TestFireDelivery_Scenario1_OriginNotModelForgeable pins AC1.4:
// The tool schema exposes NO "origin" argument and the model cannot influence
// the captured id.
func TestFireDelivery_Scenario1_OriginNotModelForgeable(t *testing.T) {
	t.Parallel()

	// AC1.4a: The tool schema exposes NO "origin" argument.
	tl := agent.NewScheduleTool(newStubScheduleManager())
	schema := tl.Spec().Schema
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	props, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}
	if _, hasOrigin := props["origin"]; hasOrigin {
		t.Fatal("schema exposes an 'origin' property — must NOT be model-forgeable")
	}

	// AC1.4b: A model-supplied "origin" in the raw JSON is IGNORED
	// (the bound id wins). Create a schedule with "origin" in args and
	// assert it does NOT reach the spec's OriginSessionID field.
	mgr := newStubScheduleManager()
	wrapper := agent.NewSessionOriginScheduleManager(mgr)
	wrapper.BindSessionOrigin(session.SessionID("real-session"))
	tl2 := agent.NewScheduleTool(wrapper)
	res, err := tl2.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo","origin":"evil-session"}`),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	if len(mgr.created) != 1 {
		t.Fatalf("created %d specs, want 1", len(mgr.created))
	}
	if mgr.created[0].OriginSessionID != "real-session" {
		t.Fatalf("OriginSessionID = %q, want %q (model-supplied 'origin' must be ignored)",
			mgr.created[0].OriginSessionID, "real-session")
	}
}

// TestFireDelivery_Scenario1_OriginIDNotModelVisible pins AC1.5:
// The OriginSessionID never appears in any model-visible surface — the system
// prompt and the tool result text.
func TestFireDelivery_Scenario1_OriginIDNotModelVisible(t *testing.T) {
	t.Parallel()
	originID := session.SessionID("secret-session-12345")

	// AC1.5a: The OriginSessionID does NOT appear in the tool result text.
	mgr := newStubScheduleManager()
	wrapper := agent.NewSessionOriginScheduleManager(mgr)
	wrapper.BindSessionOrigin(originID)

	tl := agent.NewScheduleTool(wrapper)
	res, err := tl.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	// The origin session ID must NOT appear in the tool result text.
	if strings.Contains(res.Content, string(originID)) {
		t.Fatalf("tool result text contains OriginSessionID %q — must NOT be model-visible", originID)
	}

	// AC1.5b: The OriginSessionID does NOT appear in the system prompt.
	// Build a full engine loop and capture the LLMRequest.System via a request
	// observer.
	var capturedSystem string
	mgr2 := newStubScheduleManager()
	wrapper2 := agent.NewSessionOriginScheduleManager(mgr2)

	var observeOnce bool
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			if observeOnce {
				return
			}
			observeOnce = true
			capturedSystem = fmt.Sprintf("%s\n%s", req.System.StablePrefix, req.System.VolatileSuffix)
		})},
		mockllm.ToolCallTurn(toolCall("c1", "Schedule",
			`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`)),
	)

	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewScheduleTool(wrapper2))

	eng := agent.NewEngine(agent.Deps{
		LLM:          llm,
		Catalog:      cat,
		Policy:       permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		OriginBinder: wrapper2,
	})

	sess := session.New(originID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	// Run the engine to trigger the tool call and capture the system prompt.
	run := eng.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "create a schedule"})
	drain(run)

	if capturedSystem == "" {
		t.Fatal("request observer did not capture a system prompt")
	}
	// The origin session ID must NOT appear in the system prompt.
	if strings.Contains(capturedSystem, string(originID)) {
		t.Fatalf("system prompt contains OriginSessionID %q — must NOT be model-visible", originID)
	}
}
