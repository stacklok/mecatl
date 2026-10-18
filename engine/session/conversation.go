package session

import (
	"fmt"
	"slices"
	"sort"
)

// Role identifies the author of a Message in the conversation.
type Role string

const (
	// RoleSystem is the system prompt author.
	RoleSystem Role = "system"
	// RoleUser is the human/client author.
	RoleUser Role = "user"
	// RoleAssistant is the model author.
	RoleAssistant Role = "assistant"
	// RoleTool is a tool-result author.
	RoleTool Role = "tool"
)

// UserPromptProvenance identifies explicit authority provenance for a user-role message.
type UserPromptProvenance string

const (
	// UserPromptProvenanceUnknown is the fail-closed zero value used by legacy
	// snapshots and callers that record an ordinary user-role message.
	UserPromptProvenanceUnknown UserPromptProvenance = ""
	// UserPromptProvenancePrincipal marks input authenticated by the root run's
	// prompt or accepted-steer ingress.
	UserPromptProvenancePrincipal UserPromptProvenance = "principal"
	// UserPromptProvenanceHarness marks a harness-authored continuation.
	UserPromptProvenanceHarness UserPromptProvenance = "harness"
)

// Message is an immutable value object: one entry in the model-visible
// conversation history. Construct it with one of the constructors below; it
// carries no mutating methods.
type Message struct {
	// Role is the author of this message.
	Role Role
	// Text is the message body (assistant text, user prompt, etc.).
	Text string
	// ToolCalls holds the tool invocations requested by an assistant message.
	ToolCalls []ToolCall
	// ToolResult holds the result carried by a tool-role message; nil otherwise.
	ToolResult *ToolResult
	// ProviderPhase is public display metadata; exact replay belongs to Replay.
	ProviderPhase string
	// Parts carries non-text media (image/audio) on a USER message; it is nil for
	// assistant/tool/system messages. Text remains the flattened text body
	// (embedded-text resources collapse into it); Parts carries only the binary or
	// URL-referenced media that rides alongside the text. Do not mutate Parts (or a
	// Part's Data) after construction.
	Parts []Content
	// Historical preserves unattached display content across snapshot saves.
	Historical *HistoricalRecord `json:"Historical,omitempty"`
	// Legacy is private source-local verification provenance, not model history
	// or executable identity lookup. Public projections must not copy it.
	Legacy *LegacyMessageIdentity `json:"Legacy,omitempty"`
	// Replay owns occurrence-specific bindings and opaque provider replay state.
	// It is accepted atomically with this message, never as a separate sidecar.
	Replay *MessageReplay `json:"Replay,omitempty"`
	// UserPromptProvenance is explicit positive/non-principal provenance for a
	// user-role message. Unknown is deliberately not treated as principal authority.
	UserPromptProvenance UserPromptProvenance
}

// NewUserMessage constructs a user-role message.
func NewUserMessage(text string) Message {
	return Message{Role: RoleUser, Text: text}
}

// NewUserMessageWithParts constructs a user-role message carrying flattened
// text plus non-text media parts. text may be empty when parts carries the
// content; parts may be nil for a text-only message (equivalent to
// NewUserMessage).
func NewUserMessageWithParts(text string, parts []Content) Message {
	return Message{Role: RoleUser, Text: text, Parts: parts}
}

// NewSystemMessage constructs a system-role message.
func NewSystemMessage(text string) Message {
	return Message{Role: RoleSystem, Text: text}
}

// NewAssistantMessage constructs an assistant-role message carrying optional
// text and canonical tool calls.
func NewAssistantMessage(text string, calls []ToolCall) Message {
	return Message{
		Role:      RoleAssistant,
		Text:      text,
		ToolCalls: calls,
	}
}

// NewToolMessage constructs a tool-role message carrying a single tool result.
// Tool-role messages now have Parts parity with user-role messages, but the
// typed blocks live on ToolResult.Parts (not Message.Parts): Message.Parts
// stays media-only for user messages (decision #1 / Risk #4 mitigation (a)),
// and providers read ToolResult.Parts for tool results.
func NewToolMessage(result ToolResult) Message {
	r := result
	return Message{Role: RoleTool, ToolResult: &r}
}

// Conversation is the model-visible message history of a session. It is an
// entity owned by the Session aggregate; mutate it only through Session methods.
type Conversation struct {
	// Messages is the ordered history sent to the model.
	Messages []Message
}

// Append adds an independently owned message to the conversation history.
func (c *Conversation) Append(m Message) {
	c.Messages = append(c.Messages, cloneMessage(m))
}

// Len reports the number of messages in the conversation.
func (c *Conversation) Len() int {
	return len(c.Messages)
}

// ValidateToolPairing reports whether the message history is well-paired for
// provider replay: every tool-result message (RoleTool) must answer a preceding
// assistant ToolCall, and every assistant ToolCall must be answered by a
// following tool-result. It is BIDIRECTIONAL because providers reject BOTH
// shapes — an orphaned tool result (no matching tool_use/function_call above it)
// AND a dangling tool call (no result below it) draw an HTTP 400. It is pure (no
// I/O, no new deps): used by compaction to refuse emitting a history that would
// brick the session, and by ReplaceHistory as the aggregate-level guard. Canonical
// call IDs must be nonempty and unique across the entire retained history, even
// after a call has been answered; each call has exactly one final result.
//
// An empty or nil slice is trivially valid.
func ValidateToolPairing(msgs []Message) error {
	open, err := validateToolHistory(msgs)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		// Surface one dangling id deterministically for a stable error message.
		ids := make([]string, 0, len(open))
		for id := range open {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		return fmt.Errorf("dangling tool call %q (no following tool result)", ids[0])
	}
	return nil
}

// validateToolHistory allows outstanding calls while a turn is being dispatched.
func validateToolHistory(msgs []Message) (map[ToolCallID]struct{}, error) {
	open := make(map[ToolCallID]struct{})
	seen := make(map[ToolCallID]struct{})
	for i, m := range msgs {
		if err := ValidateMessageReplay(m); err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		if len(m.ToolCalls) > 0 && m.Role != RoleAssistant {
			return nil, fmt.Errorf("message %d: tool calls require assistant role", i)
		}
		if m.ToolResult != nil && m.Role != RoleTool {
			return nil, fmt.Errorf("message %d: tool result requires tool role", i)
		}
		switch m.Role {
		case RoleAssistant:
			for _, c := range m.ToolCalls {
				if c.ID == "" {
					return nil, fmt.Errorf("message %d: empty tool call ID", i)
				}
				if _, ok := seen[c.ID]; ok {
					return nil, fmt.Errorf("message %d: repeated canonical tool call ID", i)
				}
				seen[c.ID] = struct{}{}
				open[c.ID] = struct{}{}
			}
		case RoleTool:
			if m.ToolResult == nil {
				return nil, fmt.Errorf("message %d: tool-role message carries no result", i)
			}
			id := m.ToolResult.CallID
			if id == "" {
				return nil, fmt.Errorf("message %d: empty tool result call ID", i)
			}
			if _, ok := open[id]; !ok {
				return nil, fmt.Errorf("message %d: orphaned tool result (no outstanding assistant tool call)", i)
			}
			delete(open, id)
		case RoleSystem, RoleUser:
		default:
			return nil, fmt.Errorf("message %d: invalid message role", i)
		}
	}
	return open, nil
}

// CloneMessages returns an independently owned copy of all message storage,
// including arguments, results, media, audiences, and opaque replay bytes.
// A nil input returns nil.
func CloneMessages(msgs []Message) []Message {
	out := slices.Clone(msgs)
	for i := range out {
		out[i] = cloneMessage(out[i])
	}
	return out
}

func cloneMessage(m Message) Message {
	m.Legacy = m.Legacy.clone()
	if m.Historical != nil {
		h := *m.Historical
		h.Parts = cloneContents(h.Parts)
		m.Historical = &h
	}
	m.ToolCalls = slices.Clone(m.ToolCalls)
	for i := range m.ToolCalls {
		m.ToolCalls[i].Args = slices.Clone(m.ToolCalls[i].Args)
	}
	m.Parts = cloneContents(m.Parts)
	if m.ToolResult != nil {
		result := *m.ToolResult
		result.Parts = cloneContents(result.Parts)
		m.ToolResult = &result
	}
	if m.Replay != nil {
		replay := *m.Replay
		replay.Bindings = slices.Clone(replay.Bindings)
		if replay.Provider != nil {
			provider := *replay.Provider
			provider.Data = slices.Clone(provider.Data)
			replay.Provider = &provider
		}
		m.Replay = &replay
	}
	return m
}

func cloneContents(parts []Content) []Content {
	out := slices.Clone(parts)
	for i := range out {
		out[i].Data = slices.Clone(out[i].Data)
		out[i].Audience = slices.Clone(out[i].Audience)
	}
	return out
}

// ForkSnapshot returns an independently owned copy of c's history with any
// TRAILING UNANSWERED tool calls stripped, so it is tool-pairing-valid
// (ValidateToolPairing passes). It is the seam a fork:true Subagent child seeds from: at dispatch time
// the parent's trailing assistant message carries the Subagent{fork:true} call
// itself, whose tool result is recorded only AFTER dispatch returns — so a naive
// copy would end on a dangling tool_use and draw a provider HTTP 400 on the
// child's first replay. Stripping only the trailing-orphan tail preserves every
// prior turn (including fully-answered tool pairs); a conversation with no
// trailing orphan is returned cloned-but-unchanged. A turn-0 (empty, or
// just-the-fork-call) parent yields an empty snapshot — trivially pairing-valid,
// and the fork degrades to a fresh-context child, which is benign by design.
//
// A nil receiver returns nil.
func ForkSnapshot(c *Conversation) []Message {
	if c == nil {
		return nil
	}
	msgs := CloneMessages(c.Messages)
	// Find the last assistant message; collect the call IDs answered by tool-role
	// messages that FOLLOW it. Any of that assistant's ToolCalls left unanswered is
	// a trailing orphan whose presence would fail pairing — drop the whole trailing
	// assistant turn (the unanswered fork call) so the snapshot ends paired.
	lastAssistant := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return msgs
	}
	answered := make(map[ToolCallID]struct{})
	for _, m := range msgs[lastAssistant+1:] {
		if m.Role == RoleTool && m.ToolResult != nil {
			answered[m.ToolResult.CallID] = struct{}{}
		}
	}
	hasOrphan := false
	for _, call := range msgs[lastAssistant].ToolCalls {
		if _, ok := answered[call.ID]; !ok {
			hasOrphan = true
			break
		}
	}
	if !hasOrphan {
		return msgs
	}
	// Truncate to just before the trailing assistant turn (and any trailing tool
	// results that followed it, which belong to that orphaned turn). The prior
	// turns — fully paired by construction — are preserved.
	return msgs[:lastAssistant]
}

// StripProviderState returns independently owned canonical history without active
// provider replay or phase. Legacy source coordinates remain verification metadata.
// Pairing tokens and target item IDs are reconstructed at the LLM/adapter seam.
func StripProviderState(messages []Message) []Message {
	out := CloneMessages(messages)
	for i := range out {
		out[i].ProviderPhase = ""
		out[i].Replay = nil
	}
	return out
}

// Turn records one model call together with the tools it triggered. It is a
// value object summarizing a single iteration of the agent loop.
type Turn struct {
	// Index is the zero-based position of this turn in the session.
	Index int
	// Assistant is the assistant message produced by the model call.
	Assistant Message
	// Results holds the results of the tools the assistant requested.
	Results []ToolResult
	// Usage is the token accounting for this turn's model call.
	Usage Usage
}
