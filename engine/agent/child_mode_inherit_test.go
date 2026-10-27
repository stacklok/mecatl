package agent_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestEffectiveChildMode pins the inheritance table: an unconfigured child
// inherits an auto or yolo parent and otherwise runs in default; every
// explicitly configured child mode, default included, is kept.
func TestEffectiveChildMode(t *testing.T) {
	for _, tc := range []struct {
		configured, parent, want session.PermissionMode
	}{
		{"", session.ModeYolo, session.ModeYolo},
		{"", session.ModeAuto, session.ModeAuto},
		{"", session.ModeAccept, session.ModeDefault},
		{"", session.ModePlan, session.ModeDefault},
		{"", "", session.ModeDefault},
		{session.ModeDefault, session.ModeYolo, session.ModeDefault},
		{session.ModeDefault, session.ModeAuto, session.ModeDefault},
		{session.ModePlan, session.ModeYolo, session.ModePlan},
		{session.ModeAccept, session.ModeAuto, session.ModeAccept},
	} {
		if got := agent.EffectiveChildModeForTest(tc.configured, tc.parent); got != tc.want {
			t.Errorf("effectiveChildMode(%q, parent %q) = %q, want %q", tc.configured, tc.parent, got, tc.want)
		}
	}
}

// TestSubagentInheritsYoloParentMode drives a real parent run: a child tool
// that asks under the default mode (no rules, headless → denied) runs when the
// parent session is in auto or yolo, because the child inherits the parent's mode.
func TestSubagentInheritsYoloParentMode(t *testing.T) {
	for _, tc := range []struct {
		parent session.PermissionMode
		want   bool
	}{
		{session.ModeDefault, false},
		{session.ModeAuto, true},
		{session.ModeYolo, true},
	} {
		var executed atomic.Bool
		childTool := &fakeTool{name: "Mutate", readOnly: false,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed.Store(true)
				return session.NewToolResult(in.ID, "ran"), nil
			}}
		childEngine := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(
				mockllm.ToolCallTurn(toolCall("k1", "Mutate", `{}`)),
				mockllm.TextTurn("child done"),
			),
			Catalog: catalogWith(t, childTool),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "child-model",
		})
		task := agent.NewSubagentTool(childEngine)
		e := newEngine(agent.Deps{LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
			mockllm.TextTurn("ok"),
		), Catalog: catalogWith(t, task)})
		sess := newSession(t, session.Limits{})
		if err := sess.SetMode(tc.parent); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("parent run did not complete")
		}
		if executed.Load() != tc.want {
			t.Errorf("parent %s: child mutating tool executed=%t, want %t", tc.parent, executed.Load(), tc.want)
		}
	}
}

// mutatingChildEngine builds a child engine whose only tool mutates and asks
// under the default mode (no rules, headless → denied), counting executions.
func mutatingChildEngine(t *testing.T, executed *atomic.Int32, turns ...mockllm.Turn) *agent.Engine {
	t.Helper()
	childTool := &fakeTool{name: "Mutate", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Add(1)
			return session.NewToolResult(in.ID, "ran"), nil
		}}
	return agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(turns...),
		Catalog: catalogWith(t, childTool),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "child-model",
	})
}

// runParent drives one parent run in mode to completion and returns its events.
func runParent(t *testing.T, sess *session.Session, mode session.PermissionMode, parentLLM port.LLMProvider, tools ...tool.Tool) []session.Event {
	t.Helper()
	if sess.State == session.StateCompleted {
		if err := sess.Reopen(); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tools...)})
	return drainObserving(t, e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}), nil)
}

// TestExplicitDefaultChildModeIsKeptUnderYoloParent proves an explicitly
// configured default mode is a restriction, not "inherit": fresh foreground
// and background Subagent children and Parallel branches keep default's ask
// (headless → denied) under a yolo parent, while unconfigured ones inherit yolo.
func TestExplicitDefaultChildModeIsKeptUnderYoloParent(t *testing.T) {
	childTurns := func() []mockllm.Turn {
		return []mockllm.Turn{mockllm.ToolCallTurn(toolCall("k1", "Mutate", `{}`)), mockllm.TextTurn("child done")}
	}
	for _, tc := range []struct {
		name     string
		explicit bool
		build    func(*agent.Engine) tool.Tool
		parent   []mockllm.Turn
	}{
		{"subagent", true, func(e *agent.Engine) tool.Tool {
			return agent.NewSubagentTool(e, agent.WithChildMode(session.ModeDefault))
		},
			[]mockllm.Turn{mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)), mockllm.TextTurn("ok")}},
		{"subagent-inherit", false, func(e *agent.Engine) tool.Tool { return agent.NewSubagentTool(e) },
			[]mockllm.Turn{mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)), mockllm.TextTurn("ok")}},
		{"subagent-background", true, func(e *agent.Engine) tool.Tool {
			return agent.NewSubagentTool(e, agent.WithChildMode(session.ModeDefault))
		},
			[]mockllm.Turn{mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go","background":true}`)), mockllm.TextTurn("ok")}},
		{"parallel", true, func(e *agent.Engine) tool.Tool {
			return agent.NewParallelTool(e, &memForker{}, agent.WithParallelChildMode(session.ModeDefault))
		}, []mockllm.Turn{mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A"]}`)), mockllm.TextTurn("ok")}},
		{"parallel-inherit", false, func(e *agent.Engine) tool.Tool { return agent.NewParallelTool(e, &memForker{}) },
			[]mockllm.Turn{mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A"]}`)), mockllm.TextTurn("ok")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executed atomic.Int32
			delegate := tc.build(mutatingChildEngine(t, &executed, childTurns()...))
			if tc.name == "subagent-background" {
				evs := runParent(t, newSession(t, session.Limits{}), session.ModeYolo, newScriptedLLM(
					mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go","background":true}`)),
					handleTurn(t, "p1", toolCall("p2", "SubagentStatus", `{"wait_ms":30000}`), "agent_id"),
					mockllm.TextTurn("ok"),
				), delegate, agent.NewSubagentStatusTool())
				collected := resultByCallID(evs)[eventCallID(t, evs, "SubagentStatus", 0)]
				if collected == nil || collected.IsError || !strings.Contains(collected.Content, "child done") {
					t.Fatalf("background child was not collected after denied mutation: %+v", collected)
				}
				denied := false
				for _, ev := range evs {
					denied = denied || ev.Type == session.EvSubagentTool && ev.Subagent != nil && ev.Subagent.InnerKind == session.EvToolResult && ev.Subagent.ToolName == "Mutate" && ev.Subagent.IsError
				}
				if !denied {
					t.Fatal("background child mutation was not denied under explicit default mode")
				}
			} else {
				runParent(t, newSession(t, session.Limits{}), session.ModeYolo, mockllm.New(tc.parent...), delegate, agent.NewSubagentStatusTool())
			}
			if got, want := executed.Load() == 1, !tc.explicit; got != want {
				t.Errorf("child mutating tool executed=%t under a yolo parent, want %t", got, want)
			}
		})
	}
}

// TestResumedSubagentReadmitsModeAfterParentDowngrade proves a child persisted
// under a yolo parent does not keep yolo's authority when it is resumed, after a
// restart, by a parent that has since been downgraded to default. Both the
// foreground and the background resume paths re-admit the stored mode before
// the child runs; the child's history is preserved.
func TestResumedSubagentReadmitsModeAfterParentDowngrade(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "foreground"
		if background {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			store := memstore.New()
			var executed atomic.Int32
			sess := newSession(t, session.Limits{})

			// Phase 1: the yolo parent creates the child, whose mutation runs.
			first := agent.NewSubagentTool(mutatingChildEngine(t, &executed,
				mockllm.ToolCallTurn(toolCall("k1", "Mutate", `{}`)), mockllm.TextTurn("FIRST_PASS")),
				agent.WithSubagentStore(store))
			firstEvents := runParent(t, sess, session.ModeYolo, mockllm.New(
				mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)), mockllm.TextTurn("ok")), first)
			if executed.Load() != 1 {
				t.Fatalf("yolo parent: child mutation executed %d times, want 1", executed.Load())
			}
			started := resultByCallID(firstEvents)[eventCallID(t, firstEvents, "Subagent", 0)]
			if started == nil || started.IsError {
				t.Fatalf("first child did not complete: %+v", started)
			}
			childID := extractAgentID(t, started.Content)
			if childID == "" {
				t.Fatalf("first child result did not expose an agentId: %q", started.Content)
			}
			if stored, err := store.Load(context.Background(), session.SessionID(childID)); err != nil || stored.Mode != session.ModeYolo {
				t.Fatalf("persisted child = %v, %v; want mode yolo", stored, err)
			}

			// Phase 2: a restarted harness (fresh tool and engines, same store)
			// resumes the child from a parent downgraded to default.
			second := agent.NewSubagentTool(mutatingChildEngine(t, &executed,
				mockllm.ToolCallTurn(toolCall("k2", "Mutate", `{}`)), mockllm.TextTurn("SECOND_PASS")),
				agent.WithSubagentStore(store))
			parentTurns := []mockllm.Turn{mockllm.ToolCallTurn(toolCall("p2", "Subagent", resumeArgs(childID, "again"))), mockllm.TextTurn("ok")}
			parentLLM := port.LLMProvider(mockllm.New(parentTurns...))
			if background {
				parentLLM = newScriptedLLM(
					mockllm.ToolCallTurn(toolCall("p2", "Subagent", fmt.Sprintf(`{"prompt":"again","resume":%q,"background":true}`, childID))),
					handleTurn(t, "p2", toolCall("p3", "SubagentStatus", `{"wait_ms":30000}`), "agent_id"),
					mockllm.TextTurn("ok"),
				)
			}
			evs := runParent(t, sess, session.ModeDefault, parentLLM, second, agent.NewSubagentStatusTool())
			collect := eventCallID(t, evs, "Subagent", 0)
			if background {
				collect = eventCallID(t, evs, "SubagentStatus", 0)
			}
			if res := resultByCallID(evs)[collect]; res == nil || res.IsError || !strings.Contains(res.Content, "SECOND_PASS") {
				t.Fatalf("resumed child did not complete: %+v; events %v; state %s", res, typesOf(evs), sess.State)
			}
			if executed.Load() != 1 {
				t.Errorf("downgraded parent: resumed child kept yolo authority (mutation executed %d times, want 1)", executed.Load())
			}
			stored, err := store.Load(context.Background(), session.SessionID(childID))
			if err != nil {
				t.Fatal(err)
			}
			if stored.Mode != session.ModeDefault {
				t.Errorf("re-persisted child mode = %q, want %q", stored.Mode, session.ModeDefault)
			}
			var kept bool
			for _, m := range stored.Conversation.Messages {
				kept = kept || strings.Contains(m.Text, "FIRST_PASS")
			}
			if !kept {
				t.Error("resumed child lost its prior history")
			}
		})
	}
}

// TestResumedChildMode pins the re-admission table: the narrower of the stored
// mode and what a fresh child of the current parent would get.
func TestResumedChildMode(t *testing.T) {
	for _, tc := range []struct {
		stored, configured, parent, want session.PermissionMode
	}{
		{session.ModeYolo, "", session.ModeDefault, session.ModeDefault},
		{session.ModeYolo, "", session.ModeAuto, session.ModeAuto},
		{session.ModeYolo, "", session.ModeYolo, session.ModeYolo},
		{session.ModeAuto, "", session.ModeYolo, session.ModeAuto},
		{session.ModeDefault, "", session.ModeYolo, session.ModeDefault},
		{session.ModePlan, "", session.ModeYolo, session.ModePlan},
		{session.ModeYolo, session.ModeAccept, session.ModeYolo, session.ModeAccept},
	} {
		if got := agent.ResumedChildModeForTest(tc.stored, tc.configured, tc.parent); got != tc.want {
			t.Errorf("resumedChildMode(stored %q, configured %q, parent %q) = %q, want %q", tc.stored, tc.configured, tc.parent, got, tc.want)
		}
	}
}

// countingActionReviewer reviews actions, enforces its finding, and records
// every reviewed tool name. It returns assessment for Mutate and accepts every
// other tool, so the parent's own Subagent call is never the one blocked.
type countingActionReviewer struct {
	assessment agent.ReviewAssessment
	mu         sync.Mutex
	reviewed   []string
}

func (r *countingActionReviewer) Review(_ context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.reviewed = append(r.reviewed, req.EffectiveCall.Name)
	r.mu.Unlock()
	if req.EffectiveCall.Name != "Mutate" {
		return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, session.AuxiliaryUsage{}, nil
	}
	return agent.ToolReviewResult{Assessment: r.assessment}, session.AuxiliaryUsage{}, nil
}

func (*countingActionReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	return job == agent.ReviewJobAction, true
}

func (r *countingActionReviewer) reviewedTools() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reviewed...)
}

// TestInheritingChildOfAutoParentConsultsChecker proves an unconfigured
// Subagent child of an auto parent runs under the parent's enforcing checker,
// not as plain allow-all: the child's mutating call is reviewed, runs when the
// checker accepts it, and is blocked when the checker prohibits it.
func TestInheritingChildOfAutoParentConsultsChecker(t *testing.T) {
	for _, tc := range []struct {
		assessment agent.ReviewAssessment
		want       int32
	}{
		{agent.ReviewAcceptable, 1},
		{agent.ReviewProhibited, 0},
	} {
		t.Run(string(tc.assessment), func(t *testing.T) {
			var executed atomic.Int32
			reviewer := &countingActionReviewer{assessment: tc.assessment}
			task := agent.NewSubagentTool(mutatingChildEngine(t, &executed,
				mockllm.ToolCallTurn(toolCall("k1", "Mutate", `{}`)), mockllm.TextTurn("child done")))
			sess := newSession(t, session.Limits{})
			if err := sess.SetMode(session.ModeAuto); err != nil {
				t.Fatal(err)
			}
			e := newEngine(agent.Deps{
				LLM:          mockllm.New(mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)), mockllm.TextTurn("ok")),
				Catalog:      catalogWith(t, task),
				ToolReviewer: reviewer,
			})
			drainObserving(t, e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}), nil)
			if got := executed.Load(); got != tc.want {
				t.Errorf("child Mutate executed %d times under an auto parent with a %s checker, want %d", got, tc.assessment, tc.want)
			}
			if !slices.Contains(reviewer.reviewedTools(), "Mutate") {
				t.Errorf("checker never reviewed the child's Mutate call; reviewed %v", reviewer.reviewedTools())
			}
		})
	}
}

// rejectingActionReviewer prohibits every action and enforces its finding.
type rejectingActionReviewer struct{}

func (rejectingActionReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, session.AuxiliaryUsage{}, nil
}

func (rejectingActionReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	return job == agent.ReviewJobAction, true
}

// TestSharedReviewerEnforcesForNarrowerChildOfYoloParent proves the reviewer a
// yolo parent shares with its Subagent child selects enforcement from the
// CHILD's mode: an explicitly default child's prohibited action is blocked,
// while an inheriting yolo child (like its parent) treats it as advisory.
func TestSharedReviewerEnforcesForNarrowerChildOfYoloParent(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []agent.SubagentOption
		want bool
	}{
		{"explicit-default", []agent.SubagentOption{agent.WithChildMode(session.ModeDefault)}, false},
		{"inherit", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executed atomic.Bool
			probe := &fakeTool{name: "Probe", readOnly: true,
				exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
					executed.Store(true)
					return session.NewToolResult(in.ID, "probed"), nil
				}}
			childEngine := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("k1", "Probe", `{}`)), mockllm.TextTurn("child done")),
				Catalog: catalogWith(t, probe),
				Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
				Model:   "child-model",
			})
			task := agent.NewSubagentTool(childEngine, tc.opts...)
			sess := newSession(t, session.Limits{})
			if err := sess.SetMode(session.ModeYolo); err != nil {
				t.Fatal(err)
			}
			e := newEngine(agent.Deps{
				LLM:          mockllm.New(mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)), mockllm.TextTurn("ok")),
				Catalog:      catalogWith(t, task),
				ToolReviewer: rejectingActionReviewer{},
			})
			drainObserving(t, e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}), nil)
			if executed.Load() != tc.want {
				t.Errorf("child Probe executed=%t under a yolo parent with a rejecting reviewer, want %t", executed.Load(), tc.want)
			}
		})
	}
}
