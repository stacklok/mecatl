package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestDelegationCanonicalIdentityAcrossChildrenAndSessions(t *testing.T) {
	for _, name := range []string{"Subagent", "Parallel", "Team"} {
		t.Run(name, func(t *testing.T) {
			store := memstore.New()
			seenCalls := map[session.ToolCallID]bool{}
			seenSessions := map[session.SessionID]bool{}
			read := &fakeTool{name: "Read", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult(call.ID, "read"), nil
			}}
			for owner := range 2 {
				factoryCalls := 0
				factory := func(agent.ModelTarget) (*agent.Engine, bool) {
					factoryCalls++
					return childEngineWith(&branchProvider{summary: "child done"}, catalogWith(t, read)), true
				}
				resolver := func(provider, model string) (agent.ResolvedModelSelector, error) {
					return agent.ResolvedModelSelector{Target: agent.ModelTarget{Provider: provider, Model: model}}, nil
				}
				var delegation tool.Tool
				var args string
				wantChildren := 0
				switch name {
				case "Subagent":
					delegation = agent.NewSubagentTool(childEngineWith(mockllm.New(), catalogWith(t)), agent.WithSubagentStore(store), agent.WithSubagentSelectorResolver(resolver), agent.WithSubagentEngineFactory(factory))
					args = `{"prompt":"explore","model":"child-model"}`
					wantChildren = 2
				case "Parallel":
					delegation = agent.NewParallelTool(childEngineWith(mockllm.New(), catalogWith(t)), &memForker{}, agent.WithParallelStore(store), agent.WithParallelSelectorResolver(resolver), agent.WithParallelEngineFactory(factory))
					args = `{"tasks":["explore A","explore B"],"model":"child-model"}`
					wantChildren = 4
				case "Team":
					// Read is deliberately absent from member tools: synthetic unknown-tool
					// results must retain the child's accepted ID, including key reuse.
					memberFactory := func(tm *team.Team, spec agent.MemberSpec, model string) agent.MemberBuild {
						providers := map[string]*mockllm.Provider{spec.Name: mockllm.New(
							mockllm.ToolCallTurn(toolCall("k", "Read", `{}`)),
							mockllm.ToolCallTurn(toolCall("k", "Read", `{}`)),
							mockllm.TextTurn("member done"), mockllm.TextTurn("summary"),
						)}
						return teamToolFactory(t, providers)(tm, spec, model)
					}
					delegation = agent.NewTeamTool(memberFactory, agent.WithTeamToolStore(store))
					args = `{"goal":"explore","members":[{"name":"lead","role":"lead"},{"name":"worker","role":"worker"}]}`
					wantChildren = 4
				}
				parent := session.New(session.SessionID(fmt.Sprintf("identity-%s-%d", name, owner)), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
				llm := mockllm.New(mockllm.ToolCallTurn(toolCall("k", name, args)), mockllm.ToolCallTurn(toolCall("k", name, args)), mockllm.TextTurn("done"))
				recorder := &canonicalResultRecorder{}
				e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, delegation), ToolCallRecorder: recorder})
				events := drain(e.Run(t.Context(), parent, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
				if lastResult(t, events).Stop != session.StopEndTurn {
					t.Fatalf("parent failed: %+v", lastResult(t, events))
				}
				parentCalls := map[session.ToolCallID]bool{}
				for i := range 2 {
					id := eventCallID(t, events, name, i)
					if id == "k" || seenCalls[id] {
						t.Fatalf("parent invocation collision: %q", id)
					}
					seenCalls[id], parentCalls[id] = true, true
					if result := resultByCallID(events)[id]; result == nil || result.IsError {
						t.Fatalf("parent result: %+v", result)
					}
				}
				if len(recorder.calls) != 2 || len(recorder.results) != 2 {
					t.Fatalf("delegation audit recorder count: %+v", recorder)
				}
				for i, call := range recorder.calls {
					if !parentCalls[call.ID] || recorder.results[i].CallID != call.ID {
						t.Fatalf("delegation audit recorder changed identity: %+v", recorder)
					}
				}
				branches := map[session.ToolCallID]map[int]session.SessionID{}
				for _, ev := range events {
					if ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
						p := ev.Parallel
						if branches[p.ParentCallID] == nil {
							branches[p.ParentCallID] = map[int]session.SessionID{}
						}
						branches[p.ParentCallID][p.BranchIndex] = session.SessionID(p.ChildID)
					}
				}
				children := map[session.SessionID]*session.Session{}
				projections := map[session.ToolCallID]map[session.EventType]int{}
				starts, ends := 0, 0
				for _, ev := range events {
					var parentID session.ToolCallID
					var childID session.SessionID
					var childCall session.ToolCallID
					var inner session.EventType
					switch {
					case ev.Subagent != nil:
						parentID, childID, childCall, inner = ev.Subagent.ParentCallID, session.SessionID(ev.Subagent.ChildID), ev.Subagent.ChildToolCallID, ev.Subagent.InnerKind
					case ev.Parallel != nil:
						parentID, childID, childCall, inner = ev.Parallel.ParentCallID, session.SessionID(ev.Parallel.ChildID), ev.Parallel.ChildToolCallID, ev.Parallel.InnerKind
						if ev.Parallel.Kind == session.ParallelBranchTool {
							childID = branches[parentID][ev.Parallel.BranchIndex]
						}
					case ev.Team != nil:
						parentID, childID, childCall, inner = ev.Team.ParentCallID, session.SessionID(ev.Team.MemberSessionID), ev.Team.ChildToolCallID, ev.Team.InnerKind
					default:
						continue
					}
					if !parentCalls[parentID] {
						t.Fatalf("delegation has a foreign parent reference: %+v", ev)
					}
					switch ev.Type {
					case session.EvSubagentStart, session.EvParallelStart, session.EvTeamStart:
						starts++
					case session.EvSubagentEnd, session.EvParallelEnd, session.EvTeamEnd:
						ends++
					}
					if childID == "" {
						continue
					}
					child := children[childID]
					if child == nil {
						var err error
						child, err = store.Load(t.Context(), childID)
						if err != nil {
							t.Fatal(err)
						}
						wantKind := map[string]session.SessionKind{"Subagent": session.SessionKindSubagent, "Parallel": session.SessionKindParallelBranch, "Team": session.SessionKindTeamMember}[name]
						if child.Kind != wantKind || seenSessions[childID] || child.Relationship.ParentSessionID != parent.ID || child.Relationship.ParentIncarnation != parent.Incarnation() || child.Relationship.CallID != parentID {
							t.Fatalf("child namespace/lineage mismatch: %s %+v", childID, child.Relationship)
						}
						seenSessions[childID], children[childID] = true, child
					}
					if childCall != "" {
						found := false
						for _, m := range child.Conversation.Messages {
							for _, c := range m.ToolCalls {
								found = found || c.ID == childCall
							}
						}
						if !found {
							t.Fatalf("projection does not reference an actual child call: %+v", ev)
						}
						if projections[childCall] == nil {
							projections[childCall] = map[session.EventType]int{}
						}
						projections[childCall][inner]++
					}
				}
				if starts != 2 || ends != 2 || len(children) != wantChildren || name != "Team" && factoryCalls == 0 {
					t.Fatalf("starts=%d ends=%d children=%d factory=%d", starts, ends, len(children), factoryCalls)
				}
				for _, child := range children {
					if err := session.ValidateToolPairing(child.Conversation.Messages); err != nil {
						t.Fatal(err)
					}
					calls := 0
					for _, m := range child.Conversation.Messages {
						for _, c := range m.ToolCalls {
							calls++
							if c.ID == "k" || seenCalls[c.ID] || m.Replay == nil || m.Replay.Bindings[0].CallID != c.ID || m.Replay.Bindings[0].PairingID != "k" {
								t.Fatalf("child invocation/replay collision: %+v", m)
							}
							seenCalls[c.ID] = true
							p := projections[c.ID]
							if p[session.EvToolCall] != 1 || p[session.EvToolResultAvailable] != 1 || p[session.EvToolResult] != 1 {
								t.Fatalf("call/available/final delivery lost identity: %s %v", c.ID, p)
							}
						}
					}
					if calls == 0 {
						t.Fatal("child accepted no calls")
					}
					data, err := sessnap.Marshal(child)
					if err != nil {
						t.Fatal(err)
					}
					reloaded, err := sessnap.Unmarshal(data)
					if err != nil || !reflect.DeepEqual(reloaded.Relationship, child.Relationship) || !reflect.DeepEqual(reloaded.Conversation.Messages, child.Conversation.Messages) {
						t.Fatalf("child snapshot changed identity/replay: %v", err)
					}
				}
			}
		})
	}
}

func TestSubagentCancelledChildRetainsCanonicalIdentity(t *testing.T) {
	park := newParkingTool()
	store := memstore.New()
	childLLM := mockllm.New(mockllm.ToolCallTurn(toolCall("k", "Wait", `{}`)), mockllm.TextTurn("unreachable"))
	delegation := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, park)), agent.WithSubagentStore(store))
	parent := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("k", "Subagent", `{"prompt":"wait"}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, delegation)})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	run := e.Run(ctx, parent, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	t.Cleanup(run.Cancel)
	events := drainObserving(t, run, func(ev session.Event) {
		if ev.Type == session.EvSubagentTool && ev.Subagent.InnerKind == session.EvToolCall {
			select {
			case <-park.started:
			case <-ctx.Done():
				t.Error("child never reached tool execution")
				return
			}
			if !run.CancelChild(ev.Subagent.ChildID) {
				t.Error("live child could not be cancelled")
			}
		}
	})
	parentID := eventCallID(t, events, "Subagent", 0)
	childID := eventSubagentID(t, events, 0)
	child, err := store.Load(t.Context(), session.SessionID(childID))
	if err != nil {
		t.Fatal(err)
	}
	callID := boundCallID(t, child, "k")
	stop, _ := child.RecordedStopReason()
	if callID == parentID || callID == "k" || child.Relationship.CallID != parentID || stop != session.StopCancelled {
		t.Fatalf("cancelled child lost identity/lineage: %s %+v", callID, child.Relationship)
	}
	counts := map[session.EventType]int{}
	for _, ev := range events {
		if ev.Subagent == nil {
			continue
		}
		if ev.Subagent.ParentCallID != parentID {
			t.Fatalf("cancelled child references a different delegation: %+v", ev)
		}
		if ev.Subagent.ChildToolCallID != "" {
			if ev.Subagent.ChildToolCallID != callID {
				t.Fatalf("cancelled call/result projection changed ID: %+v", ev)
			}
			counts[ev.Subagent.InnerKind]++
		}
	}
	if counts[session.EvToolCall] != 1 || counts[session.EvToolResultAvailable] != 1 || counts[session.EvToolResult] != 1 {
		t.Fatalf("cancelled call/available/final correlation: %v", counts)
	}
	if result := resultByCallID(events)[parentID]; result == nil || result.IsError {
		t.Fatalf("cancelled delegation result lost parent correlation: %+v", result)
	}
	if err := session.ValidateToolPairing(child.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentForkRetainsMigratedIdentityReplayAndProvenance(t *testing.T) {
	parent, err := sessnap.Unmarshal([]byte(`{
		"id":"fork-parent", "state":"completed", "mode":"default", "limits":{}, "counters":{}, "stop_reason":"end_turn",
		"created_at":"2025-01-02T03:04:05Z", "environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},
		"messages":[
			{"role":"user","text":"history"},
			{"role":"assistant","reasoning":"opaque replay","tool_calls":[{"ID":"k","Name":"Read","Args":{"path":"a"},"item_id":"item-a"}]},
			{"role":"tool","tool_result":{"CallID":"k","Content":"read"}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	original := session.CloneMessages(parent.Conversation.Messages)
	if err := parent.Reopen(); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	var requests []port.LLMRequest
	childLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.ToolCallTurn(toolCall("k", "Read", `{}`)), mockllm.TextTurn("done"))
	delegation := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("k", "Subagent", `{"prompt":"continue","fork":true}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, delegation)})
	events := drain(e.Run(t.Context(), parent, agent.MemEnv("/ws"), agent.RunRequest{Text: "fork"}))
	id := eventCallID(t, events, "Subagent", 0)
	result := resultByCallID(events)[id]
	if result == nil || result.IsError {
		t.Fatalf("fork failed: %+v", result)
	}
	child, err := store.Load(t.Context(), session.SessionID(extractAgentID(t, result.Content)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.Conversation.Messages[:len(original)], original) || child.Relationship.CallID != id {
		t.Fatal("fork reminted historical IDs or changed replay/provenance/lineage")
	}
	copiedID := original[1].ToolCalls[0].ID
	var freshID session.ToolCallID
	for _, m := range child.Conversation.Messages[len(original):] {
		for _, c := range m.ToolCalls {
			freshID = c.ID
		}
	}
	if freshID == "" || freshID == copiedID || freshID == id || freshID == "k" {
		t.Fatalf("new child call collided with copied or parent invocation: %q", freshID)
	}
	if len(requests) != 2 || requests[0].Messages[1].ToolCalls[0].ID != "k" || requests[0].Messages[1].ToolCalls[0].ItemID != "item-a" || requests[0].Messages[1].Replay == nil {
		t.Fatalf("copied history did not reach child provider intact: %+v", requests)
	}
	for _, req := range requests {
		assertLLMToolPairing(t, req.Messages)
	}
	for range 2 {
		data, err := sessnap.Marshal(child)
		if err != nil {
			t.Fatal(err)
		}
		child, err = sessnap.Unmarshal(data)
		if err != nil || !reflect.DeepEqual(child.Conversation.Messages[:len(original)], original) {
			t.Fatalf("copied source coordinates/replay changed on restart: %v", err)
		}
	}
	child.Conversation.Messages[1].ToolCalls[0].Args[0] = '!'
	child.Conversation.Messages[1].Replay.Bindings[0].PairingID = "changed"
	child.Conversation.Messages[1].Replay.Provider.Data[0] = '!'
	child.Conversation.Messages[1].Legacy.Calls[0].ProviderID = "changed"
	if !reflect.DeepEqual(parent.Conversation.Messages[:len(original)], original) {
		t.Fatal("child history aliases parent history")
	}
	stored, err := store.Load(t.Context(), child.ID)
	if err != nil || !reflect.DeepEqual(stored.Conversation.Messages[:len(original)], original) {
		t.Fatalf("child history aliases stored snapshot: %v", err)
	}
}
