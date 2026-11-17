package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// UsageReport is a complete latest-replacement observation, never a delta/merge.
// Its zero Go value is invalid because state is empty; active with no measurements
// is valid unknown usage. Assignment shares nested pointers, slices and raw bytes;
// Clone detaches them. See format's owner-local field contracts for selected labels,
// disjointness and evidence safety.
type UsageReport = sessionformat.UsageReport

// UsageReportState is the closed active/withdrawn vocabulary; the empty string is invalid.
type UsageReportState = sessionformat.UsageReportState

const (
	// UsageReportActive includes the identity in effective accounting.
	UsageReportActive = sessionformat.UsageReportActive
	// UsageReportWithdrawn excludes the identity without deleting its history;
	// it requires no buckets/raw and can later be replaced by an active report.
	UsageReportWithdrawn = sessionformat.UsageReportWithdrawn
)

// UsageBucket is producer-asserted disjoint consumption. Its zero Go value is valid
// unknown usage, not numerical zero. Assignment shares selected-label and counter
// pointers; Clone detaches them.
type UsageBucket = sessionformat.UsageBucket

// UsageCounters holds five optional nonnegative token counts. Nil differs from
// reported zero; breakdowns do not add to inclusive input/output. Assignment shares
// pointers; Clone detaches them. Its zero Go value means all measurements unknown,
// not numerical zero.
type UsageCounters = sessionformat.UsageCounters

// UsageReportView retains immutable context-free report backing for its lifetime.
// Zero has empty invalid state, no buckets and absent raw. Reads and collection
// creation are O(1), borrowing backing without sibling clones; traversal is
// O(buckets). MutableClone detaches the complete report; raw Data().Copy detaches
// one leaf.
//
//dtogen:include
type UsageReportView struct {
	value *sessionformat.UsageReport
}

// UsageBucketView retains immutable context-free bucket backing for its lifetime.
// Zero has absent selected labels and unknown counters. Navigation is O(1) without
// sibling clones; MutableClone detaches only this bucket's subtree.
//
//dtogen:include
type UsageBucketView struct {
	value *sessionformat.UsageBucket
}

// UsageCountersView retains immutable counter backing for its lifetime. Zero and
// absent counters return zero, false, distinct from reported zero, true. Reads are
// O(1); MutableClone detaches all present counter pointers.
//
//dtogen:include
type UsageCountersView struct {
	value *sessionformat.UsageCounters
}
