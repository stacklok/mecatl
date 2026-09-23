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

// Artifact is a presentation-neutral user-audience tool artifact. Its fields
// mirror the logical media/resource facts without depending on client events.
type Artifact struct {
	Kind, MIMEType string
	Data           []byte
	URL, Text      string
	Name, Title    string
	Description    string
}

type AssistantInput struct {
	Text, Reasoning    string
	ReasoningStreaming bool
}
type ToolCall struct {
	ID, Name  string
	Arguments string
	Artifacts []Artifact
}
type ToolResult struct {
	Body      string
	IsError   bool
	Artifacts []Artifact
}

// Usage is token accounting associated with a delegation without coupling the
// model to the client package.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
}

// RoutingDecision is the bounded, scalar router evidence retained with a
// delegation or team member. Optional values preserve source absence.
type RoutingDecision struct {
	Backend, ClassifierModel, CandidateCategory, CandidateModel string
	Confidence, MinimumConfidence                               *float64
	Outcome                                                     string
	ConsecutiveMisses, MissLimit                                int
	BreakerOpen                                                 bool
}

func Float64(v float64) *float64 { return &v }

// TraceEntry is one bounded delegation preview. A trace has at most
// MaxTraceEntries entries; child content remains a client-only preview.
type TraceEntry struct {
	Kind, Text, ToolName, Detail string
	Error                        bool
}

const MaxTraceEntries = 12

type SubagentStart struct {
	ChildID, Goal, Model, RoutedCategory, RoutedModel, RoutingReason string
	Background                                                       bool
	Routing                                                          RoutingDecision
}
type SubagentUpdate struct {
	Current, Stop, Cause string
	Trace                []TraceEntry
	ToolCount            int
	Usage                Usage
	DurationMS           int64
	Done                 bool
	Artifacts            []Artifact
}

type TeamStart struct {
	TeamID string
	Lanes  []TeamLane
}
type TeamUpdate struct {
	TeamID   string
	Lanes    []TeamLane
	Tasks    []Task
	Findings []Finding
	Rounds   int
	Stop     string
	Usage    Usage
	Done     bool
}

// TeamLane is a bounded projection of one Team member, in roster order.
type TeamLane struct {
	Name, SessionID, Role                             string
	Mutating, Lead                                    bool
	RoutedCategory, RoutedModel, RoutingReason, Model string
	Routing                                           RoutingDecision
	Current                                           string
	ToolCount                                         int
	Usage                                             Usage
	Trace                                             []TraceEntry
	Idle, Stopped                                     bool
	StopReason, Cause                                 string
	ErrorRounds                                       int
	ContextUsed, ContextWindow                        int64
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

type AppendixSnapshot struct {
	ID               BlockID
	Files            []string
	PrecedingBlockID BlockID
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
	return AppendixSnapshot{ID: c.appendix.ID, Files: cloneStrings(c.appendix.Files), PrecedingBlockID: c.precedingBlockID()}, true
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
func (c *Conversation) AddRecoveryNotice(text string) BlockID {
	return c.append(NoticeCardSnapshot{Text: text, Recover: true})
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
func (c *Conversation) ReviseAssistant(text string) bool {
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
	p.Text = text
	p.ReasoningStreaming = false
	return c.replace(i, p)
}
func (c *Conversation) AppendReasoning(text string) bool {
	if len(c.cards) == 0 || c.cards[len(c.cards)-1].payload.Kind() != KindAssistant {
		c.AddAssistant(AssistantInput{})
	}
	i := len(c.cards) - 1
	p := c.cards[i].payload.(AssistantCardSnapshot)
	p.Reasoning += text
	if p.Text == "" {
		p.ReasoningStreaming = true
	}
	return c.replace(i, p)
}
func (c *Conversation) EndReasoningStream() bool {
	if len(c.cards) == 0 {
		return false
	}
	i := len(c.cards) - 1
	p, ok := c.cards[i].payload.(AssistantCardSnapshot)
	if !ok {
		return false
	}
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
func (c *Conversation) StartTeam(callID string, start TeamStart) bool {
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	p, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok {
		return false
	}
	return c.replace(i, TeamCardSnapshot{Call: p.Call, Resolved: p.Resolved, Result: p.Result, Update: TeamUpdate{TeamID: start.TeamID, Lanes: start.Lanes}})
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
func (c *Conversation) precedingBlockID() BlockID {
	if len(c.cards) == 0 {
		return 0
	}
	return c.cards[len(c.cards)-1].id
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
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}
func cloneResult(in ToolResult) ToolResult { in.Artifacts = cloneArtifacts(in.Artifacts); return in }
func cloneArtifacts(in []Artifact) []Artifact {
	out := append([]Artifact(nil), in...)
	for i := range out {
		out[i].Data = append([]byte(nil), out[i].Data...)
	}
	return out
}
func cloneRouting(in RoutingDecision) RoutingDecision {
	out := in
	if in.Confidence != nil {
		out.Confidence = Float64(*in.Confidence)
	}
	if in.MinimumConfidence != nil {
		out.MinimumConfidence = Float64(*in.MinimumConfidence)
	}
	return out
}
func cloneTrace(in []TraceEntry) []TraceEntry {
	if len(in) > MaxTraceEntries {
		in = in[len(in)-MaxTraceEntries:]
	}
	return append([]TraceEntry(nil), in...)
}
func cloneSubagentStart(in SubagentStart) SubagentStart {
	in.Routing = cloneRouting(in.Routing)
	return in
}
func cloneSubagentUpdate(in SubagentUpdate) SubagentUpdate {
	in.Trace = cloneTrace(in.Trace)
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}
func cloneTeamLane(in TeamLane) TeamLane {
	in.Routing = cloneRouting(in.Routing)
	in.Trace = cloneTrace(in.Trace)
	return in
}
func cloneTeamUpdate(in TeamUpdate) TeamUpdate {
	in.Lanes = append([]TeamLane(nil), in.Lanes...)
	for i := range in.Lanes {
		in.Lanes[i] = cloneTeamLane(in.Lanes[i])
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
		p.Start = cloneSubagentStart(p.Start)
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
