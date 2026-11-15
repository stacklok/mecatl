package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserContextPrefix is a complete prepared user-context prefix replacement,
// independent of Instructions. ReplaceUserContextPrefix supplies history scope.
// Nil and empty Blocks explicitly clear it; no run boundary resets it.
// Assignment shares storage; Clone detaches it.
type UserContextPrefix = sessionformat.UserContextPrefix

// newUserContextPrefixRecord owns a replacement under explicit limits.
// Failure returns nil and admits nothing.
func newUserContextPrefixRecord(v UserContextPrefix, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	b.collection(len(v.Blocks))
	if b.err == nil {
		for _, block := range v.Blocks {
			checkPartsSize(block.Parts, &b)
			checkContentSourceSize(block.Source, &b)
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindUserContextPrefix, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// UserContextPrefixView retains immutable accepted or proposed backing, including its record.
// Capture and navigation cost O(1); zero has no history or blocks.
//
//dtogen:include
type UserContextPrefixView struct {
	scope historyid.HistoryID
	value *sessionformat.UserContextPrefix
}

// HistoryID returns the retained scope, or zero for zero.
func (v UserContextPrefixView) HistoryID() historyid.HistoryID { return v.scope }
