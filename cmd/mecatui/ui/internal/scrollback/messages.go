package scrollback

// UserInput supplies the text and media references for a new user card. AddUser
// copies Media, so callers retain ownership of its backing array.
type UserInput struct {
	Text  string
	Media []string
}

// AssistantInput supplies the initial text and reasoning state for a new
// assistant card. ReasoningStreaming marks reasoning that is still arriving.
type AssistantInput struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

// UserCardSnapshot is the detached payload for a user message card.
type UserCardSnapshot struct {
	Text  string
	Media []string
}

// Kind returns KindUser.
func (UserCardSnapshot) Kind() Kind       { return KindUser }
func (UserCardSnapshot) payloadSnapshot() {}

// AssistantCardSnapshot is the detached payload for an assistant message card.
type AssistantCardSnapshot struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

// Kind returns KindAssistant.
func (AssistantCardSnapshot) Kind() Kind       { return KindAssistant }
func (AssistantCardSnapshot) payloadSnapshot() {}

// MessageCards mutates user and assistant cards in its Conversation. The facade
// does not own a copy of the Conversation and is valid while that Conversation
// remains in use.
type MessageCards struct{ conversation *Conversation }

// Messages returns the facade for adding and updating message cards.
func (c *Conversation) Messages() MessageCards { return MessageCards{conversation: c} }

// AddUser appends a user card and returns its new, stable block ID.
func (m MessageCards) AddUser(in UserInput) BlockID {
	return m.conversation.append(UserCardSnapshot{Text: in.Text, Media: cloneStrings(in.Media)})
}

// AddAssistant appends an assistant card and returns its new, stable block ID.
func (m MessageCards) AddAssistant(in AssistantInput) BlockID {
	return m.conversation.append(AssistantCardSnapshot(in))
}

// AppendAssistant appends text to the final assistant card. If the final card is
// not an assistant card, it appends a new one. It returns true after that
// mutation and clears ReasoningStreaming on an existing assistant card.
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

// ReviseAssistant replaces the text of the final assistant card. If the final
// card is not an assistant card, it appends a new one. It returns true after the
// resulting mutation and clears ReasoningStreaming on an existing assistant card.
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

// AppendReasoning appends reasoning to the final assistant card, appending an
// empty assistant card first when needed. It marks reasoning as streaming while
// that card has no visible assistant text.
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

// EndReasoningStream clears ReasoningStreaming on the final assistant card. It
// returns false if the conversation is empty or its final card is not an
// assistant card.
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
