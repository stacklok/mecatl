package server_test

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func validateLLMToolPairing(messages []port.LLMMessage) error {
	open := make(map[session.ProviderCallID]bool)
	for _, m := range messages {
		for _, call := range m.ToolCalls {
			if call.ID == "" || open[call.ID] {
				return fmt.Errorf("empty or duplicate provider call %q", call.ID)
			}
			open[call.ID] = true
		}
		if r := m.ToolResult; r != nil {
			if !open[r.CallID] {
				return fmt.Errorf("orphan provider result %q", r.CallID)
			}
			delete(open, r.CallID)
		}
	}
	if len(open) != 0 {
		return fmt.Errorf("unanswered provider calls: %v", open)
	}
	return nil
}
