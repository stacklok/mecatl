package session

import (
	"bytes"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ToolDefinition retains an advertised definition, not executable behavior.
// ArgumentSchema is exact explicit JSON (including boolean schemas); assignment
// shares those bytes, Clone detaches them. No schema semantics are interpreted.
type ToolDefinition struct {
	// Name is the required advertised tool name.
	Name string
	// Description is the optional exact tool description.
	Description string
	// ArgumentSchema is required exact JSON; assignment borrows the bytes.
	ArgumentSchema []byte
}

// Validate checks UTF-8 strings, nonempty name, and JSON syntax without limits.
func (v ToolDefinition) Validate() error { return sessionformat.ToolDefinition(v).Validate() }

// Clone detaches exact schema bytes.
func (v ToolDefinition) Clone() ToolDefinition {
	return ToolDefinition(sessionformat.ToolDefinition(v).Clone())
}

// Equal compares exact values, including JSON representation.
func (v ToolDefinition) Equal(other ToolDefinition) bool {
	return sessionformat.ToolDefinition(v).Equal(sessionformat.ToolDefinition(other))
}

// ToolDefinitionView retains immutable backing storage, including schema bytes.
// Capture and scalar navigation cost O(1); zero has empty fields.
type ToolDefinitionView struct{ value *sessionformat.ToolDefinition }

// Name returns the advertised name, or empty for zero.
func (v ToolDefinitionView) Name() string {
	if v.value == nil {
		return ""
	}
	return v.value.Name
}

// Description returns the exact description, or empty for zero.
func (v ToolDefinitionView) Description() string {
	if v.value == nil {
		return ""
	}
	return v.value.Description
}

// ArgumentSchema returns independently owned exact JSON bytes, or nil for zero.
// It copies only this field, never sibling fields or definitions.
func (v ToolDefinitionView) ArgumentSchema() []byte {
	if v.value == nil {
		return nil
	}
	return bytes.Clone(v.value.ArgumentSchema)
}
