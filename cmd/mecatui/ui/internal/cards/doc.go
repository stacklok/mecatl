// Package cards prepares the structured, non-Markdown cards in mecatui's
// conversation view.
//
// It is an internal package, but its boundary is intentionally stable enough
// for other UI components and contributors to use. Cards is a pure presentation
// layer: callers provide immutable snapshots and resolved presentation inputs;
// preparation returns final card-local lines plus lockstep structural rows. It
// does not know about conversations, session events, viewports, Bubble Tea
// models, block identity, or cache lifetime.
//
// The major pieces are:
//
//   - Prepared and Row: final lines and their semantic provenance. A Row has
//     the semantic region, visible-text offset, fallback row, leading column,
//     and grapheme span needed by the parent UI to build frame provenance
//     without parsing decorated output.
//   - styled_tool.go and styled_tool_rows.go: the framed tool-card compiler and
//     its layout/provenance reconciliation for headers, arguments, results,
//     artifacts, diffs, and delegation output.
//   - plain.go and plain_cards.go: shared plain-card width and terminal-safety
//     handling plus User, Notice, Hook, Delivery, and TurnStat preparation.
//   - error_cards.go: transient and permanent error presentation, including the
//     collapsed permanent-error summary and expanded raw payload.
//
// The parent ui package owns the rest of the rendering pipeline. Conversation
// mutation advances each block's content revision; the renderer combines that
// revision with shared view context (width, expansion, theme/keymap generation,
// and rendering dialect) to decide cache reuse. After a cache miss, it adapts
// Prepared.Rows into its document-local frame by attaching block identity,
// indentation, kind, and inter-block separators.
package cards
