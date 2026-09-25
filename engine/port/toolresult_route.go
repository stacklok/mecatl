package port

import "github.com/stacklok/mecatl/engine/session"

// RouteToolResultParts is the composition-driven, capability-gated PROJECTION of
// a recorded tool result's typed blocks for the model-facing request. It returns
// the subset of tr.Parts the (provider, model) — described by caps, the SINGLE
// composition-computed capability intersection (catalog ∩ adapter) — may receive
// as typed blocks, or nil when no block survives (the caller degrades to the
// recorded model-facing Content string).
//
// SEAM. This lives in engine/port (not internal/app) for two reasons:
//  1. The provider adapters (provider/openai, provider/anthropic)
//     consume the projection at their RoleTool case; they may import engine/port
//     and engine/session but MUST NOT import internal/app (composition). Composition
//     may import them. Placing the helper here lets BOTH call it without inverting
//     the layering.
//  2. caps is already a port.ProviderCapabilities (the neutral intersection type
//     modelCapability computes), and session.ToolResult is the recorded value
//     object — both live below port, so the function depends only on its own
//     package's existing imports (port already imports session).
//
// READ-ONLY PROJECTION (Risk #1). This MUST NOT mutate the recorded
// *session.ToolResult. It builds a FRESH slice of the surviving blocks; the input
// tr (and tr.Parts) is never touched. The recorded history, the client stream, and
// the model view stay identical (the effective-payload guarantee / gauntlet-#7).
// The provider computes the projection on the fly when building its request
// message; nothing is written back into the session.
//
// CAPABILITY GATING. A block survives when the (provider, model) can receive it:
//   - BlockImage      survives iff caps.Image
//   - BlockAudio      survives iff caps.Audio
//   - BlockText, BlockResourceLink, BlockEmbeddedResource,
//     BlockStructuredContent survive (text-summarised, no modality gate) UNLESS
//     they render to EMPTY text — see the empty-render rule below.
//
// EMPTY-RENDER RULE. A text-summarised block whose rendered text
// (session.ToolBlockText) is the empty string is DROPPED. Strict providers reject
// an empty text content block on the wire: Moonshot via OpenRouter (POST
// /responses) 400s the WHOLE request with "Invalid request: text content is
// empty", and the Anthropic Messages API rejects "text content blocks must be
// non-empty". Because the LLM adapters replay full history STATELESSLY, a single
// empty-text tool-result block (e.g. an MCP fetch past the end of a document that
// returned one empty text block) poisons EVERY subsequent request and permanently
// bricks the session. Dropping it here — a REQUEST-TIME projection, never a
// history rewrite — heals an already-poisoned persisted session on replay while
// leaving the recorded history untouched (the read-only-projection discipline
// above). If ALL blocks drop, the len(out)==0 → nil return routes the caller to
// the single-string Content fallback.
//
// CALLER CONTRACT. When you degrade to tr.Content (this returns nil) and that
// string is ITSELF empty, you MUST substitute a non-empty deterministic
// placeholder before putting it on the wire — an empty string on the wire
// reproduces the exact strict-provider rejection this rule exists to prevent. The
// in-tree adapters use "(tool returned no output)".
//
// The rule is EXACT-EMPTY (session.ToolBlockText(b) == ""): a whitespace-only
// block is deliberately NOT dropped. Widening to whitespace-trimming would be a
// second behavior change on this published surface and waits for evidence a
// provider actually rejects whitespace-only text.
//
// A legacy media part (BlockKind == "", the user-message media shape that never
// rides on a tool result) is left to the user-message media path and dropped here
// — it is not a tool-result block.
//
// AUDIENCE IS NEVER A FILTER (CWE-345). Content.Audience is UNTRUSTED server
// self-attestation carried for advisory display routing ONLY. It MUST NEVER
// suppress model-facing content and NEVER gates access control; enforcement lives
// in the permission layer. This function does not read Audience at all, so a
// ["user"]-audience block still passes to the model exactly as a [] block does.
func RouteToolResultParts(tr session.ToolResult, caps ProviderCapabilities) []session.Content {
	if len(tr.Parts) == 0 {
		return nil
	}
	out := make([]session.Content, 0, len(tr.Parts))
	for _, b := range tr.Parts {
		switch b.BlockKind {
		case session.BlockImage:
			if !caps.Image {
				continue
			}
		case session.BlockAudio:
			if !caps.Audio {
				continue
			}
		case "":
			// A legacy media part is not a tool-result block; leave it to the
			// user-message media path. Dropping it here keeps the projection honest
			// (a tool result never carries legacy media).
			continue
		case session.BlockText, session.BlockResourceLink, session.BlockEmbeddedResource, session.BlockStructuredContent, session.BlockArtifact:
			// Text-summarised blocks survive — no modality gate — UNLESS they render
			// to empty text: a strict provider (Moonshot via OpenRouter, Anthropic)
			// rejects an empty text content block, and stateless full-replay makes
			// that rejection permanent, so drop the block rather than put "" on the
			// wire. See the EMPTY-RENDER RULE in the doc comment.
			if session.ToolBlockText(b) == "" {
				continue
			}
		default:
			// Fail CLOSED on an unrecognised BlockKind: skip it rather than pass it
			// ungated to the provider. This is a second-line guard — the only
			// production Parts producer runs session.ValidateToolResultParts, which
			// rejects unknown kinds upstream — but a future/unknown BlockKind must
			// never fall through as if it were an always-survives block.
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
