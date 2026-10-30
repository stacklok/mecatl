package session

import "fmt"

// ValidateIdentityPersistence validates canonical history and continuation authority
// before persistence.
func (s *Session) ValidateIdentityPersistence() error {
	if _, err := validateToolHistory(s.Conversation.Messages); err != nil {
		return fmt.Errorf("session: invalid persisted canonical history: %w", err)
	}
	return s.ValidateAuthorizationState()
}
