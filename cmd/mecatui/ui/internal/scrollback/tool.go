package scrollback

import "reflect"

// Artifact is presentation-neutral content produced by a tool. Data is binary
// content; the remaining fields describe its logical media or resource form.
type Artifact struct {
	Kind, MIMEType string
	Data           []byte
	URL, Text      string
	Name, Title    string
	Description    string
}

// ToolCall describes the request represented by a tool card. Add copies its
// Artifacts and indexes a non-empty ID for later lifecycle transitions.
type ToolCall struct {
	ID, Name  string
	Arguments string
	Artifacts []Artifact
}

// ToolResult describes a resolved tool call. IsError records an error result;
// Artifacts are detached when stored or returned in a snapshot.
type ToolResult struct {
	Body      string
	IsError   bool
	Artifacts []Artifact
}

// ToolCardSnapshot is the detached payload for a tool call. Resolved distinguishes
// a call with its terminal Result; Finished records an earlier terminal lifecycle
// projection when the result has not arrived yet. Failed classifies that projection.
type ToolCardSnapshot struct {
	Call               ToolCall
	Resolved, Finished bool
	Failed             bool
	Result             ToolResult
	available          bool // provisional result awaiting canonical confirmation
}

// Kind returns KindTool.
func (ToolCardSnapshot) Kind() Kind       { return KindTool }
func (ToolCardSnapshot) payloadSnapshot() {}

// ToolCards adds tool cards and records their one-way results.
type ToolCards struct{ conversation *Conversation }

// Tools returns the facade for adding and resolving tool cards.
func (c *Conversation) Tools() ToolCards { return ToolCards{conversation: c} }

// Add appends a pending tool card, copies its mutable artifacts, and returns its
// new stable block ID. A non-empty call.ID is available to Resolve and specialized
// subagent or team transitions.
func (t ToolCards) Add(call ToolCall) BlockID {
	c := t.conversation
	id := c.append(ToolCardSnapshot{Call: cloneCall(call)})
	if call.ID != "" {
		if c.calls == nil {
			c.calls = map[string]int{}
		}
		c.calls[call.ID] = len(c.cards) - 1
	}
	return id
}

// ReconcileUnresolved updates the pending card indexed by call.ID. It returns
// false for an unknown, resolved, finished, or non-tool card.
func (t ToolCards) ReconcileUnresolved(call ToolCall) bool {
	c := t.conversation
	i, ok := c.call(call.ID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok || payload.Resolved || payload.Finished {
		return false
	}
	updated := cloneCall(call)
	if reflect.DeepEqual(payload.Call, updated) {
		return true
	}
	payload.Call = updated
	return c.replace(i, payload)
}

// Finish records a terminal lifecycle projection before the tool result arrives.
// It returns false for an unknown, resolved, specialized, or conflicting card.
func (t ToolCards) Finish(callID string, failed bool) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok || payload.Resolved {
		return false
	}
	if payload.Finished {
		return payload.Failed == failed
	}
	payload.Finished = true
	payload.Failed = failed
	return c.replace(i, payload)
}

// Resolve records the authoritative result for the call indexed by callID.
// It confirms identical availability or replaces a different available result.
// A conflicting canonical replay fails.
func (t ToolCards) Resolve(callID string, result ToolResult) bool {
	return t.resolve(callID, result, false)
}

// ResolveAvailable settles a pending card with a transient display result.
func (t ToolCards) ResolveAvailable(callID string, result ToolResult) bool {
	return t.resolve(callID, result, true)
}

func (t ToolCards) resolve(callID string, result ToolResult, available bool) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	entry := &c.cards[i]
	var prior ToolResult
	var resolved, provisional bool
	switch payload := entry.payload.(type) {
	case ToolCardSnapshot:
		prior, resolved, provisional = payload.Result, payload.Resolved, payload.available
	case SubagentCardSnapshot:
		prior, resolved, provisional = payload.Result, payload.Resolved, payload.available
	case TeamCardSnapshot:
		prior, resolved, provisional = payload.Result, payload.Resolved, payload.available
	default:
		return false
	}
	if resolved {
		if available {
			return provisional && reflect.DeepEqual(prior, result)
		}
		if !provisional {
			return reflect.DeepEqual(prior, result)
		}
	}
	var updated PayloadSnapshot
	switch payload := entry.payload.(type) {
	case ToolCardSnapshot:
		payload.Resolved, payload.Result, payload.available = true, cloneResult(result), available
		updated = payload
	case SubagentCardSnapshot:
		payload.Resolved, payload.Result, payload.available = true, cloneResult(result), available
		updated = payload
	case TeamCardSnapshot:
		payload.Resolved, payload.Result, payload.available = true, cloneResult(result), available
		updated = payload
	}
	return c.replace(i, updated)
}
