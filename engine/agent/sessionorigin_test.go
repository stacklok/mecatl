package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestFireDelivery_Scenario1_CreateCapturesOriginSession pins AC1.1:
// A schedule created via the in-chat Schedule create verb persists the calling
// session's id in Spec.OriginSessionID, read off the run context.
func TestFireDelivery_Scenario1_CreateCapturesOriginSession(t *testing.T) {
	t.Parallel()
	mgr := newStubScheduleManager()

	// The run context's origin should be stamped by the tool.
	ctx := agent.WithSessionOrigin(context.Background(), session.SessionID("s1"))

	tl := agent.NewScheduleTool(mgr)
	res, err := tl.Execute(ctx, scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`),
		agent.MemEnv("/ws"))
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

	// A different run context carries a different origin.
	ctx = agent.WithSessionOrigin(context.Background(), session.SessionID("s2"))
	res, err = tl.Execute(ctx, scheduleCall(t,
		`{"verb":"create","name":"daily","prompt":"check ci","cron":"0 9 * * *","workspace":"/repo"}`),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("create = error %q", res.Content)
	}
	if len(mgr.created) != 2 || mgr.created[1].OriginSessionID != "s2" {
		t.Fatalf("second OriginSessionID = %q, want %q", mgr.created[1].OriginSessionID, "s2")
	}

	// An unbound context yields the EMPTY origin, not a stale or borrowed one:
	// a create that never ran under Engine.Run is originless (no delivery),
	// which is the fail-safe direction.
	mgr2 := newStubScheduleManager()
	tl2 := agent.NewScheduleTool(mgr2)
	res, err = tl2.Execute(context.Background(), scheduleCall(t,
		`{"verb":"create","name":"orphan","prompt":"x","cron":"@every 1h","workspace":"/r"}`),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("unbound Execute: %v", err)
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
	ctx := agent.WithSessionOrigin(context.Background(), session.SessionID("real-session"))
	tl2 := agent.NewScheduleTool(mgr)
	res, err := tl2.Execute(ctx, scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo","origin":"evil-session"}`),
		agent.MemEnv("/ws"))
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
	ctx := agent.WithSessionOrigin(context.Background(), originID)

	tl := agent.NewScheduleTool(mgr)
	res, err := tl.Execute(ctx, scheduleCall(t,
		`{"verb":"create","name":"nightly","prompt":"check ci","cron":"0 3 * * *","workspace":"/repo"}`),
		agent.MemEnv("/ws"))
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
	cat.MustRegister(agent.NewScheduleTool(mgr2))

	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
	})

	sess := session.New(originID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	// Run the engine to trigger the tool call and capture the system prompt.
	run := eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "create a schedule"})
	drain(run)

	if capturedSystem == "" {
		t.Fatal("request observer did not capture a system prompt")
	}
	// The origin session ID must NOT appear in the system prompt.
	if strings.Contains(capturedSystem, string(originID)) {
		t.Fatalf("system prompt contains OriginSessionID %q — must NOT be model-visible", originID)
	}
}

type concurrentOriginManager struct {
	port.ScheduleManager
	mu      sync.Mutex
	origins map[string]session.SessionID
}

func (m *concurrentOriginManager) CreateSchedule(_ context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	m.mu.Lock()
	m.origins[spec.Name] = spec.OriginSessionID
	m.mu.Unlock()
	return port.Schedule{Spec: spec}, nil
}

func (m *concurrentOriginManager) origin(name string) session.SessionID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.origins[name]
}

// interleaveTimeout bounds the two run-A/run-B rendezvous points below. Generous
// versus the real wait (microseconds) and well under the go-test timeout, so a
// broken run surfaces as a NAMED failure rather than a hang.
const interleaveTimeout = 30 * time.Second

func TestScheduleTool_ConcurrentSharedEngineRunsDoNotCrossStamp(t *testing.T) {
	firstStreamEntered := make(chan struct{})
	secondStreamEntered := make(chan struct{})
	// testDone lets the observer bail out silently once the test goroutine has
	// returned (e.g. via t.Fatalf on run A never reaching the provider) — without
	// it, this goroutine could still be parked below when the test completes and
	// call t.Errorf afterward, panicking with "Log in goroutine after test has
	// completed".
	testDone := make(chan struct{})
	t.Cleanup(func() { close(testDone) })
	var streamCalls atomic.Int64
	observer := func(port.LLMRequest) {
		call := streamCalls.Add(1)
		if call == 1 {
			close(firstStreamEntered)
			// Bounded: if run B never reaches the provider, run A would park here
			// forever and the test would HANG to the go-test timeout, dumping
			// goroutines that name this observer instead of whatever broke run B.
			// t.Errorf (never t.Fatalf) — this is not the test goroutine.
			select {
			case <-secondStreamEntered:
			case <-testDone:
			case <-time.After(interleaveTimeout):
				select {
				case <-testDone:
				default:
					t.Errorf("run B never reached the provider — run A could not be interleaved")
				}
			}
			return
		}
		if call == 2 {
			close(secondStreamEntered)
		}
	}

	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(observer)},
		mockllm.ToolCallTurn(toolCall("call-a", agent.ScheduleToolName,
			`{"verb":"create","name":"from-a","prompt":"a","cron":"@every 1h","workspace":"/ws"}`)),
		mockllm.ToolCallTurn(toolCall("call-b", agent.ScheduleToolName,
			`{"verb":"create","name":"from-b","prompt":"b","cron":"@every 1h","workspace":"/ws"}`)),
		mockllm.TextTurn("done"),
		mockllm.TextTurn("done"),
	)
	mgr := &concurrentOriginManager{
		ScheduleManager: newStubScheduleManager(),
		origins:         make(map[string]session.SessionID),
	}
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewScheduleTool(mgr))
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
	})
	ws := agent.MemEnv("/ws")
	sessA := session.New("session-a", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	sessB := session.New("session-b", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))

	runA := eng.Run(context.Background(), sessA, ws, agent.RunRequest{Text: "create a"})
	select {
	case <-firstStreamEntered:
	case <-time.After(interleaveTimeout):
		t.Fatalf("run A never reached the provider — nothing to interleave against")
	}
	runB := eng.Run(context.Background(), sessB, ws, agent.RunRequest{Text: "create b"})

	var wg sync.WaitGroup
	wg.Go(func() { drain(runA) })
	wg.Go(func() { drain(runB) })
	wg.Wait()

	if got := mgr.origin("from-a"); got != sessA.ID {
		t.Errorf("from-a origin = %q, want %q", got, sessA.ID)
	}
	if got := mgr.origin("from-b"); got != sessB.ID {
		t.Errorf("from-b origin = %q, want %q", got, sessB.ID)
	}
}
