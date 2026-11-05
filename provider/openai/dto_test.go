package openai

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func userMessage(text string) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleUser, Text: text}
}

func userMessageWithParts(text string, parts []session.Content) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleUser, Text: text, Parts: parts}
}

func assistantMessage(text, reasoning string, calls []port.LLMToolCall) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleAssistant, Text: text, ToolCalls: calls,
		Replay: packReplay(responsesReplay{Reasoning: unpackReasoningItems(reasoning)})}
}

func toolCall(id session.ProviderCallID, name string, args json.RawMessage) port.LLMToolCall {
	return port.LLMToolCall{ID: id, Name: name, Args: args}
}

func toolResult(id session.ProviderCallID, text string) port.LLMToolResult {
	return port.LLMToolResult{CallID: id, Content: text}
}

func toolResultWithParts(id session.ProviderCallID, text string, parts []session.Content) port.LLMToolResult {
	return port.LLMToolResult{CallID: id, Content: text, Parts: parts}
}

func toolMessage(result port.LLMToolResult) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleTool, ToolResult: &result}
}

func setReplayPhase(m *port.LLMMessage, phase string) {
	env, _ := unpackReplay(m.Replay)
	env.Phase = phase
	m.Replay = packReplay(env)
	m.ProviderPhase = phase
}

func replayReasoning(replay *session.ProviderReplay) []reasoningItem {
	env, _ := unpackReplay(replay)
	return env.Reasoning
}

// The old adapter's packed string fixtures remain useful for specifying the
// exact legacy-to-opaque encoding boundary. These helpers are test-only; live
// requests consume only ProviderReplay, never legacy Message fields.
type reasoningEnvelope struct {
	V     int             `json:"v"`
	Items []reasoningItem `json:"items"`
}

func packReasoningItems(items []reasoningItem) string {
	if len(items) == 0 {
		return ""
	}
	data, _ := json.Marshal(reasoningEnvelope{V: 1, Items: items})
	return string(data)
}

func unpackReasoningItems(blob string) []reasoningItem {
	var env reasoningEnvelope
	if json.Unmarshal([]byte(blob), &env) != nil || env.V != 1 || len(env.Items) == 0 {
		return nil
	}
	for _, item := range env.Items {
		if item.ID == "" || item.Blob == "" || item.After < 0 {
			return nil
		}
	}
	return env.Items
}
