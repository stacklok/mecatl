package scrollback

import "reflect"

// Usage is token accounting associated with a delegation without coupling the
// model to the client package.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
}

// RoutingDecision is the bounded, scalar router evidence retained with a
// delegation or team member. Optional values preserve source absence.
type RoutingDecision struct {
	Backend, ClassifierModel, CandidateCategory, CandidateModel string
	Confidence, MinimumConfidence                               *float64
	Outcome                                                     string
	ConsecutiveMisses, MissLimit                                int
	BreakerOpen                                                 bool
}

func Float64(v float64) *float64 { return &v }

// TraceEntry is one bounded delegation preview. A trace has at most
// MaxTraceEntries entries; child content remains a client-only preview.
type TraceEntry struct {
	Kind, Text, ToolName, Detail string
	Error                        bool
}

const MaxTraceEntries = 12

type SubagentStart struct {
	ChildID, Goal, Model, RoutedCategory, RoutedModel, RoutingReason string
	Background                                                       bool
	Routing                                                          RoutingDecision
}

type SubagentUpdate struct {
	Current, Stop, Cause string
	Trace                []TraceEntry
	ToolCount            int
	Usage                Usage
	DurationMS           int64
	Done                 bool
	Artifacts            []Artifact
}

type SubagentCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Start    SubagentStart
	Update   SubagentUpdate
}

func (SubagentCardSnapshot) Kind() Kind       { return KindSubagent }
func (SubagentCardSnapshot) payloadSnapshot() {}

type SubagentCards struct{ conversation *Conversation }

func (c *Conversation) Subagents() SubagentCards { return SubagentCards{conversation: c} }

func (s SubagentCards) Start(callID string, start SubagentStart) bool {
	c := s.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok {
		return false
	}
	return c.replace(i, SubagentCardSnapshot{
		Call: payload.Call, Resolved: payload.Resolved, Result: payload.Result, Start: start,
	})
}

func (s SubagentCards) Update(callID string, update SubagentUpdate) bool {
	c := s.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(SubagentCardSnapshot)
	if !ok {
		return false
	}
	update = cloneSubagentUpdate(update)
	if reflect.DeepEqual(payload.Update, update) {
		return true
	}
	payload.Update = update
	return c.replace(i, payload)
}

func (c *Conversation) StartSubagent(callID string, start SubagentStart) bool {
	return c.Subagents().Start(callID, start)
}
func (c *Conversation) UpdateSubagent(callID string, update SubagentUpdate) bool {
	return c.Subagents().Update(callID, update)
}
