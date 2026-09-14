package modelhook

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/governance"
)

// resultPayload is the {content, is_error} shape the loop packs a PostToolUse
// HookEvent.Input with and interprets a PostToolUse HookOutcome.Mutated as
// (symmetric with engine/agent/dispatch.go's resultPayload). It is the surface the
// Post-direction guardrail inspects and rewrites.
type resultPayload struct {
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content"`
	IsError bool            `json:"is_error"`
}

// postResultContent extracts the result "content" string from a PostToolUse
// HookEvent.Input ({args, content, is_error}). ok=false when input is not the
// expected object (then the caller falls back to the raw bytes).
func postResultContent(input json.RawMessage) (string, bool) {
	var p resultPayload
	if err := json.Unmarshal(input, &p); err != nil {
		return "", false
	}
	return p.Content, true
}

// postBlockOutcome builds the PostToolUse result rewrite used for an enforcing block.
// PostToolUse cannot veto because the tool already ran, so the result is replaced with
// the {content, is_error} shape the loop understands.
func postBlockOutcome(payload string) governance.HookOutcome {
	body, _ := json.Marshal(resultPayload{Content: payload, IsError: true})
	return governance.HookOutcome{Mutated: body, Message: payload}
}

// mergeOutcomes folds the inner runner's outcome with the guardrail checker's per
// decision 5: Block-DOMINANT (either blocks → blocked, messages concatenated
// inner-first), and on a mutation CONFLICT the checker (security) WINS. A zero
// checker outcome (safe / advisory / skipped) returns inner unchanged.
//
// ADR 0062: the checker's AskApproval bit (a Pre block refined into an approval ask)
// rides through onto the merged outcome — when the checker wants the block surfaced as
// an ask, the merged block stays askable. The bit is meaningful only on a Pre Block,
// so propagating the checker's value verbatim is correct (an inner-only block carries
// AskApproval false, the terminal-block behaviour unchanged).
func mergeOutcomes(inner, check governance.HookOutcome) governance.HookOutcome {
	if isZeroOutcome(check) {
		return inner
	}

	out := inner

	// Block-dominant: either side blocking blocks the merged outcome; the messages
	// concatenate inner-first so the operator/model sees both reasons.
	if check.Block {
		out.Block = true
	}
	// The checker's AskApproval refinement rides onto the merged outcome (ADR 0062).
	if check.AskApproval {
		out.AskApproval = true
	}
	out.Message = concatMessages(inner.Message, check.Message)

	// Mutation conflict → checker wins (security). The checker's Mutated replaces the
	// inner's whenever the checker produced one; otherwise the inner's stands.
	if len(check.Mutated) > 0 {
		out.Mutated = check.Mutated
	}
	return out
}

// isZeroOutcome reports whether o carries no block, no message, no mutation, and no
// ask refinement — the "checker did nothing" outcome (safe verdict, advisory finding,
// or a skip). AskApproval is only ever set alongside Block, so it never independently
// flips this, but the predicate stays honest.
func isZeroOutcome(o governance.HookOutcome) bool {
	return !o.Block && o.Message == "" && len(o.Mutated) == 0 && !o.AskApproval
}

// concatMessages joins two hook messages inner-first, dropping empties, so a merged
// block surfaces both the inner hook's and the guardrail's reason.
func concatMessages(inner, check string) string {
	switch {
	case inner == "":
		return check
	case check == "":
		return inner
	default:
		return inner + "; " + check
	}
}
