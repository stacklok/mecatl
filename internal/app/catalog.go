// Catalog assembly: the SINGLE registration path every full-session tool catalog
// goes through — the build-time shared catalog (buildCatalog) AND each per-session
// engine catalog (sessionEngineFactory). Issue #42 was the third firing of the
// same drift class (per-session catalogs silently missing tool families the shared
// catalog had: first MCP under bug #3, then Subagent/Team under Half B, then
// memory/Parallel/skills/resource-tools); assembleCatalog is the structural fix —
// there is no second registration list to forget to update.
//
// The split is Phase A vs assembly:
//
//   - Phase A (buildCatalog, ONCE per process): connect the global MCP manager,
//     open the flocked memory/user-model stores, start the consolidation
//     goroutines, resolve skills. Its outputs are the process-wide catalogAssets.
//   - Assembly (assembleCatalog, once per catalog): register every tool family
//     over those shared assets plus the per-session catalogSession inputs.
//
// The ONLY sanctioned per-session deltas vs the shared catalog are (1) the client
// MCP tools (a session's own mcpServers) and (2) the unwrapped hooks
// (maybeWrapUserModelReview decorates the MAIN engine only). Everything else must
// match — guarded by TestPerSessionCatalogMatchesSharedCatalog.

package app

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/tools"
)

// catalogAssets are the PHASE-A PRODUCTS: the collaborators buildCatalog itself
// CONSTRUCTS exactly once per process (connect/open/discover/build) and that every
// catalog assembly then shares. That is the membership criterion — "constructed by
// Phase A", not merely "process-wide": reg/store/policy/hooks are also process-wide
// but are constructed elsewhere and stay ordinary parameters. The assets are
// threaded by value into every assembleCatalog call — never re-opened/re-connected
// per session:
//
//   - globalMgr: the SHARED server-global MCP manager Build owns. Reused, never
//     reconnected; its Close is NEVER folded into a per-session close (guarded by
//     TestSessionEngineFactorySelectorCloseKeepsGlobalMCP).
//   - memStore/userModelStore: the tool.MemoryStore seams. The reference
//     implementation is the flocked *memory.Store whose SOLE construction sites
//     are buildCatalog (project memory) and buildUserModelStore (one Store per
//     dir, or the flock invariant breaks). TYPED-NIL DISCIPLINE: every
//     assignment to these fields is either a known-non-nil concrete store or an
//     untyped nil (buildUserModelStore returns the interface with untyped-nil
//     returns), so the `!= nil` registration checks stay sound on the interface
//     (guarded by TestBuildUserModelStoreDisabledReturnsNilInterface; see
//     buildEngine's permconfig note for the trap this rule prevents).
//   - skills/skillActivator/skillIndex: the skills seam resolved once at build
//     time (resolveSkillSeam — the FS snapshot or the remote driver): the
//     metadata snapshot the Skill tool enumerates and the ListSkills snapshot
//     projects, the Activator the tool loads bodies/payloads through, and the
//     name→body preload index agent definitions' `skills:` lists read.
//   - skillReadRoots: the read-only allowed roots derived inside the seam
//     (FSSource.AssetDirs per-skill dirs, or the driver asset cache), computed
//     ONCE and threaded into EVERY production osfs Workspace constructor (main
//     factory + all fork closures) via osfs.WithReadRoots — one computed value,
//     no second list to drift.
//   - forkReaper: ONE process-wide preserved-fork LRU shared by every Parallel
//     tool, so ForkPreservedCap stays a PROCESS bound (a per-session reaper would
//     multiply the cap by the number of sessions).
type catalogAssets struct {
	globalMgr      *mcp.Manager
	agentReg       *agents.Registry
	memStore       tool.MemoryStore
	userModelStore tool.MemoryStore
	skills         []tool.SkillMeta
	skillActivator skills.Activator
	skillIndex     skillIndex
	skillReadRoots []string
	forkReaper     *agent.LRUForkReaper
	// autoMerger is the ONE process-wide serializing tool.ForkMerger shared by
	// every merge-driving tool (Parallel single-branch auto-merge AND the writable
	// Subagent, mode:"read-write"). It wraps a forker.Merger in a forker.Serializing-
	// Merger so concurrent merges across sessions are serialized by a single mutex
	// (a per-session instance would not serialize cross-session). Built ONCE in
	// Phase A like forkReaper. Nil only on hand-rolled assets (the option is then
	// skipped, no auto-merge).
	autoMerger tool.ForkMerger
	// searchProvider is the process-wide tool.SearchProvider the WebSearch core
	// tool is built over (issue #26). It is resolved ONCE in buildCatalog
	// (buildSearchProvider): the operator-configured HTTP adapter when --websearch-url
	// is set, else a not-configured sentinel that returns ErrSearchUnavailable so the
	// always-present WebSearch tool surfaces an honest "ask the operator" message
	// rather than vanishing. Threaded onto the assets so every per-session catalog
	// reuses the SAME provider (issue #42 — no second resolution to drift), and into
	// the child catalogs for read-only-discovery parity with WebFetch.
	searchProvider tool.SearchProvider
}

// catalogSession is the PER-CATALOG variation: the resolved provider/model the
// session's sub-agent tools inherit as parent, the session's own client MCP
// manager (nil for the build-time shared catalog), and the diagnostics posture.
//
// narrate follows the build-once-facts diagnostics discipline: true ONLY for the
// build-time shared catalog, so the ENABLED/DISABLED narration lines fire once at
// startup and never per session/new. WARNs (anomalies: duplicate MCP names,
// registration failures) are NOT gated — they fire on whichever path hits them.
type catalogSession struct {
	provider   port.LLMProvider
	providerID string
	model      string
	clientMgr  *mcp.Manager
	narrate    bool
	// noFS selects the NO-FILESYSTEM catalog profile (the "no-fs" session
	// profile, issue #55): the core tier registers tools.NoFS() (WebFetch only)
	// instead of tools.All()+Bash, Parallel and SkillDraft are skipped (both are
	// filesystem acts — branch forks and draft files), and the Subagent/Team
	// children get the file-less child surface (noFSChildCatalog) with NO forkers
	// and NO shell. Everything else (global/client MCP, resource meta-tools,
	// Subagent trio, Team/InspectMember, the six memory tools, Skill) registers
	// EXACTLY as in the default profile — guarded by TestNoFSCatalogProfile,
	// which pins the EXACT name-set delta. Always false for the build-time shared
	// catalog (a process always has a default-profile shared engine).
	noFS bool
}

// assembleCatalog registers every tool family into a fresh catalog, in the
// canonical order (which preserves the global-wins MCP precedence — mcp.Register
// is first-wins + skip-and-continue):
//
//	core → server-global MCP (+ resource meta-tools) → client MCP →
//	Subagent/InspectSubagent/SubagentStatus → Parallel → Team/InspectMember →
//	memory → user-model → Skill → SkillDraft
//
// The returned close aggregates ONLY this catalog's own teardown — the Subagent
// per-def inline-MCP managers and the session's client MCP manager. It NEVER
// includes a.globalMgr (Build owns that lifecycle; a per-session CloseSession
// closing it would kill MCP for every other session). The close is always
// non-nil and safe to call.
func assembleCatalog(ctx context.Context, cfg Config, reg *providerRegistry, store port.SessionStore, hooks port.HookRunner, a catalogAssets, s catalogSession) (*tool.Catalog, func() error) {
	cat := tool.NewCatalog()
	registerCoreTools(cfg, cat, s.narrate, s.noFS, a.searchProvider)

	mountGlobalMCP(ctx, cfg, cat, a, s)
	clientClose := mountClientMCP(ctx, cfg, cat, s)

	// refMgr is the mainMgr for Subagent/member defs' MCP `reference:` resolution:
	// prefer the SHARED global manager (parity with the build-time path), falling
	// back to the session's client manager when there is no global one.
	refMgr := a.globalMgr
	if refMgr == nil {
		refMgr = s.clientMgr
	}
	subagentClose := registerSubagentTrio(ctx, cfg, cat, reg, store, hooks, a, s, refMgr)
	// Parallel is ABSENT under the no-FS profile (not merely disarmed): every
	// branch is a force-copy filesystem fork and the deliverable is a preserved
	// fork PATH — both meaningless without a filesystem.
	if !s.noFS {
		registerParallelTool(ctx, cfg, cat, reg, store, hooks, a, s)
	}
	registerTeamTools(ctx, cfg, cat, reg, store, a, s, refMgr)
	registerMemoryFamilies(ctx, cfg, cat, a)
	registerSkillFamily(ctx, cfg, cat, a, s)

	closeFn := composeCloseErr(subagentClose, clientClose)
	if closeFn == nil {
		closeFn = func() error { return nil }
	}
	return cat, closeFn
}

// mountGlobalMCP mounts the server-global MCP tools (cfg.MCPServers + ToolHive),
// reused from the SHARED already-connected manager — never reconnected here.
// Mounted BEFORE the client specs so a client tool colliding with a global one
// loses. The WARN names the dropped tools: this tier is a within-/across-global
// clash (a defective server advertising a duplicate name).
func mountGlobalMCP(ctx context.Context, cfg Config, cat *tool.Catalog, a catalogAssets, s catalogSession) {
	if a.globalMgr == nil {
		return
	}
	if skipped, rerr := mcp.Register(cat, a.globalMgr.Tools()); rerr != nil {
		cfg.diag().Log(ctx, port.LevelWarn,
			"server-global MCP: skipped duplicate tool name(s) (a server advertised a name already registered): "+strings.Join(skipped, ", "),
			"tools", strings.Join(skipped, ", "), "err", rerr)
	}
	// The MCP resource meta-tools (ListMcpResources/ReadMcpResource) ride the
	// GLOBAL manager — issue #42 family (6): a selector session must carry them
	// too when enabled.
	if !cfg.MCPResourceTools {
		if s.narrate {
			cfg.diag().Log(ctx, port.LevelInfo, "MCP resource tools DISABLED")
		}
		return
	}
	registered, rerr := mcp.RegisterResourceTools(cat, a.globalMgr)
	switch {
	case rerr != nil:
		cfg.diag().Log(ctx, port.LevelWarn, "registering MCP resource tools failed", "err", rerr)
	case registered && s.narrate:
		cfg.diag().Log(ctx, port.LevelInfo, "MCP resource tools ENABLED (ListMcpResources/ReadMcpResource)")
	case !registered && s.narrate:
		cfg.diag().Log(ctx, port.LevelInfo, "MCP resource tools DISABLED (no connected server exposes a resource)")
	}
}

// mountClientMCP mounts the client MCP tools (the ACP session/new mcpServers)
// AFTER the global tier: this WARN is the client↔global shadow tier — the one an
// end-user reads to self-diagnose a vanished tool. The returned close is the
// client manager's Close (nil when no client manager): it IS part of this
// catalog's teardown, being session-scoped.
func mountClientMCP(ctx context.Context, cfg Config, cat *tool.Catalog, s catalogSession) func() error {
	if s.clientMgr == nil {
		return nil
	}
	if skipped, rerr := mcp.Register(cat, s.clientMgr.Tools()); rerr != nil {
		cfg.diag().Log(ctx, port.LevelWarn,
			"client MCP: tool(s) shadowed by an existing server-global tool of the same name (the global tool wins): "+strings.Join(skipped, ", "),
			"tools", strings.Join(skipped, ", "), "err", rerr)
	}
	cfg.diag().Log(ctx, port.LevelInfo, "client MCP mounted for session",
		"servers", len(s.clientMgr.Servers()), "tools", len(s.clientMgr.Tools()))
	return s.clientMgr.Close
}

// registerSubagentTrio registers the Subagent delegation tool plus its two
// companion read-only tools, with the session's resolved provider/model as the
// inherited sub-agent parent. The returned close tears down the Subagent per-def
// inline-MCP managers (these connections belong to this catalog).
func registerSubagentTrio(ctx context.Context, cfg Config, cat *tool.Catalog, reg *providerRegistry, store port.SessionStore, hooks port.HookRunner, a catalogAssets, s catalogSession, refMgr *mcp.Manager) func() error {
	subagentTool, subagentClose := buildSubagentTool(ctx, cfg, reg, s.provider, s.providerID, s.model, hooks, a.agentReg, refMgr, store, a.skillReadRoots, a.skillIndex, a, s.noFS)
	cat.MustRegister(subagentTool)
	// The PULL subagent-transcript inspect tool: read-only, reads the SAME shared
	// session store the Subagent tool persists children to (ids verbatim from the
	// result's agentId trailer). Registered unconditionally wherever Subagent is —
	// unlike InspectMember it is not gated on EnableTeams.
	cat.MustRegister(agent.NewInspectSubagentTool(store))
	// The LIVE this-run child status / background-result collection tool: reads the
	// parent run's child registry via parentCaps (no store), the sole body channel
	// for `background: true` Subagent children. Registered wherever Subagent is
	// (like InspectSubagent), never in child catalogs.
	cat.MustRegister(agent.NewSubagentStatusTool())
	return subagentClose
}

// registerParallelTool registers the Parallel fan-out tool when enabled:
// child/judge engines bound to the RESOLVED session provider/model (the same
// inheritance the Subagent tool gets), each branch in its own FULLY isolated fork
// (WithForceCopy — own .git, so a branch's git cannot escape into the base). The
// preserved-winner reaper is the SHARED process-wide LRU from the assets, so
// ForkPreservedCap bounds the process, not each session.
//
// MODEL (issue #35): the BRANCH children resolve through the def-less default
// chain (SubagentModel > session model — buildParallelChildEngine), while the
// JUDGE deliberately stays on the SESSION model (the asymmetry pinned by
// TestParallelJudgeStaysOnParentModel — see buildParallelJudgeEngine's comment).
func registerParallelTool(ctx context.Context, cfg Config, cat *tool.Catalog, reg *providerRegistry, store port.SessionStore, hooks port.HookRunner, a catalogAssets, s catalogSession) {
	if !cfg.EnableParallel {
		if s.narrate {
			cfg.diag().Log(ctx, port.LevelInfo, "Parallel tool DISABLED")
		}
		return
	}
	fk := forker.New(newForkWorkspace(a.skillReadRoots), forker.WithForceCopy())
	// Parallel branches run Bash through the HARDENED, trust-UNGATED runner (issue
	// #40) — the same construction as Mutating team members (buildForceCopyRunner).
	// Ungated because a force-copy fork is created by a pure FS copy, with NO git
	// invocation at fork time (no checkout, so no smudge filter or hook can fire) —
	// the fork-time worktree-checkout RCE the trust gate closes cannot happen here.
	// Hardened because copyTree copies the base's .git VERBATIM (config, hooks,
	// .gitattributes included): a branch running git at RUN time executes over that
	// copied, possibly untrusted .git, so the env scrub (gitenv.Scrub) must pin the
	// fixed keys; the attacker-NAMED-driver residual that remains is accepted at
	// main-session parity — see buildForceCopyRunner.
	forceCopyRunner := buildForceCopyRunner(cfg)
	parallelChild := buildParallelChildEngine(cfg, reg, s.provider, s.providerID, s.model, forceCopyRunner)
	judge := agent.NewEngineJudge(buildParallelJudgeEngine(modelCfgFor(cfg, s.model), s.provider))
	opts := []agent.ParallelOption{
		agent.WithParallelSubagentStopHook(hooks),
		agent.WithParallelJudge(judge),
		// Issue #30: persist each branch's child session to the SHARED session store so
		// the parent can pull a branch's transcript via InspectSubagent using the
		// surfaced "branch id:" — the same store InspectSubagent reads and the Subagent
		// tool persists children to (disjoint "parallel-" prefix).
		agent.WithParallelStore(store),
		// OPT-IN model router (ADR 0034): the per-branch model-override factory mints a
		// branch engine on a router-classified model through the SAME contamination-safe
		// per-provider path the branch child uses (window/compactor/counter re-derived).
		// Wired unconditionally — it is consulted only when the run also wired routeTask
		// (the SubagentModelRouter dispatcher seam) AND the classifier hits, so with the
		// router OFF the Parallel tool runs byte-identically on the shared branch child.
		agent.WithParallelEngineFactory(
			buildParallelEngineFactory(cfg, reg, s.provider, s.providerID, s.model, forceCopyRunner)),
	}
	// NO nil fallback here: Phase A builds exactly ONE reaper per process —
	// silently minting a per-assembly LRU would multiply the ForkPreservedCap
	// PROCESS bound by the number of sessions. A nil reaper (hand-rolled assets)
	// leaves preserved winner forks unbounded, so it is a loud WARN, never a quiet
	// mint. (The option is skipped entirely rather than passed nil, to avoid
	// wrapping a typed-nil *LRUForkReaper in the PreservedForkStore interface.)
	if a.forkReaper != nil {
		opts = append(opts, agent.WithWinnerReaper(a.forkReaper))
	} else {
		cfg.diag().Log(ctx, port.LevelWarn,
			"Parallel preserved-fork reaper missing from catalog assets; preserved winner forks will NOT be bounded (Phase A builds it under EnableParallel — hand-rolled assets?)")
	}
	// AUTO-MERGE (default-on, no flag — see docs/adr/0039-parallel-auto-merge.md):
	// wire a forker.Merger so a SINGLE-BRANCH join=first/join=judge winner's diff is
	// merged back into the parent workspace after the run — a delegated
	// implementer's edits land without a manual copy/merge step. Multi-branch runs
	// NEVER auto-merge (the no-auto-merge boundary stays for fan-out). The merger
	// is a composition-owned adapter injected as a tool.ForkMerger; engine/agent
	// stays layering-clean (no forker import). The merger applies the security
	// mitigations (git diff --no-textconv + .gitattributes-patch refusal) so an
	// untrusted branch cannot repoint the parent's git drivers at merge time.
	// Use the SHARED process-wide serializing merger from the assets (built once in
	// Phase A), NOT a fresh forker.NewMerger() — so the SAME mutex serializes every
	// merge across Parallel AND the writable Subagent, process-wide. A nil merger
	// (hand-rolled assets) skips auto-merge entirely (the historical boundary).
	if a.autoMerger != nil {
		opts = append(opts, agent.WithAutoMerge(a.autoMerger))
	}
	cat.MustRegister(agent.NewParallelTool(parallelChild, fk, opts...))
	if s.narrate {
		cfg.diag().Log(ctx, port.LevelInfo, "Parallel tool ENABLED (parallel isolated MUTATING child branches; judge selection wired)",
			"preserved_fork_cap", forkPreservedCap(cfg))
	}
}

// registerTeamTools registers the Team tool + the PULL member-transcript inspect
// tool when teams are enabled, over the SAME wiring the gRPC CreateTeam path uses
// (buildTeamWiring is the single wiring source), with the session's resolved
// provider/model as the inherited parent.
func registerTeamTools(ctx context.Context, cfg Config, cat *tool.Catalog, reg *providerRegistry, store port.SessionStore, a catalogAssets, s catalogSession, refMgr *mcp.Manager) {
	if !cfg.EnableTeams {
		if s.narrate {
			cfg.diag().Log(ctx, port.LevelInfo, "Team tool DISABLED")
		}
		return
	}
	factory, fk, roFk, teamHooks := buildTeamWiring(ctx, cfg, reg, s.provider, s.providerID, s.model, refMgr, a.agentReg, a.skillReadRoots, a.skillIndex, a, s.noFS)
	cat.MustRegister(agent.NewTeamTool(
		agent.TeamMemberEngineFactory(factory),
		agent.WithTeamToolForker(fk),
		agent.WithTeamToolReadOnlyForker(roFk),
		agent.WithTeamToolHooks(teamHooks),
		agent.WithTeamToolStore(store),
		agent.WithTeamToolTokenBudget(cfg.MaxTeamTokens),
	))
	cat.MustRegister(agent.NewInspectMemberTool(store))
	if s.narrate {
		cfg.diag().Log(ctx, port.LevelInfo, "Team tool ENABLED (in-process coordinating subagents; mutate-serial, ASK)")
	}
}

// registerMemoryFamilies registers the memory + user-model tools over the SHARED
// flocked stores (opened once in Phase A — never re-opened here, upholding the
// one-Store-per-dir invariant). The six tools are floor-scoped Allows in
// defaultRules keyed on tool NAMES, so the per-session policy (the same instance)
// governs them identically. The nil checks are interface-nil checks, kept sound
// by the typed-nil discipline on catalogAssets (every assignment is a known-non-nil
// concrete store or an untyped nil — no typed-nil interface trap).
func registerMemoryFamilies(ctx context.Context, cfg Config, cat *tool.Catalog, a catalogAssets) {
	if a.memStore != nil {
		if err := memory.Register(cat, a.memStore); err != nil {
			cfg.diag().Log(ctx, port.LevelWarn, "registering memory tools failed; some tools may be missing", "err", err)
		}
	}
	if a.userModelStore != nil {
		if err := memory.RegisterUserModel(cat, a.userModelStore); err != nil {
			cfg.diag().Log(ctx, port.LevelWarn, "registering user-model tools failed; some tools may be missing", "err", err)
		}
	}
}

// registerSkillFamily registers the Skill tool over the build-time skills seam
// (the metadata snapshot + the Activator), plus the SkillDraft author tool
// when a quarantine dir is configured (its novelty snapshot is the metas +
// preload-bodies projection — NewDirDrafter's []skills.Skill signature kept).
//
// NO-FS PROFILE: the Skill tool stays ON — a skill body is TEXT INJECTION into
// the conversation, not a filesystem act (an out-of-workspace ASSET read would
// fail honestly through the no-FS workspace, so a no-FS skill is body-only).
// SkillDraft is OFF — drafting writes a SKILL.md into the quarantine dir, a
// filesystem-authoring act a no-FS session has no business performing.
// On the DRIVER branch the preload index is lazy (def-referenced names only),
// so most projected bodies are empty and the drafter's novelty check is
// effectively name/description-driven there — a conscious trade, not a bug
// (fetching every body eagerly just for a warn-only similarity check would
// defeat the lazy-transfer design).
func registerSkillFamily(ctx context.Context, cfg Config, cat *tool.Catalog, a catalogAssets, s catalogSession) {
	if len(a.skills) > 0 {
		if err := cat.Register(skills.NewTool(a.skills, a.skillActivator)); err != nil {
			cfg.diag().Log(ctx, port.LevelWarn, "registering skills failed; Skill tool disabled", "err", err)
		}
	}
	if !s.noFS {
		registerSkillDraft(ctx, cfg, cat, skillValues(a.skills, a.skillIndex), s.narrate)
	}
}

// noFSChildCatalog builds the file-less CHILD tool surface every no-FS
// delegation target (Subagent explorer child, per-call model-override child,
// team member) runs with: the six memory/user-model tools over the SHARED
// flocked stores, WebFetch + WebSearch (search-then-fetch discovery), and the
// server-global MCP tools — and nothing that touches a filesystem (no
// Read/Grep/Glob, no Bash, no Edit/Write). A fresh
// catalog per call (the readOnlyExplorerCatalog idiom: one catalog per engine).
// The global MCP tools are REUSED from the shared manager, never reconnected.
func noFSChildCatalog(ctx context.Context, cfg Config, a catalogAssets) *tool.Catalog {
	cat := tool.NewCatalog()
	for _, t := range tools.NoFS() {
		cat.MustRegister(t)
	}
	// WebSearch (issue #26) for read-only-discovery parity with WebFetch: a no-FS
	// explorer's natural workflow is search-then-fetch, so it carries both. Built
	// over the SAME process-wide provider as the main catalog (a.searchProvider).
	cat.MustRegister(tools.NewWebSearchTool(a.searchProvider))
	if a.globalMgr != nil {
		if skipped, rerr := mcp.Register(cat, a.globalMgr.Tools()); rerr != nil {
			cfg.diag().Log(ctx, port.LevelWarn,
				"no-FS child catalog: skipped duplicate MCP tool name(s): "+strings.Join(skipped, ", "),
				"tools", strings.Join(skipped, ", "), "err", rerr)
		}
	}
	registerMemoryFamilies(ctx, cfg, cat, a)
	return cat
}

// nonReadOnlyToolNames lists the catalog's tools reporting ReadOnly() == false.
// In a no-FS member catalog (noFSChildCatalog) those are by construction
// NON-WORKSPACE mutators (Remember/RememberUser write the flocked memory stores;
// MCP tools are remote) — there is no filesystem for them to mutate — so the
// list feeds MemberBuild.MCPToolNames, the supervisor's documented exemption for
// exactly that class, keeping the base-sharing read-only-member backstop honest.
func nonReadOnlyToolNames(cat *tool.Catalog) []string {
	var names []string
	for _, t := range cat.Tools() {
		if !t.ReadOnly() {
			names = append(names, t.Spec().Name)
		}
	}
	return names
}

// forkPreservedCap normalises cfg.ForkPreservedCap to the effective preserved-fork
// LRU bound (a non-positive value uses the default). It is read where the SHARED
// reaper is built (buildCatalog) and echoed in the build-time narration.
func forkPreservedCap(cfg Config) int {
	if cfg.ForkPreservedCap > 0 {
		return cfg.ForkPreservedCap
	}
	return agent.DefaultPreservedForkCap
}
