package session

import (
	"slices"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// activeToolResponse tracks only the latest response's call membership and pairing,
// not historical joins. It and its call entries are immutable: each successful
// tool addition replaces the call slice, so shallow admission copies and pinned
// snapshots never share mutable state. Replacement costs O(calls) scratch; call
// count is bounded by the validated response's payload/collection limits. Edges
// borrow the privately owned invocation DTO, bounded by its schema and codec
// limits, without copying arguments or prerequisite storage on replacement.
// A later outcome or successor run drops this active bookkeeping. Ordinary
// endings require the scalar unsettled count to be zero; bulk endings retain the
// immutable pairing state without manufacturing individual dispositions.
type activeToolResponse struct {
	request ModelRequestID
	calls   []activeToolCall
}

// activeToolCall borrows at most one immutable invocation and records settlement
// by any explicit disposition category. Its slice position establishes membership.
type activeToolCall struct {
	invocation *sessionformat.ToolInvocation
	settled    bool
}

// toolCall returns only a member of the matching response in the active run.
// Reconstructing a scoped identity is not sufficient evidence of membership.
func (s lifecycleState) toolCall(id RequestedCallID) (activeToolCall, bool) {
	if s.activeRun == (RunID{}) || id.requestID.runID != s.activeRun || s.tools == nil || id.requestID != s.tools.request || id.ordinal == 0 || id.ordinal > uint64(len(s.tools.calls)) {
		return activeToolCall{}, false
	}
	return s.tools.calls[id.ordinal-1], true
}

// replaceToolCall publishes fresh candidate-owned storage after all checks pass.
func (s *lifecycleState) replaceToolCall(id RequestedCallID, call activeToolCall) {
	calls := slices.Clone(s.tools.calls)
	calls[id.ordinal-1] = call
	s.tools = &activeToolResponse{request: s.tools.request, calls: calls}
}

// admitToolInvocation requires accepted response membership but not prior
// prerequisite invocation. Payload validation already proved distinct backward
// same-response components, so every prerequisite is also a response member.
func (a *admissionContext) admitToolInvocation(scope historyid.HistoryID, v *sessionformat.ToolInvocation) error {
	id := RequestedCallID{requestID: requestID(scope, v.RunID, v.RequestID), ordinal: v.CallID}
	call, ok := a.candidate.toolCall(id)
	if _, accepted := a.source.toolCall(id); !ok || !accepted || call.invocation != nil || call.settled {
		return ErrTransition
	}
	call.invocation = v
	a.candidate.replaceToolCall(id, call)
	return nil
}

// admitToolDisposition distinguishes accepted prerequisites from candidate
// pairing. All categories settle; only Returned requires settled prerequisites.
func (a *admissionContext) admitToolDisposition(scope historyid.HistoryID, v sessionformat.ToolDisposition) error {
	id := RequestedCallID{requestID: requestID(scope, v.RunID, v.RequestID), ordinal: v.CallID}
	call, ok := a.candidate.toolCall(id)
	source, accepted := a.source.toolCall(id)
	if !ok || !accepted || call.settled {
		return ErrTransition
	}
	if v.Returned != nil || v.Indeterminate != nil {
		if source.invocation == nil {
			return ErrTransition
		}
		if v.Returned != nil {
			for _, prerequisite := range source.invocation.Prerequisites {
				if !a.source.tools.calls[prerequisite-1].settled {
					return ErrTransition
				}
			}
		}
	}
	call.settled = true
	a.candidate.replaceToolCall(id, call)
	a.candidate.calls--
	return nil
}
