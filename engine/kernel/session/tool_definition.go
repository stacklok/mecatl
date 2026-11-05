package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ToolDefinition retains an advertised definition, not executable behavior.
// ArgumentSchema is exact explicit JSON (including boolean schemas); assignment
// shares those bytes, Clone detaches them. No schema semantics are interpreted.
type ToolDefinition struct {
	Name           string
	Description    string
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
