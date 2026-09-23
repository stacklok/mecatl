package scrollback

import "reflect"

// Artifact is a presentation-neutral user-audience tool artifact. Its fields
// mirror the logical media/resource facts without depending on client events.
// Artifact is part of the internal typed scrollback contract.
type Artifact struct {
	Kind, MIMEType string
	Data           []byte
	URL, Text      string
	Name, Title    string
	Description    string
}

// ToolCall is part of the internal typed scrollback contract.
type ToolCall struct {
	ID, Name  string
	Arguments string
	Artifacts []Artifact
}

// ToolResult is part of the internal typed scrollback contract.
type ToolResult struct {
	Body      string
	IsError   bool
	Artifacts []Artifact
}

// ToolCardSnapshot is part of the internal typed scrollback contract.
type ToolCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
}

// Kind is part of the internal typed scrollback contract.
func (ToolCardSnapshot) Kind() Kind       { return KindTool }
func (ToolCardSnapshot) payloadSnapshot() {}

// ToolCards is part of the internal typed scrollback contract.
type ToolCards struct{ conversation *Conversation }

// Tools is part of the internal typed scrollback contract.
func (c *Conversation) Tools() ToolCards { return ToolCards{conversation: c} }

// Add is part of the internal typed scrollback contract.
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

// Resolve is part of the internal typed scrollback contract.
func (t ToolCards) Resolve(callID string, result ToolResult) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	switch payload := c.cards[i].payload.(type) {
	case ToolCardSnapshot:
		if payload.Resolved && reflect.DeepEqual(payload.Result, result) {
			return true
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	case SubagentCardSnapshot:
		if payload.Resolved && reflect.DeepEqual(payload.Result, result) {
			return true
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	case TeamCardSnapshot:
		if payload.Resolved && reflect.DeepEqual(payload.Result, result) {
			return true
		}
		payload.Resolved = true
		payload.Result = cloneResult(result)
		return c.replace(i, payload)
	}
	return false
}
