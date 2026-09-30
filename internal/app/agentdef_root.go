package app

import (
	"context"
	"fmt"
	"strings"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// buildAgentDefRootEngine builds an agent-bound SESSION-ROOT engine (ADR
// 0353): the SAME catalog/prompt/hooks/memory core buildAgentDefEngine's three
// existing child call sites get (resolvedAgentDefCatalog), but bottomed out
// into ORDINARY main-session Deps (engineDepsForProvider + attachGuardrailReviewer)
// instead of the child-shaped newChildEngineForProvider — so guardrails stay
// wired, the governance audience is AudienceMain (via the caller-supplied main
// policy), and the ask-flow is the normal awaiting/approve path (AC1.6), never
// the child-shaped headless auto-deny or optional ask-reviewer.
//
// allowMutating is always true here (mirroring buildAgentWritableEngineFactories):
// a session root is never forced read-only the way a Subagent delegate is —
// mutation is available iff the def's own `tools:` allows it (AC1.11); the
// def's allowlist (scopedToolNamesMode's step 1) is what actually decides
// which tools appear, this flag only controls whether a mutating tool named by
// the def is force-DROPPED (it must not be). runner is the caller's
// already-attenuated command runner — nil under `profile: "no-fs"`, so
// allowShell is false too: the profile/deployment ceiling narrows the
// AVAILABLE base BEFORE the def's own allowlist is ever consulted, composing
// by intersection (AC1.15).
//
// The returned session.Authority is minted via the EXISTING mintRootAuthority
// over THIS def's own resolved catalog + resource-capability list (AC1.9) —
// never cfg.RootAuthority(kind), the deployment's build-time default closure.
func buildAgentDefRootEngine(
	ctx context.Context,
	cfg Config,
	def agents.AgentDef,
	source string,
	provider port.LLMProvider,
	providerModel session.ProviderModelID,
	windowFn func() int,
	base map[string]tool.Tool,
	allowShell bool,
	skillIdx skillIndex,
	defaultHooks port.HookRunner,
	runner tool.CommandRunner,
	mainMgr *mcp.Manager,
	store port.SessionStore,
	policy port.PermissionPolicy,
	mcpProvider mcp.Provider,
	instructions prompt.InstructionAssembler,
	provReg *providerRegistry,
	parentProviderID string,
	guardrailWaiver *modelhook.WaiverHolder,
) (*agent.Engine, func() error, session.Authority) {
	model := providerModel.ModelID
	cat, pc, hooks, mcpClose, _, resources, _ := resolvedAgentDefCatalog(
		ctx, cfg, def, source, model, base, true /*allowMutating*/, allowShell, skillIdx, defaultHooks, runner, mainMgr)

	deps := engineDepsForProvider(cfg, provider, providerModel, windowFn, store, policy, hooks, mcpProvider, instructions)
	deps.Catalog = cat
	deps.PromptConfig = pc
	// Deps.Role is deliberately left at engineDepsForProvider's default (empty)
	// — NOT set to role, despite role being exactly the diagnostic label a
	// child-shaped engine would carry there. engine/agent uses `Role != ""`
	// throughout (loop.go, steer.go, actionreview.go, dispatch.go) as the
	// signal that an engine is a delegated child, gating principal-prompt
	// trust, review-principal establishment, and external-authorization
	// presentation accordingly. This engine is a SESSION ROOT (AC1.6:
	// "ordinary main-session behavior... in every respect except its tool
	// catalog") — setting a non-empty Role here would silently mis-trigger
	// every one of those child-only behaviors for it (confirmed: doing so
	// made every "acceptable" contextual-guardrail verdict untrustworthy via
	// unestablished principal facts, and deadlocked engine/agent's existing
	// child-fixture tests that construct a Deps{Role: ...} literal directly
	// and rely on Role alone to mean "child"). The bound AgentDef's name is
	// already logged once per engine build by this function's caller
	// (agentDefSessionEngineFactory's "agent-bound session engine built" log),
	// so no diagnostic value is lost by leaving Role empty here.
	// Guardrails (ADR 0363): the SAME attach step both existing main-engine call
	// sites use (build.go's shared engine and per-session factory) — never the
	// removed buildGuardrailsHooks. This is what keeps an agent-bound session's
	// contextual review indistinguishable from any other main session's (AC1.6).
	attachGuardrailReviewer(&deps, buildGuardrailsActionReviewer(cfg, provReg, provider, parentProviderID, guardrailWaiver), cfg.guardrailDetails)

	// The session's minted Authority is derived from THIS def's own resolved
	// catalog + resource-capability list — never cfg.RootAuthority(kind), the
	// deployment's build-time default closure (AC1.9). session.SessionKindMain is
	// the only kind an agent-bound session can ever be (AC1.5 rejects the debug
	// combination at the create boundary before this ever runs).
	authority := mintRootAuthority(cat, resources, session.SessionKindMain)
	// Ceiling (AC3.1) is set HERE, at mint time, to a deep copy of the SAME
	// CapabilitySet just minted — the one place an agent-bound session's
	// Authority is ever constructed. Without this, GrantToolAuthority /
	// CompleteWorkspaceEnrollment's Ceiling checks (engine/session, AC3.2) are
	// unreachable for every real session: BindAuthority only ever sees whatever
	// Ceiling this caller supplies, and a nil Ceiling means "unrestricted."
	ceiling := governance.CapabilitySet{
		Tools:                    append([]string(nil), authority.CapabilitySet.Tools...),
		RemainingDelegationDepth: authority.CapabilitySet.RemainingDelegationDepth,
		FileSystem:               authority.CapabilitySet.FileSystem,
		DirectWrite:              authority.CapabilitySet.DirectWrite,
	}
	authority.Ceiling = &ceiling
	return agent.NewEngine(deps), mcpClose, authority
}

// agentDefLookup resolves defName against reg, tolerating a nil registry (no
// agent-definition source configured) or an empty name — both report "not
// found" rather than panicking, so agentDefSessionEngineFactory's caller
// always gets a clean ErrInvalidArgument instead of a nil-pointer fault.
func agentDefLookup(reg *agents.Registry, name string) (agents.AgentDef, bool) {
	if reg == nil || strings.TrimSpace(name) == "" {
		return agents.AgentDef{}, false
	}
	return reg.Get(name)
}

// agentDefSessionEngineFactory builds the server.AgentDefSessionEngineFactory
// that binds a session's root to a named AgentDef (ADR 0353, Task B). It
// closes over the SAME collaborators the three existing buildAgentDefEngine
// call sites already close over (the agent registry, the skill index, the
// default hook runner, the global MCP manager) plus the ordinary main-session
// store/policy/instructions/guardrailWaiver — so the resulting engine's
// guardrail wiring and governance audience are byte-identical to any other
// main session (AC1.6). An unresolvable defName is ErrInvalidArgument,
// mirroring how an unknown provider_id is handled today (AC1.4) —
// agentDefLookup resolves it through the SAME AgentDefSource-backed registry
// every other call site uses; there is no separate discovery path for a
// session-root binding (AC1.17).
//
// sel is ADR 0353's 5-case provider/model precedence input (Task C,
// resolveAgentDefRootProviderModel — a resolver DISTINCT from
// resolveChildProvider/resolveProviderModel: those implement the existing
// Subagent-delegate precedence (def-wins-over-parent, silent fallback on an
// unavailable def-pinned provider), which is the WRONG contract for this
// factory's case 5 (an unavailable def-pinned provider must FAIL session
// creation, never silently fall back). limits is the request's RAW Limits
// (createSession skips its general WithDefaults fold for an agent-bound
// create so a zero field still means "not supplied" here); mode is the
// request's resolved PermissionMode. Both are clamped tighten-only against
// the def's own configured ceiling (AC1.12/AC1.13) and returned via
// SessionEngineResult.Limits/BuiltForMode for the caller to persist.
func agentDefSessionEngineFactory(
	cfg Config,
	provReg *providerRegistry,
	provider port.LLMProvider,
	store port.SessionStore,
	policy port.PermissionPolicy,
	hooks port.HookRunner,
	mcpProvider mcp.Provider,
	instructions prompt.InstructionAssembler,
	assets catalogAssets,
	guardrailWaiver *modelhook.WaiverHolder,
) server.AgentDefSessionEngineFactory {
	mainRunner := buildCommandRunner(cfg)
	return func(ctx context.Context, sel server.ProviderSelector, profile server.SessionProfile, mode session.PermissionMode, limits session.Limits, defName string) (server.SessionEngineResult, error) {
		def, ok := agentDefLookup(assets.agentReg, defName)
		if !ok {
			return server.SessionEngineResult{}, fmt.Errorf("%w: unknown agent definition %q", server.ErrInvalidArgument, defName)
		}

		noFS := profile == server.ProfileNoFS
		base := baseSubagentTools(cfg)
		runner := mainRunner
		if noFS {
			base = baseSubagentToolsNoFS()
			runner = nil
		}
		allowShell := runner != nil

		parentProviderID := provReg.Default()
		childProvider, pid, model, windowFn, err := resolveAgentDefRootProviderModel(cfg, provReg, def, provider, parentProviderID, sel)
		if err != nil {
			return server.SessionEngineResult{}, err
		}

		// ADR 0353 tighten-only clamps (AC1.12/AC1.13): the def's own configured
		// permissionMode/Limits are the ceiling; the request may only narrow them.
		clampedMode := clampPermissionMode(resolvePermissionMode(cfg.diag(), def), mode)
		clampedLimits := tightenLimits(defLimits(def, defaultLimits()), limits)

		eng, mcpClose, authority := buildAgentDefRootEngine(ctx, cfg, def, assets.agentReg.Detail(def.Name),
			childProvider, session.ProviderModelID{ProviderID: pid, ModelID: model}, windowFn, base, allowShell, assets.skillIndex, hooks, runner, assets.globalMgr,
			store, policy, mcpProvider, instructions, provReg, parentProviderID, guardrailWaiver)

		closeFn := func() error {
			if mcpClose != nil {
				return mcpClose()
			}
			return nil
		}

		cfg.diag().Log(ctx, port.LevelInfo, "agent-bound session engine built",
			"agent", def.Name, "provider", pid, "model", model, "no_fs", noFS)

		return server.SessionEngineResult{
			Engine:       eng,
			Capabilities: modelCapability(provReg, pid, model),
			ProviderID:   pid,
			ModelID:      model,
			BuiltForMode: clampedMode,
			Limits:       &clampedLimits,
			Authority:    authority,
			Close:        closeFn,
		}, nil
	}
}
