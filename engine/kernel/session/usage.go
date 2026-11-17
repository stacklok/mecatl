package session

import (
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UsageDefinition is immutable first-record metadata with history-relative
// correlation components. Assignment shares optional components; Clone detaches
// them. Zero is invalid. UsageDefinitionView supplies scoped identities.
type UsageDefinition = sessionformat.UsageDefinition

// UsageGranularity is the closed invocation/aggregate observation vocabulary;
// zero is invalid. Neither value proves an upstream physical-attempt count.
type UsageGranularity = sessionformat.UsageGranularity

const (
	// UsageGranularityInvocation denotes one locally observed invocation.
	UsageGranularityInvocation = sessionformat.UsageGranularityInvocation
	// UsageGranularityAggregate denotes a combined observation without attempt breakdown.
	UsageGranularityAggregate = sessionformat.UsageGranularityAggregate
)

const (
	// UsagePurposeMain labels conversational model work, without reserved authority.
	UsagePurposeMain = sessionformat.UsagePurposeMain
)

// UsageView retains immutable record backing and enclosing history scope for its
// lifetime. Zero has zero ID, no definition and an invalid zero report. Navigation
// is O(1) without sibling clones; MutableClone detaches the payload, not enclosing
// scope.
//
//dtogen:include
type UsageView struct {
	scope historyid.HistoryID
	value *sessionformat.Usage
}

// ID returns the full scoped usage identity, or zero for a zero view.
func (v UsageView) ID() UsageID {
	if v.value == nil {
		return UsageID{}
	}
	return UsageID{historyID: v.scope, usageOrdinal: v.value.ID}
}

// Definition returns optional first-record metadata, preserving enclosing history
// scope. Absence returns a zero view, false; no historical lookup is performed.
func (v UsageView) Definition() (UsageDefinitionView, bool) {
	if v.value == nil || v.value.Definition == nil {
		return UsageDefinitionView{}, false
	}
	return UsageDefinitionView{scope: v.scope, value: v.value.Definition}, true
}

// UsageDefinitionView retains immutable first-record metadata and history scope
// for its lifetime. Zero returns empty scalars and absent correlations. Navigation
// is O(1) without cloning siblings. MutableClone exports relative DTO components,
// not the enclosing history scope.
//
//dtogen:include
type UsageDefinitionView struct {
	scope historyid.HistoryID
	value *sessionformat.UsageDefinition
}

// RunID returns optional fully scoped run correlation, including when a request
// is correlated. Absence or a zero view returns zero, false.
func (v UsageDefinitionView) RunID() (RunID, bool) {
	if v.value == nil || v.value.RunID == nil {
		return RunID{}, false
	}
	return RunID{historyID: v.scope, ordinal: *v.value.RunID}, true
}

// RequestID returns optional fully scoped request correlation. Absence or a zero
// view returns zero, false. Valid backing requires an enclosing RunID.
func (v UsageDefinitionView) RequestID() (ModelRequestID, bool) {
	if v.value == nil || v.value.RequestID == nil || v.value.RunID == nil {
		return ModelRequestID{}, false
	}
	return requestID(v.scope, *v.value.RunID, *v.value.RequestID), true
}
