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
	HistoryID historyid.HistoryID
	Route     string
	API       string
	Model     string
	Settings  []byte
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
func modelBindingFromFormat(scope historyid.HistoryID, v sessionformat.ModelBinding) ModelBinding {
	return ModelBinding{HistoryID: scope, Route: v.Route, API: v.API, Model: v.Model, Settings: v.Settings}
}

// NewModelBindingRecord encodes an owned scoped replacement under explicit limits.
// Failure returns zero; construction proves no active-run admission.
func NewModelBindingRecord(v ModelBinding, limits codec.Limits) (Record, error) {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.text(v.Route)
	b.text(v.API)
	b.text(v.Model)
	b.bytes(len(v.Settings))
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.HistoryID, value: format.Record{Kind: sessionformat.KindModelBinding, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// ModelBindingView is an immutable fixed-size accepted binding handle. Zero exports
// zero values; mutable exports detach storage.
type ModelBindingView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// HistoryID returns retained scope, or zero for zero.
func (v ModelBindingView) HistoryID() historyid.HistoryID { return v.scope }

// Value returns an independently owned public value, or zero for zero.
func (v ModelBindingView) Value() ModelBinding {
	if v.record == nil {
		return ModelBinding{}
	}
	return modelBindingFromFormat(v.scope, v.record.binding.Clone())
}

// Format returns a detached DTO, or zero for zero.
func (v ModelBindingView) Format() sessionformat.ModelBinding {
	if v.record == nil {
		return sessionformat.ModelBinding{}
	}
	return v.record.binding.Clone()
}
