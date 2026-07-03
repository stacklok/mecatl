package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
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
	// Workspaces builds a Workspace for a session root. Required.
	Workspaces WorkspaceFactory
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
	Forker tool.WorkspaceForker
	// ReadOnlyForker isolates a read-only-isolated team member's workspace as a cheap
	// git worktree (shares the base repo's `.git` ⇒ full history) so an inspect-only
	// member can run a shell (git log/show, build, test) confined to a throwaway
	// checkout. Optional; required only if the member factory marks any read-only
	// member IsolateReadOnly (which the composition root does only when this is
	// wired). When nil, read-only members base-share with no shell.
	ReadOnlyForker tool.WorkspaceForker
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
	Scheduler *scheduler.Scheduler
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

// ErrConfig is returned by NewService when a required dependency is missing.
var ErrConfig = errors.New("server: invalid config")

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
	// sessionWorkspaces holds per-session Workspace OVERRIDES. When an entry is
	// present for a session id, StartRun uses it instead of building one from the
	// shared Workspaces factory. It mirrors sessionEngines exactly: registered by a
	// surface adapter (the ACP adapter, to route file I/O through the editor's
	// fs/* buffers), preferred by StartRun, and evicted by CloseSession (editor
	// disconnect) / drained by Close (shutdown). It is bounded by connection
	// lifetime, not a count — same rationale as sessionEngines. The gRPC/HTTP
	// surfaces never register an override, so their behavior is unchanged.
	sessionWorkspaces map[session.SessionID]tool.Workspace

	// resumeMu serializes the awaiting-approval resume DECISION per session id
	// (cloud-native Phase 2): ApproveRun holds the per-session lock across the whole
	// (LookupRun-miss check → ResumeApproval → register) sequence, so two concurrent
	// Approves for the SAME awaiting session can never both spawn a resumed run. The
	// loser, on acquiring the lock, sees the now-registered live run via LookupRun and
	// routes its verdict to that run's channel (the same-process path) — the pending
	// tool executes EXACTLY ONCE. It is a keyedMutex, NOT s.mu, because the resume
	// sequence itself takes s.mu (engineAndWorkspaceFor / register) and Go mutexes are
	// not reentrant; a per-session lock also keeps unrelated sessions' resumes
	// concurrent. The keyedMutex frees a key once no caller holds it, so it never grows
	// unbounded.
	resumeMu keyedMutex

	// runEntryMu serializes the per-session RUN-ENTRY critical section (ADR 0030
	// Layer 3, the use-after-close guard): the engine-resolve (engineAndWorkspaceFor,
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

	// draining is the cloud-native drain gate (ADR 0048, mecak8s): once armed by
	// Drain, acquireLease rejects new run-entries with ErrUnavailable so a
	// shutting-down replica steers new traffic to a survivor within the
	// termination grace period. It starts false (the byte-identical default), so
	// mecated and an undrained mecak8s are unaffected. Read with atomic.Load in
	// the run-entry hot path (no s.mu).
	draining atomic.Bool

	// scheduler is the OPTIONAL in-process scheduled-tasks tick loop (issue #189,
	// Phase 1f), set from cfg.Scheduler. NewService does NOT start it — composition
	// (app.Build) calls SetFire then Start after NewService (the FireFunc closes
	// over the Service). Close stops it (drains in-flight fires + releases the
	// leader lease); Drain arms its drain gate. nil when no scheduler is wired.
	scheduler *scheduler.Scheduler

	// scheduleStoreCache memoises the type-assertion of cfg.Store for a
	// ScheduleStore (the PrunableStore/SessionLease precedent). It is computed
	// once on first scheduleStore() call and cached so the byte-identical
	// no-scheduling path stays cheap. Guarded by its own mutex (not s.mu) so a
	// schedule lookup does not contend with the run/registry hot path.
	scheduleStoreCache scheduleStoreCache
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
	// composition factory echoes. engineAndWorkspaceFor compares it against the loaded
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
type runState struct {
	run  *agent.Run
	sess *session.Session
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
	svc := &Service{
		cfg:               cfg,
		runs:              make(map[session.SessionID]*runState),
		teams:             make(map[string]*teamState),
		sessionEngines:    make(map[session.SessionID]*sessionEngine),
		sessionWorkspaces: make(map[session.SessionID]tool.Workspace),
		replayedApprovals: make(map[session.SessionID]struct{}),
		heldLeases:        make(map[session.SessionID]*heldLease),
		scheduler:         cfg.Scheduler,
	}
	// Seed the model inventory from the static snapshot. ListModels and the
	// ModelSelection cap read this atomic so a later live-catalog SetModels swap is
	// race-free. A nil cfg.Models seeds an empty (non-nil) slice so the pointer is
	// never nil.
	seed := cfg.Models
	svc.models.Store(&seed)
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

// CreateSession allocates a new idle session on the SHARED engine, persists it,
// and returns it. workspace must be non-empty. An unspecified mode falls back to
// DefaultMode. It is the no-selector, no-MCP fast path: it delegates to the
// generalized createSession with the zero selector, nil specs and the default
// profile.
func (s *Service) CreateSession(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits) (*session.Session, error) {
	return s.createSession(ctx, workspace, mode, limits, ProviderSelector{}, nil, ProfileDefault)
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
func (s *Service) CreateSessionWithProfile(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, profile SessionProfile) (*session.Session, error) {
	if sel.ProviderID == "" && sel.ModelID != "" {
		return nil, fmt.Errorf("%w: model_id requires provider_id (a bare model on the default provider is ambiguous)", ErrInvalidArgument)
	}
	return s.createSession(ctx, workspace, mode, limits, sel, nil, profile)
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
func setSessionLabels(sess *session.Session, sel ProviderSelector, profile SessionProfile) {
	sess.Profile = string(profile)
	sess.ProviderID = sel.ProviderID
	sess.ModelID = sel.ModelID
	sess.ReasoningEffort = sel.ReasoningEffort
}

func (s *Service) createSession(ctx context.Context, workspace string, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile) (*session.Session, error) {
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

	needPerSession := s.sessionNeedsPerFactory(sel, specs, profile, workspace)
	if !needPerSession {
		// Shared-engine fast path (today's behaviour, byte-identical). The labels are
		// the empty pair + default profile here (the empty-selector default profile is
		// exactly the no-per-session case), so setLabels persists nothing new — the
		// snapshot stays byte-identical to a pre-Phase-1 default session.
		sess := session.New(s.cfg.NewID(), mode, workspace, limits, s.cfg.Now())
		setSessionLabels(sess, sel, profile)
		if err := s.cfg.Store.Save(ctx, sess); err != nil {
			return nil, fmt.Errorf("server: persist session: %w", err)
		}
		return sess, nil
	}

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
	sess := session.New(s.cfg.NewID(), mode, workspace, limits, s.cfg.Now())
	// Persist the neutral provider+model selector and the profile as write-once
	// creation labels on the aggregate, so a restarted process re-derives the SAME
	// per-session engine via the factory (rehydrateSession) instead of falling to the
	// default-provider floor / inferring the profile from the empty-workspace pun.
	setSessionLabels(sess, sel, profile)

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
		// Register the no-FS Workspace as this session's per-session workspace
		// OVERRIDE under the SAME lock as the engine registration, so the moment
		// the session is visible StartRun resolves its workspace here and NEVER
		// hands the empty root to the shared osfs Workspaces factory (which would
		// MkdirAll/OpenRoot the server process's cwd — the exact hazard).
		s.sessionWorkspaces[sess.ID] = nofs.New()
	}
	s.mu.Unlock()

	if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
		// The engine was built and the slot reserved but the session could not be
		// persisted: evict the reservation (engine slot AND any workspace
		// override) and tear the per-session MCP manager down so a failed create
		// leaks neither a slot nor a connection.
		s.mu.Lock()
		delete(s.sessionEngines, sess.ID)
		delete(s.sessionWorkspaces, sess.ID)
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
		Mcp:            s.cfg.MCPProvider != nil,
		SlashCommands:  s.cfg.Commands != nil,
		Teams:          s.cfg.MemberEngine != nil,
		Agents:         len(s.cfg.Agents) > 0,
		Soul:           s.cfg.Soul != nil,
		UserModel:      s.cfg.UserModel != nil,
		ModelSelection: len(s.currentModels()) > 0,
		Memory:         has(memory.RememberToolName),
		Skills:         has(skills.ToolName),
		Bash:           has(tools.BashToolName),
		Image:          pcaps.Image,
		Audio:          pcaps.Audio,
		Posture:        s.cfg.Posture,
		Worktrees:      s.cfg.Worktrees != nil,
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
	return s.createSession(ctx, workspace, mode, limits, ProviderSelector{}, specs, ProfileDefault)
}

// SetSessionWorkspace registers a per-session Workspace OVERRIDE for id, so a
// subsequent StartRun uses ws instead of building one from the shared Workspaces
// factory. It is the seam the ACP adapter uses to route a session's file I/O
// through the editor's fs/* buffers. A second call for the same id replaces the
// override. The override is evicted by CloseSession (and drained by Close), so
// the caller MUST pair it with CloseSession on the owning connection's teardown
// (the ACP adapter tracks the session and does this on disconnect). The
// gRPC/HTTP surfaces never call this, so their workspace path is unchanged.
func (s *Service) SetSessionWorkspace(id session.SessionID, ws tool.Workspace) {
	s.mu.Lock()
	s.sessionWorkspaces[id] = ws
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
	// Drop any per-session workspace override too: it closes over the (now
	// disconnecting) connection, so it must not outlive the session.
	delete(s.sessionWorkspaces, id)
	// Drop the once-per-id approval-replay marker (cloud-native Phase 3b): the
	// OnCloseSession above Forgot this session's learned rules, so a LATER reload of
	// the same id in this process MUST be allowed to replay them from the durable log
	// again — otherwise the replay would short-circuit (marker still set) and leave
	// the rules evicted, re-opening the very re-ask wart 3b kills. Clearing it also
	// keeps the map from growing unbounded on a long-lived server.
	delete(s.replayedApprovals, id)
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
	// Stop the scheduler FIRST so in-flight fires drain while the service is
	// still alive to serve them (the FireFunc drives StartRunContent on this
	// Service). Stop cancels the tick loop, joins in-flight fires (with a grace),
	// and releases the leader lease. nil-safe (no scheduler wired). Read under
	// s.mu for consistency with SetScheduler/HasScheduler (set-once-before-serving
	// so practically safe, but -race won't catch a future caller that re-orders).
	s.mu.Lock()
	sched := s.scheduler
	s.mu.Unlock()
	if sched != nil {
		_ = sched.Stop()
	}
	s.mu.Lock()
	engines := s.sessionEngines
	s.sessionEngines = make(map[session.SessionID]*sessionEngine)
	// Drop all per-session workspace overrides on shutdown; they hold no resources
	// of their own (the underlying connection is closed separately) but must not
	// linger past the Service.
	s.sessionWorkspaces = make(map[session.SessionID]tool.Workspace)
	// Snapshot the held-lease ids so we can stop renewers + release each outside
	// the guard (releaseLease re-takes s.mu).
	leasedIDs := make([]session.SessionID, 0, len(s.heldLeases))
	for id := range s.heldLeases {
		leasedIDs = append(leasedIDs, id)
	}
	s.mu.Unlock()
	for _, se := range engines {
		if se.close != nil {
			_ = se.close()
		}
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
	s.mu.Lock()
	sched := s.scheduler
	s.mu.Unlock()
	if sched != nil {
		sched.Drain()
	}
	s.draining.Store(true)
}

// SetScheduler wires a scheduler onto an already-constructed Service. It is the
// late-bind seam for the scheduled-tasks tick loop (issue #189, Phase 1f):
// buildScheduler needs the Service for the FireFunc, so the scheduler is built
// AFTER NewService and attached here. NewService does NOT start it; composition
// calls SetFire then Start on the returned *scheduler.Scheduler. Nil-safe.
func (s *Service) SetScheduler(sch *scheduler.Scheduler) {
	s.mu.Lock()
	s.scheduler = sch
	s.mu.Unlock()
}

// HasScheduler reports whether a scheduler was wired into this Service. It is
// the read-side companion to SetScheduler: nil-safe (the byte-identical default
// wires no scheduler).
func (s *Service) HasScheduler() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduler != nil
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
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return sess, nil
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
		if rerr := sess.Recover(); rerr != nil {
			return nil, fmt.Errorf("server: recover session: %w", rerr)
		}
		if serr := s.cfg.Store.Save(ctx, sess); serr != nil {
			return nil, fmt.Errorf("server: persist recovered session: %w", serr)
		}
	}
	return sess, nil
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
		// A no-fs session's workspace override is re-registered with the engine
		// under the same lock (the create-time discipline), so StartRun never
		// consults the shared factory with the empty root.
		s.sessionWorkspaces[id] = nofs.New()
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
// UserPromptSubmit hook operate on the TEXT only (see Engine.RunContent). All
// other behaviour (workspace/engine selection, registration, drain contract) is
// identical to StartRun.
//
// It reopens-if-completed (via loadAndReopen) so a follow-up prompt on a session
// that cleanly finished a prior turn continues it — the in-process multi-turn
// counterpart to the cross-process LoadSession resume path.
func (s *Service) StartRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content) (*agent.Run, error) {
	if text == "" && len(parts) == 0 {
		return nil, fmt.Errorf("%w: prompt text or parts is required", ErrInvalidArgument)
	}
	// NOTE (ADR 0062): there is NO prompt-channel scan here. The guardrails
	// approve-once flow is OUT-OF-BAND — a PreToolUse guardrail block surfaces to the
	// human as an ordinary permission ask (Allow once / Allow & don't ask / Deny) and
	// is resolved via Approve(askID, verdict), reusing the existing approval machinery.
	// The session "Allow & don't ask again" waiver is armed IN-LOOP from a genuine
	// human AllowAlways verdict, never from a parsed directive in `text`. This replaces
	// the removed ADR-0061 /guardrail-allow prompt directive (no scan, no strip, no
	// near-miss WARN). The `text` param flows straight through.
	// loadAndReopen (not GetSession): a session that cleanly completed a prior turn
	// is in StateCompleted, and the engine's RecordUserPrompt rejects a terminal
	// state — so an in-process follow-up prompt (interactive multi-turn chat, a
	// long-lived teammate) must reopen-if-completed FIRST, exactly as the
	// cross-process LoadSession resume path does. A freshly-created idle session is
	// returned unchanged; a cancelled session is recovered via Interrupt and a
	// failed one via Recover (both history-repaired), so neither wedges the next
	// prompt on an illegal RecordUserPrompt transition (issue #51).
	sess, err := s.loadAndReopen(ctx, id)
	if err != nil {
		return nil, err
	}
	// Hold the per-session run-entry lock across engine-resolve (which may REBUILD a
	// per-session engine for a mode→model change, ADR 0030 Layer 3) + run launch +
	// register: this makes the rebuild's under-lock no-live-run check authoritative
	// (no concurrent run-entry for this id can be between its engine-read and its
	// register), so a displaced prior engine is never closed while in use. Per-session,
	// so unrelated sessions run concurrently.
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	// Cross-process single-writer gate (cloud-native Phase 4): take the session
	// lease AFTER the in-process runEntryMu so same-process exclusion stays cheap.
	// A competing live owner refuses the run with ErrSessionLeasedElsewhere; nil
	// SessionLease is the byte-identical no-lease default.
	if err := s.acquireLease(ctx, id); err != nil {
		return nil, err
	}
	engine, ws, err := s.engineAndWorkspaceFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	run := engine.RunContent(ctx, sess, ws, text, parts)
	s.register(id, run, sess)
	return run, nil
}

// engineAndWorkspaceFor resolves the engine + workspace a loaded session should run
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
func (s *Service) engineAndWorkspaceFor(ctx context.Context, sess *session.Session) (*agent.Engine, tool.Workspace, error) {
	id := sess.ID
	engine := s.cfg.Engine
	s.mu.Lock()
	se, hasEngine := s.sessionEngines[id]
	ws := s.sessionWorkspaces[id]
	s.mu.Unlock()
	switch {
	case hasEngine && se.builtForMode != "" && se.builtForMode != sess.Mode:
		// CASE 1 (ADR 0030 Layer 3): the registered per-session engine was built for a
		// DIFFERENT mode than the session now holds — a plan↔execute switch re-resolved
		// the model. Rebuild through the shared factory path, REPLACING the prior engine.
		// This runs only between turns (loadAndReopen drove the session idle and SetMode
		// is rejected mid-turn). The whole engineAndWorkspaceFor call is under the caller's
		// per-session runEntryMu, and buildAndRegisterSessionEngine re-checks s.runs[id]
		// UNDER s.mu before closing the displaced engine — that downstream check is the
		// AUTHORITATIVE use-after-close guard. This cheap pre-check is only an early-out so
		// an obviously-live session does not pay a wasted factory build.
		s.mu.Lock()
		_, live := s.runs[id]
		s.mu.Unlock()
		if live {
			return nil, nil, fmt.Errorf("%w: cannot rebuild engine for session %q mid-run (mode change must be deferred to a turn boundary)", ErrInvalidArgument, id)
		}
		sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
		profile := profileForSession(sess)
		rebuilt, err := s.buildAndRegisterSessionEngine(ctx, sess, sel, profile, sess.Mode, true)
		if err != nil {
			return nil, nil, err
		}
		se, hasEngine = rebuilt, true
		// The workspace override may have been (re-)registered by the rebuild (no-fs).
		s.mu.Lock()
		ws = s.sessionWorkspaces[id]
		s.mu.Unlock()
	case !hasEngine && !s.needsRehydration(sess) && s.cfg.ModeNeedsEngine != nil && s.cfg.SessionEngine != nil && s.cfg.ModeNeedsEngine(sess.Mode):
		// CASE 2 (ADR 0030 Layer 3): a DEFAULT-FS session that would otherwise ride the
		// shared engine, but its mode (plan) resolves a DIFFERENT model — promote it to a
		// per-session factory engine. A default-FS session has the empty selector + a real
		// workspace, so no workspace override is registered (the run-entry seam builds it
		// from the shared factory below, unchanged).
		sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
		promoted, err := s.buildAndRegisterSessionEngine(ctx, sess, sel, ProfileDefault, sess.Mode, false)
		if err != nil {
			return nil, nil, err
		}
		se, hasEngine = promoted, true
	}
	if !hasEngine && s.needsRehydration(sess) {
		// RESTART REHYDRATION (issue #55, widened in the cloud-native Phase 1): a
		// PERSISTED session that needed a PER-SESSION engine — a non-default
		// provider/model selector, OR the no-fs profile — has its engine + (for no-fs)
		// its workspace override living only in process memory; after a restart both
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
			return nil, nil, err
		}
		hasEngine = true
		if sess.Profile == string(ProfileNoFS) || sess.Workspace == "" {
			ws = nofs.New()
		}
	}
	if hasEngine {
		engine = se.engine
	}
	if ws == nil {
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
	}
	return engine, ws, nil
}

// sessionNeedsPerFactory reports whether a CreateSession with the given inputs
// must route through the per-session engine factory (rather than the shared
// engine fast path). It is the single expression behind needPerSession in
// createSession, extracted so createSession stays under the cyclomatic cap. The
// worktree arm (issue #102): a session whose workspace DIFFERS from the server's
// launch root routes through the factory so children pin their resolver to the
// session root. When DefaultWorkspace == "" (a child/member/cloud service) the
// arm never fires (a non-empty workspace can't differ from "").
func (s *Service) sessionNeedsPerFactory(sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string) bool {
	return sel != (ProviderSelector{}) || len(specs) > 0 || profile == ProfileNoFS ||
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
// somehow persisted no profile label still rehydrates).
//
// Worktree binding (issue #102, docs/adr/0032): a session whose persisted
// workspace DIFFERS from the server's launch root (DefaultWorkspace) ALSO needs
// rehydration — its per-session engine (which re-pins the CHILD permission
// resolver to the session root) lived only in process memory and is gone after a
// restart. When DefaultWorkspace is empty (a child/member service or a no-root
// cloud deployment) this arm never fires (a non-empty workspace can't differ
// from ""), so the cloud/no-root posture is byte-identical. A default FS session
// (Workspace == DefaultWorkspace) does NOT rehydrate, exactly as before.
func (s *Service) needsRehydration(sess *session.Session) bool {
	return sess.Profile == string(ProfileNoFS) ||
		sess.ProviderID != "" || sess.ModelID != "" ||
		sess.ReasoningEffort != "" ||
		sess.Workspace == "" ||
		(sess.Workspace != "" && s.cfg.DefaultWorkspace != "" && sess.Workspace != s.cfg.DefaultWorkspace)
}

// profileForSession reconstructs the SessionProfile from a loaded session's persisted
// inert labels (the SAME mapping rehydrateSession uses): the explicit no-fs label, or
// the second-defense empty-workspace inference. It is the shared profile source for the
// mode→model rebuild (CASE 1) so a no-fs session that switches mode rebuilds the no-FS
// catalog, never silently escalating onto the FS tools.
func profileForSession(sess *session.Session) SessionProfile {
	switch {
	case sess.Profile == string(ProfileNoFS):
		return ProfileNoFS
	case sess.Profile == "" && sess.Workspace == "":
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
		// Re-register the no-fs workspace override under the SAME lock as the engine
		// (the create-time discipline), so the run below — and every later run —
		// resolves its workspace here and never consults the shared factory with the
		// empty root. A selector session with a real workspace needs no override: the
		// run-entry seam builds its workspace from the shared factory as usual.
		s.sessionWorkspaces[id] = nofs.New()
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
// triggers the run-entry rebuild (engineAndWorkspaceFor swaps the registered engine
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
// internal/app/childgc.go). The predicate still genuinely protects an
// API-CLIENT-driven session that happens to carry a child prefix — StartRun
// on such an id registers it here like any other.
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
//   - if awaiting → rebuild the engine + workspace (the SAME engineAndWorkspaceFor
//     the prompt path uses, so the two cannot drift), call Engine.ResumeApproval to
//     re-enter the loop AT the ask, register the resumed run, and return it.
//
// The whole sequence runs under the per-session resume lock (s.resumeMu) so the
// LookupRun-recheck → ResumeApproval → register decision is ATOMIC per session: a
// concurrent caller blocks on the lock and, on acquiring it, takes the same-process
// branch above instead of spawning a second run (spawn-then-cancel would be unsafe —
// the loser goroutine could execute the tool before a Cancel landed). Registration
// mirrors rehydrateSession's loser-teardown/MaxSessionEngines guard via
// engineAndWorkspaceFor; the resumed run is registered into s.runs like any other so
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
	// ADR 0030 Layer 3 note: engineAndWorkspaceFor's mode→model rebuild (CASE 1) is a
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
	engine, ws, err := s.engineAndWorkspaceFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	run := engine.ResumeApproval(ctx, sess, ws, askID, verdict)
	s.register(id, run, sess)
	return run, nil
}

// Cancel cancels the session's in-flight run. The store-fallback semantics match
// Approve: ErrNotFound when the session is unknown, ErrNoActiveRun when it
// exists only in the store with no live run.
func (s *Service) Cancel(ctx context.Context, id session.SessionID) error {
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
func (s *Service) Persist(ctx context.Context, id session.SessionID) {
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	if err := s.cfg.Store.Save(ctx, st.sess); err != nil {
		// Persistence is best-effort: a Save failure must not break the live
		// stream. The run continues from in-memory state; only resume-across-
		// restart is affected.
		_ = err
	}
}

// appendEvent durably records one relayed event to the configured EventLog
// (cloud-native Phase 3a). It is called by the gRPC/HTTP relay loops for every
// HEALTHY-PATH event, beside the existing awaiting-ask Persist; it is NOT called
// on the drain-to-discard path after a dead client (the relays gate it the same
// way they gate Persist). A nil EventLog is a no-op (byte-identical to pre-3a).
// An Append failure is best-effort: it WARNs and never aborts the run (a broken
// durable log must not break the live stream).
func (s *Service) appendEvent(ctx context.Context, id session.SessionID, ev session.Event) {
	if s.cfg.EventLog == nil {
		return
	}
	if err := s.cfg.EventLog.Append(ctx, id, ev); err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "event log append failed",
			"session", string(id), "event", string(ev.Type), "err", err.Error())
	}
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
// even if the caller's subsequent run-launch (engineAndWorkspaceFor) fails — the
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

// ListSkills returns the resolved skills-inventory snapshot (possibly empty).
// It is a pure read of the injected snapshot; no live discovery.
func (s *Service) ListSkills(_ context.Context) []*mecatlv1.SkillInfo {
	return s.cfg.Skills
}

// ListModels returns the resolved selectable-model inventory snapshot (possibly
// empty) — every available provider's catalog models, secret-free. It is a pure
// read of the injected snapshot; no live discovery (multi-provider Phase 0, S3).
func (s *Service) ListModels(_ context.Context) []*mecatlv1.ModelInfo {
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
		out = append(out, &mecatlv1.UserModelEntry{Key: e.Key, Description: e.Description})
		agg.WriteString(e.Key)
		agg.WriteByte('\t')
		agg.WriteString(e.Description)
		agg.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(agg.String()))
	return &mecatlv1.GetUserModelResponse{
		Entries:   out,
		SizeBytes: int64(agg.Len()),
		Sha256:    hex.EncodeToString(sum[:]),
	}, nil
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
