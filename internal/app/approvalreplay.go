package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// replayApprovals builds the composition closure that repopulates the in-memory
// learned-rule store from a loaded session's durable EventLog allow-always
// verdicts (cloud-native Phase 3b). It is the CONSUMER of 3a's EvApproval
// AllowAlways flag, and it kills the Phase 2 re-ask wart: the permstore is
// in-memory and lost on restart, so without this a tool the user allow-always'd
// before a restart would re-ask on the first post-restart call.
//
// CORRELATION (metadata-only event → real rule from history): the EvApproval is
// deliberately metadata-only — it carries the tool NAME, the verdict, the askID,
// the gated tool-call id (Call), and the AllowAlways flag, but NEVER the raw tool
// args (gauntlet #7), so the governance.Rule cannot be reconstructed from the event
// alone. The event's Call field is the opaque ToolCall id (not secret — already
// implicitly inside the askID), so for each allow-always verdict this looks the
// ToolCall up by Call in the LOADED conversation and re-drives Policy.Learn on THAT
// call — the exact same Learn path the live verdict took, which re-derives the
// narrow tool+pattern rule via governance.LearnableRule and Records it into the
// per-session permstore. The real args come from the session's own history (a trust
// boundary it already crossed), never from the durable log, so the log stays
// metadata-only and there is no leak.
//
// It returns nil when there is no durable EventLog (the memstore/driver paths that
// have no cross-restart log to replay), making the Service's hook a no-op and
// preserving the in-memory-store behaviour byte-identical there.
func replayApprovals(log port.EventLog, policy port.PermissionPolicy, diag port.Diagnostics) func(context.Context, *session.Session) {
	if log == nil || policy == nil {
		return nil
	}
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return func(ctx context.Context, sess *session.Session) {
		// Index the loaded conversation's tool calls by id once, so each verdict's
		// correlation is an O(1) map lookup rather than an O(messages) walk.
		byID := make(map[session.ToolCallID]session.ToolCall)
		for _, m := range sess.Conversation.Messages {
			for _, c := range m.ToolCalls {
				byID[c.ID] = c
			}
		}
		for ev, err := range log.Read(ctx, sess.ID) {
			if err != nil {
				// An infra fault reading the log must not block the run: WARN and stop
				// replaying (the iterator yields nothing further after an error). A
				// session that cannot replay its verdicts simply re-asks — fail-safe.
				diag.Log(ctx, port.LevelWarn, "approval replay: event log read failed",
					"session", string(sess.ID), "err", err.Error())
				return
			}
			// SECURITY: filter on AllowAlways. A deny or allow-once verdict must NEVER
			// be replayed — only allow-always learns a durable per-session rule; a
			// one-time grant or a denial that survived a restart as a learned allow
			// would be a silent re-grant of a permission the user never gave.
			if ev.Type != session.EvApproval || ev.Approval == nil || !ev.Approval.AllowAlways || ev.Approval.Origin != session.ApprovalOriginPermission {
				continue
			}
			if ev.Approval.Call == "" {
				continue
			}
			call, ok := byID[ev.Approval.Call]
			if !ok {
				// The call this verdict gated is no longer in the loaded history (e.g.
				// dropped by a later compaction). Nothing to re-derive from; the rule
				// is simply not restored — fail-safe toward re-asking, never a silent
				// allow.
				continue
			}
			// Re-drive the SAME Learn path the live verdict took. Learn re-derives the
			// narrow tool+pattern rule from the real ToolCall and Records it (idempotent
			// — a repeat is a no-op dedup), so calling this on every loadAndReopen would
			// be harmless; the Service gates it to once-per-id regardless.
			policy.Learn(sess.ID, call)
		}
	}
}
