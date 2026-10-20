package acp

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// replay.go reconstructs an editor's transcript on session/load by re-projecting
// the persisted Conversation through the SAME projectUpdate path the live prompt
// loop uses. A re-attaching editor would otherwise see an empty transcript: the
// session state is restored, but none of the prior turns' session/update
// notifications are resent. historyEvents synthesizes the domain Events those
// turns would have emitted (in history order), and replayHistory drives them
// through projectUpdate -> notifyUpdate.
//
// INVARIANTS preserved here:
//
//   - OPEN-BEFORE-UPDATE: a tool_call (the open card) must precede any
//     tool_call_update for the same id, or a client may drop the update. This
//     holds NATURALLY from history order — the assistant message (carrying its
//     ToolCalls) is appended to the Conversation before the tool-role message
//     carrying the result, so EvToolCall is always emitted before the matching
//     EvToolResult.
//
// DELIBERATELY NOT replayed:
//
//   - RoleUser: ACP has no user_message_chunk; the editor renders the user's own
//     turns locally, and the live prompt path never emits a session/update for the
//     user's prompt either (see handleSessionPrompt — only the run's Events are
//     projected, and the prompt text is not one of them).
//   - Message.Replay: the OPAQUE provider replay carrier, NOT human-readable
//     display text. The display reasoning SUMMARY (surfaced live via
//     reasoning.delta) is never stored on the Message, so there is nothing
//     display-worthy to replay.
//   - permission.ask: an out-of-band request_permission, not a session/update; a
//     resolved historical ask leaves only its tool_call/result in history, which
//     replay does cover. We never re-prompt the editor on load.
//
// KNOWN-LOSSY on replay (faithful INTENT, not a pixel-faithful re-render — these
// abbreviate the transcript but never corrupt its content):
//
//   - Edit/Write diff blocks re-project from the call's ARGS (old_string/
//     new_string), which the live path renders BEFORE the edit, against the
//     then-current file. On replay the file already holds the edited content, so
//     an editor that renders the diff inline against the working tree may show a
//     stale base (old_string no longer present). The diff still faithfully shows
//     what the call requested; only the implied "before" state is dated.
//   - Consecutive assistant turns with no intervening tool call each project to a
//     bare agent_message_chunk; the live turn.start/turn.end boundaries are not
//     persisted, so an editor that coalesces adjacent chunks may merge two turns
//     into one block. Content is faithful; turn structure is flattened.
//
// IDEMPOTENCY: a repeated session/load re-streams the same transcript. It is
// idempotent in FINAL STATE for tool cards — each is keyed by tool-call id, so a
// re-load reopens and re-settles the same card identically (no dedupe guard
// needed). It is NOT fully idempotent for agent_message_chunk: ACP message chunks
// carry no id, so an editor that APPENDS chunks into an existing (non-fresh) view
// would duplicate the assistant text on a repeated load. In practice an editor
// reloads a session into a fresh view, so this rarely bites. Replay is
// synchronous within handleSessionLoad, so the notifications are flushed before
// the load response returns.

// historyEvents walks a Conversation's messages in order and synthesizes the
// domain Events the live loop would have emitted for them. It is PURE (no I/O, no
// shared state) so it is exhaustively table-testable, mirroring projectUpdate.
// System and user messages, and the opaque Reasoning replay blob, yield nothing
// (see the package-level rationale above).
func historyEvents(c *session.Conversation) []session.Event {
	if c == nil {
		return nil
	}
	var events []session.Event
	for _, m := range c.Messages {
		if historical := session.HistoricalMessage(m); historical != nil {
			events = append(events, session.Event{Type: session.EvHistorical, Historical: historical})
			continue
		}
		switch m.Role {
		case session.RoleSystem, session.RoleUser:
			// Nothing: system prompt is not a transcript entry; the editor renders
			// the user's own turns locally (and the live path emits no update for
			// the user's prompt either).
		case session.RoleAssistant:
			if m.Text != "" {
				events = append(events, session.Event{Type: session.EvMessageDelta, Text: m.Text})
			}
			// Skip m.Reasoning entirely: it is the opaque encrypted replay blob, not
			// display text.
			for _, tc := range m.ToolCalls {
				tc := tc // loop-local copy: avoid taking the address of the range variable
				events = append(events, session.Event{Type: session.EvToolCall, ToolCall: &tc})
			}
		case session.RoleTool:
			events = append(events, session.Event{Type: session.EvToolResult, ToolResult: m.ToolResult})
		}
	}
	return events
}

// replayHistory re-streams a persisted Conversation as session/update
// notifications by projecting each synthesized history event through the live
// projectUpdate path. A nil conversation (or one with no projectable messages) is
// a no-op. It is called synchronously from handleSessionLoad so the transcript is
// flushed to the editor before the load response returns. ctx is the load dispatch
// context, forwarded to notifyUpdate for its diagnostics trace carrier only.
func (a *Agent) replayHistory(ctx context.Context, sessionID string, c *session.Conversation) {
	if c == nil {
		return
	}
	for _, ev := range historyEvents(c) {
		for _, update := range projectUpdates(ev) {
			a.notifyUpdate(ctx, sessionID, update)
		}
	}
}
