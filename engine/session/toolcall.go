package session

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ToolCallID is the provider-private pairing identifier used for exact replay.
// It may be reused after its preceding invocation has completed.
type ToolCallID string

// InvocationID identifies one accepted invocation independently of provider replay.
type InvocationID string

// NewInvocationID mints an opaque, globally unique invocation identity.
func NewInvocationID() InvocationID {
	return InvocationID("tc_" + string(NewIncarnationID())[4:])
}

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
	// InvocationID is assigned by the engine once, before accepting the model turn.
	InvocationID InvocationID `json:"invocation_id,omitempty"`
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

// ValidateIncomingCalls rejects an entire provider turn if any pairing ID is
// duplicated or still awaiting a result in the retained conversation.
func (s *Session) ValidateIncomingCalls(calls []ToolCall) error {
	open := make(map[ToolCallID]struct{})
	for _, m := range s.Conversation.Messages {
		for _, c := range m.ToolCalls {
			if _, exists := open[c.ID]; exists {
				return fmt.Errorf("duplicate outstanding provider call ID %q", c.ID)
			}
			open[c.ID] = struct{}{}
		}
		if m.ToolResult != nil {
			if _, exists := open[m.ToolResult.CallID]; !exists {
				return fmt.Errorf("orphaned provider result ID %q", m.ToolResult.CallID)
			}
			delete(open, m.ToolResult.CallID)
		}
	}
	for _, c := range calls {
		if c.ID == "" {
			return fmt.Errorf("empty provider call ID")
		}
		if _, exists := open[c.ID]; exists {
			return fmt.Errorf("duplicate outstanding provider call ID %q", c.ID)
		}
		open[c.ID] = struct{}{}
	}
	return nil
}

// RestoreInvocationIDs translates retained legacy snapshot occurrences in memory.
// Pending references must name a unique outstanding occurrence; never guess one.
func (s *Session) RestoreInvocationIDs(ask *PendingAsk, authorization *PendingAuthorization) error {
	return s.restoreInvocationIDs(ask, authorization, "legacy_tc_", "mecatl.tool-call/legacy-v1\x00")
}

// RestoreEventInvocationIDs uses event-log occurrence anchors, which must never
// be mistaken for snapshot occurrence anchors in mixed-history projections.
func (s *Session) RestoreEventInvocationIDs(ask *PendingAsk) error {
	return s.restoreInvocationIDs(ask, nil, "legacy_event_tc_", "mecatl.tool-call/event-v1\x00")
}

func (s *Session) restoreInvocationIDs(ask *PendingAsk, authorization *PendingAuthorization, prefix, domain string) error {
	open := make(map[ToolCallID]InvocationID)
	var ordinal uint64
	// Stored snapshots and forked histories may share immutable message values.
	// Translation replaces owned copies, never mutates their shared call/result data.
	s.Conversation.Messages = slices.Clone(s.Conversation.Messages)
	for i := range s.Conversation.Messages {
		m := &s.Conversation.Messages[i]
		m.ToolCalls = slices.Clone(m.ToolCalls)
		if m.ToolResult != nil {
			result := *m.ToolResult
			m.ToolResult = &result
		}
		for j := range m.ToolCalls {
			c := &m.ToolCalls[j]
			_, duplicate := open[c.ID]
			ordinal++
			if c.InvocationID == "" {
				c.InvocationID = legacyInvocationID(s, ordinal, prefix, domain)
			}
			if duplicate {
				open[c.ID] = "" // No result or actionable continuation can choose an occurrence.
			} else {
				open[c.ID] = c.InvocationID
			}
		}
		if m.ToolResult != nil {
			if err := pairRestoredResult(m.ToolResult, open); err != nil {
				return err
			}
		}
	}
	if ask != nil && ask.Call != "" {
		id, ok := open[ask.Call]
		if ok && id != "" {
			if ask.InvocationID != "" && ask.InvocationID != id {
				return fmt.Errorf("conflicting pending ask call ID %q", ask.Call)
			}
			ask.InvocationID = id
		}
	}
	if authorization != nil {
		return restoreAuthorizationCalls(authorization, open)
	}
	return nil
}

func legacyInvocationID(s *Session, ordinal uint64, prefix, domain string) InvocationID {
	var frame [8]byte
	binary.BigEndian.PutUint64(frame[:], ordinal)
	h := sha256.New()
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write([]byte(s.ID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(s.Incarnation()))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(frame[:])
	return InvocationID(prefix + hex.EncodeToString(h.Sum(nil)))
}

func pairRestoredResult(result *ToolResult, open map[ToolCallID]InvocationID) error {
	id, exists := open[result.CallID]
	if !exists {
		return nil
	}
	if id == "" && result.InvocationID != "" {
		return fmt.Errorf("ambiguous result for provider call ID %q", result.CallID)
	}
	if id != "" {
		if result.InvocationID != "" && result.InvocationID != id {
			// Log and snapshot occurrence anchors differ; private pairing alone
			// cannot identify a log call as the resumed snapshot occurrence.
			if !strings.HasPrefix(string(id), "legacy_event_tc_") || !strings.HasPrefix(string(result.InvocationID), "legacy_tc_") {
				return fmt.Errorf("conflicting result for provider call ID %q", result.CallID)
			}
		} else {
			result.InvocationID = id
		}
	}
	delete(open, result.CallID)
	return nil
}

func restoreAuthorizationCalls(authorization *PendingAuthorization, open map[ToolCallID]InvocationID) error {
	for _, c := range append([]ToolCall{authorization.Call}, authorization.Deferred...) {
		id, ok := open[c.ID]
		if !ok || c.InvocationID != "" && c.InvocationID != id {
			return fmt.Errorf("ambiguous pending authorization call ID %q", c.ID)
		}
		if c.ID == authorization.Call.ID {
			authorization.Call.InvocationID = id
		} else {
			for j := range authorization.Deferred {
				if authorization.Deferred[j].ID == c.ID {
					authorization.Deferred[j].InvocationID = id
				}
			}
		}
	}
	return nil
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
	// InvocationID identifies the answered occurrence; CallID remains private replay data.
	InvocationID InvocationID `json:"invocation_id,omitempty"`
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
