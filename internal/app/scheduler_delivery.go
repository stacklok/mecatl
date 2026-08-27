package app

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// fireDeliveryMaxRunes is the rune budget for a fire's final text when rendered
// into a delivery harness note. It mirrors carriedContextMaxRunes so an
// excessively long fire output does not blow the origin conversation's context
// window.
const fireDeliveryMaxRunes = 10000

// renderFireStarted renders a fire's "started" notice as a FENCED-UNTRUSTED
// harness note (issue #386, Phase 4a). It mirrors renderFireDelivery but carries
// ONLY the schedule name + fire/session id — NO model-authored content (none
// exists at start: the run has not produced any output yet). The note is framed
// as data ("a scheduled task started"), never as a live instruction.
//
// It is a DISTINCT ledger entry from the terminal note (Enqueue mints a fresh
// seq per call), so an origin conversation receives exactly one start note and
// one terminal note per fire. The provenance header is passed through
// NeutraliseFraming inside FenceUntrusted so a forged marker in the
// model-authored schedule name cannot break out of the block.
func renderFireStarted(scheduleName, fireID string) string {
	header := fmt.Sprintf("[scheduled task %s started (fire %s)]", scheduleName, fireID)
	return governance.FenceUntrusted(header)
}

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
	return governance.FenceUntrusted(combined)
}
