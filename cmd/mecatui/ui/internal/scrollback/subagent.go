package scrollback

import "reflect"

// Usage records token accounting associated with a delegation or team member.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
}

// RoutingDecision retains scalar routing evidence. Nil confidence values preserve
// absence in the source event and are detached when stored or snapshotted.
type RoutingDecision struct {
	Backend, ClassifierModel, CandidateCategory, CandidateModel string
	Confidence, MinimumConfidence                               *float64
	Outcome                                                     string
	ConsecutiveMisses, MissLimit                                int
	BreakerOpen                                                 bool
}

// Float64 returns a pointer to v for optional scalar fields.
func Float64(v float64) *float64 { return &v }

// TraceEntry is one delegation preview event. At most MaxTraceEntries trailing
// entries are retained when an update is stored.
type TraceEntry struct {
	Kind, Text, ToolName, Detail string
	Error                        bool
}

// MaxTraceEntries bounds retained delegation trace entries.
const MaxTraceEntries = 12

// SubagentStart describes a started delegated child.
type SubagentStart struct {
	ChildID, Goal, Model, RoutedCategory, RoutedModel, RoutingReason string
	Background                                                       bool
	Routing                                                          RoutingDecision
}

// SubagentUpdate is the current or terminal state of a delegated child. Done
// seals the update: only an identical replay is accepted afterwards.
type SubagentUpdate struct {
	Current, Stop, Cause string
	Trace                []TraceEntry
	ToolCount            int
	Usage                Usage
	DurationMS           int64
	Done                 bool
	Artifacts            []Artifact
}

// SubagentCardSnapshot is the detached payload of a specialized Subagent tool
// card, including its call lifecycle and child state.
type SubagentCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Start    SubagentStart
	Update   SubagentUpdate
}

// Kind returns KindSubagent.
func (SubagentCardSnapshot) Kind() Kind       { return KindSubagent }
func (SubagentCardSnapshot) payloadSnapshot() {}

// SubagentCards transitions tool cards for delegated children.
type SubagentCards struct{ conversation *Conversation }

// Subagents returns the facade for subagent lifecycle transitions.
func (c *Conversation) Subagents() SubagentCards { return SubagentCards{conversation: c} }

// Start specializes the indexed pending Subagent tool card with start. It returns
// false for a missing, wrong-kind, or non-Subagent call; an identical replay
// succeeds without changing its revision.
func (s SubagentCards) Start(callID string, start SubagentStart) bool {
	c := s.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	start = cloneSubagentStart(start)
	switch payload := c.cards[i].payload.(type) {
	case ToolCardSnapshot:
		if payload.Resolved || payload.Call.Name != "Subagent" {
			return false
		}
		return c.replace(i, SubagentCardSnapshot{
			Call: payload.Call, Resolved: payload.Resolved, Result: payload.Result, Start: start,
		})
	case SubagentCardSnapshot:
		return reflect.DeepEqual(payload.Start, start)
	default:
		return false
	}
}

// UpdateStart replaces the start data for an existing subagent card. It returns
// false when callID does not identify such a card; identical input is a successful
// no-op.
func (s SubagentCards) UpdateStart(callID string, start SubagentStart) bool {
	c := s.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(SubagentCardSnapshot)
	if !ok {
		return false
	}
	start = cloneSubagentStart(start)
	if reflect.DeepEqual(payload.Start, start) {
		return true
	}
	payload.Start = start
	return c.replace(i, payload)
}

// Update records a subagent update. It returns false for a missing or wrong-kind
// card and for a conflicting update after Done; identical updates succeed as
// no-ops.
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
	if payload.Update.Done {
		return reflect.DeepEqual(payload.Update, update)
	}
	if reflect.DeepEqual(payload.Update, update) {
		return true
	}
	payload.Update = update
	return c.replace(i, payload)
}
