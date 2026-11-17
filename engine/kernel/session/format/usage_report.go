package format

import (
	"errors"
	"unicode/utf8"
)

// UsageReportState is the closed persisted report-state vocabulary. Zero and
// unknown labels are invalid; there is no finality or revision state.
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
// Zero is invalid; active with no buckets or raw evidence is valid unknown usage.
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

// UsageBucket is one producer-asserted disjoint measurement. Identical attribution
// may occur in multiple buckets; labels do not prove overlap or uniqueness. Zero
// is valid unknown consumption; counter-only and attribution-only buckets are
// valid. Assignment shares nested pointers; Clone detaches them.
type UsageBucket struct {
	// Attribution is the producer's structured assertion. Its zero value means
	// unknown; unrelated selected and observed pieces must not be spliced together.
	Attribution UsageAttribution `cbor:"a"`
	// Counters are optional measurements; absent totals are never inferred from
	// breakdowns. The zero struct means all unknown, not reported zero consumption.
	Counters UsageCounters `cbor:"c"`
}

// Validate checks child syntax, not disjointness or remote consumption.
func (v UsageBucket) Validate() error {
	if err := v.Attribution.Validate(); err != nil {
		return err
	}
	return v.Counters.Validate()
}

// UsageCounters contains exactly five optional nonnegative token counts. Input
// and Output are inclusive; cache and reasoning breakdowns are not added again.
// No accounting equations, estimates, totals, prices or spend are inferred.
// Nil differs from reported zero. Assignment shares pointers; Clone detaches them.
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

// UsageAttribution preserves separate producer assertions; zero means entirely
// unknown. No aliases are normalized, catalogs consulted, API dialect interpreted
// as provider identity, or gateway charges inferred from upstream model labels.
// Assignment shares pointers; Clone detaches them.
//
// Design status: exact retention and validation are normative. Cross-adapter
// interpretation of selected, observed and charging labels remains provisional
// until real adapters supply them; reassess then without reinterpreting stored data.
type UsageAttribution struct {
	// Selected optionally describes selection, not proven serving or charging
	// identity. Nil means unknown; present requires at least one label.
	Selected *UsageSelectedIdentity `cbor:"s,omitempty"`
	// Observed optionally identifies reported serving identity and its assertion
	// source. Nil means unknown; partial provider/model identity is valid.
	Observed *UsageObservedIdentity `cbor:"o,omitempty"`
	// Charging optionally names the service whose pricing might apply. Nil means
	// unknown; it is neither a verified bill nor a price/currency assertion.
	Charging *UsageChargingService `cbor:"c,omitempty"`
}

// Validate checks each present assertion without fabricating a combined identity.
func (v UsageAttribution) Validate() error {
	if v.Selected != nil {
		if err := v.Selected.Validate(); err != nil {
			return err
		}
	}
	if v.Observed != nil {
		if err := v.Observed.Validate(); err != nil {
			return err
		}
	}
	if v.Charging != nil {
		return v.Charging.Validate()
	}
	return nil
}

// UsageSelectedIdentity is a selected provider/route and/or model assertion, not
// a proven serving identity. At least one label is required; present labels are
// exact nonempty UTF-8 open values. Assignment shares pointers; Clone detaches them.
//
//dtogen:validate Validate
type UsageSelectedIdentity struct {
	// Provider is an optional exact provider or route label; nil means unknown.
	Provider *string `cbor:"p,omitempty"`
	// Model is an optional exact model label; nil means unknown.
	Model *string `cbor:"m,omitempty"`
}

// Validate checks label presence and syntax, without normalization or resolution.
func (v UsageSelectedIdentity) Validate() error {
	if v.Provider == nil && v.Model == nil {
		return errors.New("selected usage identity requires a label")
	}
	for _, label := range []*string{v.Provider, v.Model} {
		if label != nil && (*label == "" || !utf8.ValidString(*label)) {
			return errors.New("usage identity label requires nonempty UTF-8")
		}
	}
	return nil
}

// UsageObservedIdentity is a source-asserted provider and/or model, not
// authenticated authority. At least one exact nonempty UTF-8 open label and Source
// are required. Assignment shares optional pointers; Clone detaches them.
//
//dtogen:validate Validate
type UsageObservedIdentity struct {
	// Provider is an optional exact observed provider label; nil means unknown.
	Provider *string `cbor:"p,omitempty"`
	// Model is an optional exact observed model label; nil means unknown.
	Model *string `cbor:"m,omitempty"`
	// Source is the required exact nonempty UTF-8 open label of the reporting
	// component/API. It names an assertion source, not authenticated authority.
	Source string `cbor:"s"`
}

// Validate checks partial identity and source syntax, not remote truth.
func (v UsageObservedIdentity) Validate() error {
	if err := (UsageSelectedIdentity{Provider: v.Provider, Model: v.Model}).Validate(); err != nil {
		return err
	}
	if v.Source == "" || !utf8.ValidString(v.Source) {
		return errors.New("observed usage identity requires nonempty UTF-8 source")
	}
	return nil
}

// UsageChargingService names a source-asserted potential charging service, not
// spend or verified billing. Both exact nonempty UTF-8 open labels are required;
// zero is invalid. It owns no mutable storage.
//
//dtogen:validate Validate
type UsageChargingService struct {
	// Label is the required exact service label whose pricing might apply.
	Label string `cbor:"l"`
	// Source is the required reporting component/API label, not authority.
	Source string `cbor:"s"`
}

// Validate checks required label syntax, not charging or disclosure safety.
func (v UsageChargingService) Validate() error {
	if v.Label == "" || v.Source == "" || !utf8.ValidString(v.Label) || !utf8.ValidString(v.Source) {
		return errors.New("charging service requires nonempty UTF-8 label and source")
	}
	return nil
}
