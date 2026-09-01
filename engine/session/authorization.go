package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxAuthorizationValueBytes = 256

// ValidAuthorizationID reports whether value is a canonical public
// authorization correlation identifier. Transport adapters must apply this at
// their trust boundary before looking up an authorization.
func ValidAuthorizationID(value string) bool {
	return validAuthorizationID(value)
}

// AuthorizationBinding is opaque, private correlation data that lets the
// authorization owner verify that a restored transaction still has the same
// external binding. The session aggregate stores and compares no structure
// within it. Callers must not present or log it.
type AuthorizationBinding string

// ExternalAuthorization identifies one out-of-band authorization transaction.
// DisplayName is an optional bounded human-facing authority or service label.
type ExternalAuthorization struct {
	ID          string
	DisplayName string
	Binding     AuthorizationBinding
	ExpiresAt   time.Time
}

// PendingAuthorization is the durable continuation state for one externally
// authorized tool call and its deferred siblings.
type PendingAuthorization struct {
	Authorization ExternalAuthorization
	Call          ToolCall
	Deferred      []ToolCall
}

// Clone returns an independent copy of p, including all raw tool arguments.
func (p PendingAuthorization) Clone() PendingAuthorization {
	out := p
	out.Call = cloneAuthorizationCall(p.Call)
	out.Deferred = make([]ToolCall, len(p.Deferred))
	for i, call := range p.Deferred {
		out.Deferred[i] = cloneAuthorizationCall(call)
	}
	return out
}

func cloneAuthorizationCall(call ToolCall) ToolCall {
	call.Args = append(call.Args[:0:0], call.Args...)
	return call
}

// PauseForAuthorization records an external authorization park after the
// permission and PreToolUse gates. Call is the complete effective call after
// those gates; its identity must match the original unpaired call in the
// trailing assistant turn. Deferred holds the complete, exact later siblings in
// order; those calls have not passed PreToolUse and may not differ.
func (s *Session) PauseForAuthorization(pending PendingAuthorization) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: PauseForAuthorization from %q", ErrIllegalTransition, s.State)
	}
	if s.pending != nil {
		return fmt.Errorf("%w: permission ask already pending", ErrIllegalTransition)
	}
	if err := validatePendingAuthorization(s.Conversation.Messages, pending); err != nil {
		return fmt.Errorf("session: invalid pending authorization: %w", err)
	}
	pendingCopy := pending.Clone()
	s.pendingAuthorization = &pendingCopy
	s.State = StateAuthorizing
	return nil
}

// PendingAuthorization returns an independent copy of the parked continuation
// only while the session is StateAuthorizing.
func (s *Session) PendingAuthorization() (PendingAuthorization, bool) {
	if s.State != StateAuthorizing || s.pendingAuthorization == nil {
		return PendingAuthorization{}, false
	}
	return s.pendingAuthorization.Clone(), true
}

// ClaimAuthorization consumes the durable authorization claim and returns the
// exact call continuation.
func (s *Session) ClaimAuthorization() (PendingAuthorization, error) {
	if err := s.ValidateAuthorizationState(); err != nil {
		return PendingAuthorization{}, err
	}
	if s.State != StateAuthorizing {
		return PendingAuthorization{}, ErrNoPendingAuthorization
	}
	pending := s.pendingAuthorization.Clone()
	s.pendingAuthorization = nil
	s.State = StateRunning
	return pending, nil
}

// AbortAuthorization leaves authorizing with deterministic, ordered error
// results for the parked call and every deferred sibling. reason is a closed
// harness token; unknown values map to the fixed failed message.
func (s *Session) AbortAuthorization(reason string) ([]ToolResult, error) {
	return s.resolveAuthorization(reason)
}

// InterruptAuthorization resolves a restored authorization whose original
// runtime transaction was lost on process interruption.
func (s *Session) InterruptAuthorization() ([]ToolResult, error) {
	return s.resolveAuthorization("interrupted")
}

func (s *Session) resolveAuthorization(reason string) ([]ToolResult, error) {
	if err := s.ValidateAuthorizationState(); err != nil {
		return nil, err
	}
	if s.State != StateAuthorizing {
		return nil, ErrNoPendingAuthorization
	}
	message := authorizationAbortMessage(reason)
	pending := s.pendingAuthorization.Clone()
	results := make([]ToolResult, 0, len(pending.Deferred)+1)
	results = append(results, NewToolError(pending.Call.ID, message))
	for _, call := range pending.Deferred {
		results = append(results, NewToolError(call.ID, "authorization deferred sibling was not executed"))
	}
	s.pendingAuthorization = nil
	s.State = StateRunning
	return results, nil
}

func authorizationAbortMessage(reason string) string {
	switch reason {
	case "denied":
		return "external authorization denied"
	case "cancelled":
		return "external authorization cancelled"
	case "expired":
		return "external authorization expired"
	case "interrupted":
		return "external authorization interrupted"
	case "unavailable":
		return "external authorization unavailable"
	case "failed":
		fallthrough
	default:
		return "external authorization failed"
	}
}

// ValidateAuthorizationState verifies the lifecycle/pending correspondence and,
// while authorizing, the sole permitted temporary tool-pairing exception.
func (s *Session) ValidateAuthorizationState() error {
	switch s.State {
	case StateAuthorizing:
		if s.pending != nil || s.pendingAuthorization == nil {
			return fmt.Errorf("session: authorizing state/pending mismatch")
		}
		return validatePendingAuthorization(s.Conversation.Messages, *s.pendingAuthorization)
	default:
		if s.pendingAuthorization != nil {
			return fmt.Errorf("session: authorization pending outside authorizing state")
		}
		if s.State == StateAwaiting && s.pending == nil {
			return fmt.Errorf("session: awaiting state/pending mismatch")
		}
		if s.State != StateAwaiting && s.pending != nil {
			return fmt.Errorf("session: permission ask pending outside awaiting state")
		}
		return nil
	}
}

//nolint:gocyclo // Validation intentionally follows the history pairing state machine.
func validatePendingAuthorization(messages []Message, pending PendingAuthorization) error {
	if !validAuthorizationID(pending.Authorization.ID) {
		return fmt.Errorf("invalid authorization ID")
	}
	if pending.Authorization.DisplayName != "" && !validAuthorizationDisplayName(pending.Authorization.DisplayName) {
		return fmt.Errorf("invalid authorization display name")
	}
	if !validAuthorizationBinding(pending.Authorization.Binding) {
		return fmt.Errorf("invalid authorization binding")
	}
	if pending.Authorization.ExpiresAt.IsZero() {
		return fmt.Errorf("invalid expiry")
	}
	if !validAuthorizationCall(pending.Call) {
		return fmt.Errorf("invalid pending call")
	}
	seen := map[ToolCallID]struct{}{pending.Call.ID: {}}
	for _, call := range pending.Deferred {
		if !validAuthorizationCall(call) {
			return fmt.Errorf("invalid deferred call")
		}
		if _, exists := seen[call.ID]; exists {
			return fmt.Errorf("duplicate call ID %q", call.ID)
		}
		seen[call.ID] = struct{}{}
	}

	lastAssistant := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return fmt.Errorf("pending call has no trailing assistant turn")
	}
	calls := messages[lastAssistant].ToolCalls
	if err := ValidateToolPairing(messages[:lastAssistant]); err != nil {
		return fmt.Errorf("history before trailing assistant turn is not paired: %w", err)
	}
	callIDs := make(map[ToolCallID]struct{}, len(calls))
	for _, call := range calls {
		if !validAuthorizationCall(call) {
			return fmt.Errorf("invalid trailing assistant call")
		}
		if _, duplicate := callIDs[call.ID]; duplicate {
			return fmt.Errorf("duplicate trailing assistant call ID %q", call.ID)
		}
		callIDs[call.ID] = struct{}{}
	}
	pendingAt := -1
	for i, call := range calls {
		if call.ID == pending.Call.ID {
			if !sameAuthorizationCallIdentity(call, pending.Call) {
				return fmt.Errorf("pending call does not match trailing assistant call")
			}
			pendingAt = i
		}
	}
	if pendingAt < 0 {
		return fmt.Errorf("pending call not found in trailing assistant turn")
	}
	trailing := calls[pendingAt+1:]
	if len(trailing) != len(pending.Deferred) {
		return fmt.Errorf("deferred calls do not match trailing assistant suffix")
	}
	for i := range trailing {
		if !sameDeferredAuthorizationCall(trailing[i], pending.Deferred[i]) {
			return fmt.Errorf("deferred calls do not match trailing assistant suffix")
		}
	}

	answered := make(map[ToolCallID]struct{})
	for _, message := range messages[lastAssistant+1:] {
		if message.Role != RoleTool || message.ToolResult == nil {
			return fmt.Errorf("non-tool message follows trailing assistant turn")
		}
		id := message.ToolResult.CallID
		if _, known := callIDs[id]; !known {
			return fmt.Errorf("result for unknown call %q follows trailing assistant turn", id)
		}
		if _, duplicate := answered[id]; duplicate {
			return fmt.Errorf("duplicate result for call %q", id)
		}
		answered[id] = struct{}{}
	}
	for i, call := range calls {
		_, hasResult := answered[call.ID]
		switch {
		case i < pendingAt && !hasResult:
			return fmt.Errorf("executed call %q lacks result", call.ID)
		case i >= pendingAt && hasResult:
			return fmt.Errorf("parked call %q already has result", call.ID)
		}
	}
	return nil
}

func sameAuthorizationCallIdentity(original, effective ToolCall) bool {
	return original.ID == effective.ID && original.Name == effective.Name && original.ItemID == effective.ItemID
}

func sameDeferredAuthorizationCall(original, pending ToolCall) bool {
	return sameAuthorizationCallIdentity(original, pending) && equivalentAuthorizationArgs(original.Args, pending.Args)
}

func equivalentAuthorizationArgs(original, pending []byte) bool {
	if bytes.Equal(original, pending) {
		return true
	}
	originalCanonical, originalErr := json.Marshal(json.RawMessage(original))
	pendingCanonical, pendingErr := json.Marshal(json.RawMessage(pending))
	if originalErr != nil || pendingErr != nil {
		return false
	}
	return bytes.Equal(originalCanonical, pendingCanonical)
}

func validAuthorizationCall(call ToolCall) bool {
	return validAuthorizationID(string(call.ID)) && strings.TrimSpace(call.Name) != ""
}

func validAuthorizationBinding(binding AuthorizationBinding) bool {
	value := string(binding)
	return value != "" && len(value) <= maxAuthorizationValueBytes && utf8.ValidString(value)
}

func validAuthorizationDisplayName(value string) bool {
	return validAuthorizationValue(value) && strings.TrimSpace(value) == value
}

func validAuthorizationValue(value string) bool {
	if value == "" || len(value) > maxAuthorizationValueBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validAuthorizationID(value string) bool {
	if !validAuthorizationValue(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
