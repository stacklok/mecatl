package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// UsageReport is a complete latest-replacement observation, never a delta/merge.
// Zero is invalid; active with no measurements is valid unknown usage. Assignment
// shares nested pointers, slices and raw bytes; Clone detaches them. See format's
// owner-local field contracts for attribution, disjointness and evidence safety.
type UsageReport = sessionformat.UsageReport

// UsageReportState is the closed active/withdrawn vocabulary; zero is invalid.
type UsageReportState = sessionformat.UsageReportState

const (
	// UsageReportActive includes the identity in effective accounting.
	UsageReportActive = sessionformat.UsageReportActive
	// UsageReportWithdrawn excludes the identity without deleting its history;
	// it requires no buckets/raw and can later be replaced by an active report.
	UsageReportWithdrawn = sessionformat.UsageReportWithdrawn
)

// UsageBucket is producer-asserted disjoint consumption; zero is valid unknown
// usage. Assignment shares attribution/counter pointers; Clone detaches them.
type UsageBucket = sessionformat.UsageBucket

// UsageCounters holds five optional nonnegative token counts. Nil differs from
// reported zero; breakdowns do not add to inclusive input/output. Assignment shares
// pointers; Clone detaches them. Zero means all measurements unknown.
type UsageCounters = sessionformat.UsageCounters

// UsageAttribution keeps selected, observed and potential charging assertions
// separate. Zero means unknown; labels are exact and open, never normalized or
// proof of a bill. Assignment shares pointers; Clone detaches them.
type UsageAttribution = sessionformat.UsageAttribution

// UsageSelectedIdentity requires provider/route and/or model; zero is invalid.
// Labels are exact nonempty UTF-8. Assignment shares pointers; Clone detaches them.
type UsageSelectedIdentity = sessionformat.UsageSelectedIdentity

// UsageObservedIdentity requires provider and/or model plus an assertion Source;
// zero is invalid. Labels are exact nonempty UTF-8, not authenticated authority.
// Assignment shares pointers; Clone detaches them.
type UsageObservedIdentity = sessionformat.UsageObservedIdentity

// UsageChargingService requires exact nonempty UTF-8 service Label and Source;
// zero is invalid. It owns no mutable storage and proves neither spend nor billing.
type UsageChargingService = sessionformat.UsageChargingService

// UsageReportView retains immutable context-free report backing for its lifetime.
// Zero has empty state, no buckets and absent raw. Reads and collection creation
// are O(1), borrowing backing without sibling clones; traversal is O(buckets).
// MutableClone detaches the complete report; raw Data().Copy detaches one leaf.
//
//dtogen:include
type UsageReportView struct {
	value *sessionformat.UsageReport
}

// UsageBucketView retains immutable context-free bucket backing for its lifetime.
// Zero has unknown attribution/counters. Navigation is O(1) without sibling clones;
// MutableClone detaches only this bucket's subtree.
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

// UsageAttributionView retains immutable context-free attribution backing for its
// lifetime. Zero has no assertions. Navigation is O(1) without cloning siblings;
// MutableClone detaches this attribution subtree.
//
//dtogen:include
type UsageAttributionView struct {
	value *sessionformat.UsageAttribution
}

// UsageSelectedIdentityView retains immutable selected-identity backing for its
// lifetime. Zero labels are absent. Reads are O(1) without sibling clones;
// MutableClone detaches the optional label pointers.
//
//dtogen:include
type UsageSelectedIdentityView struct {
	value *sessionformat.UsageSelectedIdentity
}

// UsageObservedIdentityView retains immutable observed-identity backing for its
// lifetime. Zero has absent labels and empty Source. Reads are O(1) without sibling
// clones; MutableClone detaches the optional label pointers.
//
//dtogen:include
type UsageObservedIdentityView struct {
	value *sessionformat.UsageObservedIdentity
}

// UsageChargingServiceView retains immutable charging-service backing for its
// lifetime. Zero has empty Label/Source. Reads are O(1) without sibling clones;
// MutableClone returns the selected value, which owns no mutable storage.
//
//dtogen:include
type UsageChargingServiceView struct {
	value *sessionformat.UsageChargingService
}
