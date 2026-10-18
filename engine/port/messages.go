package port

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
)

// LLMToolCall is a provider-facing call, not an executable domain invocation.
// The agent converts it into a session.ToolCall with a new canonical ID only after
// validating the complete provider batch.
type LLMToolCall struct {
	ID     session.ProviderCallID
	Name   string
	Args   json.RawMessage
	ItemID session.ProviderItemID
}

// LLMToolResult answers the exact provider pairing token for its occurrence.
type LLMToolResult struct {
	CallID  session.ProviderCallID
	Content string
	IsError bool
	Parts   []session.Content
}

// LLMMessage is an independently owned provider-facing projection. Replay is
// adapter-owned opaque data; canonical occurrence bindings stay in Session.
type LLMMessage struct {
	Role                 session.Role
	Text                 string
	ToolCalls            []LLMToolCall
	ToolResult           *LLMToolResult
	Parts                []session.Content
	ProviderPhase        string
	UserPromptProvenance session.UserPromptProvenance
	Replay               *session.ProviderReplay
}

// ProjectMessages builds independently owned LLM request history from canonical
// session messages, restoring each call/result's provider pairing token. Both live
// and reloaded sessions use this boundary when constructing their next request.
//
// It projects canonical history only; it is not a legacy identity
// migration. The caller must supply canonical IDs even when Replay is nil.
// Nil carriers explicitly denote replay-free canonical history, for which stable
// provider pairing tokens are synthesized at this boundary. Present carriers
// must bind every call. Exact tokens may be reused after an occurrence completes.
// Private payloads are copied without interpretation, and failure returns no
// partial projection. Calls must have exactly one subsequent final result.
func ProjectMessages(messages []session.Message) ([]LLMMessage, error) {
	if messages == nil {
		return nil, nil
	}
	if err := session.ValidateToolPairing(messages); err != nil {
		return nil, err
	}
	out := make([]LLMMessage, len(messages))
	open := make(map[session.ToolCallID]session.ProviderCallID)
	providerOpen := make(map[session.ProviderCallID]bool)
	for i, m := range session.CloneMessages(messages) {
		projected := LLMMessage{
			Role: m.Role, Text: m.Text, Parts: m.Parts,
			ProviderPhase: m.ProviderPhase, UserPromptProvenance: m.UserPromptProvenance,
		}
		bindings := make(map[session.ToolCallID]session.ToolCallBinding)
		if m.Replay != nil {
			projected.Replay = m.Replay.Provider
			for _, binding := range m.Replay.Bindings {
				bindings[binding.CallID] = binding
			}
		}
		if m.ToolCalls != nil {
			projected.ToolCalls = make([]LLMToolCall, len(m.ToolCalls))
		}
		for j, call := range m.ToolCalls {
			binding := bindings[call.ID]
			if m.Replay == nil {
				binding.PairingID = session.ProviderCallID("engine_" + string(call.ID))
			}
			if providerOpen[binding.PairingID] {
				return nil, fmt.Errorf("message %d: provider pairing ID is still outstanding", i)
			}
			open[call.ID] = binding.PairingID
			providerOpen[binding.PairingID] = true
			projected.ToolCalls[j] = LLMToolCall{ID: binding.PairingID, Name: call.Name, Args: call.Args, ItemID: binding.ItemID}
		}
		if m.Role == session.RoleTool {
			result := m.ToolResult
			pairingID := open[result.CallID]
			projected.ToolResult = &LLMToolResult{CallID: pairingID, Content: result.Content, IsError: result.IsError, Parts: result.Parts}
			delete(open, result.CallID)
			delete(providerOpen, pairingID)
		}
		out[i] = projected
	}
	return out, nil
}
