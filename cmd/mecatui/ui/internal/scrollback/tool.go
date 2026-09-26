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
// a pending call from one with its terminal Result.
type ToolCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
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
// false for an unknown, resolved, or non-tool card.
func (t ToolCards) ReconcileUnresolved(call ToolCall) bool {
	c := t.conversation
	i, ok := c.call(call.ID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok || payload.Resolved {
		return false
	}
	updated := cloneCall(call)
	if reflect.DeepEqual(payload.Call, updated) {
		return true
	}
	payload.Call = updated
	return c.replace(i, payload)
}

// Resolve records result as the terminal result for the call indexed by callID.
// It returns false for an unknown call or a conflicting replay. An identical
// replay succeeds without changing the card or its revision.
func (t ToolCards) Resolve(callID string, result ToolResult) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	switch payload := c.cards[i].payload.(type) {
	case ToolCardSnapshot:
		if payload.Resolved {
			return reflect.DeepEqual(payload.Result, result)
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	case SubagentCardSnapshot:
		if payload.Resolved {
			return reflect.DeepEqual(payload.Result, result)
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	case TeamCardSnapshot:
		if payload.Resolved {
			return reflect.DeepEqual(payload.Result, result)
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	}
	return false
}
