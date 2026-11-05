package session

import "fmt"

// validatePendingApproval proves that the ask names one exact, unanswered
// canonical occurrence. Display arguments and AskID routing grant no identity.
func validatePendingApproval(messages []Message, ask PendingAsk) error {
	if ask.Call == "" {
		return fmt.Errorf("session: pending approval has no canonical call")
	}
	lastAssistant, foundAt, matches := -1, -1, 0
	for i, m := range messages {
		if m.Role == RoleAssistant {
			lastAssistant = i
		}
		for _, c := range m.ToolCalls {
			if c.ID == ask.Call {
				matches++
				foundAt = i
				if m.Role != RoleAssistant || c.Name != ask.Tool {
					return fmt.Errorf("session: pending approval call mismatch")
				}
			}
		}
		if m.ToolResult != nil && m.ToolResult.CallID == ask.Call {
			return fmt.Errorf("session: pending approval call already answered")
		}
	}
	if matches != 1 || foundAt != lastAssistant {
		return fmt.Errorf("session: pending approval canonical occurrence unavailable")
	}
	return nil
}
