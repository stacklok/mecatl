package sessiondebug

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// PermissionPolicy preserves deployment denies and configured asks while forcing
// every selected debug MCP call through a fresh human approval. Read-only
// annotations do not weaken this rule because outbound reads can disclose
// target-derived data.
type PermissionPolicy struct {
	base                port.PermissionPolicy
	store               port.SessionStore
	target              session.SessionID
	expectedFingerprint string
	expectedOwnerScope  [32]byte
	ownershipEnforced   bool
	headless            bool
	selected            map[string]bool
}

// NewPermissionPolicy decorates base for the selected direct MCP tools.
func NewPermissionPolicy(base port.PermissionPolicy, store port.SessionStore, target session.SessionID, expectedFingerprint string, expectedOwner *session.Principal, ownershipEnforced, headless bool, mounted []tool.Tool) *PermissionPolicy {
	selected := make(map[string]bool, len(mounted))
	for _, candidate := range mounted {
		selected[candidate.Spec().Name] = true
	}
	return &PermissionPolicy{
		base: base, store: store, target: target, expectedFingerprint: expectedFingerprint,
		expectedOwnerScope: session.PrincipalScopeHash(expectedOwner), ownershipEnforced: ownershipEnforced,
		headless: headless, selected: selected,
	}
}

// Evaluate delegates to the base policy first. It preserves base denies and
// configured asks, grants InspectSession only as a debugger floor, and forces
// selected direct MCP calls through the debug approval posture after
// revalidating the target incarnation.
func (p *PermissionPolicy) Evaluate(ctx context.Context, id session.SessionID, mode session.PermissionMode, call session.ToolCall, ws tool.WorkspaceReader) governance.PermissionDecision {
	decision := p.base.Evaluate(ctx, id, mode, call, ws)
	if call.Name == ToolName {
		if decision.Effect == governance.Deny || decision.Effect == governance.Ask && decision.AskProvenance == governance.AskProvenanceConfigured {
			return decision
		}
		return governance.PermissionDecision{Effect: governance.Allow, Reason: "target-bound debug evidence is read-only"}
	}
	if !p.selected[call.Name] {
		return decision
	}
	if decision.Effect == governance.Deny || decision.Effect == governance.Ask && decision.AskProvenance == governance.AskProvenanceConfigured {
		return decision
	}
	target, err := p.store.Load(ctx, p.target)
	principal := session.PrincipalFromContext(ctx)
	if err != nil || target == nil || target.ID != p.target ||
		session.DebugTargetFingerprint(target) != p.expectedFingerprint ||
		p.ownershipEnforced && (session.PrincipalScopeHash(target.Owner) != p.expectedOwnerScope || principal == nil || session.PrincipalScopeHash(principal) != p.expectedOwnerScope) {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "debug target is unavailable"}
	}
	if p.headless {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "debug MCP calls require an interactive operator"}
	}
	return governance.PermissionDecision{Effect: governance.Ask, Reason: "debug MCP call requires fresh current operator approval"}
}

// Learn deliberately never persists approvals for selected debug MCP calls.
func (p *PermissionPolicy) Learn(id session.SessionID, call session.ToolCall) {
	if p.selected[call.Name] {
		return
	}
	p.base.Learn(id, call)
}

var _ port.PermissionPolicy = (*PermissionPolicy)(nil)
