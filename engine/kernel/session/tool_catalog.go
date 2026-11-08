package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ToolCatalog replaces the session's complete ordered advertised catalog. Nil and
// empty Tools mean explicit emptiness; zero HistoryID is invalid. Assignment
// shares storage; Clone detaches it. Tools are declarations, not executables.
type ToolCatalog struct {
	// HistoryID is the required scope of this replacement.
	HistoryID historyid.HistoryID
	// Tools is the complete ordered catalog; assignment borrows nested storage.
	Tools []ToolDefinition
}

// Validate checks full scope, tool validity and unique names without limits.
func (v ToolCatalog) Validate() error {
	if err := validateHistoryID(v.HistoryID); err != nil {
		return err
	}
	for _, tool := range v.Tools {
		if err := tool.Validate(); err != nil {
			return err
		}
	}
	return v.toFormat().Validate()
}

// Clone detaches all tools and schema bytes.
func (v ToolCatalog) Clone() ToolCatalog {
	return toolCatalogFromFormat(v.HistoryID, v.toFormat().Clone())
}

// Equal compares scope and exact ordered declarations.
func (v ToolCatalog) Equal(other ToolCatalog) bool {
	return v.HistoryID == other.HistoryID && v.toFormat().Equal(other.toFormat())
}
func (v ToolCatalog) toFormat() sessionformat.ToolCatalog {
	out := sessionformat.ToolCatalog{}
	if v.Tools != nil {
		out.Tools = make([]sessionformat.ToolDefinition, len(v.Tools))
		for i := range v.Tools {
			out.Tools[i] = sessionformat.ToolDefinition(v.Tools[i])
		}
	}
	return out
}
func toolCatalogFromFormat(scope historyid.HistoryID, v sessionformat.ToolCatalog) ToolCatalog {
	out := ToolCatalog{HistoryID: scope}
	if v.Tools != nil {
		out.Tools = make([]ToolDefinition, len(v.Tools))
		for i := range v.Tools {
			out.Tools[i] = ToolDefinition(v.Tools[i])
		}
	}
	return out
}

// newToolCatalogRecord encodes an owned replacement under explicit limits.
// Failure returns nil. Construction proves no lifecycle admission.
func newToolCatalogRecord(v ToolCatalog, limits codec.Limits) (*materializedRecord, error) {
	if err := validateHistoryID(v.HistoryID); err != nil {
		return nil, codec.ErrInvalidData
	}
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
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindToolCatalog, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ToolCatalogView retains immutable accepted backing storage, including its
// record. Capture and navigation cost O(1); zero has no history or definitions.
type ToolCatalogView struct {
	scope historyid.HistoryID
	value *sessionformat.ToolCatalog
}

// HistoryID returns the retained scope, or zero for zero.
func (v ToolCatalogView) HistoryID() historyid.HistoryID { return v.scope }

// ToolCount returns the number of ordered definitions, or zero for zero.
func (v ToolCatalogView) ToolCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Tools)
}

// Tools returns a repeatable sequence of immutable definitions in catalog order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v ToolCatalogView) Tools() iter.Seq[ToolDefinitionView] {
	return func(yield func(ToolDefinitionView) bool) {
		for i := 0; i < v.ToolCount(); i++ {
			if !yield(v.ToolAt(i)) {
				return
			}
		}
	}
}

// ToolAt returns an immutable definition. It panics outside [0, ToolCount()).
func (v ToolCatalogView) ToolAt(i int) ToolDefinitionView {
	return ToolDefinitionView{value: &v.value.Tools[i]}
}
