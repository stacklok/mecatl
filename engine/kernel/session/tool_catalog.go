package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ToolCatalog replaces the session's complete ordered advertised catalog. Nil and
// empty Tools mean explicit emptiness; zero HistoryID is invalid. Assignment
// shares storage; Clone detaches it. Tools are declarations, not executables.
type ToolCatalog struct {
	HistoryID historyid.HistoryID
	Tools     []ToolDefinition
}

// Validate checks full scope, tool validity and unique names without limits.
func (v ToolCatalog) Validate() error {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
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

// NewToolCatalogRecord encodes an owned scoped replacement under explicit limits.
// Failure returns zero; construction proves no active-run admission.
func NewToolCatalogRecord(v ToolCatalog, limits codec.Limits) (Record, error) {
	if err := (Created{HistoryID: v.HistoryID}).Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
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
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.HistoryID, value: format.Record{Kind: sessionformat.KindToolCatalog, KindVersion: sessionformat.ToolCatalogVersion, Payload: payload}}, nil
}

// ToolCatalogView is an immutable fixed-size accepted catalog handle. Zero exports
// zero values; all mutable exports detach storage.
type ToolCatalogView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// HistoryID returns the retained scope, or zero for zero.
func (v ToolCatalogView) HistoryID() historyid.HistoryID { return v.scope }

// Value returns an independently owned public value, or zero for zero.
func (v ToolCatalogView) Value() ToolCatalog {
	if v.record == nil {
		return ToolCatalog{}
	}
	return toolCatalogFromFormat(v.scope, v.record.catalog.Clone())
}

// Format returns a detached DTO, or zero for zero.
func (v ToolCatalogView) Format() sessionformat.ToolCatalog {
	if v.record == nil {
		return sessionformat.ToolCatalog{}
	}
	return v.record.catalog.Clone()
}
