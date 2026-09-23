package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// regOf builds a one-def registry for the member-resolution tests.
func regOf(defs ...agents.AgentDef) *agents.Registry { return agents.NewRegistry(defs) }

// editCall scripts a member turn that calls Edit (a workspace-mutating tool), then a
// text turn, so we can observe whether Edit is in the member's catalog by whether
// the loop dispatches a tool.call for it (the catalog rejects an unknown tool).
func editCall() *mockllm.Provider {
	call := session.ToolCall{
		ID:   "c1",
		Name: "Edit",
		Args: json.RawMessage(`{"file_path":"/ws/f.txt","old_string":"a","new_string":"b"}`),
	}
	return mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done"))
}

// TestMemberMutatingDefKeepsEditWhenMutating proves a Mutating member whose def lists
// Edit actually dispatches an Edit tool.call (the tool is in the catalog), end to end
// through the supervisor against a forked memfs workspace.
func TestMemberMutatingDefKeepsEditWhenMutating(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	def := agents.AgentDef{Name: "writer", Description: "w", Tools: []string{"Read", "Edit"}}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, editCall(), hookexec.New(nil), regOf(def), nil, nil, false, nil)

	sup := newTestSupervisor(tm, memEnvironment("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		},
		agent.WithForker(memfsForker{}))
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "w", AgentType: "writer", Mutating: true, InitialPrompt: "edit it",
	}); err != nil {
		t.Fatalf("AddMember(mutating): %v", err)
	}

	var events []agent.TeamEvent
	sup.Run(context.Background(), func(ev agent.TeamEvent) { events = append(events, ev) })

	if !sawToolDispatched(events, "w", "Edit") {
		t.Fatalf("Mutating member with a def listing Edit should EXECUTE an Edit tool call; events=%d", len(events))
	}
}

// TestMemberReadOnlyDefDropsMutating asserts a READ-ONLY (base-sharing) member whose
// def lists Edit has Edit DROPPED — so the supervisor's AddMember backstop
// (ErrReadOnlyMemberMutating) is NOT tripped and the spawn succeeds. Symmetrically,
// the member never dispatches an Edit tool.call (it is not in the catalog).
func TestMemberReadOnlyDefDropsMutating(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	def := agents.AgentDef{Name: "reviewer", Description: "r", Tools: []string{"Read", "Edit", "Write"}}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, editCall(), hookexec.New(nil), regOf(def), nil, nil, false, nil)

	sup := newTestSupervisor(tm, memEnvironment("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	// A read-only member with a mutating-tool def must be ACCEPTED (tools dropped),
	// not rejected with ErrReadOnlyMemberMutating.
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "reviewer", AgentType: "reviewer", InitialPrompt: "review",
	}); err != nil {
		t.Fatalf("read-only member with a mutating def should be accepted (Edit dropped), got %v", err)
	}

	var events []agent.TeamEvent
	sup.Run(context.Background(), func(ev agent.TeamEvent) { events = append(events, ev) })
	// Edit dropped from the catalog → it is an UNKNOWN tool: it now opens a card
	// (card-before-result invariant) AND yields an error result, but it never EXECUTES.
	if sawToolDispatched(events, "reviewer", "Edit") {
		t.Fatal("read-only member executed Edit; it should have been dropped from the catalog")
	}
}

// TestMemberPerMemberPlanMode asserts a member whose def says permissionMode: plan
// runs its session in plan mode even when the team default differs (acceptEdits).
func TestMemberPerMemberPlanMode(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	planDef := agents.AgentDef{Name: "planner", Description: "p", PermissionMode: "plan"}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, mockllm.New(mockllm.TextTurn("x")), hookexec.New(nil), regOf(planDef), nil, nil, false, nil)

	build := factory(tm, agent.MemberSpec{Name: "planner", AgentType: "planner"}, "")
	if build.Mode != session.ModePlan {
		t.Fatalf("planner member mode = %q, want %q", build.Mode, session.ModePlan)
	}

	// A member without a def (or a default-mode def) returns the empty Mode, so the
	// supervisor falls back to the team default.
	noDefBuild := factory(tm, agent.MemberSpec{Name: "other"}, "")
	if noDefBuild.Mode != "" {
		t.Fatalf("member with no def mode = %q, want empty (team default)", noDefBuild.Mode)
	}
}

// TestMemberDefLimitsOnBuild proves a member def's maxTurns/maxToolCalls flow onto
// MemberBuild.Limits (only the def-set fields are non-zero; AddMember merges the
// rest with the team default), and a def with no limits yields a zero MemberBuild
// Limits (the supervisor then uses its team default).
func TestMemberDefLimitsOnBuild(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	boundedDef := agents.AgentDef{Name: "bounded", Description: "b", MaxTurns: 2, MaxToolCalls: 9}
	plainDef := agents.AgentDef{Name: "plain", Description: "p"}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, mockllm.New(mockllm.TextTurn("x")), hookexec.New(nil), regOf(boundedDef, plainDef), nil, nil, false, nil)

	bounded := factory(tm, agent.MemberSpec{Name: "bounded", AgentType: "bounded"}, "")
	if bounded.Limits != (session.Limits{MaxTurns: 2, MaxToolCalls: 9}) {
		t.Fatalf("bounded MemberBuild.Limits = %+v, want only the def-set fields {MaxTurns:2 MaxToolCalls:9}", bounded.Limits)
	}

	plain := factory(tm, agent.MemberSpec{Name: "plain", AgentType: "plain"}, "")
	if plain.Limits != (session.Limits{}) {
		t.Fatalf("plain MemberBuild.Limits = %+v, want zero (use the team default)", plain.Limits)
	}
}

// TestMemberReadOnlyAllowlistedToolDispatches proves a READ-ONLY member whose def
// allowlists a read-only core tool (Grep) is accepted (the AddMember
// workspace-mutating backstop is not tripped) and dispatches that tool.call.
func TestMemberReadOnlyAllowlistedToolDispatches(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	def := agents.AgentDef{Name: "searcher", Description: "s", Tools: []string{"Grep"}}
	tm := team.New("t")
	grepCall := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Grep", json.RawMessage(`{"pattern":"x"}`))),
		mockllm.TextTurn("done"),
	)
	factory := memberFactoryForTest(cfg, grepCall, hookexec.New(nil), regOf(def), nil, nil, false, nil)

	sup := newTestSupervisor(tm, memEnvironment("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	if err := sup.AddMember(context.Background(), agent.MemberSpec{
		Name: "searcher", AgentType: "searcher", InitialPrompt: "search it",
	}); err != nil {
		t.Fatalf("read-only member allowlisting Grep should be accepted, got %v", err)
	}

	var events []agent.TeamEvent
	sup.Run(context.Background(), func(ev agent.TeamEvent) { events = append(events, ev) })
	if !sawToolCall(events, "searcher", "Grep") {
		t.Fatalf("member with a def listing Grep should dispatch a Grep tool.call; events=%d", len(events))
	}
}

// TestMemberUnknownAgentTypeFallsBack asserts an unknown AgentType is forgiving: the
// factory falls back to the default member catalog (read-only base) and the spawn
// succeeds rather than failing.
func TestMemberUnknownAgentTypeFallsBack(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "m"}
	tm := team.New("t")
	factory := memberFactoryForTest(cfg, mockllm.New(mockllm.TextTurn("x")), hookexec.New(nil), regOf(), nil, nil, false, nil)

	build := factory(tm, agent.MemberSpec{Name: "ghost", AgentType: "does-not-exist"}, "")
	if build.Engine == nil {
		t.Fatal("unknown AgentType should fall back to a default engine, got nil")
	}
	if build.Mode != "" {
		t.Fatalf("unknown AgentType mode = %q, want empty (team default)", build.Mode)
	}

	sup := newTestSupervisor(tm, memEnvironment("/ws"),
		func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
			return factory(tm, spec, routedModel)
		})
	if err := sup.AddMember(context.Background(), agent.MemberSpec{Name: "ghost", AgentType: "does-not-exist"}); err != nil {
		t.Fatalf("unknown AgentType member should still enrol, got %v", err)
	}
}

// sawToolCall reports whether the tagged event stream contains a tool.call for the
// named tool produced by the named member.
func sawToolCall(events []agent.TeamEvent, member, toolName string) bool {
	for _, ev := range events {
		if ev.Member == member && ev.Event.Type == session.EvToolCall &&
			ev.Event.ToolCall != nil && ev.Event.ToolCall.Name == toolName {
			return true
		}
	}
	return false
}

// sawToolDispatched reports whether toolName was in the member's catalog and was
// actually DISPATCHED to a real tool — as opposed to dropped from the catalog (an
// unknown tool). It is the correct signal now that an UNKNOWN/dropped tool ALSO
// emits an EvToolCall card (the card-before-result invariant): a dropped tool yields
// an EvToolCall card AND an error tool.result whose content is "unknown tool ...",
// so the presence of a card no longer distinguishes dispatch from rejection. We
// match the result to its call by id (EvToolResult carries the call id, not the tool
// name) and treat the unknown-tool sentinel content as "NOT dispatched"; any other
// outcome (success OR a real tool error like a missing file) counts as dispatched.
func sawToolDispatched(events []agent.TeamEvent, member, toolName string) bool {
	var wantID string
	for _, ev := range events {
		if ev.Member != member {
			continue
		}
		if ev.Event.Type == session.EvToolCall && ev.Event.ToolCall != nil && ev.Event.ToolCall.Name == toolName {
			wantID = string(ev.Event.ToolCall.ID)
			continue
		}
		if ev.Event.Type == session.EvToolResult && ev.Event.ToolResult != nil &&
			wantID != "" && string(ev.Event.ToolResult.CallID) == wantID {
			return !strings.Contains(ev.Event.ToolResult.Content, "unknown tool")
		}
	}
	return false
}

// memfsForker forks a memfs environment for a Mutating member (a deterministic,
// offline isolation seam for the tests — it just roots a fresh memfs under a label).
type memfsForker struct{}

func (memfsForker) Fork(_ context.Context, _ tool.Environment, label string) (tool.Environment, func() error, string, error) {
	return memEnvironment("/fork/" + label), func() error { return nil }, "", nil
}
