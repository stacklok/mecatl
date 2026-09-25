package scrollback

// NoticeCardSnapshot is the detached payload for a notice; Recover marks a
// notice emitted while recovering a session.
type NoticeCardSnapshot struct {
	Text    string
	Recover bool
}

// Kind returns KindNotice.
func (NoticeCardSnapshot) Kind() Kind       { return KindNotice }
func (NoticeCardSnapshot) payloadSnapshot() {}

// TurnStatCardSnapshot is the detached payload for turn statistics text.
type TurnStatCardSnapshot struct{ Text string }

// Kind returns KindTurnStat.
func (TurnStatCardSnapshot) Kind() Kind       { return KindTurnStat }
func (TurnStatCardSnapshot) payloadSnapshot() {}

// ErrorCardSnapshot is the detached payload for an error; Permanent identifies
// an error that will not be retried.
type ErrorCardSnapshot struct {
	Text      string
	Permanent bool
}

// Kind returns KindError.
func (ErrorCardSnapshot) Kind() Kind       { return KindError }
func (ErrorCardSnapshot) payloadSnapshot() {}

// HookCardSnapshot is the detached payload recording a hook phase, tool, and
// decision, with optional display text.
type HookCardSnapshot struct{ Text, Phase, Tool, Decision string }

// Kind returns KindHook.
func (HookCardSnapshot) Kind() Kind       { return KindHook }
func (HookCardSnapshot) payloadSnapshot() {}

// DeliveryCardSnapshot is the detached payload for a scheduled delivery.
type DeliveryCardSnapshot struct{ ScheduleName, FireID, Text string }

// Kind returns KindDelivery.
func (DeliveryCardSnapshot) Kind() Kind       { return KindDelivery }
func (DeliveryCardSnapshot) payloadSnapshot() {}

// PlainCards appends non-streaming informational cards to its Conversation.
type PlainCards struct{ conversation *Conversation }

// Notices returns the facade for appending non-streaming informational cards.
func (c *Conversation) Notices() PlainCards { return PlainCards{conversation: c} }

// AddNotice appends a normal notice and returns its new, stable block ID.
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
		c.removeCard(i)
		return true
	}
	return false
}

// AddRecoveryNotice appends a recovery notice and returns its new, stable block ID.
func (p PlainCards) AddRecoveryNotice(text string) BlockID {
	return p.conversation.append(NoticeCardSnapshot{Text: text, Recover: true})
}

// AddTurnStat appends turn statistics text and returns its new, stable block ID.
func (p PlainCards) AddTurnStat(text string) BlockID {
	return p.conversation.append(TurnStatCardSnapshot{Text: text})
}

// AddError appends an error card and returns its new, stable block ID.
func (p PlainCards) AddError(text string, permanent bool) BlockID {
	return p.conversation.append(ErrorCardSnapshot{Text: text, Permanent: permanent})
}

// AddHook appends a hook card without display text and returns its new, stable block ID.
func (p PlainCards) AddHook(phase, tool, decision string) BlockID {
	return p.AddHookText("", phase, tool, decision)
}

// AddHookText appends a hook card and returns its new, stable block ID.
func (p PlainCards) AddHookText(text, phase, tool, decision string) BlockID {
	return p.conversation.append(HookCardSnapshot{Text: text, Phase: phase, Tool: tool, Decision: decision})
}

// AddDelivery appends an unscheduled delivery and returns its new, stable block ID.
func (p PlainCards) AddDelivery(fireID, text string) BlockID {
	return p.AddDeliveryWithSchedule("", fireID, text)
}

// AddDeliveryWithSchedule appends a scheduled delivery and returns its new,
// stable block ID.
func (p PlainCards) AddDeliveryWithSchedule(scheduleName, fireID, text string) BlockID {
	return p.conversation.append(DeliveryCardSnapshot{ScheduleName: scheduleName, FireID: fireID, Text: text})
}
