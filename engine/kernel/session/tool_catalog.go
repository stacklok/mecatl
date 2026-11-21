package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// ToolCatalog is the canonical ordered advertised catalog replacement, not
// executable behavior. ReplaceToolCatalog takes Session scope separately.
// Assignment shares storage; Clone detaches it. Nil and empty explicitly clear it.
type ToolCatalog = sessionformat.ToolCatalog

// newToolCatalogRecord encodes an owned replacement under explicit limits.
// Failure returns nil. Construction proves no lifecycle admission.
func newToolCatalogRecord(v ToolCatalog, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.collection(len(v.Tools))
	if b.err == nil {
		for _, tool := range v.Tools {
			b.text(tool.Name)
			b.text(tool.Description)
			b.bytes(len(tool.ArgumentSchema))
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindToolCatalog, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ToolCatalogView retains immutable accepted or proposed backing storage, including its
// record. Capture and navigation cost O(1); zero has no Session scope or definitions.
//
//dtogen:include
type ToolCatalogView struct {
	scope sessionid.SessionID
	value *sessionformat.ToolCatalog
}

// SessionID returns the retained scope, or zero for zero.
func (v ToolCatalogView) SessionID() sessionid.SessionID { return v.scope }
