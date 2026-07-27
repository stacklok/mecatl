package app

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// fireDeliveryMaxRunes is the rune budget for a fire's final text when rendered
// into a delivery harness note. It mirrors carriedContextMaxRunes so an
// excessively long fire output does not blow the origin conversation's context
// window.
const fireDeliveryMaxRunes = 10000

// renderFireDelivery renders a fire's terminal outcome as a FENCED-UNTRUSTED
// harness note suitable for delivery to the parent conversation. It is the pure
// renderer sibling of renderCarriedContext: given a schedule name, fire id,
// stop reason, and final text, it produces a note framed as data ("a scheduled
// task reported"), never as a live instruction.
//
// The provenance header names the schedule + fire id; the WHOLE header
// (including the model-authored schedule name, which is only validated
// non-empty at create) is passed through NeutraliseFraming and placed INSIDE
// the FenceUntrusted block. The fire's final text is rune-clamped to
// fireDeliveryMaxRunes. A fire with no meaningful text still renders a note
// carrying the stop reason — never a silent blank.
func renderFireDelivery(scheduleName, fireID string, stop session.StopReason, finalText string) string {
	// Build the provenance header. Use %s for the model-authored schedule name
	// (not %q) so that any embedded newlines become real lines and are caught by
	// NeutraliseFraming's per-line framingHeader check inside FenceUntrusted.
	header := fmt.Sprintf("[scheduled task %s (fire %s) completed with stop reason: %s]",
		scheduleName, fireID, stop)

	// Clamp the final text to the rune budget.
	body := finalText
	if body == "" {
		body = "(no output)"
	} else {
		body = clampRunes(body, fireDeliveryMaxRunes)
	}

	// Wrap in the fenced-untrusted block. The whole header + body is passed
	// through NeutraliseFraming inside FenceUntrusted, so a forged closing
	// marker or harness section header (in either the model-authored schedule
	// name or the fire's output text) is neutralised.
	combined := header + "\n" + body
	return agent.FenceUntrusted(combined)
}
