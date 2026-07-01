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
	// Reasoning is the provider's opaque reasoning REPLAY blob (e.g. OpenAI's
	// reasoning-item encrypted_content, or Anthropic's (thinking,signature) pair),
	// replayed back verbatim on subsequent calls and never interpreted or displayed
	// by the harness. The STRUCTURE is provider-neutral (one opaque blob per
	// message); the CONTENTS are provider-private — each adapter packs/unpacks its
	// own wire shape, so the domain value object stays a bare string (do NOT widen
	// it). It is distinct from the human-readable reasoning SUMMARY surfaced via
	// reasoning.delta events for display: that prose is never stored here.
	Reasoning string
	// ProviderPhase is the OpenAI Responses API's opaque phase marker on an
	// assistant message ("commentary" for intermediate output, "final_answer" for
	// the final answer), replayed back verbatim on subsequent calls and never
	// interpreted or displayed by the harness. For store:false manual-replay apps
	// OpenAI requires preserving and resending it, or GPT-5.x models treat preambles
	// as final answers / stop early. The STRUCTURE is provider-neutral (one opaque
	// phase string per message); the CONTENTS are provider-private (the harness
	// never branches on or validates the value — do NOT widen it). Empty string
	// means "no phase". Same discipline as Reasoning. (Named ProviderPhase, not
	// Phase, to disambiguate from the governance/hook-lifecycle Phase concept, which
	// is interpreted — the opposite contract.)
	ProviderPhase string
	// Parts carries non-text media (image/audio) on a USER message; it is nil for
	// assistant/tool/system messages. Text remains the flattened text body
	// (embedded-text resources collapse into it); Parts carries only the binary or
	// URL-referenced media that rides alongside the text. Do not mutate Parts (or a
	// Part's Data) after construction.
	Parts []Content
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
// text, reasoning, and tool calls.
func NewAssistantMessage(text, reasoning string, calls []ToolCall) Message {
	return Message{
		Role:      RoleAssistant,
		Text:      text,
		Reasoning: reasoning,
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

// Append adds a message to the conversation history.
func (c *Conversation) Append(m Message) {
	c.Messages = append(c.Messages, m)
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
// brick the session, and by ReplaceHistory as the aggregate-level guard.
//
// An empty or nil slice is trivially valid.
func ValidateToolPairing(msgs []Message) error {
	// Track which call IDs have been opened by an assistant message and not yet
	// answered. Order matters: a result must follow its call, not precede it.
	open := make(map[ToolCallID]struct{})
	for i, m := range msgs {
		switch m.Role {
		case RoleAssistant:
			for _, c := range m.ToolCalls {
				open[c.ID] = struct{}{}
			}
		case RoleTool:
			if m.ToolResult == nil {
				return fmt.Errorf("message %d: tool-role message carries no result", i)
			}
			id := m.ToolResult.CallID
			if _, ok := open[id]; !ok {
				return fmt.Errorf("message %d: orphaned tool result for call %q (no preceding assistant tool call)", i, id)
			}
			delete(open, id)
		}
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

// CloneMessages returns a copy of msgs with a FRESH backing array, so the
// returned slice and the original never alias: an Append to one cannot grow into
// the other's storage. The COPY IS SHALLOW per element, which is sound because a
// Message is an IMMUTABLE value object — its slice/pointer fields (ToolCalls,
// ToolResult, Parts) are never mutated in place after construction (the
// constructors build them once; the only mutation of a Conversation is Append,
// which adds whole new Messages, never edits an existing one's fields). A fork
// child therefore shares the parent's per-message tool-call / part data safely:
// it only appends new Messages, it never rewrites a copied one. A nil input
// returns nil.
func CloneMessages(msgs []Message) []Message {
	return slices.Clone(msgs)
}

// ForkSnapshot returns a deep-enough copy (CloneMessages — fresh backing array,
// immutable-Message elements) of c's history with any TRAILING UNANSWERED tool
// calls stripped, so the result is always tool-pairing-valid (ValidateToolPairing
// passes). It is the seam a fork:true Subagent child seeds from: at dispatch time
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
