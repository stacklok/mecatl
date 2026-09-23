package scrollback

// UserInput is part of the internal typed scrollback contract.
type UserInput struct {
	Text  string
	Media []string
}

// AssistantInput is part of the internal typed scrollback contract.
type AssistantInput struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

// UserCardSnapshot is part of the internal typed scrollback contract.
type UserCardSnapshot struct {
	Text  string
	Media []string
}

// Kind is part of the internal typed scrollback contract.
func (UserCardSnapshot) Kind() Kind       { return KindUser }
func (UserCardSnapshot) payloadSnapshot() {}

// AssistantCardSnapshot is part of the internal typed scrollback contract.
type AssistantCardSnapshot struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

// Kind is part of the internal typed scrollback contract.
func (AssistantCardSnapshot) Kind() Kind       { return KindAssistant }
func (AssistantCardSnapshot) payloadSnapshot() {}

// MessageCards owns user and assistant card mutations while Conversation keeps
// document identity and revision bookkeeping private.
// MessageCards is part of the internal typed scrollback contract.
type MessageCards struct{ conversation *Conversation }

// Messages is part of the internal typed scrollback contract.
func (c *Conversation) Messages() MessageCards { return MessageCards{conversation: c} }

// AddUser is part of the internal typed scrollback contract.
func (m MessageCards) AddUser(in UserInput) BlockID {
	return m.conversation.append(UserCardSnapshot{Text: in.Text, Media: cloneStrings(in.Media)})
}

// AddAssistant is part of the internal typed scrollback contract.
func (m MessageCards) AddAssistant(in AssistantInput) BlockID {
	return m.conversation.append(AssistantCardSnapshot(in))
}

// AppendAssistant is part of the internal typed scrollback contract.
func (m MessageCards) AppendAssistant(text string) bool {
	c := m.conversation
	if len(c.cards) == 0 {
		m.AddAssistant(AssistantInput{Text: text})
		return true
	}
	i := len(c.cards) - 1
	payload, ok := c.cards[i].payload.(AssistantCardSnapshot)
	if !ok {
		m.AddAssistant(AssistantInput{Text: text})
		return true
	}
	payload.Text += text
	payload.ReasoningStreaming = false
	return c.replace(i, payload)
}

// ReviseAssistant is part of the internal typed scrollback contract.
func (m MessageCards) ReviseAssistant(text string) bool {
	c := m.conversation
	if len(c.cards) == 0 {
		m.AddAssistant(AssistantInput{Text: text})
		return true
	}
	i := len(c.cards) - 1
	payload, ok := c.cards[i].payload.(AssistantCardSnapshot)
	if !ok {
		m.AddAssistant(AssistantInput{Text: text})
		return true
	}
	payload.Text = text
	payload.ReasoningStreaming = false
	return c.replace(i, payload)
}

// AppendReasoning is part of the internal typed scrollback contract.
func (m MessageCards) AppendReasoning(text string) bool {
	c := m.conversation
	if len(c.cards) == 0 || c.cards[len(c.cards)-1].payload.Kind() != KindAssistant {
		m.AddAssistant(AssistantInput{})
	}
	i := len(c.cards) - 1
	payload := c.cards[i].payload.(AssistantCardSnapshot)
	payload.Reasoning += text
	if payload.Text == "" {
		payload.ReasoningStreaming = true
	}
	return c.replace(i, payload)
}

// EndReasoningStream is part of the internal typed scrollback contract.
func (m MessageCards) EndReasoningStream() bool {
	c := m.conversation
	if len(c.cards) == 0 {
		return false
	}
	i := len(c.cards) - 1
	payload, ok := c.cards[i].payload.(AssistantCardSnapshot)
	if !ok {
		return false
	}
	payload.ReasoningStreaming = false
	return c.replace(i, payload)
}

// AddUser is part of the internal typed scrollback contract.
func (c *Conversation) AddUser(in UserInput) BlockID { return c.Messages().AddUser(in) }

// AddAssistant is part of the internal typed scrollback contract.
func (c *Conversation) AddAssistant(in AssistantInput) BlockID {
	return c.Messages().AddAssistant(in)
}

// AppendAssistant is part of the internal typed scrollback contract.
func (c *Conversation) AppendAssistant(text string) bool {
	return c.Messages().AppendAssistant(text)
}

// ReviseAssistant is part of the internal typed scrollback contract.
func (c *Conversation) ReviseAssistant(text string) bool {
	return c.Messages().ReviseAssistant(text)
}

// AppendReasoning is part of the internal typed scrollback contract.
func (c *Conversation) AppendReasoning(text string) bool {
	return c.Messages().AppendReasoning(text)
}

// EndReasoningStream is part of the internal typed scrollback contract.
func (c *Conversation) EndReasoningStream() bool { return c.Messages().EndReasoningStream() }
