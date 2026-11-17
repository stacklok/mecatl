package format

import (
	"errors"
	"unicode/utf8"
)

// UsageReportState is the closed persisted report-state vocabulary. The empty
// string and unknown labels are invalid; there is no finality or revision state.
type UsageReportState string

const (
	// UsageReportActive includes this identity in effective accounting.
	UsageReportActive UsageReportState = "active"
	// UsageReportWithdrawn excludes this identity, including unknown-work coverage,
	// without deleting history. A later active report restores the same definition.
	UsageReportWithdrawn UsageReportState = "withdrawn"
)

// UsageReport is a complete cumulative observation, never a delta or field patch.
// Latest accepted chronological replacement wins, even if counts decrease or an
// old observation is republished. Missing fields are not filled from prior reports.
// Measurements are best-effort showback: neither a verified bill nor a guarantee
// of complete work/attempt coverage. Assignment shares storage; Clone detaches it.
// Zero Go value is invalid because State is required; active with no buckets or raw
// evidence is valid unknown usage.
//
//dtogen:validate validateLocal
type UsageReport struct {
	// State is required active or withdrawn. Withdrawal requires no buckets or Raw.
	State UsageReportState `cbor:"s"`
	// Buckets are producer-ordered, asserted mutually disjoint consumption. Nil
	// and empty are equivalent no normalized measurements; no summing, merging,
	// deduplication or overlap proof is performed within or across usage IDs.
	Buckets []UsageBucket `cbor:"b"`
	// Raw is optional report-wide usage-only opaque evidence spanning any buckets.
	// Nil is absent. Producers must exclude response bodies, prompts, credentials
	// and unreleased content. Validation checks metadata, not the opaque schema or
	// disclosure safety; retention never implies automatic disclosure or replay.
	Raw *OpaqueProviderData `cbor:"x,omitempty"`
}

// Validate checks report state and all child payloads, not producer assertions.
func (v UsageReport) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	for _, bucket := range v.Buckets {
		if err := bucket.Validate(); err != nil {
			return err
		}
	}
	if v.Raw != nil {
		return v.Raw.Validate()
	}
	return nil
}

// validateLocal checks state after bounded codec traversal checks child payloads.
func (v UsageReport) validateLocal() error {
	switch v.State {
	case UsageReportActive:
		return nil
	case UsageReportWithdrawn:
		if len(v.Buckets) == 0 && v.Raw == nil {
			return nil
		}
	}
	return errors.New("invalid usage report state or withdrawal payload")
}

// UsageBucket is one producer-asserted disjoint measurement. Identical selected
// labels may occur in multiple buckets; labels do not prove overlap or uniqueness.
// Its zero Go value is valid unknown consumption, not numerical zero; counter-only
// and selection-only buckets are valid. Assignment shares nested pointers; Clone
// detaches them.
//
// Attribution is provisional: only selected logical route and model are retained.
// Observed serving identity and charging-service attribution may be added when
// concrete producers establish useful reporting semantics; neither is inferred
// from the selected values.
//
//dtogen:validate validateLocal
type UsageBucket struct {
	// SelectedRoute is an optional exact nonempty UTF-8 configured logical
	// provider/route, interpreted relative to the producing harness configuration.
	// Nil means unknown. It is not a global upstream identity or resolved on Load.
	SelectedRoute *string `cbor:"r,omitempty"`
	// SelectedModel is an optional exact nonempty UTF-8 declared model. Nil means
	// unknown. It is not a verified upstream model identity or resolved on Load.
	SelectedModel *string `cbor:"m,omitempty"`
	// Counters are optional measurements; absent totals are never inferred from
	// breakdowns. The zero Go value means all counts unknown, not reported zero
	// consumption.
	Counters UsageCounters `cbor:"c"`
}

// Validate checks label syntax and counters, not disjointness or remote consumption.
func (v UsageBucket) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	return v.Counters.Validate()
}

// validateLocal checks labels after bounded codec traversal validates Counters.
func (v UsageBucket) validateLocal() error {
	for _, label := range []*string{v.SelectedRoute, v.SelectedModel} {
		if label != nil && (*label == "" || !utf8.ValidString(*label)) {
			return errors.New("selected usage label requires nonempty UTF-8")
		}
	}
	return nil
}

// UsageCounters contains exactly five optional nonnegative token counts. Input
// and Output are inclusive; cache and reasoning breakdowns are not added again.
// No accounting equations, estimates, totals, prices or spend are inferred.
// Nil differs from reported zero. Assignment shares pointers; Clone detaches them.
// Its zero Go value means all counts unknown, not numerical zero.
//
//dtogen:validate Validate
type UsageCounters struct {
	// Input is optional inclusive input tokens; nil is unknown, not zero.
	Input *int64 `cbor:"i,omitempty"`
	// Output is optional inclusive output tokens; nil is unknown, not zero.
	Output *int64 `cbor:"o,omitempty"`
	// CacheRead is an optional input-cache-read breakdown, not additional tokens.
	CacheRead *int64 `cbor:"r,omitempty"`
	// CacheWrite is an optional input-cache-write breakdown, not additional tokens.
	CacheWrite *int64 `cbor:"w,omitempty"`
	// Reasoning is an optional reasoning breakdown, not additional tokens.
	Reasoning *int64 `cbor:"n,omitempty"`
}

// Validate checks nonnegative present counts, not provider accounting equations.
func (v UsageCounters) Validate() error {
	for _, n := range []*int64{v.Input, v.Output, v.CacheRead, v.CacheWrite, v.Reasoning} {
		if n != nil && *n < 0 {
			return errors.New("usage count must be nonnegative")
		}
	}
	return nil
}
