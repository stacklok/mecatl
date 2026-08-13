package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/team"
)

// teamHooks is a fake port.HookRunner that records the phases it was fired
// for and can optionally veto (Block) every call.
type teamHooks struct {
	mu       sync.Mutex
	phases   []governance.HookPhase
	events   []governance.HookEvent
	block    bool
	blockMsg string
}

func (h *teamHooks) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	h.mu.Lock()
	h.phases = append(h.phases, ev.Phase)
	h.events = append(h.events, ev)
	h.mu.Unlock()
	if h.block {
		return governance.HookOutcome{Block: true, Message: h.blockMsg}, nil
	}
	return governance.HookOutcome{}, nil
}

// eventFor returns the first recorded HookEvent for phase p (and whether one was
// recorded).
func (h *teamHooks) eventFor(p governance.HookPhase) (governance.HookEvent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range h.events {
		if ev.Phase == p {
			return ev, true
		}
	}
	return governance.HookEvent{}, false
}

func (h *teamHooks) fired(p governance.HookPhase) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, got := range h.phases {
		if got == p {
			return true
		}
	}
	return false
}

func TestAddTaskFiresTaskCreatedGate(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	hooks := &teamHooks{}
	add := toolByName(t, agent.MemberTools(tm, "alice", hooks), "AddTask")

	if res := call(t, add, `{"description":"do the thing"}`); res.IsError {
		t.Fatalf("AddTask errored: %s", res.Content)
	}
	if !hooks.fired(governance.PhaseTaskCreated) {
		t.Error("TaskCreated hook was not fired by AddTask")
	}
	if len(tm.Tasks()) != 1 {
		t.Fatalf("want 1 task created, got %d", len(tm.Tasks()))
	}
}

// TestTeamGateActorInPayloadNotSessionID pins the actor-consistency choice: a
// team-phase gate hook (TaskCreated/TaskCompleted) carries the acting member's
// name in the Input payload's "by" key, and NEVER smuggles it through SessionID
// (which is a session id, not an actor handle, and is unavailable when the
// coordination tools are built). SessionID stays empty for these tool-driven
// gates.
func TestTeamGateActorInPayloadNotSessionID(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	hooks := &teamHooks{}
	add := toolByName(t, agent.MemberTools(tm, "alice", hooks), "AddTask")

	if res := call(t, add, `{"description":"do the thing"}`); res.IsError {
		t.Fatalf("AddTask errored: %s", res.Content)
	}

	ev, ok := hooks.eventFor(governance.PhaseTaskCreated)
	if !ok {
		t.Fatal("TaskCreated hook was not recorded")
	}
	if ev.SessionID != "" {
		t.Errorf("gate hook SessionID must not carry the member name; got %q", ev.SessionID)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Input, &payload); err != nil {
		t.Fatalf("hook Input is not JSON: %v", err)
	}
	if payload["by"] != "alice" {
		t.Errorf("actor should be in payload \"by\"; got %v (payload=%v)", payload["by"], payload)
	}
}

func TestAddTaskVetoedByTaskCreatedGate(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	hooks := &teamHooks{block: true, blockMsg: "no new tasks allowed"}
	add := toolByName(t, agent.MemberTools(tm, "alice", hooks), "AddTask")

	res := call(t, add, `{"description":"do the thing"}`)
	if !res.IsError {
		t.Fatal("AddTask should be vetoed (error result) when TaskCreated blocks")
	}
	if len(tm.Tasks()) != 0 {
		t.Fatalf("vetoed AddTask still created a task: %+v", tm.Tasks())
	}
}

func TestCompleteTaskVetoedByTaskCompletedGate(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	hooks := &teamHooks{block: true, blockMsg: "tests must pass first"}
	tools := agent.MemberTools(tm, "alice", hooks)
	id, _ := tm.CreateTask("work")
	_ = tm.ClaimTask(id, "alice")

	complete := toolByName(t, tools, "CompleteTask")
	res := call(t, complete, `{"task_id":"`+string(id)+`"}`)
	if !res.IsError {
		t.Fatal("CompleteTask should be vetoed when TaskCompleted blocks")
	}
	if tm.Tasks()[0].State != team.TaskInProgress {
		t.Fatalf("vetoed CompleteTask still completed the task: %s", tm.Tasks()[0].State)
	}
}

func TestSupervisorFiresTeammateIdle(t *testing.T) {
	tm := team.New("t")
	providers := map[string]*mockllm.Provider{"solo": mockllm.New(mockllm.TextTurn("done"))}
	hooks := &teamHooks{}
	sup := agent.NewSupervisor(tm, agent.MemEnv("/ws"), memberFactory(t, tm, providers),
		agent.WithTeamHooks(hooks), agent.WithMaxRounds(5))

	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "solo", InitialPrompt: "do it"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	_ = sup.Run(context.Background(), nil)

	if !hooks.fired(governance.PhaseTeammateIdle) {
		t.Error("TeammateIdle hook was not fired after the member went idle")
	}
}
