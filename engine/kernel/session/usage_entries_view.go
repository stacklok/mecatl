package session

import (
	"iter"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// UsageEntriesView owns immutable first/latest locators built by Snapshot.Usages.
// It borrows pinned payloads for its lifetime, possibly retaining other snapshot
// backing. Copying, Len, At and ByID cost O(1); All costs O(entries visited).
// Zero is empty. Withdrawn identities remain present. No method copies payloads.
type UsageEntriesView struct {
	scope   sessionid.SessionID
	entries []usageEntryLocator
}

// usageEntryLocator joins accepted first metadata with the last complete report.
// Contiguous Session-wide allocation makes its index the private ordinal minus
// one, also the first-definition occurrence order. It is frozen before exposure.
type usageEntryLocator struct {
	first  *sessionformat.Usage
	latest *sessionformat.Usage
}

// Len returns the number of defined identities, including withdrawn ones.
func (v UsageEntriesView) Len() int { return len(v.entries) }

// At returns the ith entry in first-definition order; out-of-range indices panic.
// The returned handle retains immutable locator and payload backing.
func (v UsageEntriesView) At(i int) UsageEntryView {
	return UsageEntryView{scope: v.scope, location: &v.entries[i]}
}

// All returns repeatable indices and entries in first-definition order. Zero
// yields nothing; stopping early does not change subsequent traversals.
func (v UsageEntriesView) All() iter.Seq2[int, UsageEntryView] {
	return func(yield func(int, UsageEntryView) bool) {
		for i := range v.entries {
			if !yield(i, v.At(i)) {
				return
			}
		}
	}
}

// ByID looks up a full scoped usage identity in O(1). Zero, malformed, foreign
// and absent identities return zero, false; reconstruction proves no membership.
func (v UsageEntriesView) ByID(id UsageID) (UsageEntryView, bool) {
	if id.sessionID != v.scope || id.usageOrdinal == 0 || id.usageOrdinal > uint64(len(v.entries)) {
		return UsageEntryView{}, false
	}
	return v.At(int(id.usageOrdinal - 1)), true //nolint:gosec // Bounded by the int-sized locator count above.
}

// UsageEntryView joins immutable first metadata and the latest complete report
// at the constructing snapshot. It retains borrowed locator/payload backing for
// its lifetime; all navigation is O(1) without sibling clones. Zero has no
// identity, empty definition and invalid zero report. Later appends cannot change it.
type UsageEntryView struct {
	scope    sessionid.SessionID
	location *usageEntryLocator
}

// ID returns the full scoped identity, or zero for a zero view.
func (v UsageEntryView) ID() UsageID {
	if v.location == nil {
		return UsageID{}
	}
	return UsageID{sessionID: v.scope, usageOrdinal: v.location.first.ID}
}

// Definition returns the fixed first-record metadata with fully scoped
// correlations, or a zero view for a zero entry.
func (v UsageEntryView) Definition() UsageDefinitionView {
	if v.location == nil {
		return UsageDefinitionView{}
	}
	return UsageDefinitionView{scope: v.scope, value: v.location.first.Definition}
}

// Report returns the latest complete report, including withdrawal, or a zero
// view for a zero entry. No fields are carried forward from earlier reports.
func (v UsageEntryView) Report() UsageReportView {
	if v.location == nil {
		return UsageReportView{}
	}
	return UsageReportView{value: &v.location.latest.Report}
}
