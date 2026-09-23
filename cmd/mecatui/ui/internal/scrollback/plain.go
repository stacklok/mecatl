package scrollback

type NoticeCardSnapshot struct {
	Text    string
	Recover bool
}

func (NoticeCardSnapshot) Kind() Kind       { return KindNotice }
func (NoticeCardSnapshot) payloadSnapshot() {}

type TurnStatCardSnapshot struct{ Text string }

func (TurnStatCardSnapshot) Kind() Kind       { return KindTurnStat }
func (TurnStatCardSnapshot) payloadSnapshot() {}

type ErrorCardSnapshot struct {
	Text      string
	Permanent bool
}

func (ErrorCardSnapshot) Kind() Kind       { return KindError }
func (ErrorCardSnapshot) payloadSnapshot() {}

type HookCardSnapshot struct{ Phase, Tool, Decision string }

func (HookCardSnapshot) Kind() Kind       { return KindHook }
func (HookCardSnapshot) payloadSnapshot() {}

type DeliveryCardSnapshot struct{ FireID, Text string }

func (DeliveryCardSnapshot) Kind() Kind       { return KindDelivery }
func (DeliveryCardSnapshot) payloadSnapshot() {}

type PlainCards struct{ conversation *Conversation }

func (c *Conversation) Notices() PlainCards { return PlainCards{conversation: c} }

func (p PlainCards) AddNotice(text string) BlockID {
	return p.conversation.append(NoticeCardSnapshot{Text: text})
}

func (p PlainCards) AddRecoveryNotice(text string) BlockID {
	return p.conversation.append(NoticeCardSnapshot{Text: text, Recover: true})
}

func (p PlainCards) AddTurnStat(text string) BlockID {
	return p.conversation.append(TurnStatCardSnapshot{Text: text})
}

func (p PlainCards) AddError(text string, permanent bool) BlockID {
	return p.conversation.append(ErrorCardSnapshot{Text: text, Permanent: permanent})
}

func (p PlainCards) AddHook(phase, tool, decision string) BlockID {
	return p.conversation.append(HookCardSnapshot{Phase: phase, Tool: tool, Decision: decision})
}

func (p PlainCards) AddDelivery(fireID, text string) BlockID {
	return p.conversation.append(DeliveryCardSnapshot{FireID: fireID, Text: text})
}

func (c *Conversation) AddNotice(text string) BlockID { return c.Notices().AddNotice(text) }
func (c *Conversation) AddRecoveryNotice(text string) BlockID {
	return c.Notices().AddRecoveryNotice(text)
}
func (c *Conversation) AddTurnStat(text string) BlockID { return c.Notices().AddTurnStat(text) }
func (c *Conversation) AddError(text string, permanent bool) BlockID {
	return c.Notices().AddError(text, permanent)
}
func (c *Conversation) AddHook(phase, tool, decision string) BlockID {
	return c.Notices().AddHook(phase, tool, decision)
}
func (c *Conversation) AddDelivery(fireID, text string) BlockID {
	return c.Notices().AddDelivery(fireID, text)
}
