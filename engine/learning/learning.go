// Package learning defines host-driven completed-trajectory observation,
// evidence-backed reflection, and evaluated agent-owned skill lifecycle values.
// It owns policy and bounded contracts only; persistence, scheduling, model
// transport, and runtime catalog activation remain host concerns.
package learning

import (
	"context"
	"fmt"
	"slices"

	"github.com/stacklok/mecatl/engine/session"
)

// Mode controls automatic completed-trajectory observation. The zero value is Off.
type Mode uint8

const (
	// Off disables automatic completed-trajectory observation and is the zero value.
	Off Mode = iota
	// Review permits signal-gated reflection and durable proposal staging without memory writes.
	Review
	// Auto stages proposals and permits conservative eligible-fact promotion.
	Auto
)

// ParseMode parses the closed settings vocabulary strictly.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "off":
		return Off, nil
	case "review":
		return Review, nil
	case "auto":
		return Auto, nil
	default:
		return Off, fmt.Errorf("learning: invalid mode %q (want off, review, or auto)", s)
	}
}

func (m Mode) String() string {
	switch m {
	case Off:
		return "off"
	case Review:
		return "review"
	case Auto:
		return "auto"
	default:
		return fmt.Sprintf("Mode(%d)", m)
	}
}

// Next returns the next mode in the operator-selection cycle Off → Review → Auto → Off.
func (m Mode) Next() Mode {
	switch m {
	case Off:
		return Review
	case Review:
		return Auto
	default:
		return Off
	}
}

// Trajectory is an owned snapshot of one newly completed run. Messages has a
// fresh backing slice and never aliases a live session aggregate.
type Trajectory struct {
	SessionID session.SessionID
	Workspace string
	Stop      session.StopReason
	Usage     session.Usage
	Messages  []session.Message
	// Kind identifies the trusted producer. Automatic admission accepts main only.
	Kind session.SessionKind
	// Counters are the completed current run's model/tool counters.
	Counters session.Counters
	// Current is the verified half-open message span for the current run.
	Current MessageSpan
	// Principal is a copied completed-session owner for host partitioning.
	Principal *session.Principal
}

// NewTrajectory constructs an owned completed-run snapshot. Every mutable nested
// value is copied so an Observer cannot mutate the live or persisted Session history.
func NewTrajectory(id session.SessionID, workspace string, stop session.StopReason, usage session.Usage, messages []session.Message) Trajectory {
	return Trajectory{
		SessionID: id,
		Workspace: workspace,
		Stop:      stop,
		Usage:     usage,
		Messages:  cloneMessages(messages),
	}
}

func cloneMessages(messages []session.Message) []session.Message {
	out := slices.Clone(messages)
	for i := range out {
		out[i].ToolCalls = slices.Clone(out[i].ToolCalls)
		for j := range out[i].ToolCalls {
			out[i].ToolCalls[j].Args = slices.Clone(out[i].ToolCalls[j].Args)
		}
		out[i].Parts = cloneContents(out[i].Parts)
		if out[i].ToolResult != nil {
			result := *out[i].ToolResult
			result.Parts = cloneContents(result.Parts)
			out[i].ToolResult = &result
		}
	}
	return out
}

func cloneContents(contents []session.Content) []session.Content {
	out := slices.Clone(contents)
	for i := range out {
		out[i].Data = slices.Clone(out[i].Data)
		out[i].Audience = slices.Clone(out[i].Audience)
	}
	return out
}

// Observer synchronously observes an eligible completed trajectory. Implementations
// own any model calls or durable effects and should honor ctx.
type Observer interface {
	Observe(context.Context, Trajectory) error
}
