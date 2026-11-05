package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func userMessage(text string) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleUser, Text: text}
}

func userMessageWithParts(text string, parts []session.Content) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleUser, Text: text, Parts: parts}
}

func assistantMessage(text string, replay *session.ProviderReplay, calls []port.LLMToolCall) port.LLMMessage {
	return port.LLMMessage{Role: session.RoleAssistant, Text: text, Replay: replay, ToolCalls: calls}
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

func replayBlocks(t *testing.T, replay *session.ProviderReplay) []reasoningBlock {
	t.Helper()
	blocks, err := unpackReplay(replay)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}
