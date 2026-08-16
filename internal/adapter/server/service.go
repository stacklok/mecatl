package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/tools"
)

// WorkspaceFactory builds the session-scoped tool.Workspace for a session root.
// The server is workspace-agnostic: the composition root injects memfs (tests)
// or osfs (production) via this seam.
type WorkspaceFactory func(root string) tool.Workspace

// Clock returns the current wall time. It defaults to time.Now when nil so the
// server can stamp session creation timestamps deterministically in tests.
type Clock func() time.Time

// IDGenerator returns a fresh, unique session id. It defaults to a random hex
// id when nil; tests may inject a deterministic generator.
type IDGenerator func() session.SessionID

// ProviderSelector names a per-session provider+model (multi-provider Phase 0,
// S3). The zero value (both empty) means "server default" — the shared engine,
// no per-session build. It is a NEUTRAL value object owned by the server adapter:
// the composition root (internal/app) resolves it against the registry/catalog;
// the adapter never imports either. Setting ModelID with an empty ProviderID is a
// client error (a bare model on an env-derived default provider is ambiguous) —
// rejected at the create boundary before the factory is consulted.
type ProviderSelector struct {
	// ProviderID is the registry id ("" => server default).
	ProviderID string
	// ModelID is the model selector ("" => provider default; a non-empty id the
	// catalog doesn't know is passed through to the provider verbatim).
	ModelID string
	// ReasoningEffort is the per-session reasoning-effort selector (ADR 0055): a
	// NEUTRAL token ("" / "auto" => unset, the operator default applies; otherwise
	// low/medium/high/xhigh/max). It is OPAQUE to the adapter — the composition root
	// normalises + clamps it per provider and re-mints the engine's adapter when it
	// differs from the operator default. "" keeps the operator default (and the
	// shared engine on a byte-identical default path). It composes orthogonally with
	// ProviderID/ModelID; unlike ModelID it is meaningful WITHOUT a ProviderID (it
	// rides the server-default provider).
	ReasoningEffort string
}

// SessionEngineResult is what a SessionEngineFactory returns: the built
// per-session engine, the per-session resolved input Capabilities (a NEUTRAL
// port.ProviderCapabilities computed in composition as the catalog ∩ adapter
// intersection for the session's resolved provider+model — see internal/app
// modelCapability), and the Close func that tears down that session's MCP manager
// (a no-op when no specs). A struct (not a 4-tuple) keeps the two interface-typed
// members readable and leaves room for future per-session metadata without another
// signature churn. The Service echoes Capabilities back on CreateSessionResponse
// (session_capabilities) and never recomputes it — the composition is the single
// source so the wire echo and the ListModels view cannot disagree.
type SessionEngineResult struct {
	// Engine is the built per-session engine. Required (non-nil on a nil error).
	Engine *agent.Engine
	// Capabilities is the session's resolved input capability (catalog ∩ adapter),
	// computed in composition. The server echoes it verbatim; it never recomputes.
	Capabilities port.ProviderCapabilities
	// ProviderID/ModelID are the EFFECTIVE provider+model IDENTITY this session
	// resolved to (the empty-selector default, an explicit selector, or a passthrough
	// id), computed ONCE in composition from the SAME resolved locals that feed the
	// engine — the server echoes them verbatim on CreateSessionResponse.resolved_model
	// and never recomputes. Same single-source discipline as Capabilities. The context
	// WINDOW is NOT a frozen field: Service.ResolvedModel resolves it live-first at echo
	// time via Config.ResolveContextWindow (the SAME source the engine reads), so the
	// echo and the running engine agree after a live-catalog swap with no rebuild.
	ProviderID string
	ModelID    string
	// ReasoningEffort is the EFFECTIVE, normalised + per-provider-clamped reasoning
	// effort this session resolved to (ADR 0055): "" when unset (provider default),
	// else the neutral token actually sent to the adapter (e.g. openai + "max" echoes
	// "high"). Computed ONCE in composition from the SAME resolved value that re-mints
	// (or reuses) the engine's adapter; the server echoes it verbatim on
	// CreateSessionResponse.resolved_model and never recomputes — the SAME single-
	// source discipline as ProviderID/ModelID.
	ReasoningEffort string
	// BuiltForMode is the session PermissionMode the factory RESOLVED THE MODEL FOR
	// (ADR 0030 Layer 3, the mode→model re-resolution). The factory echoes back the
	// mode it was handed — the SAME single-source discipline as ProviderID/ModelID —
	// so the Service can stamp sessionEngine.builtForMode from this one value and later
	// detect a stale engine (sess.Mode != se.builtForMode) without re-resolving any
	// model itself. The empty value (a factory that predates the mode axis) is
	// session.ModeDefault-equivalent: the Service treats "" as "no mode pin" and the
	// stale check degrades to never-rebuild-on-mode (byte-identical to pre-Phase-3).
	BuiltForMode session.PermissionMode
	// Close tears down the session's MCP manager. Never nil (a no-op when no specs).
	Close func() error
}

// ResolvedModel is the per-session EFFECTIVE model echoed on the wire: the
// provider+model id this session resolved to plus its context window. It is the
// SINGLE composition-computed value (see Config.DefaultResolvedModel and the
// per-session SessionEngineResult fields) — the server holds it and echoes it
// verbatim, mirroring the capability-intersection single-source rule; a handler
// must never read it back off the request (model_id is empty for a default
// session and ambiguous for passthrough).
type ResolvedModel struct {
	ProviderID    string
	ModelID       string
	ContextWindow int64
	// ReasoningEffort is the effective per-session reasoning-effort token (ADR
	// 0055), "" when unset. Carried on the resolved-model echo so the wire (and the
	// TUI footer) can show the active effort; it is the value held on the
	// per-session engine record (sessionEngine.reasoningEffort), not recomputed.
	ReasoningEffort string
}

// SessionEngineFactory builds a PER-SESSION agent engine over a non-default
// provider/model selector AND/OR the client-provided streaming-HTTP MCP servers
// (specs), returning a SessionEngineResult (engine + per-session capabilities +
// close func) and an error. It is the seam the ACP adapter uses to mount an
// editor's session/new mcpServers AND the seam the gRPC/HTTP CreateSession path
// uses to bind a per-session provider/model — both WITHOUT leaking those tools (or
// the registry/catalog) into the shared engine every other session uses. A session
// needing BOTH a non-default model and client MCP gets ONE engine over ONE catalog
// from a single call (sel + specs are orthogonal inputs). The factory returns an
// error wrapping ErrInvalidArgument for an unknown/unavailable provider id. The
// composition root (internal/app) supplies it via Config.SessionEngine; when nil, a
// non-default selector or non-empty specs are rejected with ErrInvalidArgument. It
// mirrors MemberEngineFactory: the Service references the type in its signatures
// but never builds managers itself.
//
// profile is the session's tool-surface profile (issue #55), flowing exactly as
// the selector does: ProfileDefault keeps today's catalog byte-identical;
// ProfileNoFS makes the factory assemble the NO-FILESYSTEM catalog (no file
// tools, no Bash, no Parallel, no SkillDraft; file-less Subagent/Team children)
// and apply the no-FS prompt posture. A no-FS session ALWAYS routes through this
// factory — the shared engine has the FS tools baked in.
//
// workspace is the SESSION's workspace root (issue #32): the factory pins the
// per-session engine's CHILD permission resolver to it, so a per-session
// engine's subagents/members/branches resolve project permission rules from
// THEIR session's pre-fork base root — never the server flag's root, and never
// a fork root. Empty (a no-fs session, or a resume that persisted none) pins no
// project root (user/CLI rules only).
//
// mode is the session's PermissionMode (ADR 0030 Layer 3, the mode→model
// re-resolution): when it is session.ModePlan the composition factory re-resolves
// the engine's model through the `plan` slot (within the SAME provider — the
// provider stays fixed per session), so a planning turn runs on a strong-reasoning
// model and an executing turn on the session model (the opusplan pattern).
// ModeDefault/ModeAccept keep the session-selected model (byte-identical). The
// factory echoes the mode back as SessionEngineResult.BuiltForMode so the Service
// can detect a stale engine across a mode change and rebuild via THIS same path
// (the run-entry/rehydration seam), never resolving a model itself.
type SessionEngineFactory func(ctx context.Context, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode) (SessionEngineResult, error)

// Config wires the server adapter to the WP8 engine and its collaborators.
type Config struct {
	// Engine is the shared agent engine that drives every run. Required.
	Engine *agent.Engine
	// Store persists and looks up sessions. Required.
	Store port.SessionStore
	// OwnershipEnforced is true only when the request edge has a verifier wired.
	// Its zero value preserves the ownerless compatibility path. When enabled,
	// create retries compare the verified issuer/subject pair before exposing an
	// existing caller-selected ID.
	OwnershipEnforced bool
	// Workspaces builds a Workspace for a session root. Required.
	Workspaces WorkspaceFactory
	// CommandRunner is the MAIN session's bound command runner (issue #462). It is
	// the runner the main session's Environment binds when the session runs on the
	// DEFAULT workspace; a session whose workspace DIFFERS (a worktree binding, an
	// ACP buffer, a no-fs profile) builds its own runner via CommandRunnerFactory
	// (below) bound to that root, or is shell-less when the factory returns nil.
	// nil when Bash is disabled (the catalog omits Bash and the Environment's Bash
	// surfaces ErrNoShell). The forker builds its OWN bound runners for forked
	// children, so this is the main-session runner only.
	CommandRunner tool.CommandRunner
	// CommandRunnerFactory, when non-nil, builds a command runner BOUND to a
	// session root that differs from the main workspace (issue #462): a worktree-
	// bound session needs its Bash rooted at the worktree, not the launch root.
	// It is the same env-scrubbed construction as CommandRunner, parameterised by
	// the session root. nil (the default) means a differing workspace gets a
	// shell-less Environment (Bash surfaces ErrNoShell) — acceptable for no-fs /
	// ACP / cloud deployments. The factory returns nil when Bash is disabled.
	CommandRunnerFactory func(root string) tool.CommandRunner
	// EnvironmentResolver, when non-nil, resolves a persisted session.EnvironmentRef
	// to a LIVE tool.Environment for a non-in-tree Kind (ADR 0214, issue #462 phase
	// 3). It is the reattachment half of the Environment seam: a restarted process
	// reads the persisted ref off a loaded session and reattaches a live
	// Environment to the SAME backend (a remote worker, a container) rather than
	// silently re-deriving one from the workspace/profile. Context is required
	// (a future network backend may dial out). The resolver MUST return an
	// Environment whose Ref() equals the requested ref; a nil/mismatch/nil-
	// Workspace result fails loudly (composition maps it to ErrFailedPrecondition,
	// never a silent local fallback). Local/mem/nofs NEVER reach the resolver:
	// a zero ref uses the legacy Workspace-derived resolution, and the in-tree
	// Kinds resolve through the existing Workspaces/CommandRunnerFactory path.
	// nil (the default) means any non-in-tree Kind fails loudly — the feature is
	// off, byte-identical to a pre-phase-3 build. Do NOT conflate this with
	// per-session engine rehydration: rehydration rebuilds the ENGINE for a
	// persisted provider/model selector; this reattaches the ENVIRONMENT for a
	// persisted environment ref. The two are independent (a session may need
	// either, both, or neither).
	EnvironmentResolver func(context.Context, session.EnvironmentRef) (tool.Environment, error)
	// DefaultMode is applied when a CreateSession request leaves mode
	// unspecified. Defaults to session.ModeDefault when empty.
	DefaultMode session.PermissionMode
	// DefaultLimits are the stop limits applied to a session created without
	// explicit limits. Because a zero Limits value DISABLES every stop condition
	// by design in package session, the composition root injects non-zero
	// defaults here so a default session is always bounded. Per-field: a request
	// that supplies any non-zero limit field is taken as explicit and used as-is.
	DefaultLimits session.Limits
	// Now supplies the creation timestamp; defaults to time.Now.
	Now Clock
	// NewID allocates session ids; defaults to a crypto-random hex generator.
	NewID IDGenerator
	// MCPProvider exposes the connected MCP servers' resources/prompts to the
	// catalog-level inspection RPCs. Optional and nil-safe: when nil, the list
	// RPCs return empty and the read/get RPCs return ErrNoMCPProvider.
	MCPProvider mcp.Provider
	// MCPSources is the resolved MCP source inventory snapshot taken at startup.
	// It backs ListMcpSources and ListToolHiveGroups when MCPSourceProber is nil;
	// in that case both derive purely from this snapshot and perform no live
	// discovery. May be empty.
	MCPSources []source.SourceInfo
	// MCPSourceProber, when non-nil, re-consults the resolved MCP sources on each
	// ListMcpSources/ListToolHiveGroups call and returns a FRESH inventory — so a
	// client refresh reflects CURRENT source status/diagnostics (e.g. a ToolHive
	// workload that crashed or appeared after startup), not the startup snapshot.
	// It is the live-discovery seam: the composition root supplies a prober that
	// closes over the resolved []source.Source and re-runs source.InspectSources.
	// When nil, ListMcpSources falls back to the cached MCPSources snapshot. The
	// prober is read-only (streaming-HTTP / container queries only; never spawns a
	// process) and fail-soft: on any failure the Service falls back to the cached
	// snapshot so the panel always renders.
	MCPSourceProber func(ctx context.Context) []source.SourceInfo
	// Commands lists the available slash commands for a workspace, backing the
	// ListCommands RPC (the client's in-input command palette). It is the
	// composition-injected discovery seam: the composition root (internal/app)
	// closes over the SAME command expander it builds for the run path and the
	// workspace factory, so the palette offers exactly the commands a "/<cmd>"
	// prompt would expand. Optional and nil-safe: when nil (command expansion
	// disabled, or no expander enumerates), ListCommands returns an empty list.
	// It is read-only and called per request (discovery is cheap file scanning).
	Commands CommandLister

	// Worktrees lists the git worktrees of a repo root, backing the ListWorktrees
	// RPC (the client's /worktrees overlay — the first-class operator workflow for
	// binding a session to an EXISTING sibling worktree, issue #102). It is the
	// composition-injected discovery seam: the composition root supplies an
	// osfs-backed implementation that shells out to `git worktree list --porcelain`
	// with a scrubbed env, trust-gated; a no-FS/cloud deployment (or an untrusted
	// workspace) leaves it nil. Optional and nil-safe: when nil, ListWorktrees
	// returns an empty list and the ServerCapabilities.worktrees bit is false so a
	// client hides the overlay honestly. Read-only and called per request.
	Worktrees WorktreeLister

	// DefaultWorkspace is the workspace the SHARED engine was assembled for (the
	// server's launch root). A CreateSession whose workspace DIFFERS (non-empty and
	// != DefaultWorkspace) routes through the per-session engine factory so the
	// session's subagents/members pin their CHILD permission resolver to the
	// session root (the same re-pin sessionEngineFactory already applies for no-fs
	// / selector / mode sessions), and the session rehydrates to the SAME engine
	// after a process restart. The main policy ALREADY re-resolves `.mecatl/
	// settings.yaml` per workspace on every session; the per-session route closes
	// the CHILD-resolver gap (a shared-engine child would otherwise read the launch
	// root's project rules) and the restart-fidelity gap. Empty for a child/member
	// service or a no-root cloud deployment — then the trigger never fires (every
	// non-empty workspace is "different" but worktree discovery is nil there, so
	// the feature is inert). See docs/adr/0032-worktree-binding.md.
	DefaultWorkspace string

	// Agents is the resolved agent-definition snapshot taken at startup. It backs
	// ListAgents and is a pure read of this snapshot (no live discovery). The
	// composition root (internal/app) resolves the registry once and projects each
	// def into the proto form (name/description/resolved model/effective read-only
	// tool scope/permission mode/color) so the server adapter never imports the
	// agents adapter. May be empty (agent definitions disabled or none found).
	Agents []*mecatlv1.AgentInfo

	// Models is the resolved selectable-model inventory snapshot taken at startup
	// (multi-provider Phase 0, S3). It backs ListModels and is a pure read of this
	// snapshot (no live discovery — the registry's available providers + the
	// embedded catalog are both fixed for the process lifetime). The composition
	// root (internal/app) joins the registry's AVAILABLE providers to the catalog
	// and projects each model into the proto form (modelSnapshot) so the server
	// adapter never imports providercatalog or the registry. May be empty (zero
	// providers available). NO secret material (no key, env var name, or base URL).
	Models []*mecatlv1.ModelInfo

	// DefaultCapabilities is the NEUTRAL per-(default provider+default model) input
	// capability — the catalog ∩ adapter INTERSECTION computed once in composition
	// (internal/app modelCapability for the registry default + cfg.Model). It is the
	// single source for BOTH the shared/default-engine session_capabilities echo
	// (when a session uses no per-session engine) AND ProviderCapabilities() (the ACP
	// gate). The server adapter holds only this neutral value — it never imports the
	// catalog or registry. The zero value (text-only) is the safe default for a
	// child/member service with no provider. (multi-provider Phase 0, S5.)
	DefaultCapabilities port.ProviderCapabilities

	// Posture is the SERVER-WIDE operator posture-ladder tier as a string
	// ("strict"/"trusted"/"auto"/"yolo"), projected into the ServerCapabilities echo
	// as CHROME ONLY (a client renders a "⚠ auto"/"⚠ yolo" badge). It is NOT session
	// state — it never changes per session; the per-session knob is permission MODE.
	// Empty (the zero value / an unconfigured child service) yields no badge. String
	// passthrough — no enum on the wire (the EvNoProgress/StopBudget discipline).
	Posture string

	// DefaultResolvedModel is the EFFECTIVE provider+model the DEFAULT/shared engine
	// resolved to (the registry default provider + cfg.Model + the default context
	// window), computed once in composition. It is the single source for the
	// resolved_model echo of a session that uses no per-session engine, the SAME
	// composition-computed single-source discipline as DefaultCapabilities — the
	// server holds only this value and never recomputes the resolution in a handler.
	// The zero value (empty ids) is the safe default for a child/member service with
	// no provider; a client maps it to a no-model-segment header.
	DefaultResolvedModel ResolvedModel

	// DefaultModelPending is true when the DEFAULT/shared engine booted with an
	// UNRESOLVED default model (issue #262 review finding 1: the sole
	// intent-driven — ToolHive gateway — provider probed down at Build, so
	// cfg.Model stayed ""). It routes EVERY zero-selector session through the
	// per-session engine factory (sessionNeedsPerFactory) and, for a session
	// persisted before a restart into a still-down proxy, through rehydration
	// (needsRehydration) too — so the model is resolved AT SESSION-BUILD TIME
	// (resolve-at-use, mirroring the Deps.ContextWindow precedent, but at
	// session granularity) instead of being frozen at the shared engine's
	// Build-time construction. Without this, a post-boot heal
	// (registry.healDefaultModel) updates the registry's resolved default but
	// never reaches a zero-selector session, which keeps sending an empty
	// model id to the provider (the R1.4 "no-restart" promise silently broken
	// for the flagship sole-provider case). Static for the process lifetime
	// (set once in composition from the SAME condition healDefaultModel guards
	// on: an intent-driven default provider with no resolved model at Build);
	// false everywhere else (a keyed default, or an operator-configured
	// --model/--default-model) — byte-identical to today.
	DefaultModelPending bool

	// Skills is the resolved skills-inventory snapshot taken at startup. It backs
	// ListSkills and is a pure read of this snapshot (no live discovery — skills
	// are discovered once at build time and immutable for the process lifetime).
	// The composition root (internal/app) discovers the skills once and projects
	// each into the proto form (name + description); that PROJECTION (skillSnapshot)
	// lives in internal/app, not here, so the server adapter holds only the proto
	// snapshot and never reaches into the skills adapter's discovery types. May be
	// empty (skills disabled or none found).
	Skills []*mecatlv1.SkillInfo

	// Soul is the resolved soul (persona) BUILD-TIME SNAPSHOT taken at startup. It
	// backs GetSoul and is a pure read of this snapshot — the soul is selected once
	// (USER-wins precedence, project trust gate, drift check) and is immutable for the
	// process lifetime, so no live re-read is warranted. The composition root
	// (internal/app) projects the winning soul's content + soulMeta into the proto form
	// (soulSnapshot) so the server adapter never reaches into the soul adapter or the
	// composition-layer soulMeta type. nil when no soul source is wired (--no-soul or
	// none present); a nil Soul makes capabilities().Soul false and GetSoul return an
	// empty (present=false) snapshot.
	Soul *mecatlv1.SoulInfo

	// UserModel lists the CURRENT user-model entries, backing GetUserModel. Unlike
	// Soul (a startup snapshot) it is a LIVE lister: the composition root closes over
	// the user-model store's Index so a refresh reflects entries saved since startup.
	// It is the same seam idiom as Commands. Optional and nil-safe: when nil (user
	// model disabled) capabilities().UserModel is false and GetUserModel returns empty.
	UserModel UserModelLister

	// ReflectSession enables explicit completed-session reflection independently of
	// automatic learning mode. Proposals and the mutation callbacks expose the
	// bounded, caller-partitioned staged-learning review surface.
	ReflectSession          ExplicitReflector
	Proposals               learning.ProposalRepository
	ProposalPrincipal       func(*session.Principal) string
	PromoteProposal         ProposalPromoter
	UndoProposal            ProposalUndoer
	ProjectPromotionAllowed func(project string) bool
	ProposalActionAvailable func(project string) (bool, string)

	// LearnedSkills exposes caller-partitioned, agent-owned lifecycle records. The
	// publisher atomically refreshes the shared live Skill catalog after mutations.
	LearnedSkills             learning.SkillRepository
	PublishLearnedSkills      func(context.Context, learning.SkillPartition) error
	BeginSkillPublication     func() func()
	RevokeLearnedSkill        func(learning.SkillPartition, string)
	LiveSkillGeneration       func(learning.SkillPartition) uint64
	SkillActionAvailable      func(learning.SkillPartition, string) (bool, string)
	LearnedSkillNameAvailable func(string) bool
	LiveSkills                func(context.Context) []*mecatlv1.SkillInfo

	// SessionEngine builds a PER-SESSION engine over a non-default provider/model
	// selector AND/OR client-provided streaming-HTTP MCP servers (the ACP
	// session/new mcpServers). It is the seam that lets a session bind its OWN
	// provider/model or mount its OWN MCP tools without leaking them (or the
	// provider registry) into the shared Engine every other session uses. When nil,
	// a non-default selector or non-empty MCP specs are rejected with
	// ErrInvalidArgument; a session with the zero selector and no client MCP always
	// uses the shared Engine (zero overhead). The composition root (internal/app)
	// supplies it.
	SessionEngine SessionEngineFactory

	// ModeNeedsEngine reports whether a given session PermissionMode resolves a model
	// that DIFFERS from the shared engine's model (ADR 0030 Layer 3) — i.e. whether a
	// plan slot is configured and active. It is the composition-injected predicate that
	// lets a DEFAULT-FS session (which normally rides the shared engine, zero overhead)
	// be PROMOTED to a per-session factory engine when its mode would change the model.
	// The Service never resolves a model itself; it asks this predicate.
	//
	// When nil (no plan slot configured, or a deployment that predates Phase 3) a
	// default-FS session is NEVER promoted — BYTE-IDENTICAL to pre-Phase-3 behaviour
	// (a mode flip changes nothing, the shared engine is unchanged). This is the
	// regression-guard seam: composition wires it ONLY when a plan slot is active.
	ModeNeedsEngine func(mode session.PermissionMode) bool

	// MemberEngine builds a team member's Engine from the shared team and the
	// member spec (see engine/agent.MemberEngine). It is the seam that wires
	// the agent-team RPCs: when nil, those RPCs return ErrTeamsDisabled. The
	// composition root supplies it (internal/app), capturing the per-member
	// catalog (read-only base + MemberTools, plus mutating tools only for a
	// Mutating member) and the provider/model.
	MemberEngine MemberEngineFactory
	// TeamGoalUntrusted, when true, re-fences the gRPC/HTTP CreateTeam goal as
	// UNTRUSTED data in member and synthesis prompts (threaded to
	// agent.WithUntrustedGoal). DEFAULT false: the goal is the team's TRUSTED
	// top-level instruction (its provenance is the deployment/operator that owns the
	// gRPC front door, not a peer — peer messages and task descriptions stay fenced
	// regardless). A multi-tenant / relay deployment that interpolates untrusted
	// end-user text into the goal should set this true so the goal is fenced as data.
	// It is a composition decision (the deployment knows the goal's provenance); the
	// supervisor only takes the bool.
	TeamGoalUntrusted bool
	// TeamTokenBudget is the team-wide cumulative token budget threaded into every
	// CreateTeam supervisor (agent.WithTeamTokenBudget). 0 disables. The per-request
	// proto knob is deferred.
	TeamTokenBudget int
	// Forker isolates a Mutating team member's workspace (force-copy: own `.git`).
	// Optional; required only if a Mutating member is spawned.
	Forker tool.EnvironmentForker
	// ReadOnlyForker isolates a read-only-isolated team member's workspace as a cheap
	// git worktree (shares the base repo's `.git` ⇒ full history) so an inspect-only
	// member can run a shell (git log/show, build, test) confined to a throwaway
	// checkout. Optional; required only if the member factory marks any read-only
	// member IsolateReadOnly (which the composition root does only when this is
	// wired). When nil, read-only members base-share with no shell.
	ReadOnlyForker tool.EnvironmentForker
	// SharedBaseWorkspace re-views a team's base workspace for a BASE-SHARING
	// read-only member (the no-shell fallback tier) so it never inherits a relaxed
	// base's out-of-root reach (the path-escape-posture Scenario 5 boundary —
	// threaded to agent.WithTeamSharedBaseWorkspace). The composition root wires it
	// whenever the Workspaces factory may return a relaxed workspace (auto/yolo).
	// Optional; nil keeps the historical verbatim base share.
	SharedBaseWorkspace func(root string) tool.Workspace
	// TeamHooks fires the team lifecycle hooks (TeammateIdle) and is passed to
	// member coordination tools for the TaskCreated / TaskCompleted gates.
	// Optional.
	TeamHooks port.HookRunner
	// MaxTeams caps the number of live (un-cleaned) teams the registry holds at
	// once, bounding the leak when clients create teams but never CleanupTeam.
	// CreateTeam returns ErrTooManyTeams (ResourceExhausted) when the cap is
	// reached; cleaning up a created/done team frees a slot. Defaults to
	// defaultMaxTeams when zero.
	MaxTeams int

	// MaxSessionEngines caps the number of live (un-released) PER-SESSION engines
	// the registry holds at once (CWE-770). A per-session engine is registered when
	// a session needs a non-default provider/model selector OR client-provided MCP
	// servers. The ACP surface drains them on editor disconnect, but the gRPC/HTTP
	// surfaces have no teardown signal, so without a cap a hostile authed client
	// could call CreateSession with a valid provider_id repeatedly (never closing)
	// and grow the map unbounded. createSession returns ErrTooManySessionEngines
	// (ResourceExhausted) when the cap is reached; CloseSession / EndSession frees a
	// slot. It is a generous count (a session is multi-turn and its engine MUST
	// persist across turns, so this is NOT terminal-state eviction). Defaults to
	// defaultMaxSessionEngines when zero.
	MaxSessionEngines int

	// OnCloseSession, when non-nil, is invoked by CloseSession with the closing
	// session id BEFORE the per-session engine teardown. It is the composition
	// seam for releasing session-scoped state the Service does not own — currently
	// the per-session LEARNED permission rules (issue #3), evicted via
	// permstore.Memory.Forget so they do not outlive the session. Optional and
	// nil-safe.
	OnCloseSession func(session.SessionID)

	// EventLog durably records the relayed event stream per session (cloud-native
	// Phase 3a). The gRPC/HTTP relay loops Append every HEALTHY-PATH event to it,
	// beside the existing awaiting-ask Persist; the loop itself stays
	// storage-agnostic (it only emits). Optional and nil-safe: when nil the relay
	// records nothing (byte-identical to the pre-3a behaviour). The composition
	// root wires the durable jsonlstore Store (which also implements EventLog) or
	// an in-memory sibling when no store dir is configured.
	EventLog port.EventLog

	// Diagnostics is the operational logging sink the relay uses to WARN on an
	// EventLog.Append failure (a best-effort durable log must not break the live
	// stream). Optional and nil-safe: when nil, Append failures are silently
	// tolerated (the durability gap is the only effect). The composition root
	// supplies the same sink the rest of the build uses.
	Diagnostics port.Diagnostics

	// ReplayApprovals repopulates the in-memory learned-rule store (permstore) for a
	// loaded session from its durable EventLog allow-always verdicts (cloud-native
	// Phase 3b). It is the consumer that kills the Phase 2 re-ask wart: the permstore
	// is in-memory and lost on restart, so a previously allow-always'd tool would
	// otherwise re-ask after a process restart. The composition root (internal/app)
	// supplies the closure — it owns BOTH the EventLog and the Policy.Learn seam, so
	// it reads the verdicts, correlates each allow-always askID back to its ToolCall
	// in the loaded conversation (the askID encodes the call id; see agent.newAskID),
	// and re-Learns the reconstructed rule. Keeping the correlation in composition
	// keeps the EventLog event METADATA-ONLY (no raw args on the wire) while the real
	// rule is rebuilt from history the session already carries (no leak).
	//
	// The Service invokes it from loadAndReopen (the single run-entry funnel) AT MOST
	// ONCE per session id per process: a freshly-created in-memory session learns
	// live, so it never needs a replay, and a re-run of an already-replayed session
	// would only re-derive idempotent rules. Optional and nil-safe: when nil (no
	// store, or replay not wired) loadAndReopen does nothing extra.
	ReplayApprovals func(ctx context.Context, sess *session.Session)

	// ResolveContextWindow is THE live-first context-window resolver injected by
	// composition (the SAME reg.windowResolver the engine reads via Deps.ContextWindow,
	// wrapped to int64: override→live→catalog→128k floor). Service.ResolvedModel
	// consults it for BOTH the default-session and the per-session-engine branch, so
	// the wire echo is byte-identical to the engine's resolve-at-use window. nil keeps
	// the baked DefaultResolvedModel.ContextWindow verbatim — the memstore/driver/test
	// paths. Only the ContextWindow scalar is resolved; provider/model identity never
	// recomputes.
	ResolveContextWindow func(providerID, modelID string) int64

	// SessionLease is the OPTIONAL cross-process single-writer seam (cloud-native
	// Phase 4, ADR 0027). When wired, the run-entry funnel acquires a per-session
	// lease (AFTER the same-process runEntryMu, so same-process exclusion stays
	// cheap) before driving the engine, refreshes it from a Service-owned renewer
	// goroutine, and releases it on CloseSession / shutdown. A competing process
	// holding the lease makes StartRunContent / resumeFromAwaiting fail with
	// ErrSessionLeasedElsewhere. Optional and nil-safe: when nil there is NO
	// acquire, NO renewer, and NO release — byte-identical to the pre-Phase-4
	// single-writer-by-affinity posture. The loop NEVER imports port.SessionLease;
	// the renewer and the held-lease registry live entirely on Service (the same
	// storage-agnostic discipline as EventLog). A backend that reports
	// ErrLeaseUnsupported is stickily disabled (one INFO, then the no-lease path).
	SessionLease port.SessionLease

	// LeaseOwner is this process's owner-identity string for SessionLease, built
	// once per Build (e.g. "<hostname>-<pid>-<nonce>") so two Builds in one
	// process get distinct owners. Ignored when SessionLease is nil.
	LeaseOwner string

	// LeaseTTL is the lease lifetime requested at Acquire and the renew window;
	// the renewer ticks at LeaseRenewInterval (default LeaseTTL/3). A non-positive
	// value defaults to defaultLeaseTTL. Ignored when SessionLease is nil.
	LeaseTTL time.Duration

	// LeaseRenewInterval is how often the renewer refreshes a held lease. A
	// non-positive value defaults to LeaseTTL/3. Ignored when SessionLease is nil.
	LeaseRenewInterval time.Duration

	// Scheduler is the OPTIONAL in-process scheduled-tasks tick loop (issue #189,
	// Phase 1f). When wired, NewService stores it on the Service so Close drains it
	// (Stop cancels the tick loop + joins in-flight fires) and Drain arms its drain
	// gate (no new fires mid-tick during shutdown). The scheduler is STARTED by
	// composition (app.Build) AFTER NewService — its FireFunc closes over the
	// Service, so Build calls SetFire then Start; NewService does NOT start it.
	// nil = no scheduling (the byte-identical default).
	//
	// NOTE: this field is the LEGACY late-attach path. The schedule surface now
	// lives on the store-shaped scheduleManager (ADR 0076) — see ScheduleManager
	// below. NewService still seeds the Service's manager-side scheduler reference
	// from this field for byte-identical Close/Drain; composition attaches the
	// scheduler via SetScheduler (delegated to the manager) AFTER NewService.
	Scheduler *scheduler.Scheduler

	// ScheduleManager is the pre-Service store-shaped schedule manager (ADR 0076):
	// the validated create/read/update/fire seam constructed BEFORE buildEngine
	// from the store + now-func, with the scheduler / model-inventory as
	// late-bound atomic fields. When non-nil, the Service delegates its nine
	// port.ScheduleManager methods + EmitScheduleEvent + GetFire to it — the RPC
	// surface is byte-identical. When nil, the Service self-constructs one from
	// Store (the legacy test path + any caller that does not pre-construct); a
	// store that backs no ScheduleStore yields a nil manager (the honest
	// no-scheduling path). Composition (app.Build) constructs the manager from
	// the store before buildEngine and hands it here — the SAME manager its
	// shared catalog's Schedule tool factory resolves (one manager, one
	// truth). The field is the CONCRETE *scheduleManager (exposed to
	// composition as the ScheduleManagerImpl alias) so the typed-nil
	// discipline holds end-to-end: a store with no ScheduleStore yields an
	// untyped nil here, never a non-nil interface boxing a nil pointer.
	ScheduleManager *scheduleManager

	// PlanModeAutoApprove is the OPT-IN, OPERATOR-TIER-ONLY, DEFAULT-OFF flag that
	// auto-approves a plan-mode PresentPlan ask when the run ends without a human
	// operator. It is a deliberate autonomous-approval capability — an operator
	// deployment decision, NEVER load-bearing for safety. When true AND the engine
	// is headless (no interactive client attached), the Service auto-resolves a
	// parked plan-approval ask via the EXISTING ApprovePlan path (ModeDefault + a
	// loud note). It does NOT fire when interactive (a human can approve), NOT in
	// non-plan modes, NOT for non-plan asks. DEFAULT false (the existing safe
	// default: headless plan ask is auto-denied by the engine). Composition
	// (internal/app) sets this from Config.PlanModeAutoApprove; the cmd mains wire
	// the --plan-mode-auto-approve flag.
	PlanModeAutoApprove bool

	// Interactive reports whether a HUMAN approver is attached to the main engine's
	// runs (a live Converse / HTTP-SSE client that can answer a permission ask).
	// It mirrors app.Config.Interactive (threaded onto the engine's Deps.Interactive)
	// and gates the PlanModeAutoApprove observer: an interactive deployment NEVER
	// auto-approves (the human answers). DEFAULT false (fail-safe: the observer
	// treats the deployment as headless).
	Interactive bool
}

// defaultMaxTeams is the live-team registry cap applied when Config.MaxTeams is
// zero. It bounds memory growth from teams that are created but never cleaned up.
const defaultMaxTeams = 64

// defaultMaxSessionEngines is the per-session engine registry cap applied when
// Config.MaxSessionEngines is zero. It is generous (a per-session engine is a
// legitimate per-conversation resource) but finite, so a client that never
// releases its selector/MCP sessions cannot grow the map without bound (CWE-770).
const defaultMaxSessionEngines = 1024

// defaultLeaseTTL is the session-lease lifetime applied when Config.LeaseTTL is
// zero and a SessionLease is wired. The renewer ticks at LeaseTTL/3 by default,
// so a 30s TTL is refreshed every 10s — comfortably ahead of expiry even with a
// slow store, while keeping a crashed holder's lease recoverable within ~30s.
const defaultLeaseTTL = 30 * time.Second

// leaseAcquireTimeout bounds a SessionLease.Acquire / Release call. Acquire runs
// on the run-entry hot path UNDER s.runEntryMu, so a wedged k8s/driver backend
// must not stall run-entry indefinitely; Release runs on a detached ctx at
// session close. Both are single small RPCs, so a few seconds is generous.
const leaseAcquireTimeout = 5 * time.Second

// leaseRenewFraction bounds a SessionLease.Renew call to a fraction of the renew
// interval, so a wedged backend's Renew gives up well before the next tick (and
// long before the TTL) rather than blocking the renewer goroutine. The bound is
// computed from LeaseRenewInterval (renewTimeout), never a flag.
const leaseRenewFraction = 2

// recoverNoticeText is the advisory emitted as an EvRecoverNotice event when a
// permanently-failed session is recovered for re-entry (issue #346). It surfaces a
// clear, actionable message: the prior failure was permanent, so retrying the same
// request replays the same rejection. It does NOT block the run — Recover stays
// honest (retry POSSIBLE, not guaranteed). The notice is emitted at run-START and
// rendered as a transient footer status line (the run's first event overwrites it);
// it is a prompt to START A NEW SESSION, not to /clear — /clear only wipes the local
// transcript and the next prompt re-enters the SAME poisoned server session.
const recoverNoticeText = "this session's last turn failed on a permanent provider error; " +
	"retrying replays the same request and will fail again. Start a new session, or change the request."

// ErrConfig is returned by NewService when a required dependency is missing.
var ErrConfig = errors.New("server: invalid config")

// engineCloseTimeout bounds how long Close waits for all per-session engine close
// calls to complete before proceeding. It is a `var` (not a const) so a test can
// shrink it to assert Close is bounded; production keeps the conservative default.
// A wedged engine close that ignores its deadline is abandoned (best-effort), never
// allowed to stall shutdown unboundedly.
var engineCloseTimeout = 10 * time.Second

// Service is the surface-agnostic application service shared by the gRPC and
// HTTP/SSE adapters. It owns session lifecycle (create/lookup), starts runs on
// the shared engine, and keeps a registry of in-flight runs keyed by session id
// so out-of-band approve/cancel reach the right run. It is safe for concurrent
// use.
//
// # Persistence and auto-resume
//
// The engine mutates the session in place over a run and the Service persists
// it to the SessionStore at meaningful transitions: at create, when the run
// pauses awaiting approval (see Persist), and at run end. With a durable store
// (jsonlstore via --store-dir) the latest snapshot therefore survives a process
// restart.
//
// A registered run is removed by the wire adapter that owns the stream: each
// adapter `defer`s FinishRun(id, run) after it finishes draining run.Events()
// (the channel closes when the run terminates). The Service does not deregister
// runs on its own — there is no internal relay goroutine that does so.
//
// GetSession, Approve and Cancel for a session id NOT in the in-memory run
// registry fall back to SessionStore.Load, so a session created (or last
// persisted) before a restart is still observable and its terminal/awaiting
// state is loadable. Resuming an in-flight STREAM across a restart is out of
// scope: the *agent.Run and its event channel live only in process memory, so
// after a restart there is no run to deliver an approval to. A persisted
// awaiting session remains loadable (a client can GET it and re-attach), but an
// Approve/Cancel that finds the session only in the store — with no live run —
// returns ErrNoActiveRun rather than silently succeeding.
type Service struct {
	cfg Config

	// models is the selectable-model inventory, SEEDED from cfg.Models at
	// construction and atomically SWAPPED by SetModels when the composition layer's
	// background live-catalog refresh completes (multi-provider live listing). It is
	// an atomic.Pointer so ListModels and the ModelSelection capability read it
	// lock-free while the refresh writes it race-free (verified under -race). The
	// pointer is never nil after NewService (it is initialised to the seed, possibly
	// an empty slice). The registry/catalog/lister never reach here — only the
	// projected []*mecatlv1.ModelInfo crosses this seam.
	models atomic.Pointer[[]*mecatlv1.ModelInfo]

	// providerStatus carries the LIVE-LISTING outcome per intent-driven provider
	// (issue #262: the ToolHive LLM gateway) — ok/unreachable/unauthorized/empty
	// plus a short remediation hint. SEEDED empty at construction, atomically
	// SWAPPED by SetProviderStatus (the composition-layer twin of SetModels: the
	// Build-time probe sets the initial value, and every subsequent live-model
	// refresh — background or on-demand — re-projects it). Never nil after
	// NewService.
	providerStatus atomic.Pointer[[]*mecatlv1.ProviderStatus]

	// modelsRefresher is the OPTIONAL composition-supplied closure ListModels
	// calls before returning its snapshot (issue #262, R1.4: a proxy started
	// after boot must appear on the NEXT /models open, no restart). nil (the
	// default — no intent-driven provider registered) makes ListModels a pure
	// snapshot read, byte-identical to before this feature. The refresher owns
	// its own self-guarding (which providers are stale, cooldown); this seam
	// only decides WHETHER to call it. Set at most once, in Build, before the
	// service starts serving — the atomic.Pointer is defensive-safe, not
	// load-bearing for a race that cannot occur in practice.
	modelsRefresher atomic.Pointer[func(context.Context)]

	mu    sync.Mutex
	runs  map[session.SessionID]*runState
	teams map[string]*teamState
	// sessionEngines holds the per-session engines — built for a session that needs
	// a non-default provider/model selector (gRPC/HTTP CreateSession) OR
	// client-provided MCP servers (ACP session/new). The ACP surface drains them on
	// editor disconnect (closeTrackedSessions) and Service.Close drains the rest on
	// shutdown, but the gRPC/HTTP surfaces have NO connection-teardown signal — a
	// client that creates selector sessions and never calls CloseSession/EndSession
	// would otherwise grow this map unbounded (CWE-770). So it is also CAPPED at
	// Config.MaxSessionEngines (mirroring MaxTeams): createSession returns
	// ErrTooManySessionEngines once the cap is reached, and CloseSession frees a slot.
	sessionEngines map[session.SessionID]*sessionEngine
	// sessionEnvironments holds per-session Environment OVERRIDES. When an entry
	// is present for a session id, StartRun uses it as the COMPLETE execution
	// environment (Workspace + optional bound CommandRunner + accurate ref) instead
	// of building one from the shared Workspaces + runner factories. It mirrors
	// sessionEngines exactly: registered by a surface adapter (the ACP adapter,
	// to route file I/O through the editor's fs/* buffers; the no-fs profile, to
	// install the honest file-less workspace), preferred by StartRun, and evicted
	// by CloseSession (editor disconnect) / drained by Close (shutdown). It is
	// bounded by connection lifetime, not a count — same rationale as
	// sessionEngines. The gRPC/HTTP surfaces never register an override, so their
	// behavior is unchanged.
	//
	// An override is a COMPLETE shell-less (or shell-bearing) Environment the
	// creator owns: the creator supplies the accurate ref (Kind/ID) and the
	// correct CommandRunner (nil for a file-less/buffer namespace). The Service
	// does NOT guess a ref or runner from the override's presence — every
	// override carries its own truthful identity (issue #462 phase-2 finding #2).
	sessionEnvironments map[session.SessionID]tool.Environment

	// reservedIDs holds caller-chosen session ids (WithSessionID) that are
	// mid-create: reserved under s.mu at the top of createSession and released
	// (defer) once the session is registered (per-session path) or persisted
	// (shared-engine path). It closes the WithSessionID collision TOCTOU — two
	// concurrent creates with the same id would both pass a check that only read
	// sessionEngines and released the lock before the slow factory call and the
	// separate registration. A create checks (and reserves) against BOTH
	// sessionEngines (live) AND reservedIDs (in-flight) under a single lock hold.
	// Guarded by s.mu.
	reservedIDs map[session.SessionID]struct{}

	// recoverNotices carries the pre-flight advisory message for a session that just
	// recovered from a PERMANENT failure. loadAndReopen stores the notice BEFORE
	// Recover() clears the permanence flag; RecoverNotice(id) returns it once and
	// deletes the entry, so it is emitted ONCE per recovery. The map is only ever
	// populated for StateFailed→idle transitions on permanently-failed sessions;
	// a transient failure stores nothing. It is a sync.Map (lock-free for the
	// common NOT-present read path in RecoverNotice — called at every run-entry).
	recoverNotices sync.Map

	// resumeMu serializes the awaiting-approval resume DECISION per session id
	// (cloud-native Phase 2): ApproveRun holds the per-session lock across the whole
	// (LookupRun-miss check → ResumeApproval → register) sequence, so two concurrent
	// Approves for the SAME awaiting session can never both spawn a resumed run. The
	// loser, on acquiring the lock, sees the now-registered live run via LookupRun and
	// routes its verdict to that run's channel (the same-process path) — the pending
	// tool executes EXACTLY ONCE. It is a keyedMutex, NOT s.mu, because the resume
	// sequence itself takes s.mu (engineAndEnvironmentFor / register) and Go mutexes are
	// not reentrant; a per-session lock also keeps unrelated sessions' resumes
	// concurrent. The keyedMutex frees a key once no caller holds it, so it never grows
	// unbounded.
	resumeMu keyedMutex

	// runEntryMu serializes the per-session RUN-ENTRY critical section (ADR 0030
	// Layer 3, the use-after-close guard): the engine-resolve (engineAndEnvironmentFor,
	// which may REBUILD/promote a per-session engine) + run launch + register sequence
	// is taken under this per-session lock by BOTH StartRunContent and (nested inside
	// resumeMu) resumeFromAwaiting. It guarantees a mode→model rebuild that displaces a
	// prior engine cannot run concurrently with another run-entry for the SAME id, so
	// the under-lock s.runs[id] re-check in buildAndRegisterSessionEngine is
	// AUTHORITATIVE: any racing run that obtained the prior engine has already completed
	// its register() (the lock is held until then), so the rebuild reliably observes it
	// live and ABORTS rather than closing an in-use engine's MCP transport. Per-session
	// (a keyedMutex, freed when no caller holds a key) so unrelated sessions never
	// serialize; lock order is resumeMu → runEntryMu (resumeFromAwaiting takes both;
	// StartRunContent takes only runEntryMu) so the two never deadlock.
	runEntryMu keyedMutex

	// replayedApprovals tracks the session ids whose learned-rule store has already
	// been repopulated from the durable EventLog this process lifetime (cloud-native
	// Phase 3b). loadAndReopen replays a session's allow-always verdicts AT MOST ONCE
	// per id: a session created live in this process never needs it, and a re-run of
	// an already-replayed session would only re-derive idempotent rules. Guarded by
	// s.mu.
	replayedApprovals map[session.SessionID]struct{}

	// heldLeases tracks the cross-process session leases this process currently
	// holds (cloud-native Phase 4, ADR 0027). A lease is acquired ONCE per session
	// on first run-entry (after the per-session runEntryMu) and held for the
	// session's life: a per-session renewer goroutine refreshes it, and CloseSession
	// / shutdown stop the renewer and Release it. Guarded by s.mu. Nil/empty when
	// Config.SessionLease is not wired (the byte-identical default). See List 1
	// (the resource inventory) in ADR 0027.
	heldLeases map[session.SessionID]*heldLease

	// leaseDisabled is set (once) when Config.SessionLease reports
	// ErrLeaseUnsupported: the seam never works on this backend, so the run-entry
	// gate stickily stops consulting it and degrades to the no-lease path (the
	// ErrPruneUnsupported sticky-disable precedent). Guarded by s.mu.
	leaseDisabled bool

	// leaseSweepDisabled is the SessionStale-specific sibling of leaseDisabled:
	// set (once) when a staleness-sweep trial lease Acquire reports
	// ErrLeaseUnsupported, it stickily disables the whole staleness sweep (not
	// just the one candidate) for the process lifetime, per issue #475 — a
	// per-candidate fallback to local-only liveness would reintroduce the
	// cross-replica unsoundness the lease check exists to prevent. Kept
	// separate from leaseDisabled because it gates a DIFFERENT seam (the
	// staleness sweep, not run-entry acquisition) with its own diagnostic.
	// Guarded by s.mu.
	leaseSweepDisabled bool

	// draining is the cloud-native drain gate (ADR 0048, mecak8s): once armed by
	// Drain, acquireLease rejects new run-entries with ErrUnavailable so a
	// shutting-down replica steers new traffic to a survivor within the
	// termination grace period. It starts false (the byte-identical default), so
	// mecated and an undrained mecak8s are unaffected. Read with atomic.Load in
	// the run-entry hot path (no s.mu).
	draining atomic.Bool

	// shutdownCancel is called at the START of Close to signal shutdown; currently
	// its only effect is to mark the closing state (in-flight runs are cancelled
	// explicitly via run.Cancel below). Kept as a one-time idempotent signal.
	shutdownCancel context.CancelFunc

	// schedMgr is the embedded store-shaped schedule manager (ADR 0076): the
	// single truth the Service's nine port.ScheduleManager methods +
	// EmitScheduleEvent + GetFire delegate to. Constructed in NewService from
	// cfg.ScheduleManager (the pre-Service path composition hands in) OR
	// self-constructed from cfg.Store (the legacy test path + any caller that
	// does not pre-construct). nil when the store backs no ScheduleStore (the
	// honest no-scheduling path, matching ServerCapabilities.Scheduling). The
	// manager holds the cadence floor, the late-set in-process scheduler, the
	// durable EventLog, diagnostics, and the SHARED model-inventory pointer.
	schedMgr *scheduleManager

	// subscriptions is the per-session live event subscription registry (ADR 0075
	// decision #5): a connected client (e.g. the embedded server's mecatui) holds
	// open a per-session merged stream over the session's runs via Subscribe, and
	// PublishSessionEvent fans events to every subscriber for that session ID.
	// Guarded by subMu (a SEPARATE RWMutex from s.mu — a PublishSessionEvent in a
	// delivery-run goroutine must not contend with the run/registry hot path). A
	// publisher holds RLock through its non-blocking sends, so unsubscribe and Close
	// can close only after every active publisher has finished with the channel.
	// A subscriber is a non-blocking channel; on a full channel the event is dropped
	// (drain-to-discard — a dead client never wedges the delivery run). Cleaned up
	// by the subscriber's returned unsubscribe func; all remaining subscriptions are
	// closed at Close. subscriptionsClosed permanently rejects new registrations
	// after shutdown begins.
	subMu               sync.RWMutex
	subscriptions       map[session.SessionID]map[int64]chan session.Event
	subNextID           int64
	subscriptionsClosed bool
}

// heldLease is one process-held session lease plus the cancel that stops its
// renewer goroutine. The lease VALUE is refreshed in place by the renewer (under
// s.mu) so the latest token/expiry is what a Release sends.
type heldLease struct {
	lease  port.Lease
	cancel context.CancelFunc
}

// sessionEngine couples a per-session engine (built over that session's
// client-provided MCP servers) with the close func that tears down its MCP
// manager. It is registered by CreateSessionWithMCP and released by CloseSession
// (and by the Service's own Close). StartRun prefers it over the shared engine
// for the owning session id.
type sessionEngine struct {
	engine *agent.Engine
	// caps is the session's resolved input capability (catalog ∩ adapter), computed
	// in composition and echoed verbatim on CreateSessionResponse.session_capabilities
	// via SessionCapabilities. Never recomputed here — the composition is the single
	// source so the per-session echo cannot drift from the ListModels view.
	caps port.ProviderCapabilities
	// providerID/modelID are the session's EFFECTIVE provider+model IDENTITY (catalog/
	// registry resolution), computed in composition and echoed verbatim on
	// CreateSessionResponse.resolved_model via ResolvedModel. The context WINDOW is NOT
	// frozen here — ResolvedModel resolves it live-first at call time via the SAME
	// resolver the engine reads (resolve-at-use), so the echo and the running engine
	// can never diverge after a live-catalog swap. Same single-source discipline as caps.
	providerID string
	modelID    string
	// reasoningEffort is the session's EFFECTIVE reasoning-effort token (ADR 0055),
	// "" when unset. Stamped from SessionEngineResult.ReasoningEffort and echoed
	// verbatim on resolved_model via ResolvedModel — never recomputed. Same single-
	// source discipline as providerID/modelID.
	reasoningEffort string
	// builtForMode is the session PermissionMode this engine's model was RESOLVED FOR
	// (ADR 0030 Layer 3). Stamped from SessionEngineResult.BuiltForMode at every
	// construction site (create, rehydrate, mode-rebuild) — the SAME single source the
	// composition factory echoes. engineAndEnvironmentFor compares it against the loaded
	// session's current Mode: a mismatch means the session switched plan↔execute since
	// the engine was built, so the model is stale and the engine is rebuilt through the
	// shared buildAndRegisterSessionEngine path (the run-entry seam, between turns). The
	// empty value means "no mode pin" (a pre-Phase-3 factory) and never triggers a
	// rebuild — byte-identical to the old behaviour.
	builtForMode session.PermissionMode
	close        func() error
}

// runState couples an in-flight *agent.Run with the live *session.Session the
// engine mutates in place, so the Service can persist the current session state
// (e.g. on entering awaiting, or at run end) without re-loading from the store.
//
// awaiting is an atomic flag that Persist sets when the session is StateAwaiting
// — i.e. the run is parked on a permission ask and a durable StateAwaiting
// snapshot has been persisted (the relay's Persist-on-ask). It is read by Close to
// EXCLUDE such runs from the shutdown cancel loop (cancelling them would
// overwrite the resumable awaiting snapshot with cancelled). Reading the live
// sess.State from Close would race the engine loop's mutation; the flag is the
// race-free signal (Persist reads sess.State only when the loop is parked/done —
// see Persist's doc). A fire-driven run (no wire relay) never marks it, so it is
// cancelled on shutdown like any mid-stream run — which is correct: the fire path
// does not persist an awaiting snapshot, so there is no resumable state to
// preserve.
type runState struct {
	run      *agent.Run
	sess     *session.Session
	awaiting atomic.Bool
}

// keyedMutex is a map of per-key mutexes with reference counting, so a caller can
// serialize work per key (here: per session id) without a process-wide lock and
// without the key map growing unbounded — a key's entry is removed once the last
// holder unlocks. It is used to make the awaiting-approval resume decision atomic per
// session (see Service.resumeMu): only one ResumeApproval is ever spawned for a given
// awaiting session even under concurrent Approve calls.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[session.SessionID]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

// lock acquires the per-key lock and returns an unlock func that releases it and
// drops the key when no other caller holds it. The pattern is: ref under the guard,
// then block on the per-key mutex OUTSIDE the guard (so distinct keys never serialize
// and the guard is never held across the contended wait).
func (k *keyedMutex) lock(key session.SessionID) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[session.SessionID]*keyedMutexEntry)
	}
	e, ok := k.locks[key]
	if !ok {
		e = &keyedMutexEntry{}
		k.locks[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// NewService validates cfg and constructs a Service. It returns ErrConfig if
// Engine, Store or Workspaces is nil.
func NewService(cfg Config) (*Service, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("%w: Engine is required", ErrConfig)
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: Store is required", ErrConfig)
	}
	if cfg.Workspaces == nil {
		return nil, fmt.Errorf("%w: Workspaces is required", ErrConfig)
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = session.ModeDefault
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	if cfg.MaxTeams <= 0 {
		cfg.MaxTeams = defaultMaxTeams
	}
	if cfg.MaxSessionEngines <= 0 {
		cfg.MaxSessionEngines = defaultMaxSessionEngines
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = port.NopDiagnostics{}
	}
	if cfg.SessionLease != nil {
		if cfg.LeaseTTL <= 0 {
			cfg.LeaseTTL = defaultLeaseTTL
		}
		if cfg.LeaseRenewInterval <= 0 {
			cfg.LeaseRenewInterval = cfg.LeaseTTL / 3
		}
		if cfg.LeaseRenewInterval <= 0 {
			cfg.LeaseRenewInterval = cfg.LeaseTTL // tiny-TTL guard: never a zero ticker.
		}
	}
	_, shutdownCancel := context.WithCancel(context.Background())
	svc := &Service{
		cfg:                 cfg,
		shutdownCancel:      shutdownCancel,
		runs:                make(map[session.SessionID]*runState),
		teams:               make(map[string]*teamState),
		sessionEngines:      make(map[session.SessionID]*sessionEngine),
		sessionEnvironments: make(map[session.SessionID]tool.Environment),
		reservedIDs:         make(map[session.SessionID]struct{}),
		replayedApprovals:   make(map[session.SessionID]struct{}),
		heldLeases:          make(map[session.SessionID]*heldLease),
		subscriptions:       make(map[session.SessionID]map[int64]chan session.Event),
	}
	// Seed the model inventory from the static snapshot. ListModels and the
	// ModelSelection cap read this atomic so a later live-catalog SetModels swap is
	// race-free. A nil cfg.Models seeds an empty (non-nil) slice so the pointer is
	// never nil.
	seed := cfg.Models
	svc.models.Store(&seed)
	// providerStatus starts empty — no intent-driven provider has been probed
	// yet at construction time; Build's post-construction SetProviderStatus
	// call (from the Build-time probe) supplies the initial value.
	var statusSeed []*mecatlv1.ProviderStatus
	svc.providerStatus.Store(&statusSeed)
	// Construct the embedded schedule manager (ADR 0076): the store-shaped
	// create/read/update/fire seam. Composition hands a pre-Service manager via
	// cfg.ScheduleManager (constructed from the store BEFORE buildEngine); the
	// Service adopts it and LATE-BINDS its own models pointer onto the manager
	// (the model-inventory is a late-bound atomic field — the manager is
	// resolvable before buildEngine; the pointer is created here, seeded from
	// cfg.Models). This keeps ONE models pointer: SetModels swaps the Service's
	// atomic, and the manager's selector validation reads the SAME atomic — no
	// second copy, so a live-catalog refresh reflects on the next create. When
	// cfg.ScheduleManager is nil (the legacy test path + any caller that does
	// not pre-construct), the Service self-constructs one from cfg.Store — a
	// store that backs no ScheduleStore (the in-memory memstore) yields a nil
	// manager (the honest no-scheduling path, matching
	// ServerCapabilities.Scheduling). The legacy cfg.Scheduler field, when set,
	// is late-attached onto the manager (the byte-identical pre-ADR-0076
	// attach-at-construction path; composition normally attaches via
	// SetScheduler after NewService).
	if cfg.ScheduleManager != nil {
		svc.schedMgr = cfg.ScheduleManager
		svc.schedMgr.setModelsPointer(&svc.models)
	} else {
		svc.schedMgr = NewScheduleManager(ScheduleManagerConfig{
			Store:             cfg.Store,
			Now:               cfg.Now,
			Models:            &svc.models,
			Diagnostics:       cfg.Diagnostics,
			OwnershipEnforced: cfg.OwnershipEnforced,
		})
	}
	if cfg.Scheduler != nil && svc.schedMgr != nil {
		svc.schedMgr.SetScheduler(cfg.Scheduler)
	}
	return svc, nil
}

// SetModels atomically swaps the selectable-model inventory. It is the composition
// layer's seam for the background live-catalog refresh: Build seeds the embedded
// snapshot synchronously (via Config.Models) and, once the live fetch completes,
// calls SetModels with the merged result. ListModels and the ModelSelection
// capability then reflect the new set on their next read. It is concurrency-safe
// (atomic store) and carries only the projected proto slice — no registry/catalog/
// lister type crosses this boundary. A nil argument stores an empty (non-nil)
// slice so the pointer is never nil.
func (s *Service) SetModels(models []*mecatlv1.ModelInfo) {
	if models == nil {
		models = []*mecatlv1.ModelInfo{}
	}
	s.models.Store(&models)
}

// currentModels returns the live model inventory pointer's contents (never nil
// after NewService). It is the single internal read used by ListModels and the
// ModelSelection capability so they cannot disagree.
func (s *Service) currentModels() []*mecatlv1.ModelInfo {
	if p := s.models.Load(); p != nil {
		return *p
	}
	return nil
}

// SetProviderStatus atomically swaps the per-provider live-listing status
// (issue #262). It is the composition layer's seam: the Build-time probe
// supplies the initial value, and the background + on-demand live-model
// refreshes re-project it on every re-fetch. Mirrors SetModels exactly (a nil
// argument stores an empty, non-nil slice so the pointer is never nil).
func (s *Service) SetProviderStatus(status []*mecatlv1.ProviderStatus) {
	if status == nil {
		status = []*mecatlv1.ProviderStatus{}
	}
	s.providerStatus.Store(&status)
}

// ProviderStatuses returns the current per-provider live-listing status
// snapshot (never nil after NewService). ListModels' gRPC/HTTP callers thread
// it onto ListModelsResponse.provider_status alongside the model list.
func (s *Service) ProviderStatuses() []*mecatlv1.ProviderStatus {
	if p := s.providerStatus.Load(); p != nil {
		return *p
	}
	return nil
}

// SetModelsRefresher installs the OPTIONAL on-demand model-list refresher
// (issue #262, R1.4: "proxy started after boot ⇒ models appear on next
// /models open, no restart"). ListModels calls it (bounded — the refresher
// owns its own timeout/cooldown/which-providers-are-stale logic) before
// returning its snapshot. nil (the default — set once in Build, only when at
// least one intent-driven provider exists) makes ListModels a pure snapshot
// read, byte-identical to every deployment without this feature.
func (s *Service) SetModelsRefresher(fn func(context.Context)) {
	s.modelsRefresher.Store(&fn)
}

// ErrNoActiveRun is returned by Approve/Cancel when the session exists (possibly
// loaded from the store after a restart) but has no in-flight run in this process to
// deliver the control to AND nothing to resume. The session state is still loadable
// via GetSession.
//
// Since cloud-native Phase 2, an Approve/Deny against a runless AWAITING session is
// NO LONGER ErrNoActiveRun: it re-enters the loop AT the ask and resumes
// (resumeFromAwaiting). ErrNoActiveRun therefore now means "the session exists but is
// in a terminal/idle state with nothing to resume" — idle, completed, cancelled, or
// failed. A failed/cancelled session recovers only through a NEW prompt
// (loadAndReopen → Recover/Interrupt), never through the approve seam. Cancel keeps
// the original meaning for every state (no live run to cancel).
var ErrNoActiveRun = errors.New("server: no active run for session")

// CreateSessionOption is a variadic option applied to a CreateSession* call
// (the Go options idiom — NOT a method-signature widening). The only option
// today is WithSessionID, which lets a caller (the scheduler fire path) mint a
// session under a CALLER-chosen id instead of the Service's NewID generator.
// Unknown options from future callers are a no-op.
type CreateSessionOption func(*createSessionOpts)

// createSessionOpts is the resolved options struct a CreateSessionOption writes
// into. The zero value is the byte-identical no-option path. idSet distinguishes
// "WithSessionID was called (possibly with an empty id, which is rejected)" from
// "WithSessionID was never called" — both leave id == "".
type createSessionOpts struct {
	id    session.SessionID
	idSet bool
	// sourceSessionID, when non-empty, seeds the new session's conversation
	// history from the named source session (issue #20, model-switch carryover).
	// Validated + snapshotted in createSession via validateCarryover BEFORE the
	// first Store.Save so the seeded history is persisted. Empty = no carryover
	// (byte-identical default).
	sourceSessionID session.SessionID
	// owner overrides the context principal as the created session's owner
	// (ADR 0204 decision 4). ownerSet distinguishes "WithOwner was called
	// (possibly with nil — an explicitly ownerless session)" from "never
	// called", which falls back to session.PrincipalFromContext.
	owner    *session.Principal
	ownerSet bool
	// scheduled is set only by the trusted scheduler composition path. Public
	// create requests have no field that can populate it.
	scheduled *session.SessionRelationship
}

// WithSessionID overrides the session id a CreateSession* call mints. When set,
// the id MUST be non-empty and MUST NOT collide with a live per-session engine
// (the sessionEngines map); a collision is rejected with ErrInvalidArgument.
// An empty id is rejected. When no WithSessionID option is passed, the existing
// NewID path is byte-identical. It is the seam ADR 0059 decision #7 Phase-2
// uses to mint "sched--"-prefixed fire-session ids.
func WithSessionID(id session.SessionID) CreateSessionOption {
	return func(o *createSessionOpts) { o.id, o.idSet = id, true }
}

// WithSourceSession seeds a NEW session's conversation history from the named
// source session (issue #20: model-switch context carryover). The source is
// loaded through the run-entry funnel (loadAndReopen recovers terminal states
// to idle), snapshotted via session.ForkSnapshot (a deep copy with trailing
// unanswered tool calls stripped), and seeded into the new session BEFORE its
// first Store.Save via session.SeedHistory. Carryover is ALWAYS allowed across
// providers: a SAME-provider carryover replays the history verbatim (blobs
// intact, warm cache); a CROSS-provider carryover seeds a provider-neutral copy
// (session.StripProviderState clears Reasoning/ProviderPhase/ItemID). A source
// that is still running/awaiting is rejected with ErrFailedPrecondition. Empty
// (no option) is the byte-identical no-carryover path.
func WithSourceSession(id session.SessionID) CreateSessionOption {
	return func(o *createSessionOpts) { o.sourceSessionID = id }
}

// WithOwner overrides the owner a CreateSession* call stamps on the new session
// (ADR 0204 decision 4). By DEFAULT the owner comes from the verified principal
// on the context (session.PrincipalFromContext) — a caller can never name its
// own owner in the request body, which is why CreateSessionRequest has no owner
// field. This option is the in-process injection seam for a caller that already
// holds the owning principal out of band: the scheduler fire path, which runs
// under the system principal but must attribute the fire session to the
// SCHEDULE's captured owner.
//
// WithOwner(nil) is the EXPLICIT ownerless injection (a system-owned session
// with nobody to attribute it to) and does NOT fall back to the context
// principal — a fabricated owner is worse than none.
func WithOwner(p *session.Principal) CreateSessionOption {
	return func(o *createSessionOpts) { o.owner, o.ownerSet = p, true }
}

// WithScheduledRelationship is the trusted composition-only creation seam for
// scheduler fires. No public request field maps to this option.
func WithScheduledRelationship(scheduleName string, origin session.SessionID) CreateSessionOption {
	return func(o *createSessionOpts) {
		o.scheduled = &session.SessionRelationship{ScheduleName: scheduleName, OriginSessionID: origin}
	}
}

// resolveOwner picks the owner a create stamps: the explicit WithOwner value
// when the option was passed (nil included — see WithOwner), else the verified
// principal riding the context. An absent principal yields nil — the ownerless
// no-auth path, byte-identical to the pre-ADR-0204 behaviour. It NEVER
// fabricates one.
func resolveOwner(ctx context.Context, opts createSessionOpts) *session.Principal {
	if opts.ownerSet {
		return opts.owner
	}
	return session.PrincipalFromContext(ctx)
}

func newCreatedSession(id session.SessionID, mode session.PermissionMode, workspace string, limits session.Limits, createdAt time.Time, scheduled *session.SessionRelationship) (*session.Session, error) {
	if scheduled == nil {
		return session.New(id, mode, workspace, limits, createdAt), nil
	}
	return session.NewScheduled(id, mode, workspace, limits, createdAt, scheduled.ScheduleName, scheduled.OriginSessionID)
}

// CreateSession allocates a new idle session on the SHARED engine, persists it,
// and returns it. workspace must be non-empty. An unspecified mode falls back to
// DefaultMode. It is the no-selector, no-MCP fast path: it delegates to the
// generalized createSession with the zero selector, nil specs and the default
// profile.
func (s *Service) CreateSession(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits) (*session.Session, error) {
	return s.createSession(ctx, workspace, mode, limits, ProviderSelector{}, nil, ProfileDefault, createSessionOpts{})
}

// CreateSessionWithProvider creates a session bound to a non-default
// provider/model selector (multi-provider Phase 0, S3) via a PER-SESSION engine,
// with no client MCP and the DEFAULT profile. It delegates to
// CreateSessionWithProfile; see there for the selector semantics.
func (s *Service) CreateSessionWithProvider(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, sel ProviderSelector) (*session.Session, error) {
	return s.CreateSessionWithProfile(ctx, workspace, mode, limits, sel, ProfileDefault)
}

// CreateSessionWithProfile creates a session bound to an optional non-default
// provider/model selector AND a tool-surface profile (issue #55), with no client
// MCP. It is the gRPC/HTTP entry for a CreateSession request. The zero selector
// + default profile delegates to the shared-engine fast path; a non-zero
// selector OR the no-fs profile REQUIRES Config.SessionEngine (else
// ErrInvalidArgument) and resolves through the factory (an unknown/unavailable
// provider id surfaces as ErrInvalidArgument). Setting ModelID with an empty
// ProviderID is rejected (a bare model on the env-derived default provider is
// ambiguous). The workspace requirement is PROFILE-AWARE — see createSession.
//
// opts is the variadic options pattern (CreateSessionOption): WithSessionID
// overrides the minted id (ADR 0059 decision #7 Phase-2 — the scheduler fire
// path mints a "sched--"-prefixed id). Zero opts is byte-identical to the
// pre-Phase-2 signature.
func (s *Service) CreateSessionWithProfile(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, profile SessionProfile, opts ...CreateSessionOption) (*session.Session, error) {
	if sel.ProviderID == "" && sel.ModelID != "" {
		return nil, fmt.Errorf("%w: model_id requires provider_id (a bare model on the default provider is ambiguous)", ErrInvalidArgument)
	}
	var o createSessionOpts
	for _, opt := range opts {
		opt(&o)
	}
	return s.createSession(ctx, workspace, mode, limits, sel, nil, profile, o)
}

// createSession is the single create path generalizing the shared-engine fast
// path, the per-session provider/model selector, the per-session client MCP
// servers, and the session profile. A session needs a PER-SESSION engine when
// the selector is non-zero OR specs are non-empty OR the profile is no-fs (the
// shared engine has the FS tools baked in — a no-FS session MUST NOT ride it);
// otherwise it uses the shared engine (zero overhead, no registry entry —
// today's byte-identical path). The factory takes all three inputs so a session
// combining them gets ONE engine over ONE catalog. On a persist failure after
// the engine was built, the per-session MCP manager is torn down so a failed
// create never leaks it.
//
// PROFILE-AWARE workspace rule (replacing the old unconditional empty-workspace
// guard): the default profile REQUIRES a workspace (unchanged); the no-fs
// profile REQUIRES an EMPTY one — the combination is contradictory and is
// REJECTED loudly, never resolved by silently dropping either field. A no-fs
// session persists Workspace == "" and registers the no-FS Workspace as its
// per-session workspace OVERRIDE at create time (the ACP-buffer-workspace
// mechanism, same lock as the engine registration), so StartRun can never hand
// "" to the osfs workspace factory (which would MkdirAll/OpenRoot the process
// cwd).
// setSessionLabels records the neutral provider+model selector and the
// tool-surface profile onto the freshly-created aggregate as write-once creation
// labels. The aggregate stores them opaquely (it never interprets the
// ProviderSelector type, which stays a server-adapter type); persisting them is
// what lets rehydrateSession rebuild the SAME engine after a restart. For the
// empty-selector default profile this writes the zero values, so a default
// session's snapshot is byte-identical to a pre-Phase-1 one (the labels omitempty
// out of the JSON).
func setSessionLabels(sess *session.Session, sel ProviderSelector, profile SessionProfile, owner *session.Principal) error {
	sess.Profile = string(profile)
	sess.ProviderID = sel.ProviderID
	sess.ModelID = sel.ModelID
	sess.ReasoningEffort = sel.ReasoningEffort
	// The owner is WRITE-ONCE and is stamped through the aggregate (Session is an
	// aggregate — never poke the field). On a freshly-minted session the slot is
	// empty, so this cannot collide; the error is propagated rather than dropped so
	// a future caller that re-labels a LOADED session fails loudly instead of
	// silently re-owning it. A nil owner leaves the session ownerless.
	return sess.RestoreLabels(owner, "")
}

// seedCarryover seeds the freshly-created (idle) session with an optional
// carryover snapshot (issue #20). A nil snapshot is a no-op (the byte-identical
// no-carryover default); a non-nil snapshot is seeded via session.SeedHistory,
// which re-validates tool-pairing (ForkSnapshot already stripped trailing
// orphans, so this is a defense-in-depth re-check) and is legal only from
// StateIdle (a freshly-created session). The error is wrapped for the caller to
// map to a status; on failure the caller tears down any per-session engine it
// already built.
func seedCarryover(sess *session.Session, snap []session.Message) error {
	if snap == nil {
		return nil
	}
	if err := sess.SeedHistory(snap); err != nil {
		return fmt.Errorf("server: seed carryover history: %w", err)
	}
	return nil
}

// createRequest is the immutable caller-controlled shape a retried explicit ID
// must match. It deliberately excludes the owner: that comes only from the
// verified context and is checked separately.
type createRequest struct {
	workspace string
	mode      session.PermissionMode
	limits    session.Limits
	selector  ProviderSelector
	profile   SessionProfile
	sourceID  session.SessionID
}

func (r createRequest) matches(sess *session.Session) bool {
	return r.sourceID == "" && sess.Workspace == r.workspace && sess.Mode == r.mode &&
		sess.Limits == r.limits && sess.ProviderID == r.selector.ProviderID &&
		sess.ModelID == r.selector.ModelID && sess.ReasoningEffort == r.selector.ReasoningEffort &&
		sess.Profile == string(r.profile)
}

func sameCreateOwner(a, b *session.Principal) bool {
	return (a == nil && b == nil) || a.SameIdentity(b)
}

// reserveSessionID validates a caller-chosen session id (WithSessionID, ADR 0059
// decision #7 Phase-2) against THREE collision sources and reserves it for the
// duration of the create, returning a release func the caller MUST defer:
//
//  1. a LIVE per-session engine (sessionEngines — a collision would shadow an
//     in-flight session);
//  2. an in-flight create holding the id (reservedIDs — closes the old TOCTOU:
//     the prior check released s.mu before the slow factory call and the separate
//     registration, so two concurrent creates on the same id both passed);
//  3. a PERSISTED session already in the store (a completed prior create is NOT
//     in sessionEngines — e.g. the shared-engine fast path never registers there).
//
// The in-memory reservation (1)+(2) is taken under a single s.mu hold; the store
// probe (3) runs after (no I/O under the mutex). On a collision or an infra probe
// fault the reservation is released before returning the error. Once the session
// is registered (per-session) or persisted (shared) the durable collision sources
// take over, so the reservation only needs to live for the create.
func (s *Service) reserveSessionID(ctx context.Context, id session.SessionID, owner *session.Principal, request createRequest) (existing *session.Session, release func(), err error) {
	s.mu.Lock()
	_, liveEngine := s.sessionEngines[id]
	_, reserved := s.reservedIDs[id]
	if liveEngine || reserved {
		s.mu.Unlock()
		// Accurate for BOTH cases: a live per-session engine (liveEngine) OR a
		// concurrent in-flight create holding the id (reserved).
		return nil, nil, fmt.Errorf("%w: session id %q is already in use", ErrInvalidArgument, id)
	}
	s.reservedIDs[id] = struct{}{}
	s.mu.Unlock()
	release = func() {
		s.mu.Lock()
		delete(s.reservedIDs, id)
		s.mu.Unlock()
	}
	// Probe the store for a persisted session under this id. A not-found error
	// means the id is clear; any other error is an infra fault that must not
	// silently pass, so it is propagated.
	if existing, lerr := s.cfg.Store.Load(ctx, id); lerr == nil && existing != nil {
		release()
		if !s.cfg.OwnershipEnforced {
			return nil, nil, fmt.Errorf("%w: session id %q already exists", ErrInvalidArgument, id)
		}
		if sameCreateOwner(existing.Owner, owner) {
			if request.matches(existing) {
				return existing, nil, nil
			}
			return nil, nil, fmt.Errorf("%w: session id %q was retried with a different request", ErrInvalidArgument, id)
		}
		return nil, nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	} else if lerr != nil && !errors.Is(lerr, port.ErrSessionNotFound) {
		release()
		return nil, nil, fmt.Errorf("server: probe session id %q: %w", id, lerr)
	}
	return nil, release, nil
}

func (s *Service) createSession(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, opts createSessionOpts) (*session.Session, error) {
	switch profile {
	case ProfileDefault:
		if workspace == "" {
			return nil, fmt.Errorf("%w: workspace is required", ErrInvalidArgument)
		}
	case ProfileNoFS:
		if workspace != "" {
			return nil, fmt.Errorf("%w: profile %q must not carry a workspace (a no-FS session has no filesystem to root); got %q", ErrInvalidArgument, ProfileNoFS, workspace)
		}
	default:
		// Defensive: the wire handlers ParseSessionProfile first, but an
		// in-process caller could hand anything.
		return nil, fmt.Errorf("%w: unknown session profile %q (supported: \"\" (default) and %q)", ErrInvalidArgument, profile, ProfileNoFS)
	}
	if mode == "" {
		mode = s.cfg.DefaultMode
	}
	// Fill any UNSET (zero) limit field from the injected defaults, per-field. An
	// all-zero Limits inherits every default (so a default session cannot run
	// unbounded); a Limits that pins only some caps keeps those and inherits the
	// rest, rather than the old all-or-nothing substitution that silently disabled
	// the unset caps. A zero field means "unset", not "explicitly unlimited".
	limits = limits.WithDefaults(s.cfg.DefaultLimits)

	// The owner stamped on the new session: the explicit WithOwner injection, else
	// the verified principal on the context, else nil (the ownerless no-auth path).
	owner := resolveOwner(ctx, opts)

	// Resolve the session id: the caller's override (WithSessionID, ADR 0059
	// decision #7 Phase-2) wins; otherwise the Service's NewID generator mints a
	// fresh one (the byte-identical pre-Phase-2 path). WithSessionID with an EMPTY
	// id is rejected (the doc promises it), distinguished from "never called" by
	// idSet. A caller-chosen id is validated + reserved by reserveSessionID (see
	// its doc for the three collision sources); the reservation is released on
	// EVERY exit path.
	mintID := s.cfg.NewID
	if opts.idSet {
		if opts.id == "" {
			return nil, fmt.Errorf("%w: session id must not be empty", ErrInvalidArgument)
		}
		request := createRequest{workspace: workspace, mode: mode, limits: limits, selector: sel, profile: profile, sourceID: opts.sourceSessionID}
		existing, release, err := s.reserveSessionID(ctx, opts.id, owner, request)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		defer release()
		mintID = func() session.SessionID { return opts.id }
	}

	// Issue #20 (model-switch context carryover): when a source session is
	// named, validate it (turn-boundary) and snapshot its conversation ONCE
	// here, so both create branches seed the new session's history BEFORE the
	// first Store.Save (the persisted snapshot records the seeded history). The
	// snapshot is a deep copy (session.ForkSnapshot) with trailing unanswered
	// tool calls stripped, so it is tool-pairing-valid for SeedHistory. An empty
	// sourceSessionID (the default) skips carryover entirely — byte-identical to
	// the pre-issue-#20 path. The new session's resolved provider is
	// sel.ProviderID (empty => server default, resolved against
	// DefaultResolvedModel inside validateCarryover); a SAME-provider carryover
	// replays the history verbatim, a CROSS-provider carryover strips the
	// provider-private blobs (session.StripProviderState).
	// A carryover fork replaces the context owner with the source's owner below.

	var carrySnap []session.Message
	if opts.sourceSessionID != "" {
		snap, srcOwner, err := s.validateCarryover(ctx, opts.sourceSessionID, sel.ProviderID)
		if err != nil {
			return nil, err
		}
		carrySnap = snap
		// A fork inherits the SOURCE's owner, overriding the context principal
		// (and any WithOwner) — see validateCarryover.
		owner = srcOwner
	}

	needPerSession := s.sessionNeedsPerFactory(sel, specs, profile, workspace) ||
		s.cfg.LearnedSkills != nil && session.PrincipalFromContext(ctx) != nil
	if !needPerSession {
		// Shared-engine fast path (today's behaviour, byte-identical). The labels are
		// the empty pair + default profile here (the empty-selector default profile is
		// exactly the no-per-session case), so setLabels persists nothing new — the
		// snapshot stays byte-identical to a pre-Phase-1 default session.
		sess, err := newCreatedSession(mintID(), mode, workspace, limits, s.cfg.Now(), opts.scheduled)
		if err != nil {
			return nil, fmt.Errorf("server: create session metadata: %w", err)
		}
		if err := setSessionLabels(sess, sel, profile, owner); err != nil {
			return nil, err
		}
		stampDefaultEnvironmentRef(sess)
		if err := seedCarryover(sess, carrySnap); err != nil {
			return nil, err
		}
		if err := s.cfg.Store.Save(ctx, sess); err != nil {
			return nil, fmt.Errorf("server: persist session: %w", err)
		}
		return sess, nil
	}

	return s.createPerSessionEngine(ctx, mintID, mode, workspace, limits, sel, specs, profile, carrySnap, owner, opts.scheduled)
}

// createPerSessionEngine is the per-session-engine create branch, factored out
// of createSession so createSession stays under the gocyclo threshold. It
// enforces the engine-factory + registry-cap discipline (cheap pre-check, build
// outside the lock, authoritative re-check + register under the lock), seeds any
// carryover history BEFORE the first Store.Save, and on a persist failure
// evicts the reservation and tears the freshly-built engine down so a failed
// create leaks neither a slot nor a connection. See createSession for the
// profile-aware workspace rule and the carryover snapshot semantics.
func (s *Service) createPerSessionEngine(ctx context.Context, mintID func() session.SessionID, mode session.PermissionMode, workspace string, limits session.Limits, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, carrySnap []session.Message, owner *session.Principal, scheduled *session.SessionRelationship) (*session.Session, error) {
	if s.cfg.SessionEngine == nil {
		return nil, fmt.Errorf("%w: per-session engine not supported (no session-engine factory configured)", ErrInvalidArgument)
	}
	// Cheap cap pre-check (CWE-770): reject BEFORE the factory connects MCP /
	// allocates an engine when the registry is already full, so a hostile client
	// that never releases its sessions cannot even drive the (more expensive) build
	// path. The authoritative re-check under lock at registration below closes the
	// TOCTOU window (two concurrent creates racing the last slot).
	s.mu.Lock()
	full := len(s.sessionEngines) >= s.cfg.MaxSessionEngines
	s.mu.Unlock()
	if full {
		return nil, fmt.Errorf("%w: %d", ErrTooManySessionEngines, s.cfg.MaxSessionEngines)
	}

	res, err := s.cfg.SessionEngine(ctx, sel, specs, profile, workspace, mode)
	if err != nil {
		// Factory maps an unknown/unavailable provider to ErrInvalidArgument; any
		// error is propagated as-is for the caller to map to a status.
		return nil, err
	}
	eng, closeFn := res.Engine, res.Close
	sess, err := newCreatedSession(mintID(), mode, workspace, limits, s.cfg.Now(), scheduled)
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, fmt.Errorf("server: create session metadata: %w", err)
	}
	// Persist the neutral provider+model selector and the profile as write-once
	// creation labels on the aggregate, so a restarted process re-derives the SAME
	// per-session engine via the factory (rehydrateSession) instead of falling to the
	// default-provider floor / inferring the profile from the empty-workspace pun.
	if err := setSessionLabels(sess, sel, profile, owner); err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, err
	}
	stampDefaultEnvironmentRef(sess)
	if err := seedCarryover(sess, carrySnap); err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, err
	}

	// Authoritative cap check under the SAME lock as the insert (TOCTOU-safe): if
	// the registry filled between the pre-check and here, tear the freshly-built
	// engine down rather than exceed the cap. This is BEFORE the Store.Save, so a
	// cap rejection leaves NO orphan session in the store.
	s.mu.Lock()
	if len(s.sessionEngines) >= s.cfg.MaxSessionEngines {
		s.mu.Unlock()
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, fmt.Errorf("%w: %d", ErrTooManySessionEngines, s.cfg.MaxSessionEngines)
	}
	// Reserve the slot under the lock so a concurrent create cannot also claim it,
	// then persist OUTSIDE the lock (no I/O under the mutex). If the persist fails,
	// evict the reservation and tear the engine down.
	s.sessionEngines[sess.ID] = &sessionEngine{
		engine:          eng,
		caps:            res.Capabilities,
		providerID:      res.ProviderID,
		modelID:         res.ModelID,
		reasoningEffort: res.ReasoningEffort,
		builtForMode:    res.BuiltForMode,
		close:           closeFn,
	}
	if profile == ProfileNoFS {
		// Register the no-FS Environment as this session's per-session
		// environment OVERRIDE under the SAME lock as the engine registration,
		// so the moment the session is visible StartRun resolves its environment
		// here and NEVER hands the empty root to the shared osfs Workspaces
		// factory (which would MkdirAll/OpenRoot the server process's cwd — the
		// exact hazard). It is a complete shell-less Environment with an honest
		// nofs ref (no command runner: a file-less namespace has no shell).
		s.sessionEnvironments[sess.ID] = tool.MustEnvironment(defaultEnvironmentRef(sess), nofs.New(), nil)
	}
	s.mu.Unlock()

	if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
		// The engine was built and the slot reserved but the session could not be
		// persisted: evict the reservation (engine slot AND any environment
		// override) and tear the per-session MCP manager down so a failed create
		// leaks neither a slot nor a connection.
		s.mu.Lock()
		delete(s.sessionEngines, sess.ID)
		delete(s.sessionEnvironments, sess.ID)
		s.mu.Unlock()
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, fmt.Errorf("server: persist session: %w", serr)
	}
	return sess, nil
}

// capabilities reports which optional features THIS service has actually built,
// for the CreateSession response. It is the single source of truth for the
// client's honest-UI affordances; it reads the wired Config seams and the engine
// catalog (NOT a static list) so it can never claim a feature the server did not
// register. Tool presence is checked by the tools' registered names via
// Engine.HasTool, which is nil-safe (a nil engine/catalog yields the tool caps as
// false). The names are referenced from each owning package's exported constant
// — memory.RememberToolName (internal/adapter/memory.NewRememberTool),
// skills.ToolName (internal/adapter/skills.NewTool), tools.BashToolName
// (internal/adapter/tools.NewBashTool) — so the cap links to the registered name
// at COMPILE time and cannot drift on a rename.
func (s *Service) capabilities() *mecatlv1.ServerCapabilities {
	has := func(name string) bool {
		return s.cfg.Engine != nil && s.cfg.Engine.HasTool(name)
	}
	// Multimodal prompt-input caps come from the composition-computed DEFAULT
	// intersection (catalog ∩ adapter for the default provider+model), NOT the bare
	// engine.Capabilities() (which is adapter-only and would re-introduce the catalog
	// gap). The zero value (text-only) is the safe default for a child/member service
	// with no provider. This is the SAME DefaultCapabilities ProviderCapabilities()
	// returns, so the server-wide caps echo and the ACP gate share ONE source.
	pcaps := s.cfg.DefaultCapabilities
	return &mecatlv1.ServerCapabilities{
		Mcp:           s.cfg.MCPProvider != nil,
		SlashCommands: s.cfg.Commands != nil,
		Teams:         s.cfg.MemberEngine != nil,
		Agents:        len(s.cfg.Agents) > 0,
		Soul:          s.cfg.Soul != nil,
		UserModel:     s.cfg.UserModel != nil,
		// ModelSelection advertises "the client should offer the model picker". It
		// is true when either the inventory is already non-empty OR an on-demand
		// model refresher is wired (composition wires one whenever ≥1 available
		// provider has a live lister). The refresher arm matters because the
		// inventory is seeded from the CATALOG at Build and filled for UNCATALOGUED
		// providers (e.g. the ToolHive gateway, OpenCode Go) only by the ASYNC live
		// refresh — so a session created in the create-races-the-swap window would
		// otherwise read len(currentModels())==0 and freeze ModelSelection=false for
		// its whole life (caps are read once at CreateSession, never refetched). This
		// matches the client's documented semantics ("true when ≥1 provider is
		// available"); opening the picker fires ListModels, which runs the refresher
		// and populates the list, and the empty-list state is handled gracefully.
		ModelSelection:    len(s.currentModels()) > 0 || s.modelsRefresher.Load() != nil,
		Memory:            has(memory.RememberToolName),
		Skills:            has(skills.ToolName),
		Bash:              has(tools.BashToolName),
		Image:             pcaps.Image,
		Audio:             pcaps.Audio,
		Posture:           s.cfg.Posture,
		Worktrees:         s.cfg.Worktrees != nil,
		Reflection:        s.cfg.ReflectSession != nil,
		LearningProposals: s.cfg.Proposals != nil,
		LearnedSkills:     s.cfg.LearnedSkills != nil,
		Scheduling:        s.scheduleStore() != nil,
	}
}

// CreateSessionWithMCP creates a session that mounts the client-provided
// streaming-HTTP MCP servers (specs) for the lifetime of that session, via a
// PER-SESSION engine. It is the ACP session/new entry for an editor that supplies
// mcpServers.
//
//   - With NO specs it delegates to CreateSession: the session uses the SHARED
//     engine, with zero per-session overhead and no registry entry.
//   - With specs it REQUIRES Config.SessionEngine (else ErrInvalidArgument: "client
//     MCP not supported"); it builds the per-session engine via that factory, and
//     on success registers it under the new session id so StartRun routes the
//     session's runs to it. A factory error is returned as-is (the caller maps it).
//
// The per-session engine's MCP manager is torn down by CloseSession (editor
// disconnect) or by the Service's Close.
func (s *Service) CreateSessionWithMCP(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, specs []mcp.ServerConfig) (*session.Session, error) {
	// Thin wrapper over the generalized create path with the ZERO provider
	// selector and the DEFAULT profile: no specs uses the shared engine (today's
	// behaviour), specs build a per-session engine. The zero selector leaves the
	// per-session engine bound to the DEFAULT provider, matching the pre-S3 MCP
	// path exactly. (ACP carries no profile in P0 — every ACP session is the
	// default filesystem profile.)
	return s.createSession(ctx, workspace, mode, limits, ProviderSelector{}, specs, ProfileDefault, createSessionOpts{})
}

// SetSessionEnvironment registers a per-session Environment OVERRIDE for id, so a
// subsequent StartRun uses env as the COMPLETE execution environment (Workspace +
// optional bound CommandRunner + accurate ref) instead of building one from the
// shared factories. It is the seam the ACP adapter uses to route a session's file
// I/O through the editor's fs/* buffers: the ACP adapter constructs a complete
// shell-less Environment (a real-filesystem Workspace rooted at the session cwd
// with a local ref, but NO command runner — the editor provides no shell) and
// registers it here. A second call for the same id replaces the override. The
// override is evicted by CloseSession (and drained by Close), so the caller MUST
// pair it with CloseSession on the owning connection's teardown (the ACP adapter
// tracks the session and does this on disconnect). The gRPC/HTTP surfaces never
// call this, so their environment path is unchanged (issue #462 phase-2 finding #2).
func (s *Service) SetSessionEnvironment(id session.SessionID, env tool.Environment) {
	s.mu.Lock()
	s.sessionEnvironments[id] = env
	s.mu.Unlock()
}

// CloseSession tears down the per-session engine registered for id (if any) and
// removes it from the registry. It is idempotent: an id with no per-session
// engine is a no-op. The ACP adapter calls it when an editor disconnects so a
// session's client-provided MCP manager does not outlive the session.
//
// SAFE under an in-flight run (so it needs no run-aware guard like the team path):
// the close func is the MCP manager's Close, a GRACEFUL shutdown — the underlying
// go-sdk ClientSession.Close "prevents new requests from being handled, and WAITS
// for ongoing requests to return" before terminating the connection, and is
// documented idempotent + concurrency-safe. A disconnect that races a live
// engine.Run dispatching an MCP tool call therefore does NOT yank the connection
// mid-call: Close blocks until that CallTool returns (or the jsonrpc2 layer retires
// it with an error response the remoteTool maps to a model-facing error). The
// editor disconnect already implies the run is being abandoned, so blocking briefly
// for the in-flight call to unwind is the correct, leak-free behaviour.
func (s *Service) CloseSession(id session.SessionID) {
	// Release composition-owned session-scoped state first (e.g. the per-session
	// learned permission rules) so it never outlives the session, even if the
	// per-session engine teardown below is a no-op for this id.
	if s.cfg.OnCloseSession != nil {
		s.cfg.OnCloseSession(id)
	}
	s.mu.Lock()
	se, ok := s.sessionEngines[id]
	if ok {
		delete(s.sessionEngines, id)
	}
	// Drop any per-session environment override too: it closes over the (now
	// disconnecting) connection, so it must not outlive the session.
	delete(s.sessionEnvironments, id)
	// Drop the once-per-id approval-replay marker (cloud-native Phase 3b): the
	// OnCloseSession above Forgot this session's learned rules, so a LATER reload of
	// the same id in this process MUST be allowed to replay them from the durable log
	// again — otherwise the replay would short-circuit (marker still set) and leave
	// the rules evicted, re-opening the very re-ask wart 3b kills. Clearing it also
	// keeps the map from growing unbounded on a long-lived server.
	delete(s.replayedApprovals, id)
	// Drop any stored recover-notice (issue #346): a session closed without
	// re-entry after a permanent failure would otherwise leak one entry in the
	// sync.Map; like the approval-replay marker above, clearing it keeps the map
	// from growing unbounded on a long-lived server.
	s.recoverNotices.Delete(id)
	s.mu.Unlock()
	if ok && se.close != nil {
		_ = se.close()
	}
	// Stop the session's renewer and release its cross-process lease (cloud-native
	// Phase 4): the session is ending, so a competitor may now take it over. No-op
	// when no lease is wired or held.
	s.releaseLease(id)
}

// EndSession is the precondition-checked sibling of CloseSession: the
// surface-facing session-end entry for the gRPC/HTTP transports (the ACP adapter
// calls the void CloseSession directly on disconnect). It verifies the session
// exists, then runs the same teardown as CloseSession (OnCloseSession ->
// learned-rule Forget, per-session engine + workspace eviction). It returns
// ErrNotFound for a never-created id; teardown is idempotent, so closing an
// already-released (but still persisted) session succeeds. It does NOT delete the
// persisted snapshot and does NOT cancel an in-flight run (orthogonal to Cancel).
func (s *Service) EndSession(ctx context.Context, id session.SessionID) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	s.CloseSession(id)
	return nil
}

// Close tears down all per-session engines' MCP managers. It is the Service's
// shutdown hook so a process exit does not leak any per-session MCP connection.
// It is safe to call multiple times.
func (s *Service) Close() {
	// Signal shutdown so in-flight runs (scheduled fires and foreground turns)
	// observe the cancellation and unwind. This fires BEFORE the scheduler stop
	// and before engine-close so runs unblock promptly rather than waiting on
	// the full shutdown sequence.
	s.shutdownCancel()

	// Cancel every in-flight run so an LLM/MCP call blocked on its context
	// unwinds. Snapshot under s.mu, then cancel outside to avoid holding the
	// lock across Cancel (which may block briefly on hardAbort).
	//
	// AWAITING runs are EXCLUDED: a run parked on a permission ask persists a
	// durable StateAwaiting snapshot (the relay's Persist-on-ask) that is the
	// cloud-native Phase 2 resume point — a restarted process (or a peer replica)
	// re-enters the loop at the ask via ApproveRun/ApprovePlan (Engine.
	// ResumeApproval). Cancelling such a run would transition awaiting→cancelled
	// and persist the cancelled snapshot OVER the awaiting one, destroying the
	// cross-process resume contract. The parked run's goroutine waits on
	// askRegistry.await (ctx-gated, but we do NOT cancel it); it is goleak-ignored
	// (leakmain_test.go), so leaving it parked across Close is leak-clean, and the
	// durable awaiting snapshot survives the shutdown. The awaiting signal is the
	// race-free runState.awaiting atomic that Persist sets when the session is
	// StateAwaiting (the relay calls Persist on EvPermissionAsk; reading sess.State
	// there races no concurrent loop write — the loop is parked). A fire-driven run
	// (no relay) never marks the flag, so it is cancelled like any mid-stream run —
	// correct, since the fire path persists no awaiting snapshot to preserve.
	// Mid-stream (StateRunning) runs have no durable mid-flight snapshot, so they
	// ARE cancelled to unwind blocked LLM/MCP calls (the Task #3 intent).
	s.mu.Lock()
	var runs []*runState
	for _, rs := range s.runs {
		runs = append(runs, rs)
	}
	s.mu.Unlock()
	for _, rs := range runs {
		if rs.awaiting.Load() {
			continue // resumable cross-process via the durable awaiting snapshot
		}
		rs.run.Cancel()
	}

	// Stop the scheduler FIRST so in-flight fires drain while the service is
	// still alive to serve them (the FireFunc drives StartRunContent on this
	// Service). Stop cancels the tick loop, joins in-flight fires (with a grace),
	// and releases the leader lease. The scheduler lives on the embedded
	// scheduleManager (ADR 0076) as an atomic pointer; nil-safe (no scheduler
	// wired, or no manager at all). Read via HasScheduler + the manager's
	// atomic pointer — no s.mu (the manager's atomic is the single truth).
	if m := s.schedMgr; m != nil {
		if sch := m.scheduler.Load(); sch != nil {
			_ = sch.Stop()
		}
	}
	s.mu.Lock()
	engines := s.sessionEngines
	s.sessionEngines = make(map[session.SessionID]*sessionEngine)
	// Drop all per-session environment overrides on shutdown; they hold no resources
	// of their own (the underlying connection is closed separately) but must not
	// linger past the Service.
	s.sessionEnvironments = make(map[session.SessionID]tool.Environment)
	// Close all per-session event subscriptions so subscriber goroutines can exit
	// cleanly. Close owns both shutdown and channel closure while holding subMu: a
	// publisher holds subMu.RLock through its send, so no send can race close.
	s.subMu.Lock()
	s.subscriptionsClosed = true
	for _, m := range s.subscriptions {
		for _, ch := range m {
			close(ch)
		}
	}
	s.subscriptions = make(map[session.SessionID]map[int64]chan session.Event)
	s.subMu.Unlock()
	// Snapshot the held-lease ids so we can stop renewers + release each outside
	// the guard (releaseLease re-takes s.mu).
	leasedIDs := make([]session.SessionID, 0, len(s.heldLeases))
	for id := range s.heldLeases {
		leasedIDs = append(leasedIDs, id)
	}
	s.mu.Unlock()

	// Close every per-session engine in a goroutine with a BOUNDED timeout so a
	// stuck engine close (e.g. a wedged MCP transport) cannot stall shutdown
	// unboundedly. On timeout the goroutine is abandoned (best-effort) and a
	// WARN is logged; the leases still release so the process can exit.
	done := make(chan struct{})
	go func() {
		for _, se := range engines {
			if se.close != nil {
				_ = se.close()
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(engineCloseTimeout):
		s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "timed out waiting for per-session engine close; abandoning",
			"timeout", engineCloseTimeout.String())
	}

	// Stop every renewer and release every held cross-process lease on shutdown
	// (cloud-native Phase 4), so a restarted process can take the sessions over
	// without waiting out the TTL. Best-effort (detached short-timeout ctx).
	for _, id := range leasedIDs {
		s.releaseLease(id)
	}
}

// Drain arms the drain gate (ADR 0048, mecak8s): subsequent run-entries
// (StartRunContent / resumeFromAwaiting via acquireLease) are rejected with
// ErrUnavailable so a shutting-down replica stops accepting new runs and a
// rolling update steers traffic to a survivor. It is idempotent and safe to
// call from a signal handler or the /drain HTTP endpoint. In-flight runs are
// NOT cancelled here — that is the bounded GracefulStop's job in the cmd
// binary; Drain only gates new entries. The gate is one-way: there is no
// un-drain (a draining replica is retiring).
func (s *Service) Drain() {
	// Arm the scheduler's drain gate too so no NEW fires start mid-tick during
	// shutdown (in-flight fires complete or are cancelled by Close's Stop).
	// The scheduler lives on the embedded scheduleManager (ADR 0076) as an
	// atomic pointer; nil-safe (no scheduler wired, or no manager at all).
	if m := s.schedMgr; m != nil {
		if sch := m.scheduler.Load(); sch != nil {
			sch.Drain()
		}
	}
	s.draining.Store(true)
}

// SetScheduler, SetScheduleMinInterval, HasScheduler, and ScheduleManager are
// defined in schedule.go — the *Service's thin delegating schedule surface
// (ADR 0076). They forward to the embedded scheduleManager (s.schedMgr); the
// manager holds the cadence floor + the late-set in-process scheduler.

// Diagnostics returns the operational diagnostics sink the Service was
// configured with. It is the read-side accessor composition (the scheduler's
// FireFunc) uses to WARN on a non-fatal degradation (e.g. a carried-context
// prior-session-load failure that degrades to fresh-context). A NopDiagnostics
// is returned when none was wired (the constructor guarantees non-nil, so this
// is belt-and-suspenders).
func (s *Service) Diagnostics() port.Diagnostics {
	return s.cfg.Diagnostics
}

// IsDraining reports whether the drain gate is armed. It is the read-side
// companion to Drain: a cmd binary's dynamic ReadyFunc (mecak8s /readyz)
// closes over it so readiness flips to not-ready the moment Drain is armed,
// without coupling the probe to the Service's internal atomic. It is the
// ReadyFunc's read; ActiveRuns is the "how many in flight" figure. Safe to
// call from a signal handler / HTTP handler goroutine.
func (s *Service) IsDraining() bool {
	return s.draining.Load()
}

// ActiveRuns reports the number of sessions this process is actively driving —
// the count of held session leases (one per live run-entry). It is the
// "draining: N active runs" figure a graceful shutdown logs after Drain, so the
// operator can see how many in-flight runs the bounded GracefulStop will
// cancel. Zero (or no lease wired) means nothing is in flight.
func (s *Service) ActiveRuns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.heldLeases)
}

// storagePinger is a store that can report its own readiness (e.g.
// redisstore.Store). A store without a backend to ping (memstore, jsonlstore,
// grpcdriver) does not implement it, and StorageReady treats it as always
// ready — readiness is then drain-gated only.
type storagePinger interface {
	Ping(ctx context.Context) error
}

// StorageReady reports whether the session store is reachable. It is the
// readiness probe the cmd binary's ReadyFunc closes over, so /readyz tests the
// SAME store the Service serves traffic through (not a second client opened in
// the binary). A non-pinging store (memstore, jsonlstore, grpcdriver — no
// Ping method) is treated as always ready; a Redis store's Ping determines
// readiness. The caller should bound ctx (e.g. 2s) so a stalled backend fails
// the probe quickly rather than wedging readiness.
func (s *Service) StorageReady(ctx context.Context) bool {
	p, ok := s.cfg.Store.(storagePinger)
	if !ok {
		return true // a non-Redis store has no ping — always ready
	}
	return p.Ping(ctx) == nil
}

// GetSession returns the persisted session under id, or ErrNotFound.
func (s *Service) GetSession(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil || s.authorizeSession(ctx, sess) != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	// The session ID is an opaque handle, so repairing malformed bytes here would
	// silently turn one persisted identity into another before protobuf mapping.
	if !utf8.ValidString(string(sess.ID)) {
		return nil, fmt.Errorf("%w: persisted session has an invalid UTF-8 id", ErrInternal)
	}
	return sess, nil
}

// RenameSession applies an explicit operator title change to an owned main
// session. Authorization, kind/state/liveness checks, and lease acquisition are
// serialized under the same per-session mutex used by prompt starts.
func (s *Service) RenameSession(ctx context.Context, id session.SessionID, title string) (*session.Session, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, absent, err := s.managementTarget(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := sess.RenameTitle(title); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := s.cfg.Store.Save(ctx, sess); err != nil {
		return nil, fmt.Errorf("%w: rename session: %v", ErrInternal, err)
	}
	return sess, nil
}

// DeleteSession physically removes an owned main session and all store-managed
// sidecars. Absence and foreign ownership are both idempotent success, preventing
// deletion from becoming an ownership oracle. Infrastructure failures remain loud.
func (s *Service) DeleteSession(ctx context.Context, id session.SessionID) error {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, absent, err := s.managementTarget(ctx, id, true)
	if err != nil || absent {
		return err
	}
	prunable, ok := s.cfg.Store.(port.PrunableStore)
	if !ok {
		return ErrSessionDeleteUnsupported
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err := prunable.Delete(ctx, sess.ID); err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil
		}
		if errors.Is(err, port.ErrPruneUnsupported) {
			return ErrSessionDeleteUnsupported
		}
		return fmt.Errorf("%w: delete session: %v", ErrInternal, err)
	}
	s.CloseSession(id)
	return nil
}

// managementTarget performs the common management authorization and eligibility
// gate. The caller must hold runEntryMu for id. concealAbsence makes missing and
// foreign sessions indistinguishable idempotent success for DeleteSession.
func (s *Service) managementTarget(ctx context.Context, id session.SessionID, concealAbsence bool) (*session.Session, bool, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			if concealAbsence {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, false, fmt.Errorf("%w: load session: %v", ErrInternal, err)
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		if concealAbsence {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	// Session IDs are opaque handles. Repairing malformed persisted bytes would
	// silently change the identity returned by RenameSession's wire response.
	if !utf8.ValidString(string(sess.ID)) {
		return nil, false, fmt.Errorf("%w: persisted session has an invalid UTF-8 id", ErrInternal)
	}
	if sess.Kind != session.SessionKindMain || hasLegacyNonChatPrefix(id) {
		return nil, false, fmt.Errorf("%w: session is not a main session", ErrFailedPrecondition)
	}
	if sess.State == session.StateRunning || sess.State == session.StateAwaiting || s.IsLive(id) {
		return nil, false, fmt.Errorf("%w: session is active or awaiting approval", ErrFailedPrecondition)
	}
	return sess, false, nil
}

// SetMode changes the permission posture of the session under id and persists
// the change, returning the updated session. It is the out-of-band mode-switch
// seam (ACP session/set_mode). It applies to the LIVE session when a run is
// registered (so the change takes effect immediately for an idle-between-prompts
// session held in the registry) and otherwise to the stored snapshot.
//
// It returns ErrNotFound for an unknown session, ErrInvalidArgument for an empty
// mode, and propagates session.ErrIllegalTransition (wrapped as ErrInvalidArgument)
// when the session is mid-turn (running/awaiting) — the aggregate refuses a mode
// change while a turn is in flight, so the caller must defer it to the next
// prompt. A change to the mode the session already has is a no-op success.
func (s *Service) SetMode(ctx context.Context, id session.SessionID, mode session.PermissionMode) (*session.Session, error) {
	if mode == "" {
		return nil, fmt.Errorf("%w: mode is required", ErrInvalidArgument)
	}
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	// Prefer the live session the engine drives (if registered) so the change is
	// observed by the same object; otherwise operate on the stored snapshot.
	s.mu.Lock()
	st, live := s.runs[id]
	s.mu.Unlock()

	var sess *session.Session
	if live {
		sess = st.sess
	} else {
		loaded, err := s.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		sess = loaded
	}

	if err := sess.SetMode(mode); err != nil {
		// A mid-turn refusal from the aggregate is a client-sequencing error.
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := s.cfg.Store.Save(ctx, sess); err != nil {
		return nil, fmt.Errorf("server: persist session: %w", err)
	}
	return sess, nil
}

// LoadSession resumes a previously-persisted session so a subsequent StartRun
// continues it. It loads the latest snapshot from the store and, if the session
// is in a terminal state, REOPENS/INTERRUPTS/RECOVERS it to StateIdle
// (preserving the conversation history) and re-persists, so the next prompt's
// BeginTurn is legal. A cleanly COMPLETED session is reopened via Reopen; a
// CANCELLED session (an interrupted turn) is recovered via Interrupt, which also
// repairs the history (closing out any orphaned tool calls); a FAILED session
// (a transient provider failure) is recovered via Recover, the same history
// repair (issue #51) — recovery makes retry possible, not guaranteed. A session
// already idle is returned unchanged.
//
// It returns ErrNotFound when the store has no snapshot for id (including the
// in-memory store after a process restart, or when no store-dir is configured
// and the id was never created in this process).
func (s *Service) LoadSession(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.loadAndReopen(ctx, id)
}

// ForkSession creates a new peer session whose conversation history is a snapshot
// of an existing session's, inheriting the source's mode, workspace, limits, and
// provider/model/profile labels (ADR 0065). Same provider and model only; the ONE
// permitted selector delta is an optional reasoning-effort override (ADR 0068).
//
// The source is authorized and checked for main-kind, liveness, and awaiting state
// under runEntryMu, then leased before terminal recovery or snapshotting. A terminal
// source is recovered to idle first (completed→Reopen / cancelled→Interrupt /
// failed→Recover) and approval replay runs. The snapshot is session.ForkSnapshot
// (fresh backing array, trailing orphans stripped, tool-pairing-valid), seeded via
// session.SeedHistory into a fresh session.New aggregate. The new session starts
// with zeroed Counters/Usage but inherits the source's history verbatim (not re-fenced —
// peer-trust parity, same as the subagent fork).
//
// title overrides the forked session's title when non-empty; empty inherits the
// source's title verbatim.
//
// effortOverride (ADR 0068) overrides the forked session's reasoning-effort label
// when non-empty; empty inherits the source's effort verbatim. The override changes
// ONLY the effort label/engine — provider and model ALWAYS inherit (a fork carries
// provider-private replay blobs, so cross-provider/model stays out of scope). A
// non-empty override makes the fork need a per-session engine (sessionNeedsPerFactory
// fires on the changed selector), which the rehydrate path below builds.
//
// The engine is rehydrated ONLY when the source needed a per-session engine
// (non-default selector / no-fs profile / worktree workspace), mirroring
// createSession's branching on sessionNeedsPerFactory; a default-FS fork rides the
// shared engine (zero overhead, no registry entry). The MaxSessionEngines cap is
// enforced by the rehydrate path. Returns the new id.
func (s *Service) ForkSession(ctx context.Context, srcID session.SessionID, title, effortOverride string) (session.SessionID, error) {
	unlock := s.runEntryMu.lock(srcID)
	defer unlock()
	src, absent, err := s.managementTarget(ctx, srcID, false)
	if err != nil {
		return "", err
	}
	if absent {
		return "", fmt.Errorf("%w: %q", ErrNotFound, srcID)
	}
	release, err := s.acquireMutationLease(ctx, srcID)
	if err != nil {
		return "", err
	}
	defer release()
	src, err = s.reopenLoadedSession(ctx, src)
	if err != nil {
		return "", err
	}
	snap := session.ForkSnapshot(src.Conversation)
	forked := session.New(s.cfg.NewID(), src.Mode, src.Workspace, src.Limits, s.cfg.Now())
	if err := forked.SeedHistory(snap); err != nil {
		return "", fmt.Errorf("server: seed fork history: %w", err)
	}
	sel := ProviderSelector{ProviderID: src.ProviderID, ModelID: src.ModelID, ReasoningEffort: src.ReasoningEffort}
	// ADR 0068: the ONE permitted selector delta — a non-empty override replaces
	// ONLY the effort label (provider/model inherit regardless), so a mid-
	// conversation effort switch forks the transcript onto the new tier.
	if effortOverride != "" {
		sel.ReasoningEffort = effortOverride
	}
	profile := profileForSession(src)
	// The fork inherits the SOURCE's owner (ADR 0204 decision 4), NOT the
	// principal of whoever called ForkSession — otherwise fork is an
	// ownership-laundering path. An ownerless source forks ownerless.
	if err := setSessionLabels(forked, sel, profile, src.Owner); err != nil {
		return "", err
	}
	if title != "" {
		if err := forked.RenameTitle(title); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidArgument, err)
		}
	} else {
		forked.Title = src.Title
		forked.TitleProvenance = src.TitleProvenance
	}
	// Save first, then rehydrate: a rehydrate failure (e.g. ErrTooManySessionEngines)
	// leaves a valid persisted session that self-heals at the next StartRunContent
	// (needsRehydration re-runs rehydrateSession). Do NOT Store.Delete on failure —
	// it would race a concurrent rehydrating StartRunContent on the same id.
	if err := s.cfg.Store.Save(ctx, forked); err != nil {
		return "", fmt.Errorf("server: persist forked session: %w", err)
	}
	if s.sessionNeedsPerFactory(sel, nil, profile, forked.Workspace) {
		if _, err := s.rehydrateSession(ctx, forked); err != nil {
			return "", err
		}
	}
	return forked.ID, nil
}

// validateCarryover loads a source session (issue #20: model-switch context
// carryover) and returns a ForkSnapshot of its conversation ready to seed a NEW
// session's history, after enforcing the carryover invariants:
//
//   - The source is loaded through the run-entry funnel (loadAndReopen), which
//     recovers a terminal source (completed/cancelled/failed) to idle so a
//     just-finished session is carryover-eligible. A source still StateRunning
//     or StateAwaiting (mid-run) is rejected with ErrFailedPrecondition — the
//     same turn-boundary rule ForkSession enforces, because a snapshot of an
//     in-flight conversation could carry a dangling tool call.
//
// Carryover is ALWAYS allowed across providers: a model switch must never drop
// the conversation. The source's resolved provider is compared to the new
// session's resolved provider (each canonicalised the way the rest of
// createSession treats it: an empty id means the server default provider,
// Config.DefaultResolvedModel.ProviderID):
//
//   - SAME provider → the snapshot is returned VERBATIM
//     (session.ForkSnapshot(src.Conversation)). The provider-private replay
//     blobs (Message.Reasoning, Message.ProviderPhase, each ToolCall.ItemID)
//     replay intact, so the prompt-cache prefix stays warm.
//   - DIFFERENT provider → the snapshot is STRIPPED to a provider-neutral copy
//     via session.StripProviderState(session.ForkSnapshot(src.Conversation)),
//     clearing Reasoning/ProviderPhase/ItemID while preserving text/roles/
//     tool-call IDs/Args/tool results. Both the OpenAI and Anthropic adapters
//     treat an EMPTY blob as "no blob" and omit it on the wire, so a stripped
//     history replays safely to ANY provider; the new session's
//     thinking/reasoning config is derived from the NEW model, not the history.
//
// The snapshot is taken from the LOADED source, NOT a re-load, so the history
// the new session seeds is exactly the history loadAndReopen recovered.
//
// The new session's resolved provider is the caller's newProviderID (the
// ProviderSelector.ProviderID the create request carried, empty for the server
// default). The model is intentionally NOT compared: a model mismatch within a
// provider is the point of the /models picker switch, so a same-provider
// model change is permitted; a cross-provider model change strips and replays.
func (s *Service) validateCarryover(ctx context.Context, srcID session.SessionID, newProviderID string) ([]session.Message, *session.Principal, error) {
	src, err := s.loadAndReopen(ctx, srcID)
	if err != nil {
		return nil, nil, err
	}
	if src.State == session.StateRunning || src.State == session.StateAwaiting {
		return nil, nil, fmt.Errorf("%w: carryover requires a session at a turn boundary; source %q is %s", ErrFailedPrecondition, srcID, src.State)
	}
	// The SOURCE's owner travels with the carried history (ADR 0204 decision 4):
	// a fork is attributed to whoever owned the session it copied, never to the
	// caller doing the forking — otherwise fork is an ownership-laundering path
	// (copy someone else's session, become its owner). An ownerless source
	// yields an ownerless fork, never a fabricated one.
	srcOwner := src.Owner
	// Canonicalise each side: empty => the server default provider, mirroring how
	// createSession resolves the selector (a zero ProviderSelector rides the
	// shared/default engine). DefaultResolvedModel.ProviderID is the composition-
	// computed canonical default id (reg.Default()).
	defaultProv := s.cfg.DefaultResolvedModel.ProviderID
	srcProv := src.ProviderID
	if srcProv == "" {
		srcProv = defaultProv
	}
	newProv := newProviderID
	if newProv == "" {
		newProv = defaultProv
	}
	snap := session.ForkSnapshot(src.Conversation)
	if srcProv != newProv {
		// Cross-provider: strip the provider-private replay blobs so the history
		// is provider-neutral (session.StripProviderState clears Reasoning/
		// ProviderPhase/ItemID, preserving text/roles/tool-call IDs/Args/results).
		stripped := session.StripProviderState(snap)
		// When the new provider rides the openai adapter, every stripped ToolCall
		// has an empty ItemID. The openai adapter is store:false (full history
		// replay every turn) and uses ItemID (the provider's "id" field, e.g.
		// "fc_1") to de-duplicate replayed function_call items
		// (request.go:353-359). Without stable unique ids the provider
		// auto-assigns sequential fc_N values; on the SECOND post-carryover turn
		// those collide with the current response's items → "Duplicate item
		// found with id fc_N" HTTP 400 (observed on Azure GPT-5.x). Synthesise
		// stable, unique, positional ids with a carryover-namespaced prefix that
		// cannot collide with the provider's fc_ scheme. "openrouter" rides the
		// SAME openai adapter construction (internal/app/registry.go
		// newOpenAICompatEntry), so it needs the same synthesis — this is a
		// provider-id check, not an adapter-type check, because the server
		// layer only has the resolved id, not the adapter.
		if usesResponsesReplayIDs(newProv) {
			return synthesizeOpenAIItemIDs(stripped), srcOwner, nil
		}
		return stripped, srcOwner, nil
	}
	// Same provider: replay the blobs verbatim (warm cache).
	return snap, srcOwner, nil
}

// usesResponsesReplayIDs reports whether a resolved provider replays through
// the stateless OpenAI Responses adapter and therefore requires stable item IDs
// on cross-provider carryover. Keep this classification in one server-layer
// seam: provider IDs are composition-owned strings here, while the server has
// no adapter type to inspect.
func usesResponsesReplayIDs(providerID string) bool {
	switch providerID {
	case "openai", "openrouter", "openai-codex", "toolhive":
		return true
	default:
		return false
	}
}

// synthesizeOpenAIItemIDs synthesises a stable, unique ItemID for every ToolCall
// in the carried history whose ItemID is empty after cross-provider stripping.
// The OpenAI adapter uses ItemID (the provider's "id", e.g. "fc_1") to
// de-duplicate replayed function_call items in store:false stateless replay
// (request.go:353-359). After StripProviderState clears every ItemID, the provider
// auto-assigns sequential fc_N values — which on the second post-carryover turn
// collide with the current response's items → "Duplicate item found with id fc_N"
// HTTP 400. Stable positional ids ("carryover_item_N") keep a retried/re-created
// session deterministic and cannot collide with the provider's fc_ prefix.
//
// A history with no tool calls is returned unchanged. The input slice is
// shallow-copied (the backing array is fresh) so the caller's messages are never
// mutated.
func synthesizeOpenAIItemIDs(messages []session.Message) []session.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]session.Message, len(messages))
	n := 0
	for i, m := range messages {
		if len(m.ToolCalls) > 0 {
			calls := make([]session.ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				c.ItemID = fmt.Sprintf("carryover_item_%d", n)
				n++
				calls[j] = c
			}
			m.ToolCalls = calls
		}
		out[i] = m
	}
	return out
}

// maybeReplayApprovals repopulates the learned-rule store from the durable
// EventLog's allow-always verdicts for a loaded session (cloud-native Phase 3b),
// at most once per id per process. It is a no-op when no replay closure is wired
// (no store / replay disabled) or when this id was already replayed in this
// process lifetime. The composition closure owns the EventLog read + the
// askID→ToolCall correlation + the Policy.Learn re-derivation; the Service only
// gates the once-per-id call and supplies the loaded session (whose Conversation
// the correlation walks). Holding the loaded session — not re-loading — keeps the
// correlation reading the SAME history the run will use.
func (s *Service) maybeReplayApprovals(ctx context.Context, sess *session.Session) {
	if s.cfg.ReplayApprovals == nil {
		return
	}
	s.mu.Lock()
	_, done := s.replayedApprovals[sess.ID]
	if !done {
		s.replayedApprovals[sess.ID] = struct{}{}
	}
	s.mu.Unlock()
	if done {
		return
	}
	s.cfg.ReplayApprovals(ctx, sess)
}

// loadAndReopen is the shared load + reopen-if-completed / interrupt-if-cancelled
// / recover-if-failed body of LoadSession and LoadSessionWithMCP, factored out so
// the two cannot drift: it loads the latest snapshot, and if the session cleanly
// COMPLETED reopens it to idle (Reopen), or if it was CANCELLED (an interrupted
// turn) recovers it via Interrupt, or if it FAILED (a transient provider failure)
// recovers it via Recover (both repair the history), then re-persists. ErrNotFound
// propagates from GetSession.
func (s *Service) loadAndReopen(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.reopenLoadedSession(ctx, sess)
}

// reopenLoadedSession applies the existing terminal-state recovery funnel to an
// already-authorized session. Run entry uses this form so its purpose gate can
// reject a session before recovery mutates or persists it.
func (s *Service) reopenLoadedSession(ctx context.Context, sess *session.Session) (*session.Session, error) {
	id := sess.ID
	// Repopulate the in-memory learned-rule store from the durable EventLog's
	// allow-always verdicts (cloud-native Phase 3b) BEFORE the run starts, so a
	// session that allow-always'd a tool before a restart does not re-ask. Done at
	// most once per id per process (a live session learns as it runs; a re-run only
	// re-derives idempotent rules). It reads the LOADED conversation to correlate the
	// verdicts, so it must run after GetSession and before the engine runs.
	s.maybeReplayApprovals(ctx, sess)
	switch sess.State {
	case session.StateCompleted:
		if rerr := sess.Reopen(); rerr != nil {
			return nil, fmt.Errorf("server: reopen session: %w", rerr)
		}
		if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
			return nil, fmt.Errorf("server: persist reopened session: %w", serr)
		}
	case session.StateCancelled:
		if rerr := sess.Interrupt(); rerr != nil {
			return nil, fmt.Errorf("server: interrupt session: %w", rerr)
		}
		if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
			return nil, fmt.Errorf("server: persist interrupted session: %w", serr)
		}
	case session.StateFailed:
		// Capture permanence BEFORE Recover() clears it (resetToIdle
		// sets permanent=false). Store the pre-flight advisory so the
		// relay can emit an EvRecoverNotice before the next turn burns a
		// provider call on the same unrecoverable error.
		if sess.FailurePermanence() {
			// Use LoadOrStore so two concurrent loads of the same
			// session (under different surface adapters) still emit
			// exactly ONE notice. Keyed by the session id.
			s.recoverNotices.LoadOrStore(id, recoverNoticeText)
		}
		if rerr := sess.Recover(); rerr != nil {
			return nil, fmt.Errorf("server: recover session: %w", rerr)
		}
		if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
			return nil, fmt.Errorf("server: persist recovered session: %w", serr)
		}
		// case session.StateAwaiting: intentionally no-op. Awaiting is the
		// deliberately-preserved Phase 2 cross-process resume point
		// (resumeFromAwaiting) — repairing it here would clear its still-resolvable
		// PendingAsk. It stays terminal-for-loadAndReopen's purposes by falling
		// through this switch untouched.
		// case session.StateIdle: intentionally no-op. Idle is already the target
		// state every other case resets TO — nothing to repair.
		//
		// case session.StateRunning is intentionally NOT handled here (issue #475):
		// a crash-orphaned "running" snapshot is repaired by StartRunContent itself,
		// AFTER it holds the real lease/lock (see the repair beside runEntryMu/
		// acquireLease below) — never inside this pre-lock funnel, where a trial
		// lease could collide with a concurrent caller or a peer's genuine
		// acquireLease. loadAndReopen has no lock/lease of its own to make that
		// repair safe.
	}
	return sess, nil
}

// RecoverNotice returns the pre-flight advisory message for session id when
// the last loadAndReopen recovered a PERMANENTLY-failed session. It returns ""
// when there is no pending notice (the common case: a freshly-created session, a
// transient failure, or a follow-up prompt on the same recovered session). Each
// notice is consumed on the first call — a subsequent call for the same id
// returns "" — so the advisory is emitted ONCE per recovery and never repeats.
//
// It is called by the relay adapters (gRPC Converse, HTTP relayRunSSE) right
// after StartRunContent to inject an EvRecoverNotice synthetic event BEFORE
// the main event loop, so the client sees the warning before the provider call
// burns tokens on the same unrecoverable error.
func (s *Service) RecoverNotice(id session.SessionID) string {
	v, ok := s.recoverNotices.LoadAndDelete(id)
	if !ok {
		return ""
	}
	return v.(string)
}

// LoadSessionWithMCP resumes a previously-persisted session AND re-mounts the
// client-provided streaming-HTTP MCP servers (specs) for the lifetime of that
// session, via a PER-SESSION engine. It is the ACP session/load entry for an editor
// that re-supplies mcpServers on resume — the symmetric sibling of
// CreateSessionWithMCP (session/new).
//
//   - With NO specs it delegates to LoadSession: the resumed session uses the SHARED
//     engine, with zero per-session overhead and no registry entry.
//   - With specs it REQUIRES Config.SessionEngine (else ErrInvalidArgument: "client
//     MCP not supported"); it loads + reopens-if-completed FIRST (so an unknown id
//     fails fast — ErrNotFound — without a wasted MCP connect), then builds the
//     per-session engine via that factory and, on success, registers it under the
//     session id so StartRun routes the session's runs to it. A factory error is
//     returned as-is (the caller maps it).
//
// The per-session engine's MCP manager is torn down by CloseSession (editor
// disconnect) or by the Service's Close.
func (s *Service) LoadSessionWithMCP(ctx context.Context, id session.SessionID, specs []mcp.ServerConfig) (*session.Session, error) {
	if len(specs) == 0 {
		return s.LoadSession(ctx, id)
	}
	if s.cfg.SessionEngine == nil {
		return nil, fmt.Errorf("%w: client MCP not supported (no per-session engine configured)", ErrInvalidArgument)
	}
	// Load + reopen-if-completed BEFORE building the engine, so an unknown id fails
	// fast (ErrNotFound) without a wasted MCP connect. The reopen's Store.Save runs
	// here too.
	sess, err := s.loadAndReopen(ctx, id)
	if err != nil {
		return nil, err
	}
	// Re-mount client MCP on resume, re-deriving the provider+model selector AND the
	// profile from the PERSISTED snapshot labels (cloud-native Phase 1) rather than
	// hardcoding the default provider + inferring the profile from the empty
	// workspace. A selector session keeps its SAME model on resume (the persisted
	// ProviderID/ModelID), not the default-provider floor. The profile derivation
	// keeps the empty-workspace inference as the second defense for a pre-label
	// snapshot.
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	// The profile derivation keeps the empty-workspace inference as the second defense
	// for a pre-label snapshot (profileForSession).
	profile := profileForSession(sess)
	// The persisted workspace is the session's base root: the rebuilt engine's
	// child permission resolver pins to IT (issue #32) — "" for no-fs. The MODE is the
	// loaded session's persisted Mode (ADR 0030 Layer 3), so a session loaded into plan
	// mode mounts the plan model; builtForMode is stamped from the result so a later
	// in-process mode switch on this reloaded session triggers the CASE 1 rebuild.
	res, err := s.cfg.SessionEngine(ctx, sel, specs, profile, sess.Workspace, sess.Mode)
	if err != nil {
		// The session was loaded + (if needed) reopened and re-persisted, but the
		// per-session engine could not be built. We deliberately do NOT roll that
		// back: the session is now just an idle session with no per-session engine —
		// exactly a no-MCP load — which is acceptable, so there is no closeFn cleanup
		// branch here (the asymmetry from CreateSessionWithMCP, where a persist
		// failure tears the freshly-built engine down).
		return nil, err
	}
	s.mu.Lock()
	// Re-load leak guard: if a per-session engine is already registered for this id
	// (e.g. a re-load of the same session on the same connection), close the prior
	// one before replacing it so its MCP manager is not orphaned.
	if prior, ok := s.sessionEngines[id]; ok && prior.close != nil {
		_ = prior.close()
	}
	s.sessionEngines[id] = &sessionEngine{
		engine:          res.Engine,
		caps:            res.Capabilities,
		providerID:      res.ProviderID,
		modelID:         res.ModelID,
		reasoningEffort: res.ReasoningEffort,
		builtForMode:    res.BuiltForMode,
		close:           res.Close,
	}
	if profile == ProfileNoFS {
		// A no-fs session's environment override is re-registered with the engine
		// under the same lock (the create-time discipline), so StartRun never
		// consults the shared factory with the empty root. It is a complete
		// shell-less Environment with an honest nofs ref.
		s.sessionEnvironments[id] = tool.MustEnvironment(defaultEnvironmentRef(sess), nofs.New(), nil)
	}
	s.mu.Unlock()
	return sess, nil
}

// StartRun loads the session, builds its workspace, starts a run on the shared
// engine and registers the *agent.Run so Approve/Cancel can reach it. The
// caller is responsible for draining run.Events() AND, once the channel closes,
// for calling FinishRun(id, run) to remove the run from the registry (each wire
// adapter `defer`s FinishRun after the drain — see grpc.go/http.go/the ACP
// adapter). It returns ErrNotFound if the session does not exist.
func (s *Service) StartRun(ctx context.Context, id session.SessionID, text string) (*agent.Run, error) {
	return s.StartRunContent(ctx, id, text, nil)
}

// StartRunContent is the multimodal sibling of StartRun: it starts a run with a
// prompt carrying flattened text PLUS non-text media parts (image/audio). text
// may be "" when parts carries the content; at least one of text/parts must be
// non-empty (else ErrInvalidArgument). StartRun delegates here with nil parts.
// The media passes through to the engine untouched — command expansion and the
// UserPromptSubmit hook operate on the TEXT only (see Engine.Run). All
// other behaviour (workspace/engine selection, registration, drain contract) is
// identical to StartRun.
//
// It reopens-if-completed (via loadAndReopen) so a follow-up prompt on a session
// that cleanly finished a prior turn continues it — the in-process multi-turn
// counterpart to the cross-process LoadSession resume path.
// StartRunContent is the public chat-purpose multimodal sibling of StartRun.
// Only explicitly-stamped main sessions are admitted; delegation children,
// scheduled sessions, unknown metadata, and every historical child/fire prefix
// fail closed. The trusted scheduler uses StartScheduledRunContent instead.
func (s *Service) StartRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content) (*agent.Run, error) {
	return s.startRunContent(ctx, id, text, parts, runPurposeChat)
}

// StartScheduledRunContent is the trusted scheduler-purpose entry. It admits
// explicitly-stamped scheduled sessions and the historical sched-- fallback for
// legacy unknown snapshots. It is intentionally absent from public transports;
// scheduler composition calls it directly.
func (s *Service) StartScheduledRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content) (*agent.Run, error) {
	return s.startRunContent(ctx, id, text, parts, runPurposeScheduler)
}

type runPurpose uint8

const (
	runPurposeChat runPurpose = iota
	runPurposeScheduler
	scheduleFireSessionPrefix = "sched--"
)

func (s *Service) startRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content, purpose runPurpose) (*agent.Run, error) {
	if text == "" && len(parts) == 0 {
		return nil, fmt.Errorf("%w: prompt text or parts is required", ErrInvalidArgument)
	}
	// Serialize the complete run-entry transaction, including the authoritative
	// load, purpose authorization, and terminal-state recovery. Loading before this
	// lock lets two same-id starts recover the same snapshot independently.
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	// Authorize the exact id before revealing whether its metadata or legacy prefix
	// is runnable. Foreign, ownerless-under-enforcement, pruned, and absent ids all
	// remain the same ErrNotFound class.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := admitRunPurpose(sess, purpose); err != nil {
		return nil, err
	}
	// The run registry is the authoritative same-process single-run gate while the
	// loaded aggregate is non-terminal. A terminal snapshot means the registered
	// run has finished driving but its relay has not called FinishRun yet. Remove
	// that exact run before reopening so the finished relay's later FinishRun
	// cannot deregister the continuation that replaces it.
	if registered, ok := s.LookupRun(id); ok {
		if !sess.State.IsTerminal() {
			return nil, fmt.Errorf("%w: session %q already has an active run", ErrFailedPrecondition, id)
		}
		s.deregister(id, registered)
	}
	// NOTE (ADR 0062): there is NO prompt-channel scan here. The guardrails
	// approve-once flow is OUT-OF-BAND — a PreToolUse guardrail block surfaces to the
	// human as an ordinary permission ask (Allow once / Allow & don't ask / Deny) and
	// is resolved via Approve(askID, verdict), reusing the existing approval machinery.
	// The session "Allow & don't ask again" waiver is armed IN-LOOP from a genuine
	// human AllowAlways verdict, never from a parsed directive in `text`. This replaces
	// the removed ADR-0061 /guardrail-allow prompt directive (no scan, no strip, no
	// near-miss WARN). The `text` param flows straight through.
	// Apply the unchanged reopen/interrupt/recover funnel only after the trusted
	// purpose gate. Rejected kinds are never mutated as a side effect of probing.
	sess, err = s.reopenLoadedSession(ctx, sess)
	if err != nil {
		return nil, err
	}
	// Hold the per-session run-entry lock across engine-resolve (which may REBUILD a
	// per-session engine for a mode→model change, ADR 0030 Layer 3) + run launch +
	// register. The lock was acquired before loading so the entire run-entry
	// transaction observes one authoritative snapshot.
	// Cross-process single-writer gate (cloud-native Phase 4): take the session
	// lease AFTER the in-process runEntryMu so same-process exclusion stays cheap.
	// A competing live owner refuses the run with ErrSessionLeasedElsewhere; nil
	// SessionLease is the byte-identical no-lease default.
	if err := s.acquireLease(ctx, id); err != nil {
		return nil, err
	}
	// Crash-orphan repair (issue #475): loadAndReopen's switch deliberately does
	// NOT handle StateRunning (see its no-op comment) because repairing it there
	// would run before this process holds the real lease/lock, racing a
	// concurrent caller or rejecting a peer's genuine acquireLease with a trial
	// lease. Here, by contrast, runEntryMu.lock(id) + the real acquireLease above
	// have BOTH already succeeded, so this process holds the actual exclusive
	// right to drive the session — no trial lease, no age-horizon oracle needed:
	// the successful acquire IS the proof.
	//
	// One remaining hazard: runEntryMu only serializes the RUN-ENTRY section, not
	// a run's full lifetime (it is unlocked as soon as this function returns,
	// long before the registered run finishes). The authoritative registry/state
	// check above therefore runs while runEntryMu is held and before reopen/save.
	// Reaching this branch proves the running snapshot has no same-process owner
	// and may be repaired.
	if sess.State == session.StateRunning {
		if err := sess.Abandon(); err != nil {
			return nil, fmt.Errorf("server: abandon stale running session: %w", err)
		}
		if err := s.cfg.Store.Save(ctx, sess); err != nil {
			return nil, fmt.Errorf("server: persist abandoned session: %w", err)
		}
	}
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	ctx = memory.WithWorkspace(ctx, sess.Workspace)
	run := engine.Run(ctx, sess, env, agent.RunRequest{Text: text, Parts: parts})
	s.register(id, run, sess)
	return run, nil
}

// isDelegationChildSessionID reports whether id carries one of the delegation
// families' child-session id prefixes: agent.SubagentSessionPrefix,
// agent.ParallelSessionPrefix, agent.TeamSessionPrefix — the engine's exported
// id-minting convention (engine/agent/childregistry.go), the same source
// internal/app/childgc.go's childSessionPrefixes and
// internal/app/scheduler_delivery_run.go's isNonDeliverableOrigin both consume
// (this package cannot import internal/app without a cycle, so the check is
// duplicated against the SAME upstream constants rather than a shared helper).
// It deliberately does NOT match the `sched--` schedule-fire prefix: fire
// sessions ARE legitimately started via StartRunContent (scheduler_fire.go).
func isDelegationChildSessionID(id session.SessionID) bool {
	s := string(id)
	return strings.HasPrefix(s, agent.SubagentSessionPrefix) ||
		strings.HasPrefix(s, agent.ParallelSessionPrefix) ||
		strings.HasPrefix(s, agent.TeamSessionPrefix)
}

func hasLegacyNonChatPrefix(id session.SessionID) bool {
	return isDelegationChildSessionID(id) || strings.HasPrefix(string(id), scheduleFireSessionPrefix)
}

func admitRunPurpose(sess *session.Session, purpose runPurpose) error {
	if sess == nil {
		return fmt.Errorf("%w: session metadata is unavailable", ErrInvalidArgument)
	}
	if err := session.ValidateSessionMetadata(sess.Kind, sess.Relationship); err != nil {
		return fmt.Errorf("%w: invalid session metadata: %v", ErrInvalidArgument, err)
	}
	kind := sess.Kind
	if kind == "" {
		kind = session.SessionKindUnknown
	}
	switch purpose {
	case runPurposeChat:
		if kind == session.SessionKindMain && !hasLegacyNonChatPrefix(sess.ID) {
			return nil
		}
	case runPurposeScheduler:
		if kind == session.SessionKindScheduled ||
			(kind == session.SessionKindUnknown && strings.HasPrefix(string(sess.ID), scheduleFireSessionPrefix)) {
			return nil
		}
	}
	return fmt.Errorf("%w: session %q is not eligible for this run purpose", ErrInvalidArgument, sess.ID)
}

// engineAndEnvironmentFor resolves the engine + environment a loaded session should run
// on, rehydrating a per-session engine when the session needs one but its in-memory
// registration did not survive a restart. It is the SINGLE resolution point shared
// by the prompt run-entry (StartRunContent) and the awaiting-approval re-entry
// (resumeFromAwaiting) so the two paths cannot drift — both rebuild the SAME engine
// for a rehydrated selector/no-fs session, and both fall back to the shared engine
// for a default FS session.
//
// Resolution order (unchanged from the inlined StartRunContent logic): a per-session
// workspace override (e.g. the ACP fs/* buffer, the no-fs override) is preferred,
// else built from the shared factory; a per-session engine (client MCP, selector, or
// no-fs) is preferred, else the shared engine. The empty-workspace inference stays as
// the SECOND defense after the profile/selector trigger.
//
// MODE→MODEL RE-RESOLUTION (ADR 0030 Layer 3) widens the rebuild trigger between
// turns, never mid-stream (this runs at the run-entry funnel, after loadAndReopen
// drove the session idle; SetMode is rejected mid-turn, so the model is fixed per
// turn). Two cases beyond restart-rehydration:
//   - CASE 1 — a per-session engine is registered but its builtForMode no longer
//     matches the session's current Mode (the session switched plan↔execute since the
//     engine was built): REBUILD it through the shared factory path so the new turn
//     runs on the re-resolved model.
//   - CASE 2 — no per-session engine, a DEFAULT-FS session (no restart-rehydration
//     trigger), but ModeNeedsEngine reports this mode resolves a different model (a
//     plan slot is active): PROMOTE the default-FS session to a per-session factory
//     engine. ModeNeedsEngine is nil (no plan slot) ⇒ this never fires and the session
//     keeps the shared engine — BYTE-IDENTICAL to pre-Phase-3.
func (s *Service) engineAndEnvironmentFor(ctx context.Context, sess *session.Session) (*agent.Engine, tool.Environment, error) { //nolint:gocyclo // the per-session engine/environment resolution is inherently branched
	id := sess.ID
	engine := s.cfg.Engine
	s.mu.Lock()
	se, hasEngine := s.sessionEngines[id]
	envOverride, hasEnvOverride := s.sessionEnvironments[id]
	s.mu.Unlock()
	switch {
	case hasEngine && se.builtForMode != "" && se.builtForMode != sess.Mode:
		// CASE 1 (ADR 0030 Layer 3): the registered per-session engine was built for a
		// DIFFERENT mode than the session now holds — a plan↔execute switch re-resolved
		// the model. Rebuild through the shared factory path, REPLACING the prior engine.
		// This runs only between turns (loadAndReopen drove the session idle and SetMode
		// is rejected mid-turn). The whole engineAndEnvironmentFor call is under the caller's
		// per-session runEntryMu, and buildAndRegisterSessionEngine re-checks s.runs[id]
		// UNDER s.mu before closing the displaced engine — that downstream check is the
		// AUTHORITATIVE use-after-close guard. This cheap pre-check is only an early-out so
		// an obviously-live session does not pay a wasted factory build.
		s.mu.Lock()
		_, live := s.runs[id]
		s.mu.Unlock()
		if live {
			return nil, tool.Environment{}, fmt.Errorf("%w: cannot rebuild engine for session %q mid-run (mode change must be deferred to a turn boundary)", ErrInvalidArgument, id)
		}
		sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
		profile := profileForSession(sess)
		rebuilt, err := s.buildAndRegisterSessionEngine(ctx, sess, sel, profile, sess.Mode, true)
		if err != nil {
			return nil, tool.Environment{}, err
		}
		se, hasEngine = rebuilt, true
		// The environment override may have been (re-)registered by the rebuild (no-fs).
		s.mu.Lock()
		envOverride, hasEnvOverride = s.sessionEnvironments[id]
		s.mu.Unlock()
	case !hasEngine && !s.needsRehydration(sess) && s.cfg.ModeNeedsEngine != nil && s.cfg.SessionEngine != nil && s.cfg.ModeNeedsEngine(sess.Mode):
		// CASE 2 (ADR 0030 Layer 3): a DEFAULT-FS session that would otherwise ride the
		// shared engine, but its mode (plan) resolves a DIFFERENT model — promote it to a
		// per-session factory engine. A default-FS session has the empty selector + a real
		// workspace, so no environment override is registered (the run-entry seam builds it
		// from the shared factory below, unchanged).
		sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
		promoted, err := s.buildAndRegisterSessionEngine(ctx, sess, sel, ProfileDefault, sess.Mode, false)
		if err != nil {
			return nil, tool.Environment{}, err
		}
		se, hasEngine = promoted, true
	}
	if !hasEngine && s.needsRehydration(sess) {
		// RESTART REHYDRATION (issue #55, widened in the cloud-native Phase 1): a
		// PERSISTED session that needed a PER-SESSION engine — a non-default
		// provider/model selector, OR the no-fs profile — has its engine + (for no-fs)
		// its environment override living only in process memory; after a restart both
		// are gone. Without rehydration the session would silently DEGRADE onto the
		// shared engine: a no-fs session would ESCALATE onto the full FS tools + Bash
		// over a workspace built from the empty root, and a selector session would run
		// on the WRONG (default-provider) model — wrong enough that its persisted
		// MaxRunTokens budget would be metered through a different model. Rebuild the
		// SAME engine through the factory path create used, reading the PERSISTED
		// selector+profile back off the loaded session. The MaxSessionEngines cap is
		// inherited by the widened trigger (rehydrateSession enforces it). The
		// empty-workspace inference stays as the SECOND defense below.
		var err error
		se, err = s.rehydrateSession(ctx, sess)
		if err != nil {
			return nil, tool.Environment{}, err
		}
		hasEngine = true
		if !isRemoteEnvironmentRef(sess.EnvironmentRef) && (sess.Profile == string(ProfileNoFS) || sess.Workspace == "") {
			// rehydrateSession re-registered the no-fs environment override (same as
			// create); read it back so the resolution below uses the complete override.
			// A REMOTE EnvironmentRef (ADR 0214) is excluded: its Environment is
			// reattached at run entry through the EnvironmentResolver, NOT relabeled
			// no-fs from its empty persisted Workspace (a remote backend's filesystem
			// is not a local root). Reading the no-fs override here would preempt the
			// resolver below (issue #462 phase-3 finding #1).
			s.mu.Lock()
			envOverride, hasEnvOverride = s.sessionEnvironments[id]
			s.mu.Unlock()
			if !hasEnvOverride {
				// Defensive: if the override somehow was not registered, install the
				// honest file-less environment directly (the no-fs chokepoint).
				envOverride = tool.MustEnvironment(defaultEnvironmentRef(sess), nofs.New(), nil)
				hasEnvOverride = true
			}
		}
	}
	if hasEngine {
		engine = se.engine
	}
	if hasEnvOverride {
		// A per-session Environment override (ACP fs/* buffers, no-fs) is COMPLETE:
		// the creator supplied the accurate ref and the correct (possibly nil)
		// CommandRunner. Use it directly — never guess a ref or runner from the
		// override's presence (issue #462 phase-2 finding #2). Stamp the default ref
		// from the live override so a legacy zero-ref session persists it on the next
		// save (ADR 0214, issue #462 phase 3).
		stampDefaultEnvironmentRef(sess)
		return engine, envOverride, nil
	}
	// ENVIRONMENT REATTACHMENT (ADR 0214, issue #462 phase 3): a loaded session
	// with a PERSISTED non-in-tree EnvironmentRef (a remote worker, a container)
	// reattaches a LIVE Environment through the configured resolver. This runs
	// ONLY when no in-process override is registered (a remote session registers
	// none — its Environment is the reattached one, not an ACP/no-fs override).
	// A zero ref (legacy snapshot, or a pre-phase-3 session) and the in-tree
	// Kinds (local/mem/nofs) NEVER reach the resolver: the zero ref falls
	// through to the Workspace-derived default path below (and gets a fresh ref
	// stamped there), and the in-tree Kinds resolve through the existing
	// Workspaces/CommandRunnerFactory path. A non-in-tree Kind with no resolver
	// wired, a ref mismatch, or a nil-Workspace result fails loudly
	// (ErrFailedPrecondition) — never a silent local fallback.
	if isRemoteEnvironmentRef(sess.EnvironmentRef) {
		env, rerr := s.resolveEnvironmentRef(ctx, sess.EnvironmentRef)
		if rerr != nil {
			return nil, tool.Environment{}, rerr
		}
		return engine, env, nil
	}
	// Default path: build the environment from the shared Workspace + runner
	// factories. The workspace requirement is profile-aware: an empty persisted
	// workspace is a no-fs session (the defensive chokepoint — normally
	// unreachable, since create/rehydrate register the override).
	var ws tool.Workspace
	if sess.Workspace == "" {
		// DEFENSIVE CHOKEPOINT (issue #55): never hand an EMPTY root to the
		// shared Workspaces factory — the osfs factory would MkdirAll/OpenRoot
		// the server process's cwd. An empty persisted workspace is by
		// construction a no-fs session, so the honest no-filesystem workspace
		// is the only sound value here (normally unreachable: create and the
		// rehydration above both register the override).
		ws = nofs.New()
	} else {
		ws = s.cfg.Workspaces(sess.Workspace)
	}
	env, err := s.buildSessionEnvironment(sess, ws)
	if err != nil {
		return nil, tool.Environment{}, err
	}
	// Stamp the resolved default ref from the live Environment so a legacy
	// zero-ref session persists it on the next ordinary save (ADR 0214, issue
	// #462 phase 3 — no migration sweep).
	stampDefaultEnvironmentRef(sess)
	return engine, env, nil
}

// isRemoteEnvironmentRef reports whether ref names a non-in-tree backend that
// requires an EnvironmentResolver to reattach (ADR 0214). The zero ref and the
// in-tree Kinds (local/mem/nofs) return false; any other Kind returns true. The
// in-tree set is closed here (the session package owns the constants); a
// future remote transport adds its own Kind label and this predicate returns
// true for it without widening the session package.
func isRemoteEnvironmentRef(ref session.EnvironmentRef) bool {
	if ref == (session.EnvironmentRef{}) {
		return false
	}
	switch ref.Kind {
	case session.EnvKindLocal, session.EnvKindMem, session.EnvKindNoFS:
		return false
	}
	return true
}

// resolveEnvironmentRef reattaches a LIVE tool.Environment for a persisted
// non-in-tree EnvironmentRef via the configured EnvironmentResolver (ADR 0214,
// issue #462 phase 3). It validates the returned Environment's Ref() equals
// the requested ref and carries a non-nil Workspace; a nil resolver, a ref
// mismatch, or a nil-Workspace result fails loudly (ErrFailedPrecondition),
// never a silent local fallback. The resolver is composition-owned — the loop
// and the engine stay storage/transport-agnostic.
func (s *Service) resolveEnvironmentRef(ctx context.Context, ref session.EnvironmentRef) (tool.Environment, error) {
	if s.cfg.EnvironmentResolver == nil {
		return tool.Environment{}, fmt.Errorf("%w: session environment ref %q (kind %q) requires an EnvironmentResolver but none is configured", ErrFailedPrecondition, ref.ID, ref.Kind)
	}
	env, err := s.cfg.EnvironmentResolver(ctx, ref)
	if err != nil {
		return tool.Environment{}, fmt.Errorf("%w: resolve environment %q (kind %q): %v", ErrFailedPrecondition, ref.ID, ref.Kind, err)
	}
	if env.Workspace() == nil {
		return tool.Environment{}, fmt.Errorf("%w: EnvironmentResolver returned an Environment with a nil workspace for ref %q (kind %q)", ErrFailedPrecondition, ref.ID, ref.Kind)
	}
	if got := env.Ref(); got != ref {
		return tool.Environment{}, fmt.Errorf("%w: EnvironmentResolver returned a mismatched ref: got %q (kind %q), want %q (kind %q)", ErrFailedPrecondition, got.ID, got.Kind, ref.ID, ref.Kind)
	}
	return env, nil
}

// buildSessionEnvironment wraps a session's resolved Workspace into a complete
// tool.Environment for the DEFAULT (non-override) path, binding the command
// runner appropriate to the session's namespace (issue #462). It is called ONLY
// when no per-session Environment override is registered: the main session
// (running on the DEFAULT workspace) binds the main CommandRunner; a session on
// a DIFFERENT root (a worktree binding) builds a runner bound to that root via
// CommandRunnerFactory when wired, else is shell-less; an empty persisted
// workspace (a no-fs session that somehow reached the default path — normally
// unreachable, since create/rehydrate register the override) is shell-less with
// a nofs ref. The Environment carries the session's backend ref (local for an
// osfs workspace, nofs for a no-fs profile). Override creators (ACP, no-fs)
// supply their OWN complete Environment with an accurate ref via
// SetSessionEnvironment — this function never guesses a ref or runner for an
// override (issue #462 phase-2 finding #2).
func (s *Service) buildSessionEnvironment(sess *session.Session, ws tool.Workspace) (tool.Environment, error) {
	var runner tool.CommandRunner
	if sess.Workspace != "" {
		if sess.Workspace == s.cfg.DefaultWorkspace {
			runner = s.cfg.CommandRunner
		} else if s.cfg.CommandRunnerFactory != nil {
			// A worktree-bound session (or any root differing from the launch root):
			// build a runner bound to the session root so Bash observes the session
			// namespace, not the launch root.
			runner = s.cfg.CommandRunnerFactory(sess.Workspace)
		}
	}
	// The ref is the SAME default derivation the create-time stamp uses
	// (defaultEnvironmentRef is the single source — issue #462 phase-3 finding
	// #6), so the live Environment's ref and the persisted/stamped ref always
	// agree for the in-tree backends.
	ref := defaultEnvironmentRef(sess)
	return tool.NewEnvironment(ref, ws, runner)
}

// defaultEnvironmentRef computes the resolved default EnvironmentRef for a
// session from its workspace/profile. It is the SINGLE source for the in-tree
// default ref (ADR 0214, issue #462 phase 3 — finding #6 collapsed the
// duplicate derivation): buildSessionEnvironment uses it for the LIVE
// Environment's ref, and stampDefaultEnvironmentRef uses it for the ref STAMPED
// at create time so the next ordinary save persists it. The two therefore
// always agree for the in-tree backends: local (ID = workspace root) for a
// filesystem session, nofs (empty ID) for a no-fs / empty-workspace session. A
// non-zero persisted ref is NOT overwritten — only a zero (unspecified) ref
// gets the default stamped. This is the create-time stamp; reattaching a ref
// for a non-in-tree Kind goes through resolveEnvironmentRef at run entry.
func defaultEnvironmentRef(sess *session.Session) session.EnvironmentRef {
	if sess.Workspace == "" {
		return session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: ""}
	}
	return session.EnvironmentRef{Kind: session.EnvKindLocal, ID: sess.Workspace}
}

// stampDefaultEnvironmentRef stamps the resolved default EnvironmentRef onto a
// freshly-created (or zero-ref) session. It is a no-op when the session already
// carries a non-zero ref (a re-created carryover fork inherits its labels, an
// override creator stamped its own). Called at createSession after setSessionLabels
// so the first Store.Save persists the resolved default, and at run entry when a
// loaded legacy session (zero ref) is first resolved to a live Environment — the
// next ordinary save persists it (no migration sweep).
func stampDefaultEnvironmentRef(sess *session.Session) {
	if sess.EnvironmentRef != (session.EnvironmentRef{}) {
		return
	}
	sess.EnvironmentRef = defaultEnvironmentRef(sess)
}

// sessionNeedsPerFactory reports whether a CreateSession with the given inputs
// must route through the per-session engine factory (rather than the shared
// engine fast path). It is the single expression behind needPerSession in
// createSession, extracted so createSession stays under the cyclomatic cap. The
// worktree arm (issue #102): a session whose workspace DIFFERS from the server's
// launch root routes through the factory so children pin their resolver to the
// session root. When DefaultWorkspace == "" (a child/member/cloud service) the
// arm never fires (a non-empty workspace can't differ from ""). The
// DefaultModelPending arm (issue #262 review finding 1) routes EVERY
// zero-selector session through the factory when the shared engine booted
// with an unresolved intent-driven default model, so the per-session build
// resolves it at session-build time instead of freezing "".
func (s *Service) sessionNeedsPerFactory(sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string) bool {
	return sel != (ProviderSelector{}) || len(specs) > 0 || profile == ProfileNoFS ||
		s.cfg.DefaultModelPending ||
		(workspace != "" && s.cfg.DefaultWorkspace != "" && workspace != s.cfg.DefaultWorkspace)
}

// needsRehydration reports whether a loaded session that has NO live per-session
// engine registered (i.e. its in-memory registrations did not survive a restart)
// must have one rebuilt before it runs. It is the WIDENED Phase 1 trigger: the
// original issue-#55 condition was empty-Workspace (no-fs only); a session also
// needs rehydration when it persisted a non-default provider/model selector
// (`ProviderID`/`ModelID` set) or the no-fs profile, because both require the
// per-session factory engine, not the shared one. A default FS session (empty
// selector, default profile, non-empty workspace) returns false: it keeps riding
// the shared engine with zero rehydration overhead, exactly as before. The
// empty-workspace check stays as the SECOND defense (a no-fs session that
// somehow persisted no profile label still rehydrates) — but it is GUARDED
// against a REMOTE EnvironmentRef (ADR 0214, issue #462 phase 3): a remote
// session carries an empty persisted Workspace (its filesystem lives in the
// remote backend, not on a local root), so the empty-workspace arm must NOT
// fire for it — that would relabel it no-fs (profileForSession → ProfileNoFS),
// register a no-fs environment override, and preempt the EnvironmentResolver at
// run entry. A remote session still rehydrates when it carries a non-default
// provider/model selector (the selector arms fire), but never via the
// empty-workspace inference.
//
// Worktree binding (issue #102, docs/adr/0032): a session whose persisted
// workspace DIFFERS from the server's launch root (DefaultWorkspace) ALSO needs
// rehydration — its per-session engine (which re-pins the CHILD permission
// resolver to the session root) lived only in process memory and is gone after a
// restart. When DefaultWorkspace is empty (a child/member service or a no-root
// cloud deployment) this arm never fires (a non-empty workspace can't differ
// from ""), so the cloud/no-root posture is byte-identical. A default FS session
// (Workspace == DefaultWorkspace) does NOT rehydrate, exactly as before.
//
// The DefaultModelPending arm (issue #262 review finding 1) rehydrates a
// PERSISTED zero-selector session too: setSessionLabels persists the
// SELECTOR (ProviderID/ModelID/ReasoningEffort), which stays empty for a
// zero-selector session, so none of the arms above would otherwise fire for
// it — a session created before a restart into a still-down proxy would
// keep riding whatever engine gets (re)built for it without ever picking up
// a heal that lands after the restart.
func (s *Service) needsRehydration(sess *session.Session) bool {
	return s.cfg.LearnedSkills != nil && sess.Owner != nil && sess.Owner.Issuer != "" && sess.Owner.Subject != "" ||
		sess.Profile == string(ProfileNoFS) ||
		sess.ProviderID != "" || sess.ModelID != "" ||
		sess.ReasoningEffort != "" ||
		(sess.Workspace == "" && !isRemoteEnvironmentRef(sess.EnvironmentRef)) ||
		s.cfg.DefaultModelPending ||
		(sess.Workspace != "" && s.cfg.DefaultWorkspace != "" && sess.Workspace != s.cfg.DefaultWorkspace)
}

// profileForSession reconstructs the SessionProfile from a loaded session's persisted
// inert labels (the SAME mapping rehydrateSession uses): the explicit no-fs label, or
// the second-defense empty-workspace inference. It is the shared profile source for the
// mode→model rebuild (CASE 1) so a no-fs session that switches mode rebuilds the no-FS
// catalog, never silently escalating onto the FS tools. A REMOTE EnvironmentRef
// (ADR 0214, issue #462 phase 3) is excluded from the empty-workspace inference: a
// remote session carries an empty persisted Workspace (its filesystem lives in the
// remote backend), so inferring no-fs from it would relabel the session and register a
// no-fs environment override that preempts the EnvironmentResolver. A remote session
// with an explicit no-fs profile label (a hybrid that opted into no-FS tools) still
// honors the explicit label.
func profileForSession(sess *session.Session) SessionProfile {
	switch {
	case sess.Profile == string(ProfileNoFS):
		return ProfileNoFS
	case sess.Profile == "" && sess.Workspace == "" && !isRemoteEnvironmentRef(sess.EnvironmentRef):
		return ProfileNoFS
	default:
		return ProfileDefault
	}
}

// rehydrateSession rebuilds and registers the per-session engine for a persisted
// session whose in-memory registrations did not survive a process restart. It is
// called from the run-entry seam (StartRunContent) when needsRehydration is true
// and no per-session engine is registered.
//
// The rehydrated engine is built through the SAME SessionEngineFactory create
// used, reading the PERSISTED selector+profile+MODE back off the loaded session —
// so a selector session rebuilds on the SAME provider/model (not the default
// floor), a no-fs session rebuilds the no-FS catalog, and a session persisted with
// Mode=plan rebuilds on the PLAN model (ADR 0030 Layer 3 — restart-into-plan picks
// the plan slot, not the default). No client MCP is re-mounted (the client
// re-mounts via LoadSessionWithMCP; the server never stored the specs). It
// delegates the cap/lock/factory/register discipline to buildAndRegisterSessionEngine
// in FIRST-REGISTRATION-WINS mode (a concurrent rehydration losing the race keeps
// the winner's engine and tears its own down).
func (s *Service) rehydrateSession(ctx context.Context, sess *session.Session) (*sessionEngine, error) {
	if s.cfg.SessionEngine == nil {
		// NEVER fall back to the shared engine: that is exactly the degradation
		// (no-fs escalation / wrong-model) this seam exists to prevent. A session
		// needing a per-session engine could only have been created with a factory
		// configured, so this is a deployment mis-wire.
		return nil, fmt.Errorf("%w: persisted session %q cannot be rehydrated (no session-engine factory configured)", ErrInvalidArgument, sess.ID)
	}
	// Reconstruct the selector + profile from the persisted inert labels. The
	// ProviderSelector type stays server-adapter-owned; the aggregate only carried
	// the two opaque strings.
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	// Second-defense inference inside profileForSession: an empty persisted workspace
	// can only be a no-fs session (every FS path requires a non-empty workspace), so a
	// snapshot that predates the profile label still rehydrates as no-fs.
	profile := profileForSession(sess)
	// Rehydration reads the PERSISTED mode (sess.Mode) so a session that switched to
	// plan before the restart rebuilds on the plan model — surface-agnostic, the same
	// path a mid-session mode change uses.
	return s.buildAndRegisterSessionEngine(ctx, sess, sel, profile, sess.Mode, false)
}

// buildAndRegisterSessionEngine is the ONE shared build+cap-check+register+teardown
// path for a per-session engine — used by BOTH rehydrateSession (restart, issue #55 /
// cloud-native Phase 1) and the mode→model rebuild (ADR 0030 Layer 3). It calls the
// SessionEngineFactory with the resolved selector/profile/MODE, stamps the result's
// BuiltForMode, and registers it under the same cap/lock discipline as createSession
// (cheap pre-check, build outside the lock, authoritative re-check + register under
// the lock). It never resolves a model itself — the factory owns that (composition).
//
// replace selects the registration semantics:
//   - replace=false (rehydration): FIRST-REGISTRATION-WINS. If a prior engine is
//     already registered for id (a concurrent rehydration won the race), keep it and
//     tear the freshly-built loser down so no MCP manager leaks.
//   - replace=true (mode-rebuild): REPLACE the prior engine. The authoritative
//     no-live-run check (CWE-362/416 use-after-close guard) is taken UNDER s.mu in the
//     SAME critical section as the swap: if a run became live for id between the cheap
//     pre-check and here (a concurrent StartRunContent racing the rebuild), the swap is
//     ABORTED and the freshly-built engine torn down — the in-use prior engine is NEVER
//     closed. Only on a clean swap (no live run) is the displaced prior engine's Close
//     invoked, and only AFTER it is unregistered, so no other goroutine can still read it.
//
// On ProfileNoFS it (re-)registers the no-fs workspace override under the SAME lock as
// the engine, the create-time discipline.
func (s *Service) buildAndRegisterSessionEngine(ctx context.Context, sess *session.Session, sel ProviderSelector, profile SessionProfile, mode session.PermissionMode, replace bool) (*sessionEngine, error) {
	id := sess.ID
	// On replace we are swapping an existing registration, so the cap is not exceeded
	// (the slot is already counted); on a first build the pre-check rejects when full.
	if !replace {
		s.mu.Lock()
		full := len(s.sessionEngines) >= s.cfg.MaxSessionEngines
		s.mu.Unlock()
		if full {
			return nil, fmt.Errorf("%w: %d", ErrTooManySessionEngines, s.cfg.MaxSessionEngines)
		}
	}
	res, err := s.cfg.SessionEngine(ctx, sel, nil, profile, sess.Workspace, mode)
	if err != nil {
		return nil, fmt.Errorf("server: build session engine %q: %w", id, err)
	}
	se := &sessionEngine{
		engine:          res.Engine,
		caps:            res.Capabilities,
		providerID:      res.ProviderID,
		modelID:         res.ModelID,
		reasoningEffort: res.ReasoningEffort,
		builtForMode:    res.BuiltForMode,
		close:           res.Close,
	}
	s.mu.Lock()
	prior, hadPrior := s.sessionEngines[id]
	if hadPrior && !replace {
		// FIRST-REGISTRATION-WINS: a concurrent rehydration (or load) won the race;
		// keep its engine, tear ours down so the loser leaks no MCP manager.
		s.mu.Unlock()
		if se.close != nil {
			_ = se.close()
		}
		return prior, nil
	}
	if !replace && len(s.sessionEngines) >= s.cfg.MaxSessionEngines {
		s.mu.Unlock()
		if se.close != nil {
			_ = se.close()
		}
		return nil, fmt.Errorf("%w: %d", ErrTooManySessionEngines, s.cfg.MaxSessionEngines)
	}
	if replace {
		// AUTHORITATIVE no-live-run check (use-after-close guard): a run that became
		// live for id since the cheap pre-check would still be reading the prior engine
		// (and its client MCP transport). Closing it now would tear that transport out
		// from under the in-flight run. Re-check UNDER the lock that owns both s.runs and
		// the swap, and ABORT if a run is live — never close an in-use engine. The
		// freshly-built engine is discarded (its MCP torn down) so the abort leaks nothing.
		if _, live := s.runs[id]; live {
			s.mu.Unlock()
			if se.close != nil {
				_ = se.close()
			}
			return nil, fmt.Errorf("%w: cannot rebuild engine for session %q mid-run (mode change must be deferred to a turn boundary)", ErrInvalidArgument, id)
		}
	}
	s.sessionEngines[id] = se
	if profile == ProfileNoFS {
		// Re-register the no-fs environment override under the SAME lock as the engine
		// (the create-time discipline), so the run below — and every later run —
		// resolves its environment here and never consults the shared factory with the
		// empty root. It is a complete shell-less Environment with an honest nofs ref.
		// A selector session with a real workspace needs no override: the run-entry
		// seam builds its environment from the shared factory as usual.
		s.sessionEnvironments[id] = tool.MustEnvironment(defaultEnvironmentRef(sess), nofs.New(), nil)
	}
	s.mu.Unlock()
	// On a clean replace, free the displaced prior engine's MCP manager OUTSIDE the lock
	// (no I/O under the mutex). The under-lock check above proved no run was live AND the
	// new engine is now registered, so no goroutine can still read prior after this point.
	if replace && hadPrior && prior.close != nil {
		_ = prior.close()
	}
	return se, nil
}

// ProviderCapabilities reports the DEFAULT provider+model's multimodal input
// support, so a surface adapter can advertise it (e.g. ACP promptCapabilities) and
// loud-reject unsupported prompt content. It returns the composition-computed
// DefaultCapabilities — the catalog ∩ adapter INTERSECTION for the default
// provider+cfg.Model — NOT the bare engine.Capabilities() (adapter-only, which
// would over-advertise a model the adapter can transmit to but the catalog says
// cannot take image). This is the SAME value the CreateSessionResponse echoes for a
// default-engine session, so the ACP gate and the wire echo cannot disagree.
//
// ACP carries NO per-session provider/model selector in P0 (session/new passes only
// mcpServers, never a selector), so every ACP session rides the DEFAULT engine and
// the Agent's capture-once a.caps = svc.ProviderCapabilities() is correct for every
// ACP session. A per-session ACP capability gate lands only when an ACP selector
// lands (P1+) — see docs/adr/0016-multi-provider.md.
func (s *Service) ProviderCapabilities() port.ProviderCapabilities {
	return s.cfg.DefaultCapabilities
}

// SessionCapabilities reports the resolved input capability (catalog ∩ adapter)
// for the session under id: the per-session engine's precomputed neutral caps when
// a per-session engine is registered (a non-default provider/model selector or
// client MCP), else the composition-computed DefaultCapabilities (the shared-engine
// path). The value was computed ONCE in composition (modelCapability) and stored;
// SessionCapabilities never recomputes it, so the wire echo cannot drift from the
// ListModels view. It backs the CreateSessionResponse.session_capabilities echo.
func (s *Service) SessionCapabilities(id session.SessionID) port.ProviderCapabilities {
	s.mu.Lock()
	se, ok := s.sessionEngines[id]
	s.mu.Unlock()
	if ok {
		return se.caps
	}
	return s.cfg.DefaultCapabilities
}

// ResolvedModel reports the EFFECTIVE provider+model for the session under id:
// the per-session engine's precomputed resolved model when a per-session engine is
// registered (a non-default provider/model selector or client MCP), else the
// composition-computed DefaultResolvedModel (the shared/default-engine path). It
// backs the CreateSessionResponse.resolved_model echo. Mirrors SessionCapabilities
// verbatim.
//
// MODE→MODEL RE-EMIT (ADR 0030 Layer 3): the identity is read from the REGISTERED
// per-session engine (se.providerID/se.modelID). After a plan↔execute switch
// triggers the run-entry rebuild (engineAndEnvironmentFor swaps the registered engine
// on the next StartRun), ResolvedModel AUTOMATICALLY returns the re-resolved model —
// no change here. The ORDERING is deliberate: a SetMode response echoes the
// still-CURRENT (pre-rebuild) model (the model is fixed per turn; the rebuild happens
// at the NEXT run-entry, not on the SetMode call itself), so a client reads the new
// model from GetSession or the next CreateSession-style echo AFTER the mode-changed
// turn begins — never mid-turn.
//
// The provider/model IDENTITY never recomputes within a single engine generation (the
// per-session-engine value was computed ONCE in composition; the default-path ids are
// the baked DefaultResolvedModel ids), so the wire echo cannot drift from the engine
// the session actually runs on. The ContextWindow SCALAR is resolved LIVE-FIRST at call
// time for BOTH branches via the injected Config.ResolveContextWindow (so a
// GetSession after the live model-catalog swap reflects the live window, not the
// curated-catalog floor a live-only model lacks) — mirroring the live-first modality
// input SessionCapabilities already consumes. The injected resolver is the ECHO
// resolver (echoWindowResolver), which differs from the engine's resolve-at-use
// Deps.ContextWindow in ONE deliberate way: while the one-shot live refresh is still
// in flight it returns a PROVISIONAL 0 for a live-only model not yet in the catalog
// (the client treats 0 as "refetch on turn-end" — the issue #66 footer-heal gate),
// whereas the engine always floors to 128k (it can never run on a 0 window). This
// provisional 0 is DISTINCT from "resolver not wired (nil)": nil ⇒ no window scalar at
// all (the identity-only ResolvedModel); a wired resolver returning 0 is the honest
// "live answer not in yet" signal. Post-completion the resolver floors an uncatalogued
// model to 128k, so the echo settles and the heal gate closes (no-network boundedness).
func (s *Service) ResolvedModel(id session.SessionID) ResolvedModel {
	s.mu.Lock()
	se, ok := s.sessionEngines[id]
	s.mu.Unlock()
	// Identity from the per-session engine when registered (a selector/MCP/rehydrated
	// session), else the baked default identity. The WINDOW is resolved live-first
	// below for either branch — the frozen per-session scalar is gone.
	rm := s.cfg.DefaultResolvedModel
	if ok {
		rm = ResolvedModel{ProviderID: se.providerID, ModelID: se.modelID, ReasoningEffort: se.reasoningEffort}
	}
	if s.cfg.ResolveContextWindow != nil {
		// Overlay the live-first window; provider/model identity stays verbatim. The
		// ECHO resolver honours the operator override and any catalogued/live window,
		// and returns a deliberate PROVISIONAL 0 for a live-only model while the live
		// refresh is in flight (the client treats 0 as "refetch on turn-end"). A 0
		// therefore leaves rm.ContextWindow at whatever it already holds — the baked
		// seed on the default branch (itself a provisional 0 for a live-only default),
		// or 0 on the per-session branch — so the client's footer-heal gate fires and
		// self-corrects once the refresh settles. Post-completion the resolver floors
		// to 128k, so the echo never stays 0.
		if w := s.cfg.ResolveContextWindow(rm.ProviderID, rm.ModelID); w > 0 {
			rm.ContextWindow = w
		}
	}
	return rm
}

// LookupRun returns the in-flight run for a session and true, or false if no
// run is currently registered for it.
func (s *Service) LookupRun(id session.SessionID) (*agent.Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.runs[id]
	if !ok {
		return nil, false
	}
	return st.run, true
}

// IsLive reports whether a run is currently in flight for the session id — a
// pure read over the same in-flight registry LookupRun consults. It is the
// liveness predicate the composition layer's child-session GC injects so a
// sweep never deletes the snapshot of a session that is mid-run in THIS
// process.
//
// HONESTY: this knows TOP-LEVEL run ids only. Children spawned BY a live run
// (subagent-*/parallel-*/team-* ids) are driven inside their parent's run and
// never registered here, so IsLive answers false for them even mid-run
// (pinned by TestServiceIsLiveDoesNotKnowEngineChildren). Engine children are
// protected from the sweep by age horizon + snapshot freshness instead: they
// persist at their terminal AND a resumed child re-persists at resume start,
// so an in-flight child's snapshot is always fresh (see the invariant note in
// internal/app/childgc.go). A client-driven id carrying a delegation-child
// prefix (subagent-*/parallel-*/team-*) can no longer register here at all —
// StartRunContent's isDelegationChildSessionID guard rejects it with
// ErrInvalidArgument before it ever reaches this registry.
func (s *Service) IsLive(id session.SessionID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[id]
	return ok
}

// Approve resolves the paused permission ask on the session's in-flight run with
// the client's three-way verdict (deny / allow-once / allow-always).
//
// SAME-PROCESS path FIRST and unchanged: a live registered run resolves the ask
// over its in-memory channel exactly as before. On a LookupRun MISS — typically the
// process that parked the ask died and a different process now serves the Approve —
// it falls to resumeFromAwaiting (cloud-native Phase 2): if the persisted session is
// in StateAwaiting it loads the snapshot, rebuilds the engine, re-enters the loop AT
// the ask, applies the verdict, and drives to completion; the caller relays the
// returned run's events (the resumed run is registered like any other). A
// non-awaiting (idle/completed/cancelled/failed) session stays terminal and yields
// ErrNoActiveRun; an unknown session yields ErrNotFound. The returned run, when
// non-nil, is the resumed run the wire adapter must drain + FinishRun.
func (s *Service) Approve(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict) error {
	_, err := s.ApproveRun(ctx, id, askID, verdict)
	return err
}

// ApproveRun is Approve plus the resumed *agent.Run handle (cloud-native Phase 2).
// On the SAME-PROCESS path (a live registered run) it resolves the ask over the
// channel and returns (nil, nil): there is no new run, the existing relay delivers
// the verdict's effects. On the rehydrate path (no live run, the session is
// awaiting) it returns the freshly-registered resumed run so the caller can relay
// its events and FinishRun it after the drain. A nil run with a nil error means "the
// same-process channel handled it; keep relaying the existing stream".
//
// CONCURRENCY: two Approves for the SAME awaiting session that both MISS the live-run
// fast path must NOT both spawn a resumed run (Engine.ResumeApproval spawns the
// driving goroutine immediately, so the pending tool would execute twice). The resume
// decision is therefore serialized per session via s.resumeMu: under the per-session
// lock the loser re-checks LookupRun, sees the winner's now-registered run, and
// routes its verdict to that run's channel (the same-process path) — the pending tool
// runs EXACTLY ONCE. The common live-run case takes a lock-free fast path first.
//
// WIRE EXPOSURE: the rehydrate-resume path (no live run → resumeFromAwaiting) is
// reachable only through the HTTP POST /v1/sessions/{id}/approve endpoint, which
// relays the resumed run as an SSE body (see the HTTP approve handler). The gRPC
// Converse stream has NO rehydrate path: its ResumeApproval control frame resolves the
// ask against the stream's OWN live in-process run only (grpc.go readControl), so a
// gRPC client whose session was evicted has no resume path over Converse and a verdict
// frame for a dead run is silently dropped. The gRPC rehydrate path is a tracked
// follow-up (additive, out of the Phase 2 gate) — see docs/adr/0027-cloud-native.md
// Phase 2.
func (s *Service) ApproveRun(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict) (*agent.Run, error) {
	// Authorize before reading the in-memory registry: a mismatch must be
	// indistinguishable from a missing handle and cannot signal a live run.
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	// Fast path (lock-free): a live registered run resolves the ask over its channel.
	if run, ok := s.LookupRun(id); ok {
		run.Approve(askID, verdict)
		return nil, nil
	}
	return s.resumeFromAwaiting(ctx, id, askID, verdict)
}

// resumeFromAwaiting is the service half of the fourth (awaiting-only) run-entry
// seam: it handles an Approve/Deny against a session whose process died while parked
// awaiting approval. It loads the persisted session and:
//
//   - if a run got registered after the fast-path miss (a concurrent resume won) →
//     route the verdict to that run's channel and return (nil, nil) (same-process);
//   - if the session is unknown → ErrNotFound (via the store load);
//   - if the session is NOT in StateAwaiting → ErrNoActiveRun (awaiting is the ONLY
//     state that stops being terminal under Phase 2; idle/completed/cancelled/failed
//     stay terminal, so a stale Approve cannot resurrect them);
//   - if awaiting → rebuild the engine + workspace (the SAME engineAndEnvironmentFor
//     the prompt path uses, so the two cannot drift), call Engine.ResumeApproval to
//     re-enter the loop AT the ask, register the resumed run, and return it.
//
// The whole sequence runs under the per-session resume lock (s.resumeMu) so the
// LookupRun-recheck → ResumeApproval → register decision is ATOMIC per session: a
// concurrent caller blocks on the lock and, on acquiring it, takes the same-process
// branch above instead of spawning a second run (spawn-then-cancel would be unsafe —
// the loser goroutine could execute the tool before a Cancel landed). Registration
// mirrors rehydrateSession's loser-teardown/MaxSessionEngines guard via
// engineAndEnvironmentFor; the resumed run is registered into s.runs like any other so
// a concurrent Cancel/Approve reaches it and FinishRun cleans it up.
func (s *Service) resumeFromAwaiting(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict) (*agent.Run, error) {
	unlock := s.resumeMu.lock(id)
	defer unlock()

	// Re-check under the lock: a concurrent resume that won the race has registered a
	// live run. Route this verdict to its channel (same-process) instead of spawning a
	// second run — the exactly-once guarantee for the pending tool.
	if run, ok := s.LookupRun(id); ok {
		run.Approve(askID, verdict)
		return nil, nil
	}

	// GetSession (read-only snapshot load): ErrNotFound for an unknown session. We do
	// NOT use loadAndReopen here — its job is to drive completed/cancelled/failed back
	// to idle for a NEW prompt, exactly the terminal states this seam must REJECT.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.State != session.StateAwaiting {
		// Awaiting is the only state Phase 2 makes non-terminal. Everything else stays
		// stranded-for-Approve as before (last-write-wins / nothing to resume).
		return nil, ErrNoActiveRun
	}
	// ADR 0030 Layer 3 note: engineAndEnvironmentFor's mode→model rebuild (CASE 1) is a
	// NO-OP here. SetMode is rejected from StateAwaiting by the aggregate, so a parked
	// session's Mode cannot have changed since its engine was built — se.builtForMode ==
	// sess.Mode always holds, and the stale-mode branch never fires. (A restart-parked
	// awaiting session is rehydrated on its persisted Mode first, so the rebuilt engine's
	// builtForMode matches too.) The model is fixed for the resumed turn. We still take
	// runEntryMu (nested inside resumeMu, the resumeMu→runEntryMu order) around the
	// engine-resolve+register so the run-entry critical section is uniform with
	// StartRunContent — the rebuild's under-lock liveness check stays authoritative even
	// though it cannot fire on this path.
	entryUnlock := s.runEntryMu.lock(id)
	defer entryUnlock()
	// Cross-process single-writer gate (cloud-native Phase 4): the resumed run is a
	// run-entry like any other, so it acquires the session lease too — a competing
	// process that took over this evicted session must refuse the resume.
	if err := s.acquireLease(ctx, id); err != nil {
		return nil, err
	}
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	ctx = memory.WithWorkspace(ctx, sess.Workspace)
	run := engine.ResumeApproval(ctx, sess, env, askID, verdict)
	s.register(id, run, sess)
	return run, nil
}

// ApprovePlan is the atomic plan-approval RPC (issue #206, Wave 4). It resolves
// a parked PLAN-ORIGINATED permission ask (a PresentPlan call surfaced in plan
// mode) and — on an ALLOW verdict — starts a FRESH continuation run carrying the
// harness proceed message, streaming BOTH runs' events on the one returned
// channel. It composes EXISTING seams and adds NO new engine machinery:
//
//  1. A live run for the session is rejected (ErrNotAwaitingPlan → 409): an
//     approve mid-run must use the Converse ResumeApproval frame, not this RPC.
//  2. The session is loaded and must be StateAwaiting on a PLAN-ORIGINATED ask
//     (sess.PendingAsk().Origin() == AskOriginPlan); anything else is
//     ErrNotAwaitingPlan. An unknown session is ErrNotFound (via GetSession).
//  3. targetMode → verdict: ModeDefault → VerdictAllowOnce (flip to default),
//     ModeAccept → VerdictAllowAlways (flip to accept-edits), ModePlan/zero →
//     VerdictDeny (iterate, no flip, no continuation run).
//  4. resumeFromAwaiting re-enters the loop AT the ask: Engine.ResumeApproval
//     applies the verdict, the allow paths set r.planApprovedTarget, the run
//     terminates StopPlanApproved, and terminateComplete flips the session mode
//     at the terminal boundary. (The deny path synthesises a deny result and the
//     loop CONTINUES in plan mode — but since this is a fresh resumed run with no
//     further model turns scripted, it ends at StopPlanApproved-less terminal;
//     the session stays in plan mode for the next prompt.)
//  5. ATOMIC CONTINUATION (allow paths only): after the resumed run drains, a
//     FRESH run is started via the SAME StartRunContent path (loadAndReopen →
//     engineAndEnvironmentFor CASE 1 rebuild picks up the FLIPPED mode → execute
//     model) carrying the proceed message agent.PlanApprovedProceedText + an
//     optional operator note. Both runs' events are relayed on the returned
//     channel. On deny, NO continuation run starts (the session stays in plan
//     mode; the model re-plans on the next prompt).
//
// The returned channel carries the MERGED event stream of the resumed run and
// (on allow) the continuation run, closing once both have ended. The caller
// (gRPC/HTTP relay) owns the wire discipline: appendEvent per event, skip the
// three log-only kinds on the client wire, Persist on EvPermissionAsk, and —
// critically — CANCEL ctx on a send error / client disconnect so this method's
// internal ctx-watcher cancels the LIVE run (the run is registered in s.runs
// like any other; this method deregisters it after drain). The caller MUST
// cancel the passed ctx once it stops draining, or the run can wedge behind a
// dead relay (mirrors the run.Cancel() the live relays call on disconnect).
func (s *Service) ApprovePlan(ctx context.Context, id session.SessionID, targetMode session.PermissionMode, note string) (<-chan session.Event, error) {
	// Authorize before reading the in-memory registry. A foreign caller must not
	// learn that a run exists or trigger any live-run side effect.
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	// (1) A live run means an approve-mid-run: reject. The operator must use the
	// Converse ResumeApproval frame for a live run, not this atomic RPC.
	if _, ok := s.LookupRun(id); ok {
		return nil, fmt.Errorf("%w: session %q has a live run (use the Converse resume_approval frame for an in-flight run)", ErrNotAwaitingPlan, id)
	}
	// (2) Load the session to validate the plan-originated precondition and read
	// the askID. This is a read-only GetSession (NOT loadAndReopen — the session
	// is awaiting, not completed/cancelled/failed, so there is nothing to drive
	// idle). ErrNotFound propagates for an unknown session.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.State != session.StateAwaiting {
		return nil, fmt.Errorf("%w: session %q is in state %q, not awaiting", ErrNotAwaitingPlan, id, sess.State)
	}
	ask, ok := sess.PendingAsk()
	if !ok || ask.Origin() != session.AskOriginPlan {
		return nil, fmt.Errorf("%w: session %q is not awaiting a plan-approval ask", ErrNotAwaitingPlan, id)
	}
	// (3) targetMode → verdict.
	verdict, allowContinuation := planVerdictForMode(targetMode)

	// The merged event channel. Buffered to match a Run's own buffer (64) so a
	// momentarily-slow relay does not block the producer; the relay drains it.
	out := make(chan session.Event, 64)

	// (4) Resume the awaiting run (registered in s.runs by resumeFromAwaiting).
	// The askID is read off the snapshot above; resumeFromAwaiting re-validates
	// state under its resumeMu lock, so a concurrent resume that won the race
	// returns (nil, nil) — handled below (no run to drain).
	resumed, rerr := s.resumeFromAwaiting(ctx, id, ask.AskID, verdict)
	if rerr != nil {
		return nil, rerr
	}

	go func() {
		defer close(out)
		// (4a) Drain the resumed run. On the same-process channel path
		// (resumeFromAwaiting returned nil — a concurrent Approve routed the
		// verdict to an existing live run), there is nothing to relay here.
		var resumedStop session.StopReason
		if resumed != nil {
			resumedStop = s.forwardRunEvents(ctx, resumed, out)
			s.deregister(id, resumed)
		}
		// (5) Atomic continuation (allow paths only). A deny leaves the session
		// in plan mode with no continuation run — the model re-plans on the next
		// prompt. Only continue when the resumed run ended at StopPlanApproved
		// (the plan-approval clean terminal that flips the mode); a non-allow
		// terminal (cancel/error) is surfaced honestly and no continuation runs.
		if !allowContinuation || resumedStop != session.StopPlanApproved {
			return
		}
		proceed := agent.PlanApprovedProceedText
		if note != "" {
			proceed = proceed + "\n\nOperator note: " + note
		}
		// (5a) Start the continuation run via the SAME path StartRunContent uses
		// (loadAndReopen → engineAndEnvironmentFor CASE 1 rebuild on the flipped
		// mode → execute model). The StopPlanApproved-completed session is
		// reopened to idle by loadAndReopen.
		cont, cerr := s.StartRunContent(ctx, id, proceed, nil)
		if cerr != nil {
			// Surface the continuation-launch failure honestly on the stream as a
			// synthetic terminal result so the relay's client sees a terminal
			// (never a silent close). This mirrors how the engine surfaces a run-
			// entry failure: an EvResult with StopError.
			// Permanent is left false (zero value) — cerr is a service-layer error
			// (session load, engine build, etc.), not a provider rejection, so it
			// cannot implement port.PermanentError. The loop's EvResult is the
			// authoritative carrier of the permanent bit.
			res := &session.ResultPayload{
				Stop:  session.StopError,
				Error: "continuation run failed to start: " + cerr.Error(),
			}
			out <- session.Event{Type: session.EvResult, Result: res}
			return
		}
		s.forwardRunEvents(ctx, cont, out)
		s.deregister(id, cont)
	}()

	return out, nil
}

// forwardRunEvents drains run's Events() into out until the channel closes,
// returning the terminal StopReason (empty if none). It watches ctx: when ctx
// is cancelled (the relay stopped draining — client disconnect or a send-error
// cancel) it cancels the LIVE run so the producer unwedges and the run's
// terminal EvResult is emitted before close. It does NOT appendEvent / Persist
// / log-only-filter — those are the RELAY's discipline (the caller of
// ApprovePlan owns the wire), this only forwards the raw events. It registers
// nothing (the run is already registered by its producer).
func (*Service) forwardRunEvents(ctx context.Context, run *agent.Run, out chan<- session.Event) session.StopReason {
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			run.Cancel()
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)
	var stop session.StopReason
	for ev := range run.Events() {
		// Non-blocking forward with ctx-awareness: if the relay has stopped
		// draining (out would block), drop the event rather than wedging the
		// producer — the run is already being cancelled by the ctx-watcher above,
		// which will end it and close Events(). This mirrors the live relays'
		// drain-to-discard after a dead client.
		select {
		case out <- ev:
		case <-ctx.Done():
			// Keep draining Events() to completion without forwarding so the run
			// goroutine can exit; the ctx-watcher already cancelled it.
			for range run.Events() {
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	return stop
}

// planVerdictForMode maps an ApprovePlan target_mode to the session verdict and
// whether an atomic continuation run should follow. ModeDefault → allow-once
// (flip to default); ModeAccept → allow-always (flip to accept-edits);
// ModePlan/zero → deny (iterate, no flip, no continuation). This mirrors the
// engine's surfacePlanAsk verdict tail (engine/agent/dispatch.go) so the
// target_mode the operator picks drives the EXACT mode the session lands in.
func planVerdictForMode(m session.PermissionMode) (verdict session.ApprovalVerdict, allowContinuation bool) {
	switch m {
	case session.ModeAccept:
		return session.VerdictAllowAlways, true
	case session.ModeDefault:
		return session.VerdictAllowOnce, true
	default:
		// ModePlan / zero-value: deny — the operator asked to iterate; no flip,
		// no continuation run.
		return session.VerdictDeny, false
	}
}

// Cancel cancels the session's in-flight run. The store-fallback semantics match
// Approve: ErrNotFound when the session is unknown, ErrNoActiveRun when it
// exists only in the store with no live run.
func (s *Service) Cancel(ctx context.Context, id session.SessionID) error {
	// Authorize before reading the in-memory registry: cancellation is a live
	// signal and a foreign request must be absence-equivalent.
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	run, ok := s.LookupRun(id)
	if ok {
		run.Cancel()
		return nil
	}
	return s.noActiveRun(ctx, id)
}

// CancelChild cancels ONE child (a subagent) of the session's in-flight run,
// addressed by its child session id (the `agentId:` trailer / subagent.start
// child_id). It mirrors Approve exactly: LookupRun, then the store fallback —
// ErrNotFound for an unknown session, ErrNoActiveRun for a known-but-runless
// one. A live run that does not hold the child (unknown id, or the child
// already finished) yields ErrChildNotFound (HTTP 404; the stream-frame path
// ignores that race by design instead).
func (s *Service) CancelChild(ctx context.Context, id session.SessionID, childID string) error {
	// The parent session owns the live child registry; reject a foreign caller
	// before probing it or sending a child cancellation.
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	run, ok := s.LookupRun(id)
	if ok {
		if !run.CancelChild(childID) {
			return ErrChildNotFound
		}
		return nil
	}
	return s.noActiveRun(ctx, id)
}

// noActiveRun distinguishes "unknown session" (ErrNotFound) from "known session,
// no in-flight run" (ErrNoActiveRun) by loading from the store.
func (s *Service) noActiveRun(ctx context.Context, id session.SessionID) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	return ErrNoActiveRun
}

// Persist saves the current state of the session backing id, if a run is
// registered for it. It is the seam the adapters call when a run enters the
// awaiting state (so a persisted awaiting session is loadable for re-attach
// after a restart) and at run end (so the terminal state is durable). The engine
// mutates the session in place, so this captures whatever state it is in now. A
// best-effort no-op when no run is registered.
//
// When the session is StateAwaiting, Persist also marks the runState.awaiting
// flag (race-free for Close's cancel loop). The flag is the signal Close uses to
// EXCLUDE a parked-awaiting run from the shutdown cancel loop: cancelling such a
// run would overwrite the durable awaiting snapshot (the cloud-native Phase 2
// resume point) with cancelled. Persist is called by the relay/test AFTER
// observing an event (EvPermissionAsk → loop parked, or EvResult → loop done), so
// reading sess.State here races no concurrent loop write (the loop is parked or
// exited; the state write happened-before the event the caller observed). The
// flag is a server-layer atomic, never read by the engine loop.
func (s *Service) Persist(ctx context.Context, id session.SessionID) {
	// A relay persists a run on behalf of its request caller. Authorize before
	// consulting the live registry so a foreign persist is a true no-op.
	if _, err := s.GetSession(ctx, id); err != nil {
		return
	}
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	// Save FIRST, then mark awaiting on success (H1 ordering): the flag must be
	// set only after the durable StateAwaiting snapshot has actually landed, so
	// Close (which skips cancelling awaiting runs) never skips a run whose
	// resumable snapshot was never persisted. Set-before-save would let Close
	// skip a run whose Save then fails, losing the resume point.
	err := s.cfg.Store.Save(ctx, st.sess)
	if err != nil {
		// Persistence is best-effort: a Save failure must not break the live
		// stream. The run continues from in-memory state; only resume-across-
		// restart is affected. Leave the awaiting flag unset so Close still
		// cancels this run (there is no durable snapshot worth preserving).
		_ = err
		return
	}
	if st.sess.State == session.StateAwaiting {
		st.awaiting.Store(true)
	}
}

// appendEvent durably records one relayed event to the configured EventLog
// (cloud-native Phase 3a). It is called by the gRPC/HTTP relay loops for every
// HEALTHY-PATH event, beside the existing awaiting-ask Persist; it is NOT called
// on the drain-to-discard path after a dead client (the relays gate it the same
// way they gate Persist). A nil EventLog is a no-op (byte-identical to pre-3a).
// An Append failure is best-effort: it WARNs and never aborts the run (a broken
// durable log must not break the live stream).
//
// It is ALSO the SINGLE site that stamps session.Event.Actor (ADR 0204 decision
// 5): the attribution is derive-at-append, read from the CONTEXT PRINCIPAL — the
// verified caller who drove this request — so the loop stays storage- and
// identity-agnostic and every emit site leaves Actor nil. Callers pass a
// cancel-detached ctx (context.WithoutCancel), which preserves the context VALUES
// and therefore the caller. A request with no verified caller leaves it nil —
// absence is never fabricated. Do not add a second stamping path.
func (s *Service) appendEvent(ctx context.Context, id session.SessionID, ev session.Event) {
	if s.cfg.EventLog == nil {
		return
	}
	// The actor is the verified caller who ACTED, NOT the session's owner. The two
	// are different questions and routinely different values: this phase ships no
	// authorization, so any authenticated caller may act on any session, and an
	// owner-derived stamp would put caller A on every event of a run caller B drove
	// (worst on EvApproval, where the record IS a human granting a tool
	// permission). The owner stays the identity of record for "whose is this?"; the
	// actor answers "who did this?". PrincipalFromContext returns a COPY, so a
	// later mutation cannot rewrite an already-recorded event.
	ev.Actor = session.PrincipalFromContext(ctx)
	if err := s.cfg.EventLog.Append(ctx, id, ev); err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "event log append failed",
			"session", string(id), "event", string(ev.Type), "err", err.Error())
	}
}

// AppendRunEvent durably records one event of an in-process run through the
// SINGLE appendEvent path — the same durability, best-effort and Actor-stamping
// contract (never a second stamping path). It exists for the IN-PROCESS
// consumers of a run that are not wire relays: the scheduler's fire loop
// (internal/app) drives its "sched--" session's events itself, so without this
// seam a scheduled fire would be the one run whose events never reach the
// durable log. Pass a cancel-detached ctx (context.WithoutCancel), exactly as
// the relays do, so a finished run cannot abort the write.
func (s *Service) AppendRunEvent(ctx context.Context, id session.SessionID, ev session.Event) {
	s.appendEvent(ctx, id, ev)
}

// relayEvent applies the SHARED per-event relay discipline (cloud-native Phase
// 3a/3b + the plan-mode auto-approve observer, issue #206 Wave 6a) that every
// event-relay loop (gRPC Converse, gRPC ApprovePlan, HTTP relayRunSSE, HTTP
// relayEventsSSE) must run for EACH observed event, BEFORE the call site's own
// wire write. It returns forward=true when the event should be sent on the
// client wire, forward=false when it is log-only (consumed by the durable log
// ONLY, NOT relayed to the client). It performs, in order:
//
//  1. appendEvent on a cancel-detached ctx (logCtx) — the durable log records
//     EVERY event regardless of client liveness (it must survive a dead client
//     and record the post-disconnect tail, including the terminal EvResult).
//  2. skip the client wire for the three log-only kinds (EvApproval,
//     EvCompactionArchive, EvUserPrompt) — appended above but NOT forwarded.
//  3. on EvPermissionAsk: Persist (snapshot semantics, gated to the healthy
//     path — the passed ctx, NOT the cancel-detached one) and — when autoApprove
//     is true — MaybeAutoApprovePlan (the headless auto-approve observer).
//
// The autoApprove flag gates whether MaybeAutoApprovePlan fires: the live
// Converse/relayRunSSE paths pass true (they observe a fresh parked plan ask);
// the ApprovePlan/relayEventsSSE paths pass false (they are ALREADY resolving a
// plan ask — running auto-approve inside the ApprovePlan stream would recurse).
// Each call site retains its OWN wire framing (gRPC Send vs SSE Write), its
// cancel-on-error, and its drain-to-discard guard.
func (s *Service) relayEvent(ctx context.Context, logCtx context.Context, id session.SessionID, ev session.Event, autoApprove bool) (forward bool) {
	s.appendEvent(logCtx, id, ev)
	// A non-ask event means the run is PROGRESSING (a tool result, a turn end, a
	// verdict, the terminal EvResult) — it is no longer parked awaiting. Clear the
	// runState.awaiting flag so Close's cancel loop does not skip a resumed-mid-
	// stream run (which would leave it StateRunning in the store on shutdown). The
	// flag is (re)set by Persist when EvPermissionAsk parks the run again. Race-free:
	// the atomic is mutated here on the relay thread and only read by Close. EvPermissionAsk
	// itself is handled below (Persist sets the flag), so it is excluded from this clear.
	if ev.Type != session.EvPermissionAsk {
		s.mu.Lock()
		if st, ok := s.runs[id]; ok {
			st.awaiting.Store(false)
		}
		s.mu.Unlock()
	}
	// EvApproval (3a), EvCompactionArchive (3b), and EvUserPrompt (ADR 0038) are
	// consumed by the durable log ONLY — appended above but NOT relayed to the
	// client wire (the verdict record, the pre-compaction archive, and the
	// user-prompt record are log/audit history, not client events; the client
	// already holds its own prompt). Skip the client send AFTER the Append.
	if ev.Type == session.EvApproval || ev.Type == session.EvCompactionArchive || ev.Type == session.EvUserPrompt {
		return false
	}
	if ev.Type == session.EvPermissionAsk {
		s.Persist(ctx, id)
		if autoApprove {
			s.MaybeAutoApprovePlan(ctx, id, ev)
		}
	}
	return true
}

// MaybeAutoApprovePlan fires the plan-mode auto-approve (issue #206 Wave 6a) when
// the Service observes a parked plan-approval ask on a headless deployment. It is
// called by the gRPC/HTTP relay loops alongside appendEvent+Persist for every
// EvPermissionAsk, and by tests simulating the relay. It is a NO-OP unless ALL of
// the following hold:
//
//  1. cfg.PlanModeAutoApprove is true (the OPT-IN operator flag — DEFAULT OFF).
//  2. The ask is plan-originated (session.AskOriginPlan — a PresentPlan call).
//  3. The deployment is headless (no interactive human approver — the engine's
//     Deps.Interactive is false). An interactive deployment surfaces the ask to
//     the human instead; auto-approve must NOT pre-empt a human.
//
// When all three hold it auto-resolves the ask via the EXISTING ApprovePlan path
// (ModeDefault + a loud note), emitting a LOUD diagnostic so the operator sees
// that NO HUMAN reviewed the plan. It NEVER fires for a non-plan ask (a policy/
// hook ask is still the human's/auto-deny's responsibility), NEVER fires
// interactively, and is NEVER load-bearing for safety (the engine still gates
// the PresentPlan — this merely resolves the parked ask).
func (s *Service) MaybeAutoApprovePlan(ctx context.Context, id session.SessionID, ev session.Event) {
	if !s.cfg.PlanModeAutoApprove || s.cfg.Interactive {
		return
	}
	if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
		return
	}
	if ev.Ask.Origin() != session.AskOriginPlan {
		return
	}
	// Emit the LOUD diagnostic BEFORE the verdict: the operator must see that no
	// human reviewed this plan. The note is also the operator-visible reason on
	// the session.
	s.cfg.Diagnostics.Log(ctx, port.LevelWarn,
		"plan_mode_auto_approve: auto-approving plan (NO HUMAN REVIEW)",
		"session", string(id), "ask_id", ev.Ask.AskID, "tool", ev.Ask.Tool)
	// Resolve the parked plan ask. Two cases:
	//
	//  1. LIVE RUN (same-process): the run is registered in s.runs and parked on
	//     the ask. Deliver the verdict directly via run.Approve (the same path the
	//     gRPC ResumeApproval frame takes) — the run terminates StopPlanApproved and
	//     the mode flips at the terminal boundary.
	//  2. CROSS-PROCESS (the run died): no live run; the session is StateAwaiting in
	//     the store. Use the EXISTING ApprovePlan path (resumeFromAwaiting) which
	//     re-enters the loop and drives the continuation atomically.
	//
	// Case 1 is the common headless path (the run is parked in-process); case 2
	// covers a restart where the process that parked the ask died.
	if run, ok := s.LookupRun(id); ok {
		// Live run: deliver the verdict directly (ModeDefault → allow-once, the
		// planApprovedTarget flip). The run terminates StopPlanApproved and the
		// mode flips. A continuation run MUST then proceed — a headless auto-approve
		// has no operator to re-prompt, so leaving the session idle (completed at
		// StopPlanApproved) is useless. This mirrors the cross-process path
		// (ApprovePlan's atomic continuation) so BOTH live and cross-process
		// auto-approve end with an execution run, not a parked-completed session.
		// It stays composition-side: the loop only terminates StopPlanApproved +
		// flips the mode; THIS goroutine drives the continuation via the SAME
		// StartRunContent path ApprovePlan uses (loadAndReopen → execute model)
		// carrying agent.PlanApprovedProceedText.
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		// Drive the continuation run in the background. The relay that owns the
		// ORIGINAL run's client stream drains the StopPlanApproved terminal; this
		// goroutine waits for the session to reach a terminal state (the verdict
		// terminated the live run) then starts the continuation, draining ITS
		// events to the durable log (appendEvent) so it never wedges. The
		// continuation's events are NOT relayed to the original client stream
		// (same discipline as the cross-process path's drain goroutine).
		go s.autoApproveContinuation(ctx, id)
		return
	}
	// Cross-process: the run is dead, the session is parked in the store. Drive
	// the EXISTING ApprovePlan path (resumeFromAwaiting → continuation run).
	events, err := s.ApprovePlan(ctx, id, session.ModeDefault, "auto-approved: no human reviewed this plan")
	if err != nil {
		// Fail-safe: log and return. The ask stays parked; the run continues in
		// plan mode (the model iterates). An error here means the session state
		// raced (e.g. a concurrent cancel) — not a safety issue.
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn,
			"plan_mode_auto_approve: auto-approve failed (ask stays parked)",
			"session", string(id), "err", err.Error())
		return
	}
	// Drain the merged event stream in a background goroutine so the relay loop
	// is not blocked. The auto-approved run's events are forwarded to the same
	// durable log (appendEvent) and the stream is drained to completion. The
	// relay that owns the client stream handles the client-wire discipline for
	// the ORIGINAL run; this goroutine only ensures the auto-approve continuation
	// is not orphaned.
	go func() {
		for range events {
			// Drain to completion — the continuation run's events are not relayed
			// to a client here (the client's stream is the original run's), but
			// the run must not wedge behind a full channel.
		}
	}()
}

// autoApproveContinuation is the LIVE-path continuation half of
// MaybeAutoApprovePlan (issue #206 Wave 6a). After the live run's verdict
// terminates it with StopPlanApproved + the mode flip, a headless auto-approve
// has no operator to re-prompt — so this drives the continuation execution run
// (the SAME atomic behavior as ApprovePlan's cross-process continuation): it
// waits for the session to reach a terminal state (the verdict terminated the
// live run), then starts a fresh run via StartRunContent carrying
// agent.PlanApprovedProceedText, and drains that run's events to the durable
// log (appendEvent) so it never wedges. The continuation's events are NOT
// relayed to the original client stream (the relay that owns the original run's
// stream handles client-wire discipline; this goroutine only ensures the
// continuation is not orphaned, mirroring the cross-process drain goroutine).
//
// It is bounded: a session that never reaches a terminal state (e.g. a
// concurrent cancel/error) gives up after autoApproveWaitTimeout and logs a
// WARN — fail-safe, never a wedge. A session that terminated with anything other
// than StopPlanApproved (cancel/error) is NOT continued (the plan was not
// approved); only a StopPlanApproved terminal proceeds, matching
// ApprovePlan's `resumedStop == StopPlanApproved` gate.
func (s *Service) autoApproveContinuation(ctx context.Context, id session.SessionID) {
	logCtx := context.WithoutCancel(ctx)
	deadline := time.Now().Add(autoApproveWaitTimeout)
	for {
		sess, err := s.GetSession(logCtx, id)
		if err != nil {
			// Session gone — nothing to continue. Fail safe.
			return
		}
		if sess.State == session.StateCompleted || sess.State == session.StateCancelled || sess.State == session.StateFailed {
			// Only proceed to a continuation when the run terminated at
			// StopPlanApproved (the plan-approval clean terminal that flipped
			// the mode). Any other terminal (cancel/error) means the plan was
			// NOT approved — do NOT start a continuation run.
			if reason, ok := sess.RecordedStopReason(); !ok || reason != session.StopPlanApproved {
				return
			}
			break
		}
		if time.Now().After(deadline) {
			s.cfg.Diagnostics.Log(logCtx, port.LevelWarn,
				"plan_mode_auto_approve: continuation not started (session did not reach a terminal state in time)",
				"session", string(id))
			return
		}
		time.Sleep(autoApprovePollInterval)
	}
	// Start the continuation run via the SAME path StartRunContent uses
	// (loadAndReopen → engineAndEnvironmentFor CASE 1 rebuild on the flipped
	// mode → execute model). The StopPlanApproved-completed session is reopened
	// to idle by loadAndReopen.
	proceed := agent.PlanApprovedProceedText + "\n\nOperator note: auto-approved: no human reviewed this plan"
	cont, cerr := s.StartRunContent(logCtx, id, proceed, nil)
	if cerr != nil {
		s.cfg.Diagnostics.Log(logCtx, port.LevelWarn,
			"plan_mode_auto_approve: continuation run failed to start",
			"session", string(id), "err", cerr.Error())
		return
	}
	for ev := range cont.Events() {
		s.appendEvent(logCtx, id, ev)
	}
	s.deregister(id, cont)
}

// autoApproveWaitTimeout bounds how long autoApproveContinuation waits for the
// live run to reach a terminal state before giving up (fail-safe, never a
// wedge). Generous: a plan-approval run terminates promptly after the verdict,
// but a slow provider/tool turn must not be cut short.
const autoApproveWaitTimeout = 30 * time.Second

// autoApprovePollInterval is the GetSession poll cadence while waiting for the
// live run to terminate. Short so the continuation starts promptly after the
// verdict terminates the run.
const autoApprovePollInterval = 10 * time.Millisecond

// acquireMutationLease acquires a lease for one serialized management mutation
// and returns its cleanup. A lease already held for the session lifetime is left
// untouched; a lease newly acquired by this mutation is released on every exit.
// The caller must hold runEntryMu for id.
func (s *Service) acquireMutationLease(ctx context.Context, id session.SessionID) (func(), error) {
	s.mu.Lock()
	_, preHeld := s.heldLeases[id]
	s.mu.Unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return func() {}, err
	}
	s.mu.Lock()
	_, held := s.heldLeases[id]
	s.mu.Unlock()
	if preHeld || !held {
		return func() {}, nil
	}
	return func() { s.releaseLease(id) }, nil
}

// acquireLease takes (or confirms) the cross-process single-writer lease for id
// (cloud-native Phase 4). It is called at the run-entry seam AFTER the
// per-session runEntryMu so same-process exclusion stays cheap and the
// resumeMu->runEntryMu lock order holds; the lease is the CROSS-process layer on
// top of that in-process lock.
//
// It is nil-safe (no SessionLease wired -> nil, the byte-identical default) and
// session-scoped: the lease is acquired ONCE, on first run-entry, and held for
// the session's life (a re-entry while already held is a no-op). On a competing
// live owner it returns ErrSessionLeasedElsewhere; on ErrLeaseUnsupported it
// stickily disables leasing (one INFO) and returns nil. Any other Acquire error
// is surfaced as an infrastructure failure (the operator misconfigured the
// backend; fail loud rather than silently run two writers).
//
// HOLD-FOR-SESSION-LIFE is deliberate: once Acquire succeeds the lease is kept
// even if the caller's subsequent run-launch (engineAndEnvironmentFor) fails — the
// lease is released ONLY by CloseSession / shutdown (releaseLease), never per-run.
// A future reader must NOT "fix" this into a run-scoped release: that would drop
// the lease between turns and let a competitor steal a session this process is
// still driving across re-entries.
//
// The Acquire RPC is bounded by leaseAcquireTimeout so a wedged backend cannot
// stall run-entry indefinitely (this runs under s.runEntryMu on the hot path).
func (s *Service) acquireLease(ctx context.Context, id session.SessionID) error {
	// Drain gate (ADR 0048, mecak8s): once Drain is armed, refuse new run-entries
	// BEFORE leasing/launching so a shutting-down replica steers new traffic to a
	// survivor. Checked here (the single run-entry chokepoint covering
	// StartRunContent + resumeFromAwaiting) so both prompt and awaiting-resume
	// paths are gated uniformly. An in-flight same-process Approve on a LIVE run
	// does NOT pass through acquireLease (it resolves over the channel), so a
	// verdict on an already-running session stays allowed during drain. The gate
	// starts false — byte-identical default for mecated and an undrained mecak8s.
	if s.draining.Load() {
		return fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if s.cfg.SessionLease == nil {
		return nil
	}
	s.mu.Lock()
	if s.leaseDisabled {
		s.mu.Unlock()
		return nil
	}
	if _, held := s.heldLeases[id]; held {
		s.mu.Unlock()
		return nil // already ours for this session; acquire only on first entry.
	}
	s.mu.Unlock()

	acqCtx, acqCancel := context.WithTimeout(ctx, leaseAcquireTimeout)
	lease, err := s.cfg.SessionLease.Acquire(acqCtx, id, s.cfg.LeaseOwner)
	acqCancel()
	switch {
	case errors.Is(err, port.ErrLeaseHeld):
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	case errors.Is(err, port.ErrLeaseUnsupported):
		s.mu.Lock()
		firstTime := !s.leaseDisabled
		s.leaseDisabled = true
		s.mu.Unlock()
		if firstTime {
			s.cfg.Diagnostics.Log(ctx, port.LevelInfo, "session leasing unsupported by backend; disabling (running without cross-process exclusion)",
				"owner", s.cfg.LeaseOwner)
		}
		return nil
	case err != nil:
		return fmt.Errorf("server: acquire session lease %q: %w", id, err)
	}

	// Store the hold and start the renewer. A second acquire that raced us (lost
	// the Acquire call, won the map insert) is collapsed: keep the first, cancel
	// our just-started renewer for the duplicate.
	renewCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	if _, dup := s.heldLeases[id]; dup {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.heldLeases[id] = &heldLease{lease: lease, cancel: cancel}
	s.mu.Unlock()
	go s.renewLoop(renewCtx, id)
	return nil
}

// renewLoop refreshes the held lease for id on a ticker until renewCtx is
// cancelled (CloseSession / shutdown). The renewer is OWNED BY Service -- the
// loop never imports port.SessionLease (the storage-agnostic discipline).
//
// SINGLE SOURCE OF TRUTH: each tick reads the CURRENT lease from
// heldLeases[id].lease UNDER s.mu (not a goroutine-local copy), refreshes it, and
// writes the refreshed value back under s.mu — so releaseLease/onLeaseLost always
// see the latest token/expiry and there is no unguarded read of the lease value.
//
// LOSS HANDLING is graceful for TRANSIENT faults, definitive for ErrLeaseHeld:
//   - Renew -> ErrLeaseHeld is DEFINITIVE loss (someone else took the lease): cancel
//     the run immediately.
//   - Any other (transient/infra) Renew error gets a GRACE: with TTL/3 ticks there
//     are ~3 attempts before real expiry, so a single backend blip must not kill a
//     long reasoning turn. We declare loss only once a renew has failed AND
//     clock.Now() is within one renew-interval of the lease's Expiry (i.e. the next
//     tick would land past expiry). Until then we keep the run and retry next tick.
//   - A ctx-cancelled error is just shutdown/close racing a tick -> exit quietly.
func (s *Service) renewLoop(renewCtx context.Context, id session.SessionID) {
	ticker := time.NewTicker(s.cfg.LeaseRenewInterval)
	defer ticker.Stop()
	renewTimeout := s.cfg.LeaseRenewInterval / leaseRenewFraction
	if renewTimeout <= 0 {
		renewTimeout = s.cfg.LeaseRenewInterval
	}
	for {
		select {
		case <-renewCtx.Done():
			return
		case <-ticker.C:
			// Read the current lease under the lock (single source of truth). If the
			// hold is gone (released concurrently) there is nothing to renew.
			s.mu.Lock()
			h, ok := s.heldLeases[id]
			if !ok {
				s.mu.Unlock()
				return
			}
			lease := h.lease
			s.mu.Unlock()

			rCtx, rCancel := context.WithTimeout(renewCtx, renewTimeout)
			refreshed, err := s.cfg.SessionLease.Renew(rCtx, lease)
			rCancel()
			switch {
			case errors.Is(err, context.Canceled):
				return // shutdown / close raced the tick.
			case errors.Is(err, port.ErrLeaseHeld):
				// Definitive loss: a competitor holds it now.
				s.onLeaseLost(renewCtx, id, err)
				return
			case err != nil:
				// Transient/infra fault: keep the run unless we are within one renew
				// interval of expiry (the next tick would land past it).
				if s.cfg.Now().Add(s.cfg.LeaseRenewInterval).Before(lease.Expiry) {
					continue // still have headroom; retry next tick.
				}
				s.onLeaseLost(renewCtx, id, err)
				return
			}
			s.mu.Lock()
			if h, ok := s.heldLeases[id]; ok {
				h.lease = refreshed // keep the latest token/expiry for Release.
			}
			s.mu.Unlock()
		}
	}
}

// onLeaseLost handles a declared lease loss: WARN, cancel the renewer's own ctx
// (so the goroutine's WithCancel child is not leaked), cancel the session's live
// run so a competitor can take over, and drop the hold. The cancelled run
// terminates cleanly (StopCancelled is recoverable), so this is fail-safe.
func (s *Service) onLeaseLost(ctx context.Context, id session.SessionID, cause error) {
	s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "lost session lease; cancelling run",
		"session", string(id), "owner", s.cfg.LeaseOwner, "err", cause.Error())
	if run, ok := s.LookupRun(id); ok {
		run.Cancel()
	}
	s.mu.Lock()
	if h, ok := s.heldLeases[id]; ok {
		h.cancel() // release the renewer's WithCancel child (self-cancel is harmless).
		delete(s.heldLeases, id)
	}
	s.mu.Unlock()
}

// releaseLease stops the session's renewer and releases its cross-process lease,
// best-effort. It is called by CloseSession and shutdown. The lease VALUE is
// snapshotted UNDER s.mu (port.Lease is an immutable value, so the copy is
// race-free against a concurrent renewer writing heldLeases[id].lease). The
// Release runs on a cancel-detached, short-timeout context (the appendEvent
// precedent) so a shutdown-cancelled ctx cannot abort the release. A nil
// SessionLease or an unheld id is a no-op.
func (s *Service) releaseLease(id session.SessionID) {
	if s.cfg.SessionLease == nil {
		return
	}
	s.mu.Lock()
	h, ok := s.heldLeases[id]
	var lease port.Lease
	if ok {
		lease = h.lease // guarded snapshot of the latest token/expiry.
		delete(s.heldLeases, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	h.cancel() // stop the renewer first.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), leaseAcquireTimeout)
	defer cancel()
	if err := s.cfg.SessionLease.Release(ctx, lease); err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session lease release failed",
			"session", string(id), "owner", s.cfg.LeaseOwner, "err", err.Error())
	}
}

// staleSessionWindow is the staleness age horizon for a persisted "running"
// snapshot (issue #475): a snapshot younger than this is never a candidate,
// regardless of any liveness signal (mirrors
// internal/adapter/scheduler/scheduler.go's staleFireWindow/staleFireThreshold
// idiom — a package var so a test can shrink it). It exists because it is the
// ONLY defense that covers subagent-*/parallel-*/team-* child sessions at all:
// IsLive's own doc comment says it never knows about engine children, so a
// liveness-only oracle would be blind to exactly the population issue #475's
// confirmed bug came from. It is also why this whole design is honestly
// "last-write-wins narrowed by a window," not atomic — Store.Save has no
// fencing/CAS, so nothing here actually stops an already-in-flight e.save from
// a genuinely live run landing after a sweep's repair; the wide age window is
// the only thing making that acceptable.
var staleSessionWindow = 30 * time.Minute

// staleTrialLeaseSuffix names this process's OWN trial-Acquire owner for
// SessionStale's secondary lease refinement — suffixed onto the real
// s.cfg.LeaseOwner (never a new unrelated string) so the trial is
// self-attributable in lease-backend diagnostics.
const staleTrialLeaseSuffix = "-stale-trial"

// SessionStale reports whether meta's persisted StateRunning snapshot is a
// crash-orphan (issue #475) rather than a genuinely in-flight run. It decides;
// it does not write — SettleIfStale performs the actual repair once a caller
// has decided a candidate is stale. Exported for internal/app's composition-
// level sweep (Step 4) to consume, mirroring IsLive/Diagnostics.
//
// The order matters and mirrors internal/adapter/scheduler/scheduler.go's
// shouldReconcileStaleFire/isPriorFireLive (age gate first, lease as a
// secondary refinement, trial lease released immediately, never held across
// a write) — an earlier draft of this fix inverted that order and is why the
// design history in the accompanying plan calls this out explicitly:
//
//  1. Age horizon (staleSessionWindow) is a HARD PRECONDITION: a fresh
//     snapshot is never stale, no matter what liveness/lease signals say.
//  2. IsLive(id): a same-process live run is never stale.
//  3. If a port.SessionLease is wired, a bounded TRIAL Acquire is the
//     secondary refinement:
//     - ErrLeaseHeld, but s.heldLeases[id] shows THIS process already holds
//     the REAL lease for id: that is the self-held-lease correction — a
//     run that died without releasing its own lease is evidence of
//     staleness, not liveness. Treat it as stale.
//     - ErrLeaseHeld otherwise (a genuinely different, live owner holds it):
//     not stale.
//     - success (nobody held it): release the trial immediately (this
//     function only decides; it never holds a lease across the caller's
//     later write) and report stale.
//     - ErrLeaseUnsupported: sticky-disable the WHOLE sweep for the process
//     lifetime (see LeaseSweepDisabled) rather than silently falling back
//     to local-only liveness for this one candidate — the fallback would
//     reintroduce the exact cross-replica unsoundness the lease branch
//     exists to prevent, for the one backend where this error is actually
//     reachable. Report not stale.
//     - any other error/timeout: fail-safe, not stale.
//  4. No lease wired at all: age + IsLive is the complete policy — a
//     not-live, past-window candidate IS stale (the single-process/file-
//     storage default path).
func (s *Service) SessionStale(ctx context.Context, meta port.SessionMeta) bool {
	// 1. Age horizon — a hard precondition, checked before anything else.
	if s.cfg.Now().Sub(meta.ModifiedAt) < staleSessionWindow {
		return false
	}
	// 2. Local liveness.
	if s.IsLive(meta.ID) {
		return false
	}
	// No lease wired: age + IsLive is the complete policy.
	if s.cfg.SessionLease == nil {
		return true
	}
	s.mu.Lock()
	disabled := s.leaseSweepDisabled
	s.mu.Unlock()
	if disabled {
		return false
	}
	trialCtx, cancel := context.WithTimeout(ctx, leaseAcquireTimeout)
	lease, err := s.cfg.SessionLease.Acquire(trialCtx, meta.ID, s.cfg.LeaseOwner+staleTrialLeaseSuffix)
	cancel()
	switch {
	case errors.Is(err, port.ErrLeaseHeld):
		s.mu.Lock()
		_, selfHeld := s.heldLeases[meta.ID]
		s.mu.Unlock()
		// The self-held-lease correction: ErrLeaseHeld against our OWN trial
		// call (a different owner string than the real hold, so the backend
		// sees a genuine conflict) is NOT evidence of a live peer when this
		// process itself is the one holding the real lease — it is evidence
		// this process's own prior run died without releasing it.
		return selfHeld
	case errors.Is(err, port.ErrLeaseUnsupported):
		s.mu.Lock()
		firstTime := !s.leaseSweepDisabled
		s.leaseSweepDisabled = true
		s.mu.Unlock()
		if firstTime {
			s.cfg.Diagnostics.Log(ctx, port.LevelInfo, "session staleness sweep: lease backend does not support leasing; disabling the sweep",
				"owner", s.cfg.LeaseOwner)
		}
		return false
	case err != nil:
		// Infra error or timeout — fail-safe: never mass-abandon on a flaky
		// lease backend.
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session staleness sweep: trial lease acquire failed; treating as not stale (fail-safe)",
			"session", string(meta.ID), "err", err.Error())
		return false
	}
	// Success: nobody held it. Release the trial immediately — this function
	// only decides staleness, it performs no write, so there is nothing to
	// hold the lease across.
	relCtx, relCancel := context.WithTimeout(context.WithoutCancel(ctx), leaseAcquireTimeout)
	_ = s.cfg.SessionLease.Release(relCtx, lease)
	relCancel()
	return true
}

// LeaseSweepDisabled reports whether SessionStale has stickily disabled the
// staleness sweep for the process lifetime (an ErrLeaseUnsupported backend).
// Exported for internal/app's Step 4 sweep to check before scanning, mirroring
// SessionStale/IsLive/Diagnostics.
func (s *Service) LeaseSweepDisabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseSweepDisabled
}

// SettleIfStale repairs a session id that a caller has ALREADY decided is
// stale (via SessionStale): it loads the raw snapshot, re-checks
// State==StateRunning and !IsLive(id) (closing the TOCTOU between whatever
// decided staleness and this load — the snapshot may have moved on since, or
// a genuinely live run may have started in the gap), and if it is genuinely
// still running and not locally live, abandons it via Session.Abandon() and
// persists the repair. It performs NO staleness decision of its own.
//
// This is the SWEEP's (Step 4) repair path ONLY: the sweep discovers a
// candidate id from a metadata scan with no in-memory session for it, so it
// must Load fresh from the store. A caller that already holds an in-memory
// *session.Session (Step 3's run-entry funnel, `loadAndReopen`) must NOT call
// this function — repairing the on-disk copy via a fresh Load would leave the
// funnel's OWN in-memory sess (already loaded, about to be handed to
// engine.Run) untouched and still carrying its unpaired tool_use, so the
// HTTP-400 this whole fix exists to prevent would survive unnoticed. The
// funnel instead calls sess.Abandon() + Store.Save directly on the session it
// already holds.
//
// Returns whether it actually settled something (false, nil is the honest
// no-op result for a session that already moved on, e.g. a race with a
// genuinely live re-entry or a peer's own settle).
func (s *Service) SettleIfStale(ctx context.Context, id session.SessionID) (bool, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		return false, fmt.Errorf("server: load session for stale settle: %w", err)
	}
	if sess.State != session.StateRunning {
		return false, nil
	}
	// ponytail: narrows, doesn't close, the TOCTOU window between the sweep's
	// staleness decision and this write — a real run could still register
	// between this check and the Save below. Store.Save has no CAS; closing
	// it fully needs one. See ADR write-up (Step 5).
	if s.IsLive(id) {
		return false, nil
	}
	if err := sess.Abandon(); err != nil {
		return false, fmt.Errorf("server: abandon stale running session: %w", err)
	}
	if err := s.cfg.Store.Save(ctx, sess); err != nil {
		return false, fmt.Errorf("server: persist abandoned session: %w", err)
	}
	return true, nil
}

// register records run (and the live session it drives) as the in-flight run
// for id.
func (s *Service) register(id session.SessionID, run *agent.Run, sess *session.Session) {
	s.mu.Lock()
	s.runs[id] = &runState{run: run, sess: sess}
	s.mu.Unlock()
}

// deregister removes the in-flight run for id (only if it is still the one
// recorded, so a later run for the same session is never clobbered).
func (s *Service) deregister(id session.SessionID, run *agent.Run) {
	s.mu.Lock()
	if st, ok := s.runs[id]; ok && st.run == run {
		delete(s.runs, id)
	}
	s.mu.Unlock()
}

// FinishRun removes run from the in-flight registry for id. It is the EXPORTED
// counterpart of register that every wire adapter must call (typically via
// `defer`) once it has finished draining run.Events(), so a completed run does
// not leak in the registry. It is idempotent and only removes the entry if run
// is still the one recorded (a later run for the same session is never
// clobbered), so it is safe to call unconditionally after a drain.
func (s *Service) FinishRun(id session.SessionID, run *agent.Run) {
	s.deregister(id, run)
}

// Subscribe registers a new per-session live event subscriber and returns a
// receive-only channel of session.Event plus an unsubscribe function. Every call
// to PublishSessionEvent for the given session id fans the event to ALL currently-
// registered subscribers. The channel carries a buffer of 64 events (matching
// a Run's own event buffer). When the channel is full, PublishSessionEvent DROPS
// the event (non-blocking drain-to-discard — a dead client never wedges the
// producer). The returned unsubscribe func is IDEMPOTENT (safe to call more than
// once, e.g. an explicit call plus a deferred one): it removes this subscription
// and closes the channel exactly once so the subscriber goroutine can exit
// cleanly.
//
// Subscribe's sole entry point is the gRPC StreamSessionLive wire handler — an
// UNTRUSTED boundary, not a trusted in-process caller. When OwnershipEnforced
// is set it authorizes via GetSession (issue #368) before registering a
// subscriber, so a caller who cannot load the session cannot observe its live
// events either; the check mirrors StreamSessionEvents exactly.
func (s *Service) Subscribe(ctx context.Context, id session.SessionID) (<-chan session.Event, func(), error) {
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSession(ctx, id); err != nil {
			return nil, nil, err
		}
	}
	ch := make(chan session.Event, 64)
	s.subMu.Lock()
	if s.subscriptionsClosed {
		close(ch)
		s.subMu.Unlock()
		return ch, func() {}, nil
	}
	s.subNextID++
	subID := s.subNextID
	if s.subscriptions[id] == nil {
		s.subscriptions[id] = make(map[int64]chan session.Event)
	}
	s.subscriptions[id][subID] = ch
	s.subMu.Unlock()

	var unsubOnce sync.Once
	unsub := func() {
		unsubOnce.Do(func() {
			s.subMu.Lock()
			defer s.subMu.Unlock()
			if m, ok := s.subscriptions[id]; ok && m[subID] == ch {
				delete(m, subID)
				if len(m) == 0 {
					delete(s.subscriptions, id)
				}
				close(ch)
			}
		})
	}
	return ch, unsub, nil
}

// PublishSessionEvent fans the event to every subscriber registered for the given
// session id. It is NON-BLOCKING: a full subscriber channel drops the event (the
// subscriber is dead/disconnected — drain-to-discard without wedging the producer).
// Events published here are the SAME events the run's own Events() channel carries
// (projection equivalence); the subscriber receives the raw session.Event, never a
// proto type. The loop stays storage-agnostic — it never calls this; the relay or
// the delivery driver (deliverFireResult) publishes.
func (s *Service) PublishSessionEvent(id session.SessionID, ev session.Event) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscriptions[id] {
		select {
		case ch <- ev:
		default:
			// drain-to-discard: the subscriber's channel is full — a dead
			// client that stopped draining. Drop the event without blocking.
		}
	}
}

// randomID returns a 128-bit random hex session id.
func randomID() session.SessionID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return session.SessionID(hex.EncodeToString(b[:]))
}

// --- MCP inspection ----------------------------------------------------------
//
// These operations expose the connected MCP servers' resources/prompts and the
// resolved source inventory over the network surface. They are catalog-level
// (independent of any session/run). The provider is nil-safe: list operations
// degrade to empty, and the two operations that genuinely require a live
// provider (ReadMcpResource / GetMcpPrompt) return ErrNoMCPProvider.

// ListMcpResources returns the resource snapshots for server (empty = union of
// all servers). Returns nil with no provider configured.
func (s *Service) ListMcpResources(ctx context.Context, server string) ([]mcp.Resource, error) {
	if s.cfg.MCPProvider == nil {
		return nil, nil
	}
	res, err := s.cfg.MCPProvider.ListResources(ctx, server)
	if err != nil {
		return nil, classifyMCPError(err)
	}
	return res, nil
}

// ReadMcpResource reads a single resource by URI from the named server. server
// and uri must be non-empty; a nil provider yields ErrNoMCPProvider; an unknown
// server name yields ErrInvalidArgument; a read/transport fault on a known
// server yields ErrInternal.
func (s *Service) ReadMcpResource(ctx context.Context, server, uri string) (mcp.ResourceContents, error) {
	if server == "" || uri == "" {
		return mcp.ResourceContents{}, fmt.Errorf("%w: server and uri are required", ErrInvalidArgument)
	}
	if s.cfg.MCPProvider == nil {
		return mcp.ResourceContents{}, ErrNoMCPProvider
	}
	c, err := s.cfg.MCPProvider.ReadResource(ctx, server, uri)
	if err != nil {
		return mcp.ResourceContents{}, classifyMCPError(err)
	}
	return c, nil
}

// ListMcpPrompts returns the prompt snapshots for server (empty = union of all
// servers). Returns nil with no provider configured.
func (s *Service) ListMcpPrompts(ctx context.Context, server string) ([]mcp.Prompt, error) {
	if s.cfg.MCPProvider == nil {
		return nil, nil
	}
	ps, err := s.cfg.MCPProvider.ListPrompts(ctx, server)
	if err != nil {
		return nil, classifyMCPError(err)
	}
	return ps, nil
}

// GetMcpPrompt expands a named prompt with args on the named server. server and
// name must be non-empty; a nil provider yields ErrNoMCPProvider; an unknown
// server name yields ErrInvalidArgument; an unknown prompt, missing required
// arg, or other expansion fault on a known server yields ErrInternal.
func (s *Service) GetMcpPrompt(ctx context.Context, server, name string, args map[string]string) (mcp.PromptResult, error) {
	if server == "" || name == "" {
		return mcp.PromptResult{}, fmt.Errorf("%w: server and name are required", ErrInvalidArgument)
	}
	if s.cfg.MCPProvider == nil {
		return mcp.PromptResult{}, ErrNoMCPProvider
	}
	res, err := s.cfg.MCPProvider.GetPrompt(ctx, server, name, args)
	if err != nil {
		return mcp.PromptResult{}, classifyMCPError(err)
	}
	return res, nil
}

// classifyMCPError maps a provider error to the right service sentinel so the
// network surfaces can distinguish a client mistake from a downstream fault. An
// unknown-server name (mcp.ErrUnknownServer) is genuinely a client error →
// ErrInvalidArgument (InvalidArgument / HTTP 400). Anything else is a
// transport/protocol fault on a connected server → ErrInternal (Internal /
// HTTP 500). Empty-required-field validation is handled by the callers before
// the provider is consulted and stays InvalidArgument.
func classifyMCPError(err error) error {
	if errors.Is(err, mcp.ErrUnknownServer) {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return fmt.Errorf("%w: %v", ErrInternal, err)
}

// ListMcpSources returns the MCP source inventory (possibly empty). When a
// MCPSourceProber is configured it RE-CONSULTS the resolved sources for live
// status/diagnostics on every call (so a client refresh reflects current state,
// not the startup snapshot); on a prober that returns nil it falls back to the
// cached startup snapshot so the panel always renders. With no prober it is a
// pure read of the injected snapshot (no live discovery).
func (s *Service) ListMcpSources(ctx context.Context) []source.SourceInfo {
	return s.liveSources(ctx)
}

// liveSources returns the freshest inventory available: the prober's result when
// it is configured and yields anything, otherwise the cached startup snapshot.
// Centralising this keeps ListMcpSources and ListToolHiveGroups consistent — a
// refresh that re-probes sources is reflected in both the panel and the groups.
func (s *Service) liveSources(ctx context.Context) []source.SourceInfo {
	if s.cfg.MCPSourceProber != nil {
		if probed := s.cfg.MCPSourceProber(ctx); probed != nil {
			return probed
		}
	}
	return s.cfg.MCPSources
}

// ListToolHiveGroups derives the distinct, non-empty ToolHive groups from the
// inventory (the live re-probe when a MCPSourceProber is set, else the startup
// snapshot — see liveSources). It considers only sources whose Kind is
// "toolhive". Output is sorted for deterministic results.
func (s *Service) ListToolHiveGroups(ctx context.Context) []string {
	seen := make(map[string]struct{})
	var groups []string
	for _, src := range s.liveSources(ctx) {
		if src.Kind != "toolhive" || src.Group == "" {
			continue
		}
		if _, ok := seen[src.Group]; ok {
			continue
		}
		seen[src.Group] = struct{}{}
		groups = append(groups, src.Group)
	}
	sort.Strings(groups)
	return groups
}

// ListAgents returns the resolved agent-definition snapshot (possibly empty).
// It is a pure read of the injected snapshot; no live discovery.
func (s *Service) ListAgents(_ context.Context) []*mecatlv1.AgentInfo {
	return s.cfg.Agents
}

// ListSkills returns the current skills inventory (possibly empty).
func (s *Service) ListSkills(ctx context.Context) []*mecatlv1.SkillInfo {
	if s.cfg.BeginSkillPublication != nil {
		unlock := s.cfg.BeginSkillPublication()
		defer unlock()
	}
	if s.cfg.PublishLearnedSkills != nil {
		if partition, err := s.skillPartition(ctx, ""); err == nil {
			publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillPublicationTimeout)
			_ = s.cfg.PublishLearnedSkills(publishCtx, partition)
			cancel()
		}
	}
	if s.cfg.LiveSkills != nil {
		return s.cfg.LiveSkills(ctx)
	}
	return s.cfg.Skills
}

// ListModels returns the resolved selectable-model inventory snapshot (possibly
// empty) — every available provider's catalog models, secret-free. When an
// on-demand refresher is installed (issue #262, R1.4) it is invoked FIRST
// (self-guarded: it decides which providers are stale and enforces its own
// cooldown) so a provider that just came back up is reflected on THIS call,
// with no restart; with no refresher installed (the byte-identical default)
// this is a pure read of the injected snapshot, exactly as before.
func (s *Service) ListModels(ctx context.Context) []*mecatlv1.ModelInfo {
	if p := s.modelsRefresher.Load(); p != nil && *p != nil {
		(*p)(ctx)
	}
	return s.currentModels()
}

// --- Soul + user-model inspection --------------------------------------------

// GetSoul returns the resolved soul (persona) snapshot (the build-time
// projection injected via Config.Soul). It is a pure read of that snapshot; no
// live re-read. When no soul source is wired it returns an empty snapshot
// (present=false), never nil, so the wire adapters always have a SoulInfo to
// serialize.
func (s *Service) GetSoul(_ context.Context) *mecatlv1.SoulInfo {
	if s.cfg.Soul == nil {
		return &mecatlv1.SoulInfo{}
	}
	return s.cfg.Soul
}

// UserModelEntry is the surface-agnostic listing metadata for one user-model
// fact (key + description, value omitted), mirroring tool.MemoryEntry's index
// shape. The Service exposes its own type so the wire adapters and the
// composition seam (UserModelLister) need not import the memory adapter.
type UserModelEntry struct {
	// Key is the entry's stable key.
	Key string
	// Description is the entry's one-line description.
	Description string
}

// UserModelLister enumerates the CURRENT user-model entries (key + description,
// value omitted). It is the composition-injected seam backing GetUserModel: the
// composition root closes over the user-model store's Index so a fetch reflects
// the live store state. It is read-only.
type UserModelLister interface {
	// List returns the user-model entries, key-sorted, or an error on a genuine
	// store fault.
	List(ctx context.Context) ([]UserModelEntry, error)
}

// UserModelInspector is the optional exact-entry detail capability. Keeping it
// separate preserves base-only/old remote list compatibility.
type UserModelInspector interface {
	Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error)
}

const maxUserModelHistory = 16

// GetUserModel returns the CURRENT user-model entries (a live read of the wired
// lister) plus aggregate size + hash over the rendered "key — description" rows.
// A nil lister (user model disabled) yields an empty response. A store fault is
// returned as ErrInternal so the wire adapters surface it distinctly.
func (s *Service) GetUserModel(ctx context.Context) (*mecatlv1.GetUserModelResponse, error) {
	if s.cfg.UserModel == nil {
		return &mecatlv1.GetUserModelResponse{}, nil
	}
	entries, err := s.cfg.UserModel.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list user model: %v", ErrInternal, err)
	}
	out := make([]*mecatlv1.UserModelEntry, 0, len(entries))
	var agg strings.Builder
	for _, e := range entries {
		description := e.Description
		if tool.SecretShapedMemoryValue(e.Key, description) {
			description = "[withheld: secret-shaped memory description]"
		}
		key := userModelWireText(e.Key)
		description = userModelWireText(description)
		out = append(out, &mecatlv1.UserModelEntry{Key: key, Description: description})
		agg.WriteString(key)
		agg.WriteByte('\t')
		agg.WriteString(description)
		agg.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(agg.String()))
	return &mecatlv1.GetUserModelResponse{
		Entries:   out,
		SizeBytes: int64(agg.Len()),
		Sha256:    hex.EncodeToString(sum[:]),
	}, nil
}

// GetUserModelDetail returns one exact entry without exposing a mutation path.
// Base-only listers honestly return nil detail.
func (s *Service) GetUserModelDetail(ctx context.Context, key string) (*mecatlv1.UserModelDetail, error) {
	inspector, ok := s.cfg.UserModel.(UserModelInspector)
	if !ok || key == "" {
		return nil, nil
	}
	record, found, err := inspector.Inspect(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect user model: %v", ErrInternal, err)
	}
	if !found {
		return nil, nil
	}
	revisions := record.Revisions
	if len(revisions) > maxUserModelHistory {
		revisions = revisions[len(revisions)-maxUserModelHistory:]
	}
	history := make([]*mecatlv1.UserModelRevision, len(revisions))
	for i, revision := range revisions {
		history[i] = toProtoUserModelRevision(revision)
	}
	return &mecatlv1.UserModelDetail{Current: toProtoUserModelRevision(record.Current), History: history, HistoryAvailable: record.Current.Version != ""}, nil
}

func toProtoUserModelRevision(revision tool.MemoryRevision) *mecatlv1.UserModelRevision {
	value := revision.Value
	if tool.SecretShapedMemoryValue(revision.Key, value) {
		value = "[withheld: secret-shaped memory value]"
	}
	description := revision.Description
	if tool.SecretShapedMemoryValue(revision.Key, description) {
		description = "[withheld: secret-shaped memory description]"
	}
	out := &mecatlv1.UserModelRevision{Key: userModelWireText(revision.Key), Value: userModelWireText(value), Description: userModelWireText(description), Version: userModelWireText(string(revision.Version)), Status: userModelWireText(string(revision.Status)), Writer: userModelWireText(string(revision.Writer)), Origin: userModelWireText(string(revision.Origin)), SourceSessionId: userModelWireText(revision.Source.SessionID), SourceProposalId: userModelWireText(revision.Source.ProposalID)}
	if !revision.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(revision.UpdatedAt)
	}
	return out
}

func userModelWireText(value string) string {
	return tool.CanonicalMemoryText(value)
}

// --- Slash command discovery -------------------------------------------------

// Command is the surface-agnostic listing metadata for one slash command (name +
// short description), mirroring prompt.Command. The Service exposes its own type
// so the wire adapters and the composition seam (CommandLister) need not import
// the prompt domain package directly.
type Command struct {
	// Name is the command's invocation name (without the leading "/").
	Name string
	// Description is a short, capped one-line summary for the palette.
	Description string
}

// CommandLister enumerates the slash commands available under a workspace root.
// It is the composition-injected discovery seam backing ListCommands: the
// composition root supplies an implementation that closes over the run-path
// command expander and the workspace factory, so the palette and the run path
// agree on which commands exist. It is read-only.
type CommandLister interface {
	// List returns the commands discovered under root, de-duplicated by name and
	// name-sorted, or an error on a genuine discovery fault.
	List(ctx context.Context, root string) ([]Command, error)
}

// ListCommands returns the available slash commands for the given workspace
// root. An empty root, a nil lister (command expansion disabled), or a lister
// that enumerates nothing all yield an empty slice. A discovery fault from the
// lister is returned as ErrInternal so the wire adapters surface it distinctly.
func (s *Service) ListCommands(ctx context.Context, workspace string) ([]Command, error) {
	if s.cfg.Commands == nil || workspace == "" {
		return nil, nil
	}
	cmds, err := s.cfg.Commands.List(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("%w: list commands: %v", ErrInternal, err)
	}
	return cmds, nil
}

// --- Worktree discovery (issue #102) ----------------------------------------

// Worktree is one discovered git worktree of a repo, mirroring the proto Worktree
// message (a `git worktree list --porcelain` record). The Service exposes its own
// proto-free type so the wire adapters and the composition seam (WorktreeLister)
// need not import the proto package directly.
type Worktree struct {
	// Path is the absolute working-tree path (the value handed to
	// CreateSessionRequest.workspace to bind a session to this worktree).
	Path string
	// Branch is the checked-out ref name; empty for a detached HEAD.
	Branch string
	// Head is the commit SHA the worktree is at.
	Head string
	// Bare is true for a bare worktree.
	Bare bool
}

// WorktreeLister enumerates the git worktrees of the repo rooted at root. It is
// the composition-injected discovery seam backing ListWorktrees (the client's
// /worktrees overlay): the composition root supplies an osfs-backed implementation
// that shells out to `git worktree list --porcelain` with a scrubbed env,
// trust-gated; a no-FS/cloud deployment (or an untrusted workspace) leaves it nil.
// It is read-only and nil-safe: when nil, ListWorktrees returns an empty list and
// ServerCapabilities.worktrees is false. It NEVER performs a live model/network
// call and never mutates anything.
type WorktreeLister interface {
	// List returns the worktrees of the repo at root (the main worktree first, in
	// `git worktree list` order), or an error on a genuine discovery fault. An
	// untrusted/non-repo root yields an empty slice, NOT an error (fail-soft).
	List(ctx context.Context, root string) ([]Worktree, error)
}

// ListWorktrees returns the git worktrees of the repo rooted at the given
// workspace. An empty root, a nil lister (worktree discovery disabled — a no-FS
// or cloud server), or a lister that enumerates nothing all yield an empty slice.
// A discovery fault from the lister is returned as ErrInternal so the wire
// adapters surface it distinctly. Read-only.
//
// Security: when DefaultWorkspace is configured, only the default workspace root
// is allowed. A client supplying any other path would otherwise trigger a git
// shell-out against an arbitrary directory; the clamp returns empty instead.
func (s *Service) ListWorktrees(ctx context.Context, workspace string) ([]Worktree, error) {
	if s.cfg.Worktrees == nil || workspace == "" {
		return nil, nil
	}
	if s.cfg.DefaultWorkspace != "" && workspace != s.cfg.DefaultWorkspace {
		return nil, nil
	}
	wts, err := s.cfg.Worktrees.List(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("%w: list worktrees: %v", ErrInternal, err)
	}
	return wts, nil
}

// --- Stored-session inventory (issue #245 Phase 1) --------------------------

// SessionSummary is one stored session's picker metadata — id, timestamps,
// state, turn count, and the resolved model id. It carries NO conversation
// content: it is the cheap row a client renders in an "open existing session"
// picker. The Service exposes its own proto-free type so the wire adapters
// (toProtoSessionSummaries) and any in-process consumer need not import the
// proto package. `model_id` is a bare opaque string (NOT a full ResolvedModel)
// to keep the picker row cheap and provider-neutral.
type SessionSummary struct {
	// SessionID is the stored session's id.
	SessionID string
	// ModifiedAtUnix is the last-write timestamp in Unix seconds (the
	// PrunableStore row mtime; the sort key for the picker).
	ModifiedAtUnix int64
	// State is the persisted lifecycle state (idle/running/awaiting/completed/...).
	// Empty when the snapshot could not be loaded (a corrupt store row still
	// surfaces its id/mtime).
	State string
	// Turns is the persisted model-call count. Zero when the snapshot could not
	// be loaded.
	Turns int
	// ModelID is the resolved model id this session ran on (bare string, no
	// provider context). Empty when the session never resolved a model or the
	// snapshot could not be loaded.
	ModelID string
	// CreatedAtUnix is the creation timestamp in Unix seconds. Zero when the
	// snapshot could not be loaded.
	CreatedAtUnix int64
	// Title is the human-readable session label (seeded once from the first
	// genuine user prompt, clamped to 120 runes). Populated from the snapshot
	// Title, or — when that is empty — from the lazy deriveTitle fallback
	// (walks the conversation for the first genuine user prompt). Empty for a
	// session with no genuine prompt.
	Title string
	// TitleProvenance reports whether the title is prompt-derived, operator-authored,
	// or legacy/unknown.
	TitleProvenance session.TitleProvenance
	// Workspace is the stored session root used for search and display.
	Workspace string
	// Owner is the verified caller the session is attributed to.
	Owner *session.Principal
	// Kind and Relationship are the durable trusted-producer taxonomy.
	Kind         session.SessionKind
	Relationship session.SessionRelationship
	// Capabilities and Reasons describe each public action valid for this row.
	// ReasonCode is the legacy aggregate public-chat reason.
	Capabilities SessionInventoryCapabilities
	Reasons      SessionInventoryActionReasons
	ReasonCode   CapabilityReason
}

// SessionInventoryCapabilities is the proto-free action posture for one row.
type SessionInventoryCapabilities struct {
	PublicChat              bool
	Inspect                 bool
	AuthoritativeTranscript bool
	ActivityReplay          bool
	CopyID                  bool
	ViewTranscript          bool
	Fork                    bool
	Rename                  bool
	Delete                  bool
}

// SessionInventoryActionReasons carries one closed reason for each disabled action.
type SessionInventoryActionReasons struct {
	PublicChat     CapabilityReason
	Inspect        CapabilityReason
	CopyID         CapabilityReason
	ViewTranscript CapabilityReason
	Fork           CapabilityReason
	Rename         CapabilityReason
	Delete         CapabilityReason
}

// CapabilityReason is a stable machine-readable explanation for a disabled
// inventory action.
type CapabilityReason string

const (
	// CapabilityReasonInspectOnlyKind means the session kind is available for
	// inspection but cannot be driven through the public chat entry point.
	CapabilityReasonInspectOnlyKind CapabilityReason = "inspect_only_kind"
	// CapabilityReasonAwaitingApproval means the chat has an unresolved approval.
	CapabilityReasonAwaitingApproval CapabilityReason = "awaiting_approval"
	// CapabilityReasonActiveElsewhere means another live run currently owns the chat.
	CapabilityReasonActiveElsewhere CapabilityReason = "active_elsewhere"
	// CapabilityReasonTranscriptUnavailable means no complete snapshot transcript can be loaded.
	CapabilityReasonTranscriptUnavailable CapabilityReason = "transcript_unavailable"
	// CapabilityReasonStorageUnsupported means the configured store cannot perform the action.
	CapabilityReasonStorageUnsupported CapabilityReason = "storage_unsupported"
	// CapabilityReasonUnknown means the row cannot prove action eligibility.
	CapabilityReasonUnknown CapabilityReason = "unknown"
)

const (
	// DefaultSessionInventoryPageSize applies when the caller omits page_size.
	DefaultSessionInventoryPageSize = 50
	// MaxSessionInventoryPageSize is the hard response-row bound.
	MaxSessionInventoryPageSize = 100
)

// ListSessionsPageRequest asks for one bounded inventory page.
type ListSessionsPageRequest struct {
	PageSize int
	Cursor   string
}

// ListSessionsPage is one bounded owner-filtered inventory response.
type ListSessionsPage struct {
	Sessions   []SessionSummary
	NextCursor string
	TotalCount int
}

type inventoryCursor struct {
	ModifiedAtUnixNano int64  `json:"m"`
	SessionID          string `json:"i"`
}

func encodeInventoryCursor(cursor *port.SessionMetadataCursor) (string, error) {
	if cursor == nil {
		return "", nil
	}
	data, err := json.Marshal(inventoryCursor{ModifiedAtUnixNano: cursor.ModifiedAt.UnixNano(), SessionID: string(cursor.ID)})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeInventoryCursor(token string) (*port.SessionMetadataCursor, error) {
	if token == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid session inventory cursor", ErrInvalidArgument)
	}
	var cursor inventoryCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.SessionID == "" {
		return nil, fmt.Errorf("%w: invalid session inventory cursor", ErrInvalidArgument)
	}
	return &port.SessionMetadataCursor{ModifiedAt: time.Unix(0, cursor.ModifiedAtUnixNano), ID: session.SessionID(cursor.SessionID)}, nil
}

func inventoryCapabilities(kind session.SessionKind, id session.SessionID, state session.State, live bool) (SessionInventoryCapabilities, SessionInventoryActionReasons) {
	caps := SessionInventoryCapabilities{Inspect: true, CopyID: id != ""}
	reasons := SessionInventoryActionReasons{
		PublicChat: CapabilityReasonUnknown, CopyID: CapabilityReasonUnknown,
		ViewTranscript: CapabilityReasonTranscriptUnavailable, Fork: CapabilityReasonUnknown,
		Rename: CapabilityReasonUnknown, Delete: CapabilityReasonUnknown,
	}
	if caps.CopyID {
		reasons.CopyID = ""
	}
	if kind != session.SessionKindMain || hasLegacyNonChatPrefix(id) {
		reasons.PublicChat = CapabilityReasonInspectOnlyKind
		reasons.Fork = CapabilityReasonInspectOnlyKind
		reasons.Rename = CapabilityReasonInspectOnlyKind
		reasons.Delete = CapabilityReasonInspectOnlyKind
		return caps, reasons
	}
	if state == session.StateAwaiting {
		reasons.PublicChat = CapabilityReasonAwaitingApproval
		reasons.Fork = CapabilityReasonAwaitingApproval
		reasons.Rename = CapabilityReasonAwaitingApproval
		reasons.Delete = CapabilityReasonAwaitingApproval
		return caps, reasons
	}
	if state == session.StateRunning || live {
		reasons.PublicChat = CapabilityReasonActiveElsewhere
		reasons.Fork = CapabilityReasonActiveElsewhere
		reasons.Rename = CapabilityReasonActiveElsewhere
		reasons.Delete = CapabilityReasonActiveElsewhere
		return caps, reasons
	}
	caps.PublicChat, caps.Fork, caps.Rename = true, true, true
	reasons.PublicChat, reasons.Fork, reasons.Rename = "", "", ""
	return caps, reasons
}

func validSessionRelationshipUTF8(relationship session.SessionRelationship) bool {
	return utf8.ValidString(string(relationship.ParentSessionID)) &&
		utf8.ValidString(string(relationship.CallID)) &&
		utf8.ValidString(relationship.ScheduleName) &&
		utf8.ValidString(string(relationship.OriginSessionID)) &&
		utf8.ValidString(relationship.TeamID) &&
		utf8.ValidString(relationship.MemberName)
}

func validSessionIdentityMetadata(id session.SessionID, relationship session.SessionRelationship) bool {
	return id != "" && utf8.ValidString(string(id)) && validSessionRelationshipUTF8(relationship)
}

func metadataKeyAfter(row port.SessionDiscoveryMeta, cursor *port.SessionMetadataCursor) bool {
	return row.ModifiedAt.Before(cursor.ModifiedAt) ||
		(row.ModifiedAt.Equal(cursor.ModifiedAt) && row.ID > cursor.ID)
}

func validateSessionMetadataPage(page port.SessionMetadataPage, request port.SessionMetadataPageRequest) error {
	if len(page.Sessions) > request.Limit {
		return fmt.Errorf("pager returned %d rows for limit %d", len(page.Sessions), request.Limit)
	}
	if page.TotalCount < 0 || page.TotalCount < len(page.Sessions) {
		return fmt.Errorf("pager returned invalid total count %d", page.TotalCount)
	}
	for i, row := range page.Sessions {
		if !validSessionIdentityMetadata(row.ID, row.Relationship) {
			return fmt.Errorf("pager returned invalid session identity metadata")
		}
		if request.OwnershipEnforced && (request.Owner == nil || !request.Owner.SameIdentity(row.Owner)) {
			return fmt.Errorf("pager returned a session outside the requested owner scope")
		}
		if request.Cursor != nil && !metadataKeyAfter(row, request.Cursor) {
			return fmt.Errorf("pager returned a row before its cursor")
		}
		if i > 0 && !metadataKeyAfter(row, &port.SessionMetadataCursor{
			ModifiedAt: page.Sessions[i-1].ModifiedAt,
			ID:         page.Sessions[i-1].ID,
		}) {
			return fmt.Errorf("pager returned rows out of keyset order")
		}
	}
	if page.NextCursor != nil {
		if len(page.Sessions) == 0 || page.NextCursor.ID == "" || !utf8.ValidString(string(page.NextCursor.ID)) {
			return fmt.Errorf("pager returned an invalid next cursor")
		}
		last := page.Sessions[len(page.Sessions)-1]
		if !page.NextCursor.ModifiedAt.Equal(last.ModifiedAt) || page.NextCursor.ID != last.ID {
			return fmt.Errorf("pager next cursor does not identify the final row")
		}
	}
	return nil
}

// ListSessionPage returns one bounded keyset page. The optional pager is a
// deployment capability: unsupported stores fail honestly instead of falling
// back to an unbounded response. Ownership criteria are sent to the store so
// filtering occurs before page formation and TotalCount.
func (s *Service) ListSessionPage(ctx context.Context, request ListSessionsPageRequest) (ListSessionsPage, error) {
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok {
		return ListSessionsPage{}, port.ErrSessionMetadataPagingUnsupported
	}
	limit := request.PageSize
	if limit < 0 {
		return ListSessionsPage{}, fmt.Errorf("%w: page_size must be non-negative", ErrInvalidArgument)
	}
	if limit == 0 {
		limit = DefaultSessionInventoryPageSize
	}
	if limit > MaxSessionInventoryPageSize {
		limit = MaxSessionInventoryPageSize
	}
	cursor, err := decodeInventoryCursor(request.Cursor)
	if err != nil {
		return ListSessionsPage{}, err
	}
	pageRequest := port.SessionMetadataPageRequest{
		Limit: limit, Cursor: cursor, OwnershipEnforced: s.cfg.OwnershipEnforced,
		Owner: session.PrincipalFromContext(ctx),
	}
	page, err := pager.PageSessionMetadata(ctx, pageRequest)
	if err != nil {
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			return ListSessionsPage{}, err
		}
		return ListSessionsPage{}, fmt.Errorf("%w: list session page: %v", ErrInternal, err)
	}
	if err := validateSessionMetadataPage(page, pageRequest); err != nil {
		return ListSessionsPage{}, fmt.Errorf("%w: invalid session metadata page: %v", ErrInternal, err)
	}
	out := ListSessionsPage{Sessions: make([]SessionSummary, 0, len(page.Sessions)), TotalCount: page.TotalCount}
	for _, meta := range page.Sessions {
		out.Sessions = append(out.Sessions, s.summaryFromDiscoveryMeta(meta))
	}
	out.NextCursor, err = encodeInventoryCursor(page.NextCursor)
	if err != nil {
		return ListSessionsPage{}, fmt.Errorf("%w: encode session inventory cursor: %v", ErrInternal, err)
	}
	return out, nil
}

func sessionDeleteSupported(store port.SessionStore) bool {
	if _, ok := store.(port.PrunableStore); !ok {
		return false
	}
	if support, ok := store.(port.SessionDeleteSupport); ok {
		return support.SupportsSessionDelete()
	}
	return true
}

func (s *Service) summaryFromDiscoveryMeta(meta port.SessionDiscoveryMeta) SessionSummary {
	created := int64(0)
	if !meta.CreatedAt.IsZero() {
		created = meta.CreatedAt.Unix()
	}
	kind := meta.Kind
	if kind == "" {
		kind = session.SessionKindUnknown
	}
	caps, reasons := inventoryCapabilities(kind, meta.ID, meta.State, s.IsLive(meta.ID))
	caps.AuthoritativeTranscript = meta.State != ""
	caps.ViewTranscript = caps.AuthoritativeTranscript
	if caps.ViewTranscript {
		reasons.ViewTranscript = ""
	}
	deletableStore := sessionDeleteSupported(s.cfg.Store)
	caps.Delete = caps.Rename && deletableStore
	if caps.Delete {
		reasons.Delete = ""
	} else if caps.Rename && !deletableStore {
		reasons.Delete = CapabilityReasonStorageUnsupported
	}
	caps.ActivityReplay = s.cfg.EventLog != nil
	return SessionSummary{
		SessionID: string(meta.ID), ModifiedAtUnix: meta.ModifiedAt.Unix(), State: string(meta.State),
		Turns: meta.Turns, ModelID: meta.ModelID, CreatedAtUnix: created, Title: meta.Title,
		TitleProvenance: meta.TitleProvenance,
		Workspace:       meta.Workspace, Owner: meta.Owner.Clone(), Kind: kind, Relationship: meta.Relationship,
		Capabilities: caps, Reasons: reasons, ReasonCode: reasons.PublicChat,
	}
}

func (s *Service) summaryFromMeta(meta port.SessionMeta) SessionSummary {
	return s.summaryFromDiscoveryMeta(port.SessionDiscoveryMeta{
		ID: meta.ID, ModifiedAt: meta.ModifiedAt, State: meta.State, Turns: meta.Turns,
		ModelID: meta.ModelID, CreatedAt: meta.CreatedAt, Title: meta.Title, Owner: meta.Owner,
	})
}

// StreamSessionEvents replays a session's durable event log as a lazy iterator
// over the recorded events (cloud-native Phase 3a read-back). It is the
// service-layer surface over port.EventLog.Read that the gRPC/HTTP handlers
// stream to a client opening an existing session (issue #245 Phase 1).
//
// A nil EventLog (no durable log configured) returns ErrNoEventLog so the wire
// adapters map to UNIMPLEMENTED (HTTP 501) — honestly reporting the surface is
// absent rather than pretending an unknown id. An unknown/pruned session id
// yields an EMPTY iterator (absence is data): port.EventLog.Read is defined to
// return an empty stream for an unknown id, so the service surfaces that
// verbatim. The loop stays storage-agnostic — this method never starts a run or
// makes a model call. Read-only.
//
// The returned iterator yields the events the relay PERSISTED — including the
// three log-only kinds (EvApproval/EvCompactionArchive/EvUserPrompt) a LIVE
// Converse relay skips on the client wire. The caller (the gRPC/HTTP handler)
// relays ALL of them: a client opening a PAST session wants the verdicts and
// user prompts, as they ARE the transcript. They are already metadata-only /
// redacted by construction (gauntlet #7: no raw args/deny-reason bodies/child
// content ever cross), so no extra filter applies at this layer.
func (s *Service) StreamSessionEvents(ctx context.Context, id session.SessionID) (iter.Seq2[session.Event, error], error) {
	if s.cfg.EventLog == nil {
		return nil, ErrNoEventLog
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSession(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.cfg.EventLog.Read(ctx, id), nil
}

// ListSessions returns the stored-session inventory — the picker metadata a
// client renders to let an operator open an EXISTING session by id (issue #245
// Phase 1). It is backed by port.PrunableStore.List (type-asserted on the
// configured store); a store that does not implement PrunableStore, or one that
// returns ErrPruneUnsupported, degrades to an EMPTY slice — never an error — so
// a no-persistence/cloud server honestly reports "no sessions".
//
// Each row carries only picker metadata (id, timestamps, state, turn count,
// model id); NO conversation content is loaded. For each PrunableStore row the
// service best-effort loads the snapshot to populate State/Turns/CreatedAtUnix
// and ModelID from the session's own PERSISTED sess.ModelID (NOT
// Service.ResolvedModel, which falls back to the shared default engine's model
// for a non-live session — that would misreport every session that was ever run
// on a non-default model); a Load failure leaves those fields zeroed but still
// returns the row (a corrupt snapshot file is surfaced in the picker with its
// id/mtime, so the operator can see it exists even if it can't be opened). Rows
// are sorted most-recently-active first (modified_at descending). Read-only.
//
// Cost: each row does a Store.Load (jsonlstore: reads the last snapshot line).
// Acceptable for a picker; no pagination in Phase 1.
//
// FAST PATH: when the store implements port.MetaLister (jsonlstore does),
// ListSessions uses MetaList — a CHEAP last-line read that skips the full
// conversation — instead of a full Load per row. This keeps listing N sessions
// O(N × last-line-read) rather than O(N × filesize) for large histories. The
// MetaLister path is the same latest-line-wins source Load trusts; a store that
// does NOT implement MetaLister falls back to the Load-per-row path (correct,
// just slower; memstore/redisstore/grpcdriver use it until they implement
// MetaList). The Title from MetaList is the snapshot Title ONLY — the lazy
// deriveTitle fallback (walking the conversation) is NOT available on the fast
// path; a session whose Title was never seeded shows "" on the fast path. That
// is acceptable for a picker (the snapshot Title is seeded by the loop on the
// first genuine prompt, so the common case is populated).
func (s *Service) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	// FAST PATH: a store that implements MetaLister enumerates picker metadata
	// cheaply (last-line read, no conversation unmarshal).
	if ml, ok := s.cfg.Store.(port.MetaLister); ok {
		return s.listSessionsMeta(ctx, ml)
	}
	ps, ok := s.cfg.Store.(port.PrunableStore)
	if !ok {
		return nil, nil
	}
	rows, err := ps.List(ctx)
	if err != nil {
		if errors.Is(err, port.ErrPruneUnsupported) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: list sessions: %v", ErrInternal, err)
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		if s.cfg.OwnershipEnforced {
			sess, lerr := s.cfg.Store.Load(ctx, r.ID)
			if lerr != nil || !s.ownsResource(ctx, sess.Owner) {
				continue
			}
		}
		kind := session.SessionKindUnknown
		caps, reasons := inventoryCapabilities(kind, r.ID, "", s.IsLive(r.ID))
		summary := SessionSummary{
			SessionID: string(r.ID), ModifiedAtUnix: r.ModifiedAt.Unix(),
			Kind: kind, Capabilities: caps, Reasons: reasons, ReasonCode: reasons.PublicChat,
		}
		if sess, lerr := s.cfg.Store.Load(ctx, r.ID); lerr == nil && sess != nil {
			if !s.ownsResource(ctx, sess.Owner) {
				continue
			}
			summary.State = string(sess.State)
			summary.Turns = sess.Counters.Turns
			summary.CreatedAtUnix = sess.CreatedAt.Unix()
			if sess.ModelID != "" {
				summary.ModelID = sess.ModelID
			}
			summary.Title = DeriveTitle(sess)
			summary.TitleProvenance = sess.TitleProvenance
			summary.Workspace = sess.Workspace
			// Clone: the row must not carry a live pointer into the loaded
			// session, or a consumer of the row can rewrite the recorded owner.
			summary.Owner = sess.Owner.Clone()
			summary.Kind = sess.Kind
			if summary.Kind == "" {
				summary.Kind = session.SessionKindUnknown
			}
			summary.Relationship = sess.Relationship
			summary.Capabilities, summary.Reasons = inventoryCapabilities(summary.Kind, sess.ID, sess.State, s.IsLive(sess.ID))
			summary.ReasonCode = summary.Reasons.PublicChat
			summary.Capabilities.AuthoritativeTranscript = true
			summary.Capabilities.ViewTranscript = true
			summary.Reasons.ViewTranscript = ""
			deletableStore := sessionDeleteSupported(s.cfg.Store)
			summary.Capabilities.Delete = summary.Capabilities.Rename && deletableStore
			if summary.Capabilities.Delete {
				summary.Reasons.Delete = ""
			} else if summary.Capabilities.Rename && !deletableStore {
				summary.Reasons.Delete = CapabilityReasonStorageUnsupported
			}
			summary.Capabilities.ActivityReplay = s.cfg.EventLog != nil
		}
		out = append(out, summary)
	}
	// Most-recently-active first (modified_at descending). Stable on ties so the
	// store's own ordering is preserved within an equal-mtime batch.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModifiedAtUnix > out[j].ModifiedAtUnix
	})
	return out, nil
}

// listSessionsMeta builds the SessionSummary slice from a MetaLister's cheap
// metadata projection (no conversation unmarshal). It is the fast-path
// implementation of ListSessions for stores that implement port.MetaLister.
func (s *Service) listSessionsMeta(ctx context.Context, ml port.MetaLister) ([]SessionSummary, error) {
	rows, err := ml.MetaList(ctx)
	if err != nil {
		if errors.Is(err, port.ErrPruneUnsupported) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: list sessions: %v", ErrInternal, err)
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		if s.cfg.OwnershipEnforced {
			sess, lerr := s.cfg.Store.Load(ctx, r.ID)
			if lerr != nil || !s.ownsResource(ctx, sess.Owner) {
				continue
			}
		}
		summary := s.summaryFromMeta(r)
		// A zero CreatedAt (a snapshot with no created_at, or a corrupt row that
		// left CreatedAt at the zero time) maps to 0, NOT the zero time's Unix
		// value (-62135596800) — matching the Load-fails zeroed-fields behaviour.
		if r.CreatedAt.IsZero() {
			summary.CreatedAtUnix = 0
		}
		out = append(out, summary)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModifiedAtUnix > out[j].ModifiedAtUnix
	})
	return out, nil
}

// DeriveTitle returns the session's human-readable label: the snapshot Title if
// set, else the clamped text of the FIRST genuine user prompt found by walking
// sess.Conversation.Messages (via session.IsGenuineUserPrompt, which skips
// synthesised compaction summaries), else "" (no genuine prompt). It is the lazy
// display-time fallback for a session whose Title was never seeded (e.g. a
// session created before the Title field existed, or one whose first prompt
// was multimodal-only). It does NOT mutate sess.Title — NO write-on-read: the
// snapshot stays the authoritative set-once label, and the derived value is a
// pure read projection the caller places on the wire. Used by ListSessions
// (picker) and the GetSession handlers.
func DeriveTitle(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	if sess.Title != "" {
		return sess.Title
	}
	if sess.Conversation == nil {
		return ""
	}
	for _, m := range sess.Conversation.Messages {
		if session.IsGenuineUserPrompt(m) && strings.TrimSpace(m.Text) != "" {
			return session.ClampTitle(m.Text)
		}
	}
	return ""
}
