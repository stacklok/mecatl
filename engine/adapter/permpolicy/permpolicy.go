// Package permpolicy adapts the session-free permission Evaluator in package
// governance to the port.PermissionPolicy interface, which is expressed in terms
// of session types (session.PermissionMode, session.ToolCall).
//
// It lives in the adapter layer rather than in package governance for a
// structural reason: governance MUST NOT import session (session already imports
// governance, so the reverse edge would form an import cycle, and governance's
// doc.go forbids it). port.PermissionPolicy's signature uses session types, so
// any concrete implementation of it must import session — and therefore cannot
// live in governance. This package is that thin translation seam; the actual
// deny → ask → allow logic, compound-Shell splitting and plan-mode gating all
// live in package governance and are unit-tested there.
package permpolicy

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// RuleResolver supplies the FILE-BASED permission rules that apply to a given
// session workspace (issue #13). The composition layer injects a concrete
// resolver (the permconfig adapter) that discovers `.mecatl/settings.yaml` (and
// Claude-imported settings.json) under the workspace root and the user-global XDG
// location, gates project ALLOW rules behind a trust flag, and caches the result
// per root.
//
// It is the per-session analogue of the learned-rule store: where the store keys
// rules by sessionID, the resolver keys them by ws.Root(). The policy merges both
// into the SAME lowest-scope `extra` channel of the governance Evaluator, so a
// config rule and a learned rule share the deny-dominant fold — neither can ever
// out-rank or weaken a configured/static deny or ask.
//
// Resolve is called on EVERY Evaluate (so a session always sees the config for ITS
// workspace), so implementations MUST cache: discovery is file I/O. A nil ws means
// "no project config" — the resolver should return only its user-global rules (or
// nil). A nil resolver disables file-based config entirely.
type RuleResolver interface {
	// Resolve returns the permission rules that apply to the given workspace,
	// already tagged with their config scope (project vs user). The caller treats
	// the result as additive extras at evaluation time. ws is read-only (Root +
	// Read + Stat) and may be nil.
	Resolve(ctx context.Context, ws tool.WorkspaceReader) []governance.Rule
}

// Policy implements port.PermissionPolicy by delegating to a governance
// Evaluator. It maps the session.PermissionMode posture onto the Evaluator's
// plan-mode flag and forwards the tool call's name and raw arguments.
//
// It also threads an optional per-session LEARNED-rule store (issue #3): on an
// "allow always" verdict the agent loop calls Learn, which derives a narrow
// tool+exact-pattern rule and records it in the store; every Evaluate then merges
// that session's learned rules in at the LOWEST scope. The store is optional —
// when nil, Learn is a no-op and Evaluate behaves exactly as the static policy.
//
// A third, optional collaborator is the RuleResolver (issue #13): file-based
// permission config, re-resolved PER SESSION against the session's workspace root.
// Resolver rules ride the SAME lowest-scope `extra` channel as learned rules, so
// they merge into the deny-dominant fold alongside them. The resolver is optional —
// when nil, Evaluate consults only the static rules + learned rules (today's
// behaviour), which is what the allow-all demo/forked-member policies want.
type Policy struct {
	eval      *governance.Evaluator // standard evaluator (no loose substitution)
	evalLoose *governance.Evaluator // yolo evaluator (with loose substitution for ModeYolo)
	store     port.PermissionStore
	resolver  RuleResolver
}

// allowAllCLIRules returns the mode-driven allow-all rules injected by ModeAuto
// and ModeYolo at evaluation time (not at startup). ScopeCLI is above the
// built-in floor (ScopeBuiltinDefault), so a configured Deny or Ask in any
// scope above the floor still wins — the same guarantee the startup yoloAllowAllRule
// provided. It uses no Audience tag (AudienceAll), so it matches both main and
// child evaluators; for child engines the child floor already covers mutates, so
// this is harmless redundancy.
func allowAllCLIRules() []governance.Rule {
	return []governance.Rule{{Scope: governance.ScopeCLI, Effect: governance.Allow}}
}

// AllowAllFloorRules returns the CANONICAL "default child posture" ruleset: a
// single allow-all at ScopeBuiltinDefault. The floor scope is load-bearing for
// the issue-#32 typed provenance: a blanket allow-all must NOT register as a
// CONFIGURED rule (scope above the floor), or every substitution-floored child
// ask would receive configured-allow-floor provenance and auto-approve instead
// of surfacing through the child-ask model. It is the
// SINGLE source both the composition layer (internal/app childRules) and every
// fixture that means "default child posture" build from, so the two cannot
// drift. A fresh slice is returned per call (callers may append configured
// rules). Equivalent to a zero-Scope allow-all when no configured rules are
// present (the floor exception keys on the ASK side's
// scope, never the allow's).
func AllowAllFloorRules() []governance.Rule {
	return []governance.Rule{{Scope: governance.ScopeBuiltinDefault, Effect: governance.Allow}}
}

// NewPolicy constructs a Policy over the given merged permission rules (across
// any mix of Scopes) and an optional per-session learned-rule store. Precedence
// (deny → ask → allow, higher Scope wins among same-effect conflicts) is resolved
// per call by the underlying Evaluator. A nil store disables rule learning: Learn
// is a no-op and Evaluate consults only the static rules.
//
// Optional governance.EvaluatorOptions are forwarded to the underlying Evaluator.
// A second evalLoose evaluator is built with WithLooseSubstitution(true) added;
// it is selected by ModeYolo at Evaluate time. The resolver (file-based config)
// is unset; use NewPolicyWithResolver to wire it.
func NewPolicy(rules []governance.Rule, store port.PermissionStore, evalOpts ...governance.EvaluatorOption) *Policy {
	looseOpts := append(append([]governance.EvaluatorOption{}, evalOpts...), governance.WithLooseSubstitution(true))
	return &Policy{
		eval:      governance.NewEvaluator(rules, evalOpts...),
		evalLoose: governance.NewEvaluator(rules, looseOpts...),
		store:     store,
	}
}

// NewPolicyWithResolver is NewPolicy plus a RuleResolver that supplies file-based
// permission rules re-resolved per session against the session's workspace root
// (issue #13). A nil resolver behaves exactly like NewPolicy. Optional
// governance.EvaluatorOptions are forwarded to the underlying Evaluator.
func NewPolicyWithResolver(rules []governance.Rule, store port.PermissionStore, resolver RuleResolver, evalOpts ...governance.EvaluatorOption) *Policy {
	looseOpts := append(append([]governance.EvaluatorOption{}, evalOpts...), governance.WithLooseSubstitution(true))
	return &Policy{
		eval:      governance.NewEvaluator(rules, evalOpts...),
		evalLoose: governance.NewEvaluator(rules, looseOpts...),
		store:     store,
		resolver:  resolver,
	}
}

// acceptEditsRules are the floor-loosening ALLOW rules session.ModeAccept
// ("accept-edits") contributes: Edit and Write at ScopeCLI. Being scoped ABOVE
// ScopeBuiltinDefault, they can only ever LOOSEN the built-in mutate-ask floor
// (resolveSimpleRule's existing issue-#13 mechanic: a higher-precedence Allow
// may loosen ONLY an Ask scoped at ScopeBuiltinDefault) — they can never suppress
// a CONFIGURED deny/ask for Edit/Write from any scope above the floor, and Shell
// is untouched (accept-edits auto-accepts file edits only, mirroring the
// client-visible "accept edits" contract; it is not a broader auto-run mode).
var acceptEditsRules = []governance.Rule{
	{Scope: governance.ScopeCLI, Tool: "Edit", Effect: governance.Allow},
	{Scope: governance.ScopeCLI, Tool: "Write", Effect: governance.Allow},
}

// Evaluate returns the permission decision for tool call c under mode, scoped to
// sessionID and the session workspace ws.
//
// Mode drives the full effective policy at evaluation time:
//   - ModePlan: hard-deny of mutating tools BEFORE any rule (plan-mode gate).
//   - ModeAccept: injects acceptEditsRules (Edit/Write auto-allow).
//   - ModeAuto: injects allowAllCLIRules() — a ScopeCLI allow-all that loosens
//     the built-in mutate-ask floor. Configured deny/ask in any scope still win.
//   - ModeYolo: same as ModeAuto plus loose substitution (the built-in
//     substitution Ask floor is lifted so $()/backtick commands auto-run).
//   - ModeDefault: static rules + learned allows only.
//
// Allow-all behavior is NOT baked into the evaluator's base rules at startup;
// it is injected here based on the session's CURRENT mode. Switching from
// yolo→default actually removes the allow-all floor. A configured Deny in any
// scope and a deliberately configured Ask always win.
func (p *Policy) Evaluate(ctx context.Context, sessionID session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) port.PermissionResult {
	var extra []governance.Rule
	planMode := mode == session.ModePlan
	eval := p.eval // default: standard evaluator, no loose substitution

	switch mode {
	case session.ModeAccept:
		extra = append(extra, acceptEditsRules...)
	case session.ModeAuto:
		extra = append(extra, allowAllCLIRules()...)
	case session.ModeYolo:
		extra = append(extra, allowAllCLIRules()...)
		eval = p.evalLoose // yolo: loose substitution enabled
	}
	if p.store != nil {
		extra = append(extra, p.store.Rules(sessionID)...)
	}
	if p.resolver != nil {
		extra = append(extra, p.resolver.Resolve(ctx, ws)...)
	}
	return port.PermissionResult{Decision: eval.EvaluateWith(c.Name, c.Args, planMode, extra)}
}

// Learn records a per-session allow rule for tool call c (the model's "allow
// always" verdict). It derives the rule via governance.LearnableRule and stores
// it only when the call is safely learnable (a single, non-substituted Shell
// command, or a non-Shell call with a targetable pattern); otherwise it is a
// no-op. With no store configured it is always a no-op.
func (p *Policy) Learn(sessionID session.SessionID, c session.ToolCall) {
	if p.store == nil {
		return
	}
	if rule, ok := p.eval.LearnableRule(c.Name, c.Args); ok {
		p.store.Record(sessionID, rule)
	}
}
