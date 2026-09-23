package scrollback

// NoticeCardSnapshot is part of the internal typed scrollback contract.
type NoticeCardSnapshot struct {
	Text    string
	Recover bool
}

// Kind is part of the internal typed scrollback contract.
func (NoticeCardSnapshot) Kind() Kind       { return KindNotice }
func (NoticeCardSnapshot) payloadSnapshot() {}

// TurnStatCardSnapshot is part of the internal typed scrollback contract.
type TurnStatCardSnapshot struct{ Text string }

// Kind is part of the internal typed scrollback contract.
func (TurnStatCardSnapshot) Kind() Kind       { return KindTurnStat }
func (TurnStatCardSnapshot) payloadSnapshot() {}

// ErrorCardSnapshot is part of the internal typed scrollback contract.
type ErrorCardSnapshot struct {
	Text      string
	Permanent bool
}

// Kind is part of the internal typed scrollback contract.
func (ErrorCardSnapshot) Kind() Kind       { return KindError }
func (ErrorCardSnapshot) payloadSnapshot() {}

// HookCardSnapshot is part of the internal typed scrollback contract.
type HookCardSnapshot struct{ Text, Phase, Tool, Decision string }

// Kind is part of the internal typed scrollback contract.
func (HookCardSnapshot) Kind() Kind       { return KindHook }
func (HookCardSnapshot) payloadSnapshot() {}

// DeliveryCardSnapshot is part of the internal typed scrollback contract.
type DeliveryCardSnapshot struct{ ScheduleName, FireID, Text string }

// Kind is part of the internal typed scrollback contract.
func (DeliveryCardSnapshot) Kind() Kind       { return KindDelivery }
func (DeliveryCardSnapshot) payloadSnapshot() {}

// PlainCards is part of the internal typed scrollback contract.
type PlainCards struct{ conversation *Conversation }

// Notices is part of the internal typed scrollback contract.
func (c *Conversation) Notices() PlainCards { return PlainCards{conversation: c} }

// AddNotice is part of the internal typed scrollback contract.
func (p PlainCards) AddNotice(text string) BlockID {
	return p.conversation.append(NoticeCardSnapshot{Text: text})
}

// RetractLatestNotice removes the most-recent matching notice. It is used when
// a provisional approval notice is refused before it reaches the server.
func (p PlainCards) RetractLatestNotice(text string) bool {
	c := p.conversation
	for i := len(c.cards) - 1; i >= 0; i-- {
		notice, ok := c.cards[i].payload.(NoticeCardSnapshot)
		if !ok || notice.Text != text {
			continue
		}
		c.cards = append(c.cards[:i:i], c.cards[i+1:]...)
		for callID, index := range c.calls {
			if index > i {
				c.calls[callID] = index - 1
			}
		}
		return true
	}
	return false
}

// AddRecoveryNotice is part of the internal typed scrollback contract.
func (p PlainCards) AddRecoveryNotice(text string) BlockID {
	return p.conversation.append(NoticeCardSnapshot{Text: text, Recover: true})
}

// AddTurnStat is part of the internal typed scrollback contract.
func (p PlainCards) AddTurnStat(text string) BlockID {
	return p.conversation.append(TurnStatCardSnapshot{Text: text})
}

// AddError is part of the internal typed scrollback contract.
func (p PlainCards) AddError(text string, permanent bool) BlockID {
	return p.conversation.append(ErrorCardSnapshot{Text: text, Permanent: permanent})
}

// AddHook is part of the internal typed scrollback contract.
func (p PlainCards) AddHook(phase, tool, decision string) BlockID {
	return p.AddHookText("", phase, tool, decision)
}

// AddHookText is part of the internal typed scrollback contract.
func (p PlainCards) AddHookText(text, phase, tool, decision string) BlockID {
	return p.conversation.append(HookCardSnapshot{Text: text, Phase: phase, Tool: tool, Decision: decision})
}

// AddDelivery is part of the internal typed scrollback contract.
func (p PlainCards) AddDelivery(fireID, text string) BlockID {
	return p.AddDeliveryWithSchedule("", fireID, text)
}

// AddDeliveryWithSchedule is part of the internal typed scrollback contract.
func (p PlainCards) AddDeliveryWithSchedule(scheduleName, fireID, text string) BlockID {
	return p.conversation.append(DeliveryCardSnapshot{ScheduleName: scheduleName, FireID: fireID, Text: text})
}
