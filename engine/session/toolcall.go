package session

import (
	"encoding/json"
	"fmt"
)

// ToolCallID uniquely identifies a tool invocation within a session. It is
// produced by the LLM and used to pair a ToolCall with its ToolResult.
type ToolCallID string

// ToolCall is an immutable value object: a request from the model to invoke a
// named tool with tool-specific arguments. It is produced by the LLM provider
// and consumed by both the Tooling and Governance contexts. Construct it with
// NewToolCall; it carries no mutating methods.
//
// JSON tags use capitalized keys (json:"ID", json:"Name", json:"Args") to
// preserve the wire format that predates JSON tagging — old snapshots serialized
// these fields as "ID", "Name", "Args" by Go's default reflection rule, so the
// tags below preserve exact backward compatibility. ItemID uses a lowercase tag
// following the ProviderPhase precedent (additive, omitempty).
type ToolCall struct {
	// ID pairs this call with its ToolResult.
	ID ToolCallID `json:"ID"`
	// Name is the tool name as registered in the catalog.
	Name string `json:"Name"`
	// Args is the raw, tool-specific argument payload, validated against the
	// tool's JSON schema by the Tool itself.
	Args json.RawMessage `json:"Args"`
	// ItemID is the provider-assigned item-level unique identifier for this
	// function_call output item (e.g. the "id" field in the OpenAI Responses
	// API, distinct from ID which carries "call_id"). Carried opaquely — same
	// discipline as Message.Reasoning and Message.ProviderPhase — so the adapter
	// can round-trip it for store:false stateless multi-turn replay. Empty string
	// means "no item id" and the field is wire-omitted on replay. The STRUCTURE
	// is provider-neutral; the CONTENTS are provider-private — do NOT interpret
	// or validate this value in domain code.
	ItemID string `json:"item_id,omitempty"`
}

// NewToolCall constructs a ToolCall value object.
func NewToolCall(id ToolCallID, name string, args json.RawMessage) ToolCall {
	return ToolCall{ID: id, Name: name, Args: args}
}

// ParseArgs unmarshals a ToolCall's JSON arguments into dst. It is the single
// canonical implementation of the "decode a tool call's args, fail with a
// model-facing string" mechanic that the agent loop and the adapter-layer tools
// both repeat. It lives on the domain ToolCall (which both layers already import)
// so neither has to depend on the other.
//
// An empty payload (no args) is treated as an empty object: dst is left at its
// zero value and ok=true, so a tool with all-optional arguments works without the
// model having to send an explicit "{}". On malformed JSON it returns a
// model-facing error string (not a Go error) and ok=false; on success it returns
// "" and true. The returned msg is intended to be fed straight back to the model
// via NewToolError, so callers may prefix it with the tool name.
func ParseArgs(call ToolCall, dst any) (msg string, ok bool) {
	if len(call.Args) == 0 {
		return "", true
	}
	if err := json.Unmarshal(call.Args, dst); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), false
	}
	return "", true
}

// ToolResult is an immutable value object: the outcome of executing a ToolCall,
// paired to it by CallID. Construct it with NewToolResult or NewToolError; it
// carries no mutating methods.
//
// Content-vs-Parts precedence: both Content and Parts may be present. Content
// is the default model-facing string (always set by the legacy constructors);
// Parts carries typed tool-result blocks (text/image/audio/resource). Consumers
// prefer Parts when non-empty, falling back to Content. A legacy/empty-Parts
// result is byte-identical to the pre-Parts shape.
type ToolResult struct {
	// CallID is the ID of the ToolCall this result answers.
	CallID ToolCallID
	// Content is the result body, already token-shaped/truncated by the tool.
	Content string
	// IsError reports whether the tool failed; an error result is still fed
	// back to the model so it can recover.
	IsError bool
	// Parts carries typed tool-result blocks (text/image/audio/resource). Empty
	// for the legacy string-only path; when non-empty, consumers prefer Parts
	// over Content. Distinct from Message.Parts (which is media-only for user
	// messages) — providers read ToolResult.Parts, not Message.Parts, for tool
	// results.
	Parts []Content `json:"Parts,omitempty"`
}

// NewToolResult constructs a successful ToolResult for the given call.
func NewToolResult(callID ToolCallID, content string) ToolResult {
	return ToolResult{CallID: callID, Content: content}
}

// NewToolError constructs an error ToolResult for the given call.
func NewToolError(callID ToolCallID, content string) ToolResult {
	return ToolResult{CallID: callID, Content: content, IsError: true}
}

// NewToolResultWithParts constructs a successful ToolResult carrying typed blocks
// alongside the default model-facing Content string. content is the plain
// model-facing summary; parts are the typed tool-result blocks. Consumers prefer
// Parts when non-empty.
func NewToolResultWithParts(callID ToolCallID, content string, parts []Content) ToolResult {
	return ToolResult{CallID: callID, Content: content, Parts: parts}
}
