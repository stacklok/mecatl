package session

import "iter"

// ToolCallsView owns immutable call locators and a full-scoped identity lookup,
// borrowing pinned payloads for its lifetime. Copying the collection is O(1), as
// are Len and At; ByID takes expected O(1). Zero is empty. Only RunView.ToolCalls
// constructs the association; retained collections never change after appends.
type ToolCallsView struct {
	calls []toolCallLocator
	byID  map[RequestedCallID]int
	bulk  RunEndedView
}

// toolCallLocator retains public views of the requested call and its optional
// facts, borrowing pinned payloads. Construction associates accepted facts in
// chronological order before publishing any handle; locators never change after.
type toolCallLocator struct {
	requested   RequestedCallView
	invocation  ToolInvocationView
	disposition ToolDispositionView
}

// Len returns the number of requested calls across all responses in the run.
func (v ToolCallsView) Len() int { return len(v.calls) }

// At returns the ith call in response order then requested-call order. It panics
// outside [0, Len()). The handle retains its locator and immutable payloads.
func (v ToolCallsView) At(i int) ToolCallView {
	return ToolCallView{location: &v.calls[i], bulk: v.bulk}
}

// All returns a repeatable ordered sequence of indices and pinned call handles.
// Zero yields nothing; stopping early does not change subsequent traversals.
func (v ToolCallsView) All() iter.Seq2[int, ToolCallView] {
	return func(yield func(int, ToolCallView) bool) {
		for i := range v.calls {
			if !yield(i, v.At(i)) {
				return
			}
		}
	}
}

// ByID looks up a full scoped call identity. Zero, invalid, foreign and absent
// identities return zero, false; reconstruction alone does not prove membership.
func (v ToolCallsView) ByID(id RequestedCallID) (ToolCallView, bool) {
	i, ok := v.byID[id]
	if !ok {
		return ToolCallView{}, false
	}
	return v.At(i), true
}
