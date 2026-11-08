package session

import (
	"bytes"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ModelBinding replaces declared logical preparation choices, not observed
// upstream identity or compatibility. Route, API (including dialect/version), and
// Model are exact nonempty strings; aliases are allowed. Settings retains exact
// explicit JSON, never resolved defaults, credentials, headers or config dumps.
// Assignment shares Settings; Clone detaches it. Zero is invalid. The caller owns
// retention safety and completeness; this passive value certifies neither.
type ModelBinding struct {
	// HistoryID is the required scope of this replacement.
	HistoryID historyid.HistoryID
	// Route is a logical provider/route name relative to the preparing harness's
	// configuration, not a global provider identity or observed upstream endpoint.
	Route string
	// API is the required declared API dialect and version.
	API string
	// Model is the required declared model name or alias.
	Model string
	// Settings is exact explicit JSON; assignment borrows the bytes.
	Settings []byte
}

// Validate checks scope, binding strings and JSON syntax without resource limits.
func (v ModelBinding) Validate() error {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return err
	}
	return v.toFormat().Validate()
}

// Clone detaches exact settings bytes.
func (v ModelBinding) Clone() ModelBinding { v.Settings = bytes.Clone(v.Settings); return v }

// Equal compares scope and exact values, including JSON representation.
func (v ModelBinding) Equal(other ModelBinding) bool {
	return v.HistoryID == other.HistoryID && v.toFormat().Equal(other.toFormat())
}
func (v ModelBinding) toFormat() sessionformat.ModelBinding {
	return sessionformat.ModelBinding{Route: v.Route, API: v.API, Model: v.Model, Settings: v.Settings}
}

// newModelBindingRecord encodes an owned scoped replacement under explicit limits.
// Failure returns nil; target history is checked before scope omission.
// Construction proves no lifecycle admission.
func newModelBindingRecord(scope historyid.HistoryID, v ModelBinding, limits codec.Limits) (*materializedRecord, error) {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	if scope != v.HistoryID {
		return nil, ErrIdentity
	}
	b := newValueBudget(limits)
	b.text(v.Route)
	b.text(v.API)
	b.text(v.Model)
	b.bytes(len(v.Settings))
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindModelBinding, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ModelBindingView retains immutable accepted backing storage, including its
// record. Capture and scalar navigation cost O(1); zero has empty fields.
type ModelBindingView struct {
	scope historyid.HistoryID
	value *sessionformat.ModelBinding
}

// HistoryID returns retained scope, or zero for zero.
func (v ModelBindingView) HistoryID() historyid.HistoryID { return v.scope }

// Route returns the harness-relative logical route, or empty for zero.
func (v ModelBindingView) Route() string {
	if v.value == nil {
		return ""
	}
	return v.value.Route
}

// API returns the declared API, or empty for zero.
func (v ModelBindingView) API() string {
	if v.value == nil {
		return ""
	}
	return v.value.API
}

// Model returns the declared model, or empty for zero.
func (v ModelBindingView) Model() string {
	if v.value == nil {
		return ""
	}
	return v.value.Model
}

// Settings returns independently owned exact JSON bytes, or nil for zero.
// It copies only this field, never other accepted content.
func (v ModelBindingView) Settings() []byte {
	if v.value == nil {
		return nil
	}
	return bytes.Clone(v.value.Settings)
}
