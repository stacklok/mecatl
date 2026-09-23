// Package scrollback owns Mecatui's logical conversation cards. It deliberately
// contains no renderer, terminal, viewport, or client-event dependency.
//
//nolint:revive // This internal package deliberately exposes a typed package contract without public-module documentation.
package scrollback

import "reflect"

type BlockID uint64

type Kind uint8

const (
	KindUser Kind = iota
	KindAssistant
	KindTool
	KindSubagent
	KindTeam
	KindNotice
	KindTurnStat
	KindError
	KindHook
	KindDelivery
)

// PayloadSnapshot is closed: consumers can inspect package-defined snapshots but
// cannot create another card family or mutate model-owned payload state.
type PayloadSnapshot interface {
	Kind() Kind
	payloadSnapshot()
}

type BlockSnapshot struct {
	ID       BlockID
	Revision uint64
	Payload  PayloadSnapshot
}

type UserInput struct {
	Text  string
	Media []string
}
type AssistantInput struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}
type ToolCall struct {
	ID, Name  string
	Arguments map[string]string
	Artifacts []string
}
type ToolResult struct {
	Body      string
	IsError   bool
	Artifacts []string
}
type TraceEntry struct {
	Kind, Text string
	Error      bool
}
type SubagentStart struct {
	Goal  string
	Model string
}
type SubagentUpdate struct {
	Current   string
	Trace     []TraceEntry
	ToolCount int
	Done      bool
	Stop      string
}
type TeamUpdate struct {
	Lanes    map[string][]TraceEntry
	Tasks    []Task
	Findings []Finding
	Done     bool
	Stop     string
}
type Task struct {
	ID, Description, State, Assignee string
	Dependencies                     []string
}
type Finding struct{ Member, Body string }

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

type ToolCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
}

func (ToolCardSnapshot) Kind() Kind       { return KindTool }
func (ToolCardSnapshot) payloadSnapshot() {}

type SubagentCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Start    SubagentStart
	Update   SubagentUpdate
}

func (SubagentCardSnapshot) Kind() Kind       { return KindSubagent }
func (SubagentCardSnapshot) payloadSnapshot() {}

type TeamCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Update   TeamUpdate
}

func (TeamCardSnapshot) Kind() Kind       { return KindTeam }
func (TeamCardSnapshot) payloadSnapshot() {}

type NoticeCardSnapshot struct{ Text string }

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

type AppendixSnapshot struct {
	ID    BlockID
	Files []string
}

type card struct {
	id       BlockID
	revision uint64
	payload  PayloadSnapshot
}
type Conversation struct {
	cards    []card
	nextID   BlockID
	calls    map[string]int
	appendix *AppendixSnapshot
	seen     map[string]struct{}
}

func (c *Conversation) Len() int { return len(c.cards) }
func (c *Conversation) SnapshotAt(i int) BlockSnapshot {
	card := c.cards[i]
	return BlockSnapshot{ID: card.id, Revision: card.revision, Payload: clonePayload(card.payload)}
}
func (c *Conversation) AppendixSnapshot() (AppendixSnapshot, bool) {
	if c.appendix == nil {
		return AppendixSnapshot{}, false
	}
	return AppendixSnapshot{ID: c.appendix.ID, Files: cloneStrings(c.appendix.Files)}, true
}
func (c *Conversation) append(payload PayloadSnapshot) BlockID {
	c.nextID++
	c.cards = append(c.cards, card{id: c.nextID, payload: clonePayload(payload)})
	return c.nextID
}
func (c *Conversation) AddUser(in UserInput) BlockID {
	return c.append(UserCardSnapshot{Text: in.Text, Media: cloneStrings(in.Media)})
}
func (c *Conversation) AddAssistant(in AssistantInput) BlockID {
	return c.append(AssistantCardSnapshot(in))
}
func (c *Conversation) AddNotice(text string) BlockID {
	return c.append(NoticeCardSnapshot{Text: text})
}
func (c *Conversation) AddTurnStat(text string) BlockID {
	return c.append(TurnStatCardSnapshot{Text: text})
}
func (c *Conversation) AddError(text string, permanent bool) BlockID {
	return c.append(ErrorCardSnapshot{Text: text, Permanent: permanent})
}
func (c *Conversation) AddHook(phase, tool, decision string) BlockID {
	return c.append(HookCardSnapshot{Phase: phase, Tool: tool, Decision: decision})
}
func (c *Conversation) AddDelivery(fireID, text string) BlockID {
	return c.append(DeliveryCardSnapshot{FireID: fireID, Text: text})
}
func (c *Conversation) AddToolCall(call ToolCall) BlockID {
	id := c.append(ToolCardSnapshot{Call: cloneCall(call)})
	if call.ID != "" {
		if c.calls == nil {
			c.calls = map[string]int{}
		}
		c.calls[call.ID] = len(c.cards) - 1
	}
	return id
}
func (c *Conversation) AppendAssistant(text string) bool {
	if len(c.cards) == 0 {
		c.AddAssistant(AssistantInput{Text: text})
		return true
	}
	i := len(c.cards) - 1
	p, ok := c.cards[i].payload.(AssistantCardSnapshot)
	if !ok {
		c.AddAssistant(AssistantInput{Text: text})
		return true
	}
	p.Text += text
	p.ReasoningStreaming = false
	return c.replace(i, p)
}
func (c *Conversation) ResolveTool(callID string, result ToolResult) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	switch p := c.cards[i].payload.(type) {
	case ToolCardSnapshot:
		if p.Resolved && reflect.DeepEqual(p.Result, result) {
			return true
		}
		p.Resolved = true
		p.Result = cloneResult(result)
		return c.replace(i, p)
	case SubagentCardSnapshot:
		if p.Resolved && reflect.DeepEqual(p.Result, result) {
			return true
		}
		p.Resolved = true
		p.Result = cloneResult(result)
		return c.replace(i, p)
	case TeamCardSnapshot:
		if p.Resolved && reflect.DeepEqual(p.Result, result) {
			return true
		}
		p.Resolved = true
		p.Result = cloneResult(result)
		return c.replace(i, p)
	}
	return false
}
func (c *Conversation) StartSubagent(callID string, start SubagentStart) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	p, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok {
		return false
	}
	return c.replace(i, SubagentCardSnapshot{Call: p.Call, Resolved: p.Resolved, Result: p.Result, Start: start})
}
func (c *Conversation) UpdateSubagent(callID string, update SubagentUpdate) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	p, ok := c.cards[i].payload.(SubagentCardSnapshot)
	if !ok {
		return false
	}
	update = cloneSubagentUpdate(update)
	if reflect.DeepEqual(p.Update, update) {
		return true
	}
	p.Update = update
	return c.replace(i, p)
}
func (c *Conversation) StartTeam(callID string) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	p, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok {
		return false
	}
	return c.replace(i, TeamCardSnapshot{Call: p.Call, Resolved: p.Resolved, Result: p.Result})
}
func (c *Conversation) UpdateTeam(callID string, update TeamUpdate) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	p, ok := c.cards[i].payload.(TeamCardSnapshot)
	if !ok {
		return false
	}
	update = cloneTeamUpdate(update)
	if reflect.DeepEqual(p.Update, update) {
		return true
	}
	p.Update = update
	return c.replace(i, p)
}
func (c *Conversation) RecordFileChange(path string) BlockID {
	if path == "" {
		return 0
	}
	if c.seen == nil {
		c.seen = map[string]struct{}{}
	}
	if _, ok := c.seen[path]; ok {
		if c.appendix == nil {
			return 0
		}
		return c.appendix.ID
	}
	c.seen[path] = struct{}{}
	if c.appendix == nil {
		c.nextID++
		c.appendix = &AppendixSnapshot{ID: c.nextID}
	}
	c.appendix.Files = append(c.appendix.Files, path)
	return c.appendix.ID
}
func (c *Conversation) call(id string) (int, bool) { i, ok := c.calls[id]; return i, ok }
func (c *Conversation) replace(i int, p PayloadSnapshot) bool {
	p = clonePayload(p)
	if reflect.DeepEqual(c.cards[i].payload, p) {
		return true
	}
	c.cards[i].payload = p
	c.cards[i].revision++
	return true
}
func cloneStrings(in []string) []string { return append([]string(nil), in...) }
func cloneCall(in ToolCall) ToolCall {
	in.Arguments = maps(in.Arguments)
	in.Artifacts = cloneStrings(in.Artifacts)
	return in
}
func cloneResult(in ToolResult) ToolResult { in.Artifacts = cloneStrings(in.Artifacts); return in }
func maps(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneSubagentUpdate(in SubagentUpdate) SubagentUpdate {
	in.Trace = append([]TraceEntry(nil), in.Trace...)
	return in
}
func cloneTeamUpdate(in TeamUpdate) TeamUpdate {
	if in.Lanes != nil {
		out := make(map[string][]TraceEntry, len(in.Lanes))
		for k, v := range in.Lanes {
			out[k] = append([]TraceEntry(nil), v...)
		}
		in.Lanes = out
	}
	in.Tasks = append([]Task(nil), in.Tasks...)
	for i := range in.Tasks {
		in.Tasks[i].Dependencies = cloneStrings(in.Tasks[i].Dependencies)
	}
	in.Findings = append([]Finding(nil), in.Findings...)
	return in
}
func clonePayload(in PayloadSnapshot) PayloadSnapshot {
	switch p := in.(type) {
	case UserCardSnapshot:
		p.Media = cloneStrings(p.Media)
		return p
	case AssistantCardSnapshot:
		return p
	case ToolCardSnapshot:
		p.Call = cloneCall(p.Call)
		p.Result = cloneResult(p.Result)
		return p
	case SubagentCardSnapshot:
		p.Call = cloneCall(p.Call)
		p.Result = cloneResult(p.Result)
		p.Update = cloneSubagentUpdate(p.Update)
		return p
	case TeamCardSnapshot:
		p.Call = cloneCall(p.Call)
		p.Result = cloneResult(p.Result)
		p.Update = cloneTeamUpdate(p.Update)
		return p
	case NoticeCardSnapshot:
		return p
	case TurnStatCardSnapshot:
		return p
	case ErrorCardSnapshot:
		return p
	case HookCardSnapshot:
		return p
	case DeliveryCardSnapshot:
		return p
	default:
		panic("scrollback: unknown payload")
	}
}
