package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UsageDefinition is immutable first-record metadata with history-relative
// correlation components. Assignment shares optional components; Clone detaches
// them. Zero is invalid. UsageDefinitionView supplies scoped identities.
type UsageDefinition = sessionformat.UsageDefinition

const (
	// UsagePurposeMain labels conversational model work, without reserved authority.
	UsagePurposeMain = sessionformat.UsagePurposeMain
)

// AddUsage allocates a provisional history-wide identity with an active report.
// At most one correlation may be supplied; a request supplies its run.
// Nil/nil denotes session-level work; non-nil IDs must be valid in this history.
// Correlation must precede this addition in
// accepted history or this batch, but need not be active or have an outcome.
// Scope is checked before payloads. Failure returns zero and consumes nothing;
// success owns the report backing but establishes no acceptance until Submit.
func (b *SessionCommit) AddUsage(run *RunID, request *ModelRequestID, purpose string, report UsageReport) (UsageID, error) {
	if err := b.check(true); err != nil {
		return UsageID{}, err
	}
	if run != nil && request != nil {
		return UsageID{}, codec.ErrInvalidData
	}
	definition := UsageDefinition{Purpose: purpose}
	if request != nil {
		if request.Validate() != nil {
			return UsageID{}, codec.ErrInvalidData
		}
		if request.runID.historyID != b.owner.state.view.historyID {
			return UsageID{}, ErrIdentity
		}
		definition.RunID = new(request.runID.ordinal)
		definition.RequestID = new(request.requestOrdinal)
	}
	if run != nil {
		if run.Validate() != nil {
			return UsageID{}, codec.ErrInvalidData
		}
		if run.historyID != b.owner.state.view.historyID {
			return UsageID{}, ErrIdentity
		}
		definition.RunID = new(run.ordinal)
	}
	ordinal, err := nextOrdinal(b.admission.candidate.usages)
	if err != nil {
		return UsageID{}, err
	}
	id := UsageID{historyID: b.owner.state.view.historyID, usageOrdinal: ordinal}
	if err := b.add(newUsageRecord(sessionformat.Usage{ID: ordinal, Definition: &definition, Report: report}, b.owner.policy.PayloadLimits)); err != nil {
		return UsageID{}, err
	}
	return id, nil
}

// ReplaceUsage adds a complete report for an identity introduced in accepted
// history or earlier in this batch. It never merges fields or changes metadata;
// decreases, duplicates, withdrawal and restoration are legal. Scope is checked
// before payloads. Failure changes neither earlier additions nor accepted facts.
func (b *SessionCommit) ReplaceUsage(id UsageID, report UsageReport) error {
	if err := b.check(true); err != nil {
		return err
	}
	if id.Validate() != nil {
		return codec.ErrInvalidData
	}
	if id.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	return b.add(newUsageRecord(sessionformat.Usage{ID: id.usageOrdinal, Report: report}, b.owner.policy.PayloadLimits))
}

// newUsageRecord bounds caller storage before encoding and detaches all leaves.
func newUsageRecord(v sessionformat.Usage, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	if v.Definition != nil {
		b.pointer()
		b.text(v.Definition.Purpose)
	}
	b.text(string(v.Report.State))
	b.collection(len(v.Report.Buckets))
	if b.err != nil {
		return nil, b.err
	}
	for _, bucket := range v.Report.Buckets {
		for _, label := range [...]*string{bucket.SelectedRoute, bucket.SelectedModel} {
			if label != nil {
				b.pointer()
				b.text(*label)
			}
		}
		c := bucket.Counters
		for _, count := range [...]*int64{c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
			if count != nil {
				b.pointer()
			}
		}
	}
	checkOpaqueSize(v.Report.Raw, &b)
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindUsage, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// admitUsage checks contiguous first definitions and prefix-only correlation.
// Replacements use only the allocation scalar: copied admission never owns a
// mutable registry or scans earlier reports to rediscover fixed metadata.
func (a *admissionContext) admitUsage(v sessionformat.Usage, prefixes [][]*materializedRecord) error {
	s := &a.candidate
	if !s.created {
		return ErrTransition
	}
	if v.Definition == nil {
		if v.ID == 0 || v.ID > s.usages {
			return ErrTransition
		}
		return nil
	}
	next, err := nextOrdinal(s.usages)
	if err != nil {
		return err
	}
	if v.ID != next {
		return ErrTransition
	}
	d := v.Definition
	if d.RunID != nil && (*d.RunID == 0 || *d.RunID > s.runs) {
		return ErrTransition
	}
	if d.RequestID != nil {
		found := false
		for _, prefix := range prefixes {
			for _, r := range prefix {
				if r.value.Kind == sessionformat.KindModelRequest && r.request.RunID == *d.RunID && r.request.ID == *d.RequestID {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return ErrTransition
		}
	}
	s.usages = next
	return nil
}

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
