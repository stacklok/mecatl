package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// toolByName returns the named coordination tool from a member's tool set.
func toolByName(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found among member tools", name)
	return nil
}

// call invokes a tool with JSON args and returns the result, failing on a
// harness-level error.
func call(t *testing.T, tl tool.Tool, argsJSON string) session.ToolResult {
	t.Helper()
	res, err := tl.Execute(context.Background(),
		session.NewToolCall("c1", tl.Spec().Name, json.RawMessage(argsJSON)), agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("%s.Execute: harness error %v", tl.Spec().Name, err)
	}
	return res
}

func TestMemberToolsExposesTheCoordinationTools(t *testing.T) {
	tools := agent.MemberTools(team.New("t"), "alice", nil)
	want := []string{"SendMessage", "AddTask", "ClaimTask", "CompleteTask", "ListTasks", "RecordFinding"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, n := range want {
		toolByName(t, tools, n) // fails if missing
	}
	// Only ListTasks is read-only.
	for _, tl := range tools {
		ro := tl.ReadOnly()
		if (tl.Spec().Name == "ListTasks") != ro {
			t.Errorf("%s ReadOnly()=%v, want %v", tl.Spec().Name, ro, tl.Spec().Name == "ListTasks")
		}
	}
}

// TestMemberToolNamesIncludesRecordFinding asserts MemberToolNames (the set the
// supervisor's workspace-mutating-tool backstop exempts) picks up RecordFinding
// automatically, because it derives the set from MemberTools. This is load-bearing:
// a base-sharing read-only member may hold RecordFinding (it mutates only TEAM
// state) without tripping ErrReadOnlyMemberMutating.
func TestMemberToolNamesIncludesRecordFinding(t *testing.T) {
	names := agent.MemberToolNames()
	if _, ok := names["RecordFinding"]; !ok {
		t.Fatalf("MemberToolNames() = %v, want it to contain RecordFinding", names)
	}
}

// TestRecordFindingTool exercises the RecordFinding coordination tool: a valid
// finding appends to the ledger and returns the confirmation; an empty body is a
// model-addressable error; a finding from a member not on the roster surfaces the
// AppendFinding error rather than a harness fault.
func TestRecordFindingTool(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	record := toolByName(t, agent.MemberTools(tm, "alice", nil), "RecordFinding")

	// Empty body → model-addressable error result.
	if res := call(t, record, `{"finding":""}`); !res.IsError {
		t.Fatal("RecordFinding with empty body should be an error result")
	}
	if len(tm.Findings()) != 0 {
		t.Fatalf("empty finding must not append; ledger = %+v", tm.Findings())
	}

	// Valid finding → appended, confirmation returned.
	res := call(t, record, `{"finding":"the parser drops trailing commas"}`)
	if res.IsError {
		t.Fatalf("valid RecordFinding errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "recorded") {
		t.Errorf("RecordFinding result = %q, want a 'recorded' confirmation", res.Content)
	}
	got := tm.Findings()
	if len(got) != 1 || got[0].Member != "alice" || got[0].Body != "the parser drops trailing commas" {
		t.Fatalf("ledger = %+v, want one finding from alice", got)
	}

	// A finding from a non-roster member surfaces the aggregate's error.
	ghost := toolByName(t, agent.MemberTools(tm, "ghost", nil), "RecordFinding")
	if res := call(t, ghost, `{"finding":"i am not here"}`); !res.IsError {
		t.Fatal("RecordFinding from a non-member should be an error result")
	}
}

func TestSendMessageDeliversAndValidates(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	_ = tm.AddMember("bob", "")
	tools := agent.MemberTools(tm, "alice", nil)
	send := toolByName(t, tools, "SendMessage")

	// Missing recipient → error result (not harness error).
	if res := call(t, send, `{"body":"hi"}`); !res.IsError {
		t.Fatal("SendMessage with no 'to' should be an error result")
	}
	// Unknown recipient → error result.
	if res := call(t, send, `{"to":"ghost","body":"hi"}`); !res.IsError {
		t.Fatal("SendMessage to unknown member should be an error result")
	}
	// Valid send → delivered to bob's inbox.
	if res := call(t, send, `{"to":"bob","body":"hello bob"}`); res.IsError {
		t.Fatalf("valid SendMessage errored: %s", res.Content)
	}
	msgs, err := tm.Drain("bob")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(msgs) != 1 || msgs[0].From != "alice" || msgs[0].Body != "hello bob" {
		t.Fatalf("bob inbox = %+v, want one msg from alice", msgs)
	}
}

func TestAddClaimCompleteTaskRoundTrip(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	tools := agent.MemberTools(tm, "alice", nil)
	add := toolByName(t, tools, "AddTask")
	claim := toolByName(t, tools, "ClaimTask")
	complete := toolByName(t, tools, "CompleteTask")

	// AddTask requires a description.
	if res := call(t, add, `{}`); !res.IsError {
		t.Fatal("AddTask with no description should error")
	}
	res := call(t, add, `{"description":"write the parser"}`)
	if res.IsError {
		t.Fatalf("AddTask errored: %s", res.Content)
	}
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("team has %d tasks, want 1", len(tasks))
	}
	id := string(tasks[0].ID)

	// Claim next (no task_id) returns the task and marks it in-progress + assigned.
	res = call(t, claim, `{}`)
	if res.IsError || !strings.Contains(res.Content, id) {
		t.Fatalf("ClaimTask result = %q (err=%v), want it to name %s", res.Content, res.IsError, id)
	}
	if got := tm.Tasks()[0]; got.State != team.TaskInProgress || got.Assignee != "alice" {
		t.Fatalf("after claim task = %+v, want in_progress/alice", got)
	}

	// Complete it.
	res = call(t, complete, `{"task_id":"`+id+`"}`)
	if res.IsError {
		t.Fatalf("CompleteTask errored: %s", res.Content)
	}
	if got := tm.Tasks()[0]; got.State != team.TaskCompleted {
		t.Fatalf("after complete task state = %s, want completed", got.State)
	}
}

func TestClaimTaskNothingAvailable(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	claim := toolByName(t, agent.MemberTools(tm, "alice", nil), "ClaimTask")
	res := call(t, claim, `{}`)
	if res.IsError {
		t.Fatalf("ClaimTask on empty list should be a non-error result, got error: %s", res.Content)
	}
	if !strings.Contains(strings.ToLower(res.Content), "no claimable") {
		t.Fatalf("ClaimTask empty result = %q, want a 'no claimable task' message", res.Content)
	}
}

func TestListTasksRendersStateAndDeps(t *testing.T) {
	tm := team.New("t")
	_ = tm.AddMember("alice", "")
	a, _ := tm.CreateTask("build A")
	_, _ = tm.CreateTask("build B", a)
	list := toolByName(t, agent.MemberTools(tm, "alice", nil), "ListTasks")

	res := call(t, list, `{}`)
	if res.IsError {
		t.Fatalf("ListTasks errored: %s", res.Content)
	}
	for _, want := range []string{"build A", "build B", "pending", "deps=" + string(a)} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("ListTasks output missing %q:\n%s", want, res.Content)
		}
	}
}
