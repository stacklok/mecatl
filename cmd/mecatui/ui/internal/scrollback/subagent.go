package scrollback

import "reflect"

// Usage is token accounting associated with a delegation without coupling the
// model to the client package.
// Usage is part of the internal typed scrollback contract.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
}

// RoutingDecision is the bounded, scalar router evidence retained with a
// delegation or team member. Optional values preserve source absence.
// RoutingDecision is part of the internal typed scrollback contract.
type RoutingDecision struct {
	Backend, ClassifierModel, CandidateCategory, CandidateModel string
	Confidence, MinimumConfidence                               *float64
	Outcome                                                     string
	ConsecutiveMisses, MissLimit                                int
	BreakerOpen                                                 bool
}

// Float64 is part of the internal typed scrollback contract.
func Float64(v float64) *float64 { return &v }

// TraceEntry is one bounded delegation preview. A trace has at most
// MaxTraceEntries entries; child content remains a client-only preview.
// TraceEntry is part of the internal typed scrollback contract.
type TraceEntry struct {
	Kind, Text, ToolName, Detail string
	Error                        bool
}

// MaxTraceEntries bounds retained delegation trace entries.
const MaxTraceEntries = 12

// SubagentStart is part of the internal typed scrollback contract.
type SubagentStart struct {
	ChildID, Goal, Model, RoutedCategory, RoutedModel, RoutingReason string
	Background                                                       bool
	Routing                                                          RoutingDecision
}

// SubagentUpdate is part of the internal typed scrollback contract.
type SubagentUpdate struct {
	Current, Stop, Cause string
	Trace                []TraceEntry
	ToolCount            int
	Usage                Usage
	DurationMS           int64
	Done                 bool
	Artifacts            []Artifact
}

// SubagentCardSnapshot is part of the internal typed scrollback contract.
type SubagentCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Start    SubagentStart
	Update   SubagentUpdate
}

// Kind is part of the internal typed scrollback contract.
func (SubagentCardSnapshot) Kind() Kind       { return KindSubagent }
func (SubagentCardSnapshot) payloadSnapshot() {}

// SubagentCards is part of the internal typed scrollback contract.
type SubagentCards struct{ conversation *Conversation }

// Subagents is part of the internal typed scrollback contract.
func (c *Conversation) Subagents() SubagentCards { return SubagentCards{conversation: c} }

// Start is part of the internal typed scrollback contract.
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

// Update is part of the internal typed scrollback contract.
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
