package ui

import (
	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

type toolcallProjectionState uint8

const (
	toolcallPending toolcallProjectionState = iota
	toolcallAwaitingResult
	toolcallProvisional
	toolcallProvisionalFailed
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
	return toolcallProjection{
		blockID: metadata.ID, revision: metadata.Revision,
		displayName: displayName, fullName: fullName,
		intent: renderfmt.ToolIntent(metadata.Name, metadata.Arguments),
		state:  toolCallState(metadata),
	}
}

// toolCallState is the lifecycle-only metadata projection used on warm render
// passes. It deliberately does not inspect presentation fields such as name or
// arguments, which are only needed when preparing a cache-miss card.
func toolCallState(metadata scrollback.ToolCallMetadata) toolcallProjectionState {
	state := toolcallPending
	switch {
	case metadata.ResultReceived && !metadata.Provisional && metadata.ResultError:
		state = toolcallFailed
	case metadata.ResultReceived && !metadata.Provisional:
		state = toolcallDone
	case metadata.ResultReceived && metadata.ResultError:
		state = toolcallProvisionalFailed
	case metadata.ResultReceived:
		state = toolcallProvisional
	case metadata.LifecycleFailed || (metadata.Terminal && subagentStopErrored(metadata.Stop)):
		state = toolcallFailed
	case metadata.Terminal:
		state = toolcallAwaitingResult
	}
	return state
}

func (p toolcallProjection) settled() bool {
	return p.state == toolcallDone || p.state == toolcallFailed
}

func (p toolcallProjection) line() renderfmt.ToolLine {
	return renderfmt.PresentToolLine(p.displayName, p.intent, p.state.renderfmtState())
}

func (s toolcallProjectionState) renderfmtState() renderfmt.ToolState {
	switch s {
	case toolcallAwaitingResult:
		return renderfmt.ToolAwaitingResult
	case toolcallProvisional:
		return renderfmt.ToolFinalizing
	case toolcallProvisionalFailed:
		return renderfmt.ToolFinalizingFailed
	case toolcallDone:
		return renderfmt.ToolSucceeded
	case toolcallFailed:
		return renderfmt.ToolFailed
	default:
		return renderfmt.ToolRunning
	}
}

// status is retained as a test seam for the full lifecycle label; renderers use ToolLine.
func (s toolcallProjectionState) status() (glyph, text string) {
	if s == toolcallDone {
		return "✓", statusDone
	}
	line := renderfmt.PresentToolLine("", "", s.renderfmtState())
	return line.Glyph(), line.Status()
}
