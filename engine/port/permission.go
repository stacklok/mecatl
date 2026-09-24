package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// PermissionResult is the complete result of one permission evaluation.
// AuxiliaryUsage is non-zero only when composition performs auxiliary model work.
type PermissionResult struct {
	Decision       governance.PermissionDecision
	AuxiliaryUsage session.AuxiliaryUsage
}

// PermissionPolicy evaluates a tool call under a permission mode, resolving
// across merged scopes with deny → ask → allow precedence. It is implemented by
// the permpolicy adapter (a session-aware wrapper over the session-free
// governance.Evaluator), not by governance itself, which cannot import session.
type PermissionPolicy interface {
	// Evaluate returns the permission decision for tool call c under mode, scoped
	// to sessionID so per-session LEARNED rules (see Learn) are consulted in
	// addition to the static rule set. The learned rules only ever ADD allows at
	// the lowest scope: a static deny/ask still wins, and plan mode still
	// hard-denies mutations BEFORE any learned rule is consulted.
	//
	// ws is the session's workspace, taken as a READ-ONLY tool.WorkspaceReader
	// (Root + Read + Stat) — the policy only ever LOOKS at the workspace, never
	// mutates it. It is the discovery root for FILE-BASED permission config
	// (issue #13): a RuleResolver re-resolves the project-level
	// `.mecatl/settings.yaml` (and Claude-imported rules) against ws.Root() per
	// session, so two sessions rooted at different workspaces can resolve the SAME
	// tool call differently. ws may be nil (e.g. a child/member engine with no
	// resolver wired) — implementations must treat a nil ws as "no project config".
	Evaluate(ctx context.Context, sessionID session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) PermissionResult

	// Learn records a per-session allow rule derived from tool call c (the model's
	// "allow always" verdict). It is a no-op when c is not safely learnable (a
	// compound/substituted Shell command, or a call with no targetable pattern —
	// see governance.LearnableRule). It NEVER overrides a deny or bypasses plan
	// mode: the learned rule is consulted by Evaluate at the lowest scope only.
	Learn(sessionID session.SessionID, c session.ToolCall)
}

// PermissionStore holds the per-session LEARNED permission rules an "allow
// always" verdict records. It is the small mutable seam the otherwise-immutable
// governance.Evaluator is missing: the Evaluator stays session-free and
// immutable; this store keys learned rules by session so the policy can merge
// them in per call. Implementations must be safe for concurrent use.
type PermissionStore interface {
	// Record stores a learned rule for sessionID. Implementations should dedupe
	// identical rules so repeated "allow always" for the same call does not grow
	// the set unbounded.
	Record(sessionID session.SessionID, rule governance.Rule)
	// Rules returns a snapshot (copy) of the rules learned for sessionID, safe for
	// the caller to read without holding any lock. An unknown session yields nil.
	Rules(sessionID session.SessionID) []governance.Rule
}
