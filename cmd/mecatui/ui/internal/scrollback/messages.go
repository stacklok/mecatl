package scrollback

type UserInput struct {
	Text  string
	Media []string
}

type AssistantInput struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

type UserCardSnapshot struct {
	Text  string
	Media []string
}

func (UserCardSnapshot) Kind() Kind       { return KindUser }
func (UserCardSnapshot) payloadSnapshot() {}

type AssistantCardSnapshot struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}

func (AssistantCardSnapshot) Kind() Kind       { return KindAssistant }
func (AssistantCardSnapshot) payloadSnapshot() {}

// MessageCards owns user and assistant card mutations while Conversation keeps
// document identity and revision bookkeeping private.
type MessageCards struct{ conversation *Conversation }

func (c *Conversation) Messages() MessageCards { return MessageCards{conversation: c} }

func (m MessageCards) AddUser(in UserInput) BlockID {
	return m.conversation.append(UserCardSnapshot{Text: in.Text, Media: cloneStrings(in.Media)})
}

func (m MessageCards) AddAssistant(in AssistantInput) BlockID {
	return m.conversation.append(AssistantCardSnapshot(in))
}

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

func (c *Conversation) AddUser(in UserInput) BlockID { return c.Messages().AddUser(in) }
func (c *Conversation) AddAssistant(in AssistantInput) BlockID {
	return c.Messages().AddAssistant(in)
}
func (c *Conversation) AppendAssistant(text string) bool {
	return c.Messages().AppendAssistant(text)
}
func (c *Conversation) ReviseAssistant(text string) bool {
	return c.Messages().ReviseAssistant(text)
}
func (c *Conversation) AppendReasoning(text string) bool {
	return c.Messages().AppendReasoning(text)
}
func (c *Conversation) EndReasoningStream() bool { return c.Messages().EndReasoningStream() }
