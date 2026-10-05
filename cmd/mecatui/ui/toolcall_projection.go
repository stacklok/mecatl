package ui

import (
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

type toolcallProjectionState uint8

const (
	toolcallPending toolcallProjectionState = iota
	toolcallAwaitingResult
	toolcallProvisional
	toolcallDone
	toolcallFailed
)

type toolcallProjection struct {
	blockID     scrollback.BlockID
	revision    uint64
	displayName string
	fullName    string
	intent      string
	state       toolcallProjectionState
}

func projectToolCall(metadata scrollback.ToolCallMetadata) toolcallProjection {
	fullName := terminaltext.SanitizeSingleLine(metadata.Name)
	displayName := fullName
	if title, ok := mcpTitle(metadata.Name); ok {
		displayName = terminaltext.SanitizeSingleLine(title)
	}
	state := toolcallPending
	switch {
	case metadata.ResultReceived && !metadata.Provisional && metadata.ResultError:
		state = toolcallFailed
	case metadata.ResultReceived && !metadata.Provisional:
		state = toolcallDone
	case metadata.ResultReceived:
		state = toolcallProvisional
	case metadata.LifecycleFailed || (metadata.Terminal && subagentStopErrored(metadata.Stop)):
		state = toolcallFailed
	case metadata.Terminal:
		state = toolcallAwaitingResult
	}
	return toolcallProjection{
		blockID: metadata.ID, revision: metadata.Revision,
		displayName: displayName, fullName: fullName,
		intent: terminaltext.SanitizeSingleLine(toolcallIntentFor(metadata.Name, metadata.Arguments)),
		state:  state,
	}
}

func (p toolcallProjection) settled() bool {
	return p.state == toolcallDone || p.state == toolcallFailed
}

// status is the single glyph, text, and theme-slot mapping for a lifecycle state.
func (s toolcallProjectionState) status() (glyph, text, style string) {
	switch s {
	case toolcallAwaitingResult:
		return "…", "awaiting result", "toolName"
	case toolcallProvisional:
		return "…", "awaiting confirmation", "toolName"
	case toolcallDone:
		return "✓", statusDone, "toolOk"
	case toolcallFailed:
		return "✗", statusFailed, "toolErr"
	default:
		return "…", "running", "toolName"
	}
}
