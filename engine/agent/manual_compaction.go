package agent

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
)

// ManualCompactionResult describes one out-of-band compaction attempt.
// Archive and Summary are populated only when Changed is true and are suitable
// for the existing compaction notice and durable archive events.
type ManualCompactionResult struct {
	Changed bool
	Archive []session.Message
	Summary string
}

// budgetedCompactor is the optional internal seam used only by automatic
// compaction. Manual compaction continues to use the compactor's configured
// Compact behaviour.
type budgetedCompactor interface {
	compactToBudget(context.Context, *session.Conversation, int) ([]session.Message, string, session.AuxiliaryUsage, error)
}

// compactionCandidate runs a compactor and applies the one candidate-admission
// contract shared by automatic and manual compaction. Callers choose input
// ownership: the hot automatic path passes the live immutable conversation,
// while CompactSession passes a deep copy to isolate the aggregate from an
// out-of-band compactor.
func (e *Engine) compactionCandidate(ctx context.Context, input *session.Conversation, budget int) (ManualCompactionResult, []session.Message, session.AuxiliaryUsage, error) {
	original := input.Messages
	var candidate []session.Message
	var summary string
	var usage session.AuxiliaryUsage
	var err error
	if c, ok := e.deps.Compactor.(budgetedCompactor); ok && budget > 0 {
		candidate, summary, usage, err = c.compactToBudget(ctx, input, budget)
	} else {
		candidate, summary, usage, err = e.deps.Compactor.Compact(ctx, input)
	}
	if err != nil {
		return ManualCompactionResult{}, nil, usage, err
	}
	if len(candidate) == 0 {
		return ManualCompactionResult{}, nil, usage, nil
	}
	if err := session.ValidateToolPairing(candidate); err != nil {
		return ManualCompactionResult{}, nil, usage, fmt.Errorf("%w: %v", ErrCompactionWouldOrphan, err)
	}
	if e.deps.TokenCounter.CountMessages(candidate) >= e.deps.TokenCounter.CountMessages(original) {
		return ManualCompactionResult{}, nil, usage, nil
	}
	return ManualCompactionResult{Changed: true, Summary: summary}, candidate, usage, nil
}

func cloneCompactionMessages(messages []session.Message) []session.Message {
	out := make([]session.Message, len(messages))
	for i, message := range messages {
		out[i] = message
		out[i].ToolCalls = append([]session.ToolCall(nil), message.ToolCalls...)
		for j := range out[i].ToolCalls {
			out[i].ToolCalls[j].Args = append([]byte(nil), message.ToolCalls[j].Args...)
		}
		out[i].Parts = cloneCompactionParts(message.Parts)
		if message.ToolResult != nil {
			result := *message.ToolResult
			result.Parts = cloneCompactionParts(message.ToolResult.Parts)
			out[i].ToolResult = &result
		}
	}
	return out
}

func cloneCompactionParts(parts []session.Content) []session.Content {
	out := append([]session.Content(nil), parts...)
	for i := range out {
		out[i].Data = append([]byte(nil), parts[i].Data...)
		out[i].Audience = append([]string(nil), parts[i].Audience...)
	}
	return out
}

// CompactSession runs the configured Compactor once at a session turn boundary,
// regardless of the automatic compaction threshold. It creates no conversation
// turn. Empty, identical, and non-reducing candidates are successful no-ops.
// Invalid tool pairing and compactor failures are returned without mutation.
func (e *Engine) CompactSession(ctx context.Context, sess *session.Session) (ManualCompactionResult, error) {
	if sess == nil {
		return ManualCompactionResult{}, fmt.Errorf("agent: compact session: nil session")
	}
	if sess.State == session.StateRunning || sess.State == session.StateAwaiting {
		return ManualCompactionResult{}, fmt.Errorf("%w: CompactSession from %q", session.ErrIllegalTransition, sess.State)
	}

	original := sess.Conversation.Messages
	input := &session.Conversation{Messages: cloneCompactionMessages(original)}
	result, candidate, usage, err := e.compactionCandidate(ctx, input, 0)
	// The caller already owns this aggregate at a legal turn boundary, so returned
	// accounting is recorded synchronously even when compaction itself fails.
	sess.RecordAuxiliaryUsage(usage)
	if err != nil || !result.Changed {
		return result, err
	}
	if err := sess.ReplaceHistoryAtBoundary(candidate); err != nil {
		return ManualCompactionResult{}, err
	}
	// Message elements are immutable, so the replaced backing slice remains a
	// safe archive after the aggregate takes ownership of the candidate.
	result.Archive = original
	return result, nil
}
