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
	"github.com/stacklok/mecatl/engine/adapter/memledger"
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
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// WorkspaceFactory builds the session-scoped tool.Workspace for a session root.
// The server is workspace-agnostic: the composition root injects memfs (tests)
// or osfs (production) via this seam.
type WorkspaceFactory func(root string) tool.Workspace

// Clock returns the current wall time. It defaults to time.Now when nil so the
// server can stamp session creation timestamps deterministically in tests.
type Clock func() time.Time

// AuthorizationTimer is a cancellable process-local expiry handle.
type AuthorizationTimer interface{ Stop() bool }

// AuthorizationTimerFactory creates one expiry observation for a parked authorization.
type AuthorizationTimerFactory func(time.Duration, func()) AuthorizationTimer

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
	// DebugMCPTools is the exact direct-tool ceiling resolved by a debug factory.
	// It contains model-facing tool names only and is persisted on the session.
	DebugMCPTools []string
	// MountedClientMCP names the client-provided MCP servers that ACTUALLY
	// CONNECTED for this session — never an echo of what was requested. It is nil
	// when no specs were passed, and SHORTER than the request when some server was
	// unreachable (mcp.NewManager keeps only successful connections).
	//
	// It exists because connecting is best-effort in composition and that is the
	// right default for ONE of the two callers, not both. The ACP adapter wants a
	// usable session even when an editor's MCP server is down; a gRPC/HTTP
	// CreateSession caller cannot see composition's WARN and would otherwise be
	// handed a session ID for a session missing tools it asked for, with no way to
	// detect it. So the factory reports the mounted set and the Service enforces
	// all-or-nothing on the wire path only (see verifyClientMCPMounted).
	//
	// A factory reached through the WIRE path MUST populate it. Leaving it empty
	// while servers were requested is treated as "nothing mounted" and fails the
	// create, rather than as "no claim made" — a guarantee a factory can silently
	// opt out of by forgetting a field is not a guarantee. Only WithClientMCP
	// (the wire-only option) turns the check on, so the ACP path and every
	// selector-only factory are unaffected.
	MountedClientMCP []string
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
// tools, no Shell, no Parallel, no SkillDraft; file-less Subagent/Team children)
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

// DebugSessionEngineFactory builds the dedicated engine for a debug session.
// targetFingerprint and targetOwner bind every evidence/MCP read to the exact
// authorized target incarnation; neither may be projected to the model or wire.
type DebugSessionEngineFactory func(ctx context.Context, sel ProviderSelector, profile SessionProfile, mode session.PermissionMode, target session.SessionID, targetFingerprint string, targetOwner *session.Principal, selectedServers, toolCeiling []string) (SessionEngineResult, error)

// ModelInventory is the resolved composition-owned inventory shared by
// ListModels and model-facing discovery. Implementations must provide atomic reads
// and swaps. Nil retains the Service-owned compatibility path.
type ModelInventory interface {
	CurrentModels() []*mecatlv1.ModelInfo
	SetModels([]*mecatlv1.ModelInfo)
}

func seedModelInventory(cfg Config) []*mecatlv1.ModelInfo {
	if cfg.ModelInventory == nil {
		return cfg.Models
	}
	if cfg.Models != nil {
		cfg.ModelInventory.SetModels(cfg.Models)
	}
	return cfg.ModelInventory.CurrentModels()
}

// SessionEngineWithToolsFactory is the explicit per-session catalogue seam used
// for host-owned wrapper tools. Tools are ordinary arguments: catalogue assembly
// must not recover them from context values or a process-global fallback.
type SessionEngineWithToolsFactory func(ctx context.Context, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (SessionEngineResult, error)

// Config wires the Service's collaborators and resolved composition values.
type Config struct {
	// BuildID is the composed binary build identity exposed by GetServerInfo only.
	BuildID string
	// ServerImplementation is the stable composition family exposed by GetServerInfo.
	// NewService admits only [a-z][a-z0-9-]{0,63}; invalid values report "unknown".
	// It must not identify an instance, deployment, topology, configuration,
	// capabilities, or authentication.
	ServerImplementation string
	// ProviderEndpoint returns the configured endpoint for a provider already known
	// to the caller and composition. It must not perform discovery, session/store or
	// config reads, or other side effects; GetServerInfo sanitizes its result before
	// every response boundary. nil and unknown providers report unavailable.
	ProviderEndpoint func(providerID string) string
	// Engine is the shared agent engine that drives every run. Required.
	Engine *agent.Engine
	// DebugSessionEngine builds dedicated no-filesystem debug-session engines.
	// Nil disables creation and makes persisted debug sessions fail closed at
	// rehydration rather than falling back to Engine or SessionEngine.
	DebugSessionEngine DebugSessionEngineFactory
	// DebugMCP reports that the debug factory can borrow selected direct tools from
	// a configured global MCP manager.
	DebugMCP bool
	// Store persists and looks up sessions. Required.
	Store port.SessionStore
	// StorageManagementAuthorized gates process-wide storage health. A nil
	// authorizer disables the management capability. It must be derived from the
	// trusted request context, never request-supplied owner data.
	StorageManagementAuthorized func(context.Context) bool
	// LocalStorageMaintenanceSingleWriter is true only when composition has proved
	// the store itself is private to this process (the in-process IsLive registry
	// plus backend family locks are then sufficient). Management authorization is
	// not such a proof. Any durable or otherwise shareable store must leave this
	// false and wire a working SessionLease before destructive migration, cleanup,
	// or automatic retention is advertised or run.
	LocalStorageMaintenanceSingleWriter bool
	// SessionLiveness carries process-local engine-owned child activity. Service's
	// own runs map covers top-level runs; delegation children never enter that map,
	// so destructive maintenance must consult both. Cross-process activity remains
	// protected by SessionLease.
	SessionLiveness port.SessionLiveness
	// RetentionPolicy is the effective operator policy projected into health.
	RetentionPolicy RetentionPolicy
	// StorageMaintenanceStatus reports the shared retention/migration/cleanup lifecycle.
	StorageMaintenanceStatus func() StorageMaintenanceStatus
	// StorageMaintenanceUpdate receives sanitized lifecycle transitions. nil keeps
	// maintenance APIs functional without process-wide health observability.
	StorageMaintenanceUpdate func(StorageMaintenanceEvent)
	// OwnershipEnforced is true only when the request edge has a verifier wired.
	// Its zero value preserves the ownerless compatibility path. When enabled,
	// create retries compare the verified issuer/subject pair before exposing an
	// existing caller-selected ID, and the store must implement port.SessionCreator
	// so no generated, forked, or scheduled session can overwrite an existing snapshot.
	OwnershipEnforced bool
	// ClientMCPOnCreate permits CLIENT-PROVIDED MCP servers on a session-creating
	// API request (CreateSessionRequest.mcp_servers and its HTTP peer). It is a
	// deployment/composition policy in the shape ADR 0237 requires, NOT an
	// inference the server package makes from its own socket state.
	//
	// The zero value FAILS CLOSED: a Service built without an explicit grant
	// refuses the field. That direction is deliberate — accepting an arbitrary
	// outbound endpoint plus its auth headers from an API caller lends the server's
	// ambient network authority to a remote principal, so a composition root that
	// has not thought about it must not accidentally grant it. mecated derives it
	// from listener topology (clientMCPOnCreateForListeners): permitted only on a
	// UNIX-socket gRPC listener with HTTP disabled. An attacker-named endpoint
	// carrying caller-supplied credentials lends greater ambient authority than
	// ordinary loopback traffic.
	//
	// It gates the WIRE surface only. The in-process CreateSessionWithMCP /
	// LoadSessionWithMCP entries are unaffected: their caller is the ACP adapter,
	// which is a stdio peer of the operator's own editor and has no listener at
	// all, so listener-derived policy is meaningless there.
	//
	// The SAME value drives the mcp_servers_on_create advertisement (FeatureScope),
	// so a deployment cannot advertise what it will refuse.
	ClientMCPOnCreate bool
	// PlacementProvider is the deployment-owned atomic placement seam (ADR 0291).
	// Bind authorizes creation/successor choices, Reattach resolves only an exact
	// persisted EnvironmentRef, and ListWorktrees issues source-scoped ephemeral
	// selectors. app.Build always supplies the trusted local default; alternative
	// composition may supply one provider that owns worktree or remote placements.
	// It is mandatory.
	PlacementProvider PlacementProvider
	// PlacementScope is the trusted deployment scope supplied to every provider
	// Bind. It must be non-empty when PlacementProvider is configured.
	PlacementScope PlacementScope
	// SessionReadLedger optionally selects a durable read-before-write ledger for
	// each session independently of its placement's content backend. The returned
	// handle must be non-nil; nil fails the run closed.
	SessionReadLedger func(session.SessionID) tool.ReadLedger
	// RootAuthority mints a complete authority set for a newly composed root.
	// A nil callback preserves host-managed legacy sessions; app.Build always wires
	// this callback with its assembled catalog. Carryover forks copy their source
	// authority instead of invoking it.
	RootAuthority func(session.SessionKind) session.Authority
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
	// AuthorizationTimer creates process-local expiry observations. It defaults to time.AfterFunc.
	AuthorizationTimer AuthorizationTimerFactory
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

	// SharedEngineRoot is the verified workspace root the shared engine's policy
	// collaborators were assembled for. Every filesystem-capable placement with a
	// different root must use SessionEngine, including custom environment kinds and
	// rootless shared deployments.
	SharedEngineRoot string

	// Agents is the resolved agent-definition snapshot taken at startup. It backs
	// ListAgents and is a pure read of this snapshot (no live discovery). The
	// composition root (internal/app) resolves the registry once and projects each
	// def into the proto form (name/description/resolved model/effective read-only
	// tool scope/permission mode/color) so the server adapter never imports the
	// agents adapter. May be empty (agent definitions disabled or none found).
	Agents []*mecatlv1.AgentInfo

	// Models seeds the resolved selectable-model inventory projected by composition.
	// It carries public ModelInfo metadata only and remains the compatibility path
	// when ModelInventory is nil.
	Models []*mecatlv1.ModelInfo
	// ModelInventory optionally supplies the composition-owned atomic inventory
	// shared by ListModels and model-facing discovery. When set, SetModels updates
	// this source and the Service's local scheduling projection in one operation.
	ModelInventory ModelInventory

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
	// DeploymentID is an optional, opaque, operator-set label for this deployment,
	// surfaced on GetServerInfo. It is empty by default and is NEVER derived from
	// hostname, pod name, or environment: infrastructure topology is not something
	// an authenticated caller is owed, and a label the operator did not choose is a
	// leak with no consenting author. Bounded and validated at the composition
	// root (mecated --deployment-id), not here. See ADR 0248.
	DeploymentID string

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
	// automatic learning mode. Attempts expose only content-free lifecycle
	// projections from the verified caller's private partition. Proposals and the
	// mutation callbacks expose the bounded, caller-partitioned staged-learning
	// review surface.
	ReflectSession          ExplicitReflector
	Attempts                learning.AttemptRepository
	AttemptPrincipal        func(*session.Principal) string
	Proposals               learning.ProposalRepository
	ProposalManifest        ProposalManifestLoader
	ProposalPrincipal       func(*session.Principal) string
	PromoteProposal         ProposalPromoter
	UndoProposal            ProposalUndoer
	ProjectPromotionAllowed func(project string) bool
	ProposalActionAvailable func(project string) (bool, string)

	// DreamReviewer is the transport-neutral, process-local manual consolidation
	// coordinator. DreamCapabilities is the composition-computed availability
	// snapshot for the exact Build-owned project-memory and user-model stores. Both
	// zero values keep manual dreaming unavailable.
	DreamReviewer     DreamReviewer
	DreamCapabilities DreamCapabilities

	// LearnedSkills exposes caller-partitioned, agent-owned lifecycle records. The
	// publisher atomically refreshes the shared live Skill catalog after mutations.
	LearnedSkills             learning.SkillRepository
	PublishLearnedSkills      func(context.Context, learning.SkillPartition) error
	BeginSkillPublication     func(learning.SkillPartition) func()
	LiveSkillGeneration       func(learning.SkillPartition) uint64
	SkillActionAvailable      func(learning.SkillPartition, string) (bool, string)
	LearnedSkillNameAvailable func(string) bool
	LiveSkills                func(context.Context) []*mecatlv1.SkillInfo

	// TitleGenerationEligible is the composition-resolved eligibility check for
	// server-owned automatic title generation. Nil and false keep the durable
	// lifecycle disabled; true persists pending at session creation. It receives
	// only the neutral fixed session selector, never registry or credential access.
	TitleGenerationEligible func(ProviderSelector) bool
	// TitleGenerator is the optional Build-owned, server-private title call. When
	// absent automatic work is unavailable; it never receives session authority.
	TitleGenerator SessionTitleGenerator
	// TitleGeneratorForSession resolves a title generator for the session's fixed
	// provider selector. It takes precedence over TitleGenerator when present.
	TitleGeneratorForSession func(ProviderSelector) SessionTitleGenerator

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
	// SessionEngineWithTools is required when MCPBroker is wired. It receives the
	// exact wrappers owned by the local broker attachment.
	SessionEngineWithTools SessionEngineWithToolsFactory
	// MCPBroker owns logical broker state; Service owns only local attachments.
	MCPBroker brokercontract.Service
	// MCPConnectorInspector exposes local-only inventory for the bundled broker.
	MCPConnectorInspector brokercontract.ConnectorInspector
	// WorkspaceEnrollment advertises the optional pre-prompt enrollment capability.
	// It must be true only when MCPBroker attachments implement the enrollment boundary.
	WorkspaceEnrollment bool

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
	SharedBaseWorkspace func(tool.Workspace) tool.Workspace
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

	// EventLog durably records the relay-side event projection per session
	// (cloud-native Phase 3a). Relays observe every event, including the
	// drain-to-discard tail, while their run-scoped recorders coalesce streaming
	// text deltas before Append; the loop itself stays storage-agnostic (it only
	// emits). Optional and nil-safe: when nil the relay records nothing
	// (byte-identical to the pre-3a behaviour). The composition root wires the
	// durable jsonlstore Store (which also implements EventLog) or an in-memory
	// sibling when no store dir is configured.
	EventLog port.EventLog

	// Diagnostics is the operational logging sink the relay uses to WARN once per
	// recorder after EventLog.Append failures (a best-effort durable log must not
	// break the live stream). Optional and nil-safe: when nil, Append failures are
	// silently tolerated (the durability gap is the only effect). The composition
	// root supplies the same sink the rest of the build uses.
	Diagnostics port.Diagnostics

	// SessionLoadFailureMetric records one bounded class for each non-not-found
	// GetSession load failure under ownership enforcement. It receives no target,
	// principal, locator, cause, blob, or size. Optional and nil-safe; diagnostics
	// remain enabled when this callback is nil.
	SessionLoadFailureMetric func(port.SessionLoadFailureClass)

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

	// MutationCapability is the process-local admission gate shared by Service
	// and the engine's guarded persistence/recorder adapters. Composition supplies
	// one instance to both. When nil, Service creates the matching local gate;
	// no-lease and unsupported-lease paths remain pass-through.
	MutationCapability *SessionMutationCapability

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

	// placementBinder is the sole creation/successor placement binding seam.
	// It is nil only for legacy hand-built configurations that have not migrated.
	placementBinder *PlacementBinder

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
	// teamsReserving counts CreateTeam calls that have passed the MaxTeams check
	// but have not yet registered. Enrolment acquires member leases and publishes
	// durable member snapshots, so the cap must be claimed BEFORE that work: a
	// capacity refusal afterwards would strand both. Guarded by mu, like teams.
	teamsReserving int
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
	// brokerAttachments are process-local handles. Closing one never deletes the
	// broker's logical session state. brokerMu serializes attach/build/commit,
	// local detach, and permanent logical deletion for one canonical session ID;
	// it is separate from runEntryMu because engine rebuilds may already hold that
	// run-entry lock.
	brokerAttachments map[session.SessionID]brokercontract.Attachment
	brokerMu          keyedMutex
	// authorizationExpiry is Service-owned and guarded by mu.
	authorizationExpiry map[session.SessionID]*authorizationExpiry
	// beforeAuthorizationContinuationStart is an inert test synchronization seam.
	// It is configured before serving and runs while the continuation handoff lock
	// is held, immediately before cancellation is disarmed.
	beforeAuthorizationContinuationStart func()
	// steerPromotionRegistered is an inert test synchronization seam. It runs
	// after a promoted steer has registered its replacement run.
	steerPromotionRegistered func()
	closed                   bool
	shutdownComplete         bool
	closeMu                  sync.Mutex
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

	// clientMCPSpecs holds the ORIGINAL client-supplied MCP server specs for a
	// session's lifetime, guarded by mu. The Service never threaded these through
	// a session-engine REBUILD (mode change, ADR-0310 full enrollment freeze, or a
	// broker grant refresh) — callSessionEngine's specs argument was hardcoded nil
	// there — so a rebuild silently dropped every client-provided MCP tool. This
	// map closes that gap: written once at session creation (createPerSessionEngine)
	// and on an ACP client's session/load (LoadSessionWithMCP), read by
	// buildAndRegisterSessionEngineWithBrokerTools in place of nil. It is absent
	// for a session with no client MCP (the common case), present only entries
	// deleted on session removal (CloseSession, a failed create's rollback) or
	// full shutdown — never on a mere rebuild-attempt rollback, since the specs
	// remain valid for the session's surviving prior engine.
	clientMCPSpecs map[session.SessionID][]mcp.ServerConfig

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

	// runEntryGenerations fences requests that were admitted before a Clear crossed
	// its cancellation boundary. Callers snapshot the generation before waiting on
	// per-session coordination and validate it after acquiring runEntryMu; Clear
	// advances it while holding runEntryMu and s.mu. Entries intentionally survive
	// source settlement so a later request for the same id can snapshot the new
	// generation and proceed while already-queued requests remain retired.
	// Guarded by s.mu.
	runEntryGenerations map[session.SessionID]uint64

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
	// lostOwnership remembers that this Service definitively lost an id even after
	// the heavyweight heldLease/capability tombstones are removed at lifecycle
	// settlement. Cleared by explicit CloseSession teardown, and — the ONE other
	// caller, narrowly scoped — by acquireLeaseCore's bypassTombstone=true path
	// (used only via acquireMutationLeaseForStaleSettle, i.e. SettleIfStale/the
	// stale-session reconcile sweep) on a genuine re-Acquire success. Every other
	// caller (acquireLease/reaffirmLease with bypassTombstone=false) still fails
	// fast on it forever.
	lostOwnership map[session.SessionID]struct{}

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

	// titleCoordinator owns bounded asynchronous title work outside chat runs.
	// It is nil when title generation is unavailable.
	titleCoordinator *titleCoordinator

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

	// cleanupTokenKey signs opaque caller/scope/generation-bound confirmation
	// handles; cleanupPlans and cleanupJobs retain bounded payloads/projections for
	// apply and management inspection during this process lifetime. Guarded by s.mu.
	cleanupTokenKey [32]byte
	cleanupPlans    map[string]cleanupTokenPayload
	cleanupJobs     map[string]cleanupJobRecord

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

	// watches is the per-session DURABLE watch registry (ADR 0250): every
	// attached WatchSessionEvents reader, so a failed durable append can terminate
	// the ones in THIS process with a delivery gap (decision 6's guaranteed tier)
	// and shutdown can stop them all.
	//
	// It is deliberately NOT the subscriptions registry above, and the difference
	// is the whole feature. A subscription is an in-memory fan-out that DROPS for
	// a slow client; a watch reads the durable log itself, so it is
	// cross-process-capable, resumable from a cursor, and terminated — never
	// silently dropped — when it falls behind. The Service holds only what it
	// needs to stop a watcher and to tell it why: a watch's position, filter, and
	// buffer live with its own goroutine, so a failing appender's walk of this map
	// stays cheap on the relay thread.
	//
	// Guarded by watchMu, a THIRD mutex for the same reason subMu is a second one:
	// appendEvent walks this map on the relay thread and must not contend with the
	// run/registry hot path. watchesClosed permanently rejects new attachments
	// after shutdown begins, so no pump goroutine outlives the Service.
	watchMu       sync.Mutex
	watches       map[session.SessionID]map[*watchRegistration]struct{}
	watchesClosed bool

	// cursorLog is cfg.EventLog narrowed to the cursor seam, or nil when the
	// configured backend does not implement it.
	//
	// Resolved ONCE at construction rather than type-asserted per call: the backend
	// cannot change at runtime, and appendEvent is on the relay hot path (every
	// coalesced delta chunk of every turn goes through it). A nil here is the
	// honest "this deployment cannot serve a watch" signal both appendEvent and
	// watchLog read.
	cursorLog port.CursorEventLog
}

// heldLease is one process-held session lease plus the cancel that stops its
// renewer goroutine. The lease VALUE is refreshed in place by the renewer (under
// s.mu) so the latest token/expiry is what a Release sends.
type heldLease struct {
	lease  port.Lease
	ctx    context.Context
	cancel context.CancelFunc
	valid  bool
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
	run  *agent.Run
	sess *session.Session
	// cancelling is guarded by Service.mu. Clear marks the exact registered
	// lifecycle before signalling cancellation so no approval, steer, or admission
	// promotion can restart work across the irreversible clear boundary.
	cancelling bool
	// resumeAdmission distinguishes the provisional lifecycle installed by
	// resumeFromAwaiting from ordinary prompt/retry admission. A concurrent approval
	// must wait for the former under resumeMu instead of treating its nil run as a
	// terminal registry entry.
	resumeAdmission bool
	// admissionCancel is non-nil until the provisional run-entry has atomically
	// promoted to a real agent.Run. Drain and lease loss cancel it before any
	// provider/tool work can start.
	admissionCancel context.CancelFunc
	// runContextStop releases the lease-linked launch context after the promoted
	// run has settled. It must outlive the run-entry call itself.
	runContextStop context.CancelFunc
	awaiting       atomic.Bool
	// titleRevision is the last title metadata revision successfully persisted and
	// published for this run. It starts from the admitted durable snapshot so
	// prompt-ingress changes publish only after their save succeeds.
	titleRevision uint64
	// persistMu makes admission of the durable awaiting save and drain's
	// awaiting/non-awaiting decision one lifecycle transaction. Live approval
	// and cancellation signals cross the same barrier: they either wait for an
	// admitted awaiting save, or mark the pending ask stale before waking the
	// engine so a delayed relay never snapshots a concurrently-resuming session.
	// Backend calls admitted before invalidation may still complete.
	persistMu sync.Mutex
	// resolvedAskID and cancelSignaled are guarded by persistMu. They close the
	// event-delivery race where a control reaches a detached/background run after
	// the engine emitted permission.ask but before its relay starts Persist.
	resolvedAskID  string
	cancelSignaled bool
	// preserveDurable prevents a shutdown-cancelled local awaiting run from
	// overwriting the already-durable PendingAsk handoff point.
	preserveDurable atomic.Bool
	settled         chan struct{}
	settledOnce     sync.Once
	// removeCapabilityOnSettle retains denial when normal teardown releases a
	// lease while stale run references are still unwinding.
	removeCapabilityOnSettle bool
}

func (st *runState) approvalRun() *agent.Run {
	if st.cancelling {
		return nil
	}
	return st.run
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

// normalizeServerImplementation admits only a stable, non-identifying composition
// family token for GetServerInfo. All other input is intentionally indistinguishable.
func normalizeServerImplementation(value string) string {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return "unknown"
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		return "unknown"
	}
	return value
}

// NewService validates cfg and constructs a Service using a background startup
// context. Composition roots with a lifecycle context should call
// NewServiceContext.
func NewService(cfg Config) (*Service, error) {
	return NewServiceContext(context.Background(), cfg)
}

// NewServiceContext validates cfg and constructs a Service. ctx bounds and
// propagates trusted startup context to configured placement providers.
func NewServiceContext(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("%w: Engine is required", ErrConfig)
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: Store is required", ErrConfig)
	}
	placementBinder, err := configuredPlacementBinder(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cfg.ServerImplementation = normalizeServerImplementation(cfg.ServerImplementation)
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = session.ModeDefault
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AuthorizationTimer == nil {
		cfg.AuthorizationTimer = func(delay time.Duration, f func()) AuthorizationTimer { return time.AfterFunc(delay, f) }
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
	if cfg.MutationCapability == nil {
		cfg.MutationCapability = NewSessionMutationCapability(cfg.SessionLease != nil)
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
	var cleanupTokenKey [32]byte
	if _, err := rand.Read(cleanupTokenKey[:]); err != nil {
		return nil, fmt.Errorf("server: initialize cleanup token signer: %w", err)
	}
	_, shutdownCancel := context.WithCancel(context.Background())
	svc := &Service{
		cfg:                 cfg,
		placementBinder:     placementBinder,
		shutdownCancel:      shutdownCancel,
		runs:                make(map[session.SessionID]*runState),
		teams:               make(map[string]*teamState),
		sessionEngines:      make(map[session.SessionID]*sessionEngine),
		brokerAttachments:   make(map[session.SessionID]brokercontract.Attachment),
		authorizationExpiry: make(map[session.SessionID]*authorizationExpiry),
		sessionEnvironments: make(map[session.SessionID]tool.Environment),
		clientMCPSpecs:      make(map[session.SessionID][]mcp.ServerConfig),
		reservedIDs:         make(map[session.SessionID]struct{}),
		runEntryGenerations: make(map[session.SessionID]uint64),
		replayedApprovals:   make(map[session.SessionID]struct{}),
		heldLeases:          make(map[session.SessionID]*heldLease),
		lostOwnership:       make(map[session.SessionID]struct{}),
		cleanupTokenKey:     cleanupTokenKey,
		cleanupPlans:        make(map[string]cleanupTokenPayload),
		cleanupJobs:         make(map[string]cleanupJobRecord),
		subscriptions:       make(map[session.SessionID]map[int64]chan session.Event),
	}
	svc.titleCoordinator = buildTitleCoordinator(svc, cfg)
	// Narrow the durable log to the cursor seam once (ADR 0250). A backend that
	// does not implement it leaves this nil, and the watch surface reports the
	// feature unsupported rather than degrading to a full replay.
	if cfg.EventLog != nil {
		if cl, ok := cfg.EventLog.(port.CursorEventLog); ok {
			svc.cursorLog = cl
		}
	}
	// Seed the model inventory from the static snapshot. ListModels and the
	// ModelSelection cap read this atomic so a later live-catalog SetModels swap is
	// race-free. A nil cfg.Models seeds an empty (non-nil) slice so the pointer is
	// never nil.
	seed := seedModelInventory(cfg)
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
	svc.wireScheduleManager(cfg)
	return svc, nil
}

func (s *Service) placementDiscoveryAvailable() bool {
	_, ok := s.cfg.PlacementProvider.(PlacementDiscoverer)
	return ok
}

// BindPlacement atomically authorizes and resolves a placement through the
// deployment provider. The caller principal comes only from the authenticated
// context and the scope only from trusted composition; neither is supplied by
// the selector or inferred from possession of its opaque ID.
func (s *Service) BindPlacement(ctx context.Context, selector PlacementSelector, operation PlacementOperation) (PlacementBinding, error) {
	if s == nil || s.placementBinder == nil {
		return PlacementBinding{}, fmt.Errorf("%w: no PlacementProvider is configured", ErrConfig)
	}
	binding, err := s.placementBinder.Bind(ctx, PlacementBindRequest{
		Selector:  selector,
		Principal: session.PrincipalFromContext(ctx),
		Scope:     s.cfg.PlacementScope,
		Operation: operation,
	})
	if err != nil {
		s.logPlacementProviderError(ctx, "bind", err)
	}
	return binding, err
}

// ReattachPlacement authorizes and resolves the exact persisted environment
// identity. It never invokes Bind and therefore cannot follow a changed default.
func (s *Service) ReattachPlacement(ctx context.Context, ref session.EnvironmentRef) (PlacementBinding, error) {
	if s == nil || s.placementBinder == nil {
		return PlacementBinding{}, fmt.Errorf("%w: no PlacementProvider is configured", ErrFailedPrecondition)
	}
	binding, err := s.placementBinder.Reattach(ctx, PlacementReattachRequest{
		Ref: ref, Principal: session.PrincipalFromContext(ctx), Scope: s.cfg.PlacementScope,
	})
	if err != nil {
		s.logPlacementProviderError(ctx, "reattach", err)
		return PlacementBinding{}, err
	}
	return binding, nil
}

func (s *Service) schedulePlacementScope() PlacementScope {
	return s.cfg.PlacementScope
}

// ReattachPlacementInScope requires the durable schedule scope to match this
// deployment before reauthorizing and resolving the exact persisted ref.
func (s *Service) ReattachPlacementInScope(ctx context.Context, ref session.EnvironmentRef, scope string) (PlacementBinding, error) {
	if s == nil || scope == "" || PlacementScope(scope) != s.schedulePlacementScope() {
		return PlacementBinding{}, fmt.Errorf("%w: scheduled placement scope changed", ErrFailedPrecondition)
	}
	return s.ReattachPlacement(ctx, ref)
}

// wireScheduleManager attaches the post-construction seams the schedule manager
// can only receive once the Service exists. Both are no-ops without a manager.
func (s *Service) wireScheduleManager(cfg Config) {
	if s.schedMgr == nil {
		return
	}
	if cfg.Scheduler != nil {
		s.schedMgr.SetScheduler(cfg.Scheduler)
	}
	// Exact placement is private durable state. Install the resolver on every
	// schedule-capable Service; it binds the deployment default for out-of-band
	// creates and reauthorizes an invoking session's exact ref.
	s.schedMgr.setPlacementForCreate(s.resolveSchedulePlacement)
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
	if s.cfg.ModelInventory != nil {
		s.cfg.ModelInventory.SetModels(models)
		models = s.cfg.ModelInventory.CurrentModels()
	}
	s.models.Store(&models)
}

// currentModels returns the live model inventory pointer's contents (never nil
// after NewService). It is the single internal read used by ListModels and the
// ModelSelection capability so they cannot disagree.
func (s *Service) currentModels() []*mecatlv1.ModelInfo {
	if s.cfg.ModelInventory != nil {
		return s.cfg.ModelInventory.CurrentModels()
	}
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
	// debugTargetID binds a separate debug session to one authorized target.
	debugTargetID          session.SessionID
	debugTargetIncarnation session.IncarnationID
	debugMCPServers        []string
	// clientMCP are the client-provided MCP servers to mount for this session,
	// already classified AND policy-checked by Service.ClientMCPFromWire. Empty is
	// the byte-identical shared-engine path.
	clientMCP []mcp.ServerConfig
	// clientMCPStrict requires every server in clientMCP to actually connect, or
	// the create fails with ErrClientMCPUnreachable. Set only by WithClientMCP,
	// which is the wire path's only entry — the ACP path keeps composition's
	// best-effort mount. See verifyClientMCPMounted.
	clientMCPStrict bool
	// placement is a trusted, already-reauthorized exact binding supplied only by
	// server composition (scheduled fire). It bypasses default placement binding.
	placement *PlacementBinding
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

// WithPlacementBinding supplies a trusted exact binding already reauthorized by
// composition. It is intended for scheduled fire only; public transports cannot
// construct or select it.
func WithPlacementBinding(binding PlacementBinding) CreateSessionOption {
	return func(o *createSessionOpts) { o.placement = &binding }
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

// WithDebugTarget creates a dedicated debug session bound to id. The target is
// authorized through the ordinary absence-shaped ownership seam and is only
// loaded for validation; its state and conversation are never changed or copied.
func WithDebugTarget(id session.SessionID) CreateSessionOption {
	return func(o *createSessionOpts) { o.debugTargetID = id }
}

// WithDebugMCP selects already-configured server-global MCP servers for a debug
// session. Names are validated at the create boundary and no connection details
// cross this seam.
func WithDebugMCP(names []string) CreateSessionOption {
	return func(o *createSessionOpts) { o.debugMCPServers = append([]string(nil), names...) }
}

// ClientMCPGrant is a DECIDED client-MCP result: specs that have passed the shared
// classifier AND this deployment's policy gate. Only Service.ClientMCPFromWire
// mints a non-empty one, because specs is unexported — so the invariant is carried
// by the TYPE rather than by a doc comment asking callers to behave.
//
// That matters because WithClientMCP and the CreateSession* entries are all
// exported: before this, any in-process caller (the scheduler, a future
// composition root, a later refactor of the ACP adapter) could construct the
// option from raw specs and bypass both the classifier and the gate. A
// ClientMCPGrant{} built outside this package is EMPTY, which is inert — the
// worst a bypass attempt achieves is a session with no client MCP, never an
// unvalidated mount.
type ClientMCPGrant struct {
	specs []mcp.ServerConfig
}

// IsEmpty reports whether the grant carries no servers — either because the
// request declared none, or because it is a zero value built outside this package.
func (g ClientMCPGrant) IsEmpty() bool { return len(g.specs) == 0 }

// WithClientMCP mounts client-provided streaming-HTTP MCP servers for the new
// session's lifetime, via a per-session engine (which requires
// Config.SessionEngine, else ErrInvalidArgument).
//
// It takes a ClientMCPGrant, not raw specs: the grant is unforgeable outside this
// package, so "validated and policy-checked" is a type-level fact rather than a
// convention. This option does not validate and must not — one classifier, one
// gate, at the wire seam.
//
// It also arms the ALL-OR-NOTHING mount requirement (clientMCPStrict): every
// requested server must actually connect or the create fails. That is the wire
// contract, and this option is the wire's only entry, so the two travel
// together rather than as a separate flag a handler could forget. The ACP path
// (CreateSessionWithMCP) deliberately does not come through here and keeps
// composition's best-effort behaviour.
//
// An EMPTY grant is a no-op: it arms nothing, so a caller that passes a zero
// value gets the ordinary shared-engine create rather than a strict-mode session
// with nothing to mount.
func WithClientMCP(grant ClientMCPGrant) CreateSessionOption {
	return func(o *createSessionOpts) {
		if grant.IsEmpty() {
			return
		}
		o.clientMCP = append([]mcp.ServerConfig(nil), grant.specs...)
		o.clientMCPStrict = true
	}
}

func debugMCPNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

func validateDebugMCPNames(target session.SessionID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if target == "" {
		return fmt.Errorf("%w: debug MCP servers are legal only with a debug target", ErrInvalidArgument)
	}
	if len(names) > 16 {
		return fmt.Errorf("%w: at most 16 debug MCP servers may be selected", ErrInvalidArgument)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if len(name) == 0 || len(name) > 64 || strings.Contains(name, "__") {
			return fmt.Errorf("%w: invalid debug MCP server name %q", ErrInvalidArgument, name)
		}
		for _, r := range name {
			if !debugMCPNameRune(r) {
				return fmt.Errorf("%w: invalid debug MCP server name %q", ErrInvalidArgument, name)
			}
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate debug MCP server %q", ErrInvalidArgument, name)
		}
		seen[name] = struct{}{}
	}
	return nil
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

func newCreatedSession(id session.SessionID, mode session.PermissionMode, ref session.EnvironmentRef, limits session.Limits, createdAt time.Time, opts createSessionOpts) (*session.Session, error) {
	switch {
	case opts.debugTargetID != "":
		return session.NewDebug(id, mode, ref, limits, createdAt, opts.debugTargetID, opts.debugTargetIncarnation)
	case opts.scheduled != nil:
		return session.NewScheduled(id, mode, ref, limits, createdAt, opts.scheduled.ScheduleName, opts.scheduled.OriginSessionID, opts.scheduled.OriginIncarnation)
	default:
		return session.New(id, mode, ref, limits, createdAt), nil
	}
}

// CreateSession allocates a new idle session on the server-owned default placement,
// persists it, and returns it. An unspecified mode falls back to DefaultMode. It
// is the no-selector, no-MCP fast path.
func (s *Service) CreateSession(ctx context.Context, mode session.PermissionMode, limits session.Limits) (*session.Session, error) {
	return s.createSession(ctx, mode, limits, ProviderSelector{}, nil, ProfileDefault, createSessionOpts{})
}

// CreateSessionWithProvider creates a session bound to a non-default
// provider/model selector (multi-provider Phase 0, S3) via a PER-SESSION engine,
// with no client MCP and the DEFAULT profile.
func (s *Service) CreateSessionWithProvider(ctx context.Context, mode session.PermissionMode, limits session.Limits, sel ProviderSelector) (*session.Session, error) {
	return s.CreateSessionWithProfile(ctx, mode, limits, sel, ProfileDefault)
}

// CreateSessionWithProfile creates a session bound to an optional non-default
// provider/model selector AND a tool-surface profile (issue #55), with no client
// MCP. It is the gRPC/HTTP entry for a CreateSession request. The zero selector
// + default profile delegates to the shared-engine fast path; a non-zero
// selector OR the no-fs profile REQUIRES Config.SessionEngine (else
// ErrInvalidArgument) and resolves through the factory (an unknown/unavailable
// provider id surfaces as ErrInvalidArgument). Setting ModelID with an empty
// ProviderID is rejected (a bare model on the env-derived default provider is
// ambiguous). Placement is bound exclusively from the server-owned default or
// explicit no-FS profile.
//
// opts is the variadic options pattern (CreateSessionOption): WithSessionID
// overrides the minted id (ADR 0059 decision #7 Phase-2 — the scheduler fire
// path mints a "sched--"-prefixed id). Zero opts is byte-identical to the
// pre-Phase-2 signature.
func (s *Service) CreateSessionWithProfile(ctx context.Context, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, profile SessionProfile, opts ...CreateSessionOption) (*session.Session, error) {
	if sel.ProviderID == "" && sel.ModelID != "" {
		return nil, fmt.Errorf("%w: model_id requires provider_id (a bare model on the default provider is ambiguous)", ErrInvalidArgument)
	}
	var o createSessionOpts
	for _, opt := range opts {
		opt(&o)
	}
	return s.createSessionWithOptions(ctx, mode, limits, sel, profile, o)
}

func (s *Service) createSessionWithOptions(ctx context.Context, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, profile SessionProfile, opts createSessionOpts) (*session.Session, error) {
	if err := validateDebugMCPNames(opts.debugTargetID, opts.debugMCPServers); err != nil {
		return nil, err
	}
	return s.createSession(ctx, mode, limits, sel, opts.clientMCP, profile, opts)
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
// The placement binder returns the complete environment for either the
// server-owned default or explicit no-FS attenuation. A no-FS session registers
// that complete environment as its per-session OVERRIDE at create time (the
// ACP-buffer-workspace mechanism, under the same lock as engine registration),
// so run entry never constructs a filesystem workspace for it.
// setSessionLabels records the neutral provider+model selector and the
// tool-surface profile onto the freshly-created aggregate as write-once creation
// labels. The aggregate stores them opaquely (it never interprets the
// ProviderSelector type, which stays a server-adapter type); persisting them is
// what lets rehydrateSession rebuild the SAME engine after a restart. For the
// empty-selector default profile this writes the zero values, so a default
// session's snapshot is byte-identical to a pre-Phase-1 one (the labels omitempty
// out of the JSON).
func setSessionLabels(sess *session.Session, sel ProviderSelector, profile SessionProfile, owner *session.Principal, authority session.Authority) error {
	sess.Profile = string(profile)
	sess.ProviderID = sel.ProviderID
	sess.ModelID = sel.ModelID
	sess.ReasoningEffort = sel.ReasoningEffort
	// Owner and authority are independently stamped at the same root seam.
	return sess.RestoreLabels(owner, authority)
}

func (s *Service) setTitleGenerationEligibility(sess *session.Session, sel ProviderSelector) {
	if s.cfg.TitleGenerationEligible != nil && s.cfg.TitleGenerationEligible(sel) {
		sess.SetTitleGeneration(session.TitleGenerationPending)
	}
}

func (s *Service) setPerSessionLabels(sess *session.Session, sel ProviderSelector, profile SessionProfile, owner *session.Principal, opts createSessionOpts, res SessionEngineResult, broker []tool.Tool, carried session.Authority, carriedBound bool) error {
	authority := s.rootAuthority(sess.Kind, carried, carriedBound)
	// Broker wrappers are created only after the process root authority was
	// minted. Include this session's exact wrappers in a fresh root without
	// widening authority carried from another session.
	if !carriedBound && len(broker) != 0 {
		seen := make(map[string]struct{}, len(authority.CapabilitySet.Tools)+len(broker))
		for _, name := range authority.CapabilitySet.Tools {
			seen[name] = struct{}{}
		}
		for _, candidate := range broker {
			name := candidate.Spec().Name
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			authority.CapabilitySet.Tools = append(authority.CapabilitySet.Tools, name)
		}
		sort.Strings(authority.CapabilitySet.Tools)
	}
	if sess.Kind == session.SessionKindDebug {
		authority.CapabilitySet.Tools = append(authority.CapabilitySet.Tools, res.DebugMCPTools...)
		sess.DebugMCPServers = append([]string(nil), opts.debugMCPServers...)
		sess.DebugMCPTools = append([]string(nil), res.DebugMCPTools...)
	}
	return setSessionLabels(sess, sel, profile, owner, authority)
}

func (s *Service) rootAuthority(kind session.SessionKind, carried session.Authority, carriedBound bool) session.Authority {
	if carriedBound {
		return carried
	}
	if s.cfg.RootAuthority == nil {
		return session.Authority{}
	}
	return s.cfg.RootAuthority(kind)
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
	environmentRef session.EnvironmentRef
	mode           session.PermissionMode
	limits         session.Limits
	selector       ProviderSelector
	profile        SessionProfile
	sourceID       session.SessionID
	kind           session.SessionKind
	relationship   session.SessionRelationship
}

func newCreateRequest(ref session.EnvironmentRef, mode session.PermissionMode, limits session.Limits, selector ProviderSelector, profile SessionProfile, sourceID session.SessionID, opts createSessionOpts) createRequest {
	request := createRequest{environmentRef: ref, mode: mode, limits: limits, selector: selector, profile: profile, sourceID: sourceID, kind: session.SessionKindMain}
	switch {
	case opts.debugTargetID != "":
		request.kind = session.SessionKindDebug
		request.relationship = session.SessionRelationship{DebugTargetID: opts.debugTargetID, DebugTargetIncarnation: opts.debugTargetIncarnation}
	case opts.scheduled != nil:
		request.kind = session.SessionKindScheduled
		request.relationship = *opts.scheduled
	}
	return request
}

func (r createRequest) matches(sess *session.Session) bool {
	return r.sourceID == "" && sess.EnvironmentRef == r.environmentRef && sess.Mode == r.mode &&
		sess.Limits == r.limits && sess.ProviderID == r.selector.ProviderID &&
		sess.ModelID == r.selector.ModelID && sess.ReasoningEffort == r.selector.ReasoningEffort &&
		sess.Profile == string(r.profile) && sess.Kind == r.kind && sess.Relationship == r.relationship
}

func sameCreateOwner(a, b *session.Principal) bool {
	return (a == nil && b == nil) || a.SameIdentity(b)
}

func (s *Service) classifyCreateWinner(existing *session.Session, owner *session.Principal, request createRequest) (*session.Session, error) {
	if !s.cfg.OwnershipEnforced {
		return nil, fmt.Errorf("%w: session id %q already exists", ErrInvalidArgument, existing.ID)
	}
	if !sameCreateOwner(existing.Owner, owner) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, existing.ID)
	}
	if !request.matches(existing) {
		return nil, fmt.Errorf("%w: session id %q was retried with a different request", ErrInvalidArgument, existing.ID)
	}
	return existing, nil
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
	// means the id is clear; any other infrastructure fault fails closed and is
	// exposed only through a content-free public category.
	if existing, lerr := s.cfg.Store.Load(ctx, id); lerr == nil && existing != nil {
		release()
		winner, classifyErr := s.classifyCreateWinner(existing, owner, request)
		return winner, nil, classifyErr
	} else if lerr != nil && !errors.Is(lerr, port.ErrSessionNotFound) {
		release()
		s.logDiscoveryError(ctx, "probe session placement", lerr)
		return nil, nil, fmt.Errorf("%w: placement storage failed", ErrInternal)
	}
	return nil, release, nil
}

func (s *Service) persistNewSession(ctx context.Context, sess *session.Session) error {
	if creator, ok := s.cfg.Store.(port.SessionCreator); ok {
		return creator.Create(ctx, sess)
	}
	if s.cfg.OwnershipEnforced {
		return fmt.Errorf("%w: ownership enforcement requires a session store with atomic create capability", ErrConfig)
	}
	return s.saveSession(ctx, sess)
}

func (s *Service) persistCreatedSession(ctx context.Context, sess *session.Session, owner *session.Principal, request *createRequest) (*session.Session, error) {
	if err := s.persistNewSession(ctx, sess); err != nil {
		if existing, ok, collisionErr := s.resolveCreateCollision(ctx, sess.ID, owner, request, err); ok || collisionErr != nil {
			return existing, collisionErr
		}
		if errors.Is(err, port.ErrSessionAlreadyExists) {
			return nil, fmt.Errorf("%w", port.ErrSessionAlreadyExists)
		}
		if errors.Is(err, ErrConfig) {
			return nil, err
		}
		s.logDiscoveryError(ctx, "persist session placement", err)
		return nil, fmt.Errorf("%w: placement storage failed", ErrInternal)
	}
	if sess.TitleGeneration != session.TitleGenerationDisabled {
		s.publishTitle(context.WithoutCancel(ctx), sess)
	}
	return sess, nil
}

func (s *Service) resolveCreateCollision(ctx context.Context, id session.SessionID, owner *session.Principal, request *createRequest, createErr error) (*session.Session, bool, error) {
	if !errors.Is(createErr, port.ErrSessionAlreadyExists) || request == nil {
		return nil, false, nil
	}
	existing, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		s.logDiscoveryError(ctx, "load session placement collision", err)
		return nil, false, fmt.Errorf("%w: placement storage failed", ErrInternal)
	}
	winner, err := s.classifyCreateWinner(existing, owner, *request)
	return winner, err == nil, err
}

func (s *Service) authorizeDebugTarget(ctx context.Context, id session.SessionID) error {
	target, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return fmt.Errorf("%w", ErrNotFound)
		}
		return fmt.Errorf("server: load debug target: %w", err)
	}
	if target == nil || target.ID != id || s.authorizeSession(ctx, target) != nil {
		return fmt.Errorf("%w", ErrNotFound)
	}
	return nil
}

func (s *Service) validateDebugCreate(ctx context.Context, profile SessionProfile, specs []mcp.ServerConfig, opts createSessionOpts) error {
	if opts.debugTargetID == "" {
		return nil
	}
	if profile != ProfileNoFS {
		return fmt.Errorf("%w: debug sessions require profile %q", ErrInvalidArgument, ProfileNoFS)
	}
	if opts.sourceSessionID != "" || opts.scheduled != nil || len(specs) > 0 {
		return fmt.Errorf("%w: debug target cannot be combined with source, scheduled, or client MCP relationships", ErrInvalidArgument)
	}
	if opts.idSet && opts.id == opts.debugTargetID {
		return fmt.Errorf("%w: debug session must be separate from its target", ErrInvalidArgument)
	}
	return s.authorizeDebugTarget(ctx, opts.debugTargetID)
}

func (s *Service) bindRelatedIncarnations(ctx context.Context, opts *createSessionOpts) error {
	if opts.scheduled != nil && opts.scheduled.OriginSessionID != "" {
		origin, err := s.cfg.Store.Load(ctx, opts.scheduled.OriginSessionID)
		if err != nil {
			return fmt.Errorf("%w: scheduled origin is unavailable", ErrNotFound)
		}
		opts.scheduled.OriginIncarnation = origin.Incarnation()
	}
	if opts.debugTargetID != "" {
		target, err := s.cfg.Store.Load(ctx, opts.debugTargetID)
		if err != nil || target == nil || s.authorizeSession(ctx, target) != nil {
			return fmt.Errorf("%w: debug target is unavailable", ErrNotFound)
		}
		opts.debugTargetIncarnation = target.Incarnation()
	}
	return nil
}

//nolint:gocyclo // Creation intentionally keeps placement, ownership, limits, engine selection, and persistence in one transaction.
func (s *Service) createSession(ctx context.Context, mode session.PermissionMode, limits session.Limits, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, opts createSessionOpts) (*session.Session, error) {
	if profile != ProfileDefault && profile != ProfileNoFS {
		return nil, fmt.Errorf("%w: unknown session profile %q", ErrInvalidArgument, profile)
	}
	if err := s.validateDebugCreate(ctx, profile, specs, opts); err != nil {
		return nil, err
	}
	var (
		err       error
		workspace string
	)
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
	var placement *PlacementBinding
	if opts.placement != nil {
		if err := validatePlacementBinding(*opts.placement); err != nil {
			return nil, err
		}
		placement = opts.placement
		workspace = placement.Environment.Workspace().Root()
		if profile == ProfileNoFS && workspace != "" || profile != ProfileNoFS && workspace == "" {
			return nil, ErrInvalidPlacementBinding
		}
	} else {
		workspace, placement, err = s.bindPlacementForCreate(ctx, profile, owner)
		if err != nil {
			return nil, err
		}
		if placement != nil && placement.Ref.Kind == session.EnvKindNoFS {
			profile = ProfileNoFS
		}
	}
	if placement != nil && placement.Close != nil {
		defer func() { _ = placement.Close() }()
	}
	if err := s.bindRelatedIncarnations(ctx, &opts); err != nil {
		return nil, err
	}

	// Resolve the session id: the caller's override (WithSessionID, ADR 0059
	// decision #7 Phase-2) wins; otherwise the Service's NewID generator mints a
	// fresh one (the byte-identical pre-Phase-2 path). WithSessionID with an EMPTY
	// id is rejected (the doc promises it), distinguished from "never called" by
	// idSet. A caller-chosen id is validated + reserved by reserveSessionID (see
	// its doc for the three collision sources); the reservation is released on
	// EVERY exit path.
	var retryRequest *createRequest
	mintID := s.cfg.NewID
	if opts.idSet {
		if opts.id == "" {
			return nil, fmt.Errorf("%w: session id must not be empty", ErrInvalidArgument)
		}
		request := newCreateRequest(placement.Ref, mode, limits, sel, profile, opts.sourceSessionID, opts)
		retryRequest = &request
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
	// A broker attachment is keyed by the canonical persisted identity. Mint and
	// reserve generated IDs before any attachment or catalogue construction.
	if s.cfg.MCPBroker != nil && !opts.idSet {
		id := mintID()
		request := newCreateRequest(placement.Ref, mode, limits, sel, profile, opts.sourceSessionID, opts)
		// Populate the outer retryRequest too (not just the local var used for
		// reserveSessionID above): persistCreatedSession's collision-retry path
		// (resolveCreateCollision) needs a non-nil *createRequest to classify an
		// idempotent-retry winner on this generated-id branch, exactly as the
		// opts.idSet branch above already does. Before this fix, retryRequest
		// stayed nil here (the "request" identifier above is a fresh local, not
		// the outer var), so resolveCreateCollision's request==nil guard always
		// short-circuited and a genuine ErrSessionAlreadyExists from persistNewSession
		// always hard-failed instead of resolving to the existing winner.
		retryRequest = &request
		existing, release, reserveErr := s.reserveSessionID(ctx, id, owner, request)
		if reserveErr != nil {
			return nil, reserveErr
		}
		if existing != nil {
			release()
			return nil, fmt.Errorf("%w: generated session id %q already exists", ErrInvalidArgument, id)
		}
		defer release()
		mintID = func() session.SessionID { return id }
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
	var carriedAuthority session.Authority
	carriedAuthorityBound := false
	if opts.sourceSessionID != "" {
		snap, srcOwner, sourceAuthority, sourceAuthorityBound, err := s.validateCarryover(ctx, opts.sourceSessionID, sel.ProviderID)
		if err != nil {
			return nil, err
		}
		carrySnap = snap
		carriedAuthority, carriedAuthorityBound = sourceAuthority, sourceAuthorityBound
		// A fork inherits the SOURCE's owner, overriding the context principal
		// (and any WithOwner) — see validateCarryover.
		owner = srcOwner
	}

	needPerSession := s.cfg.MCPBroker != nil || s.sessionNeedsPerFactory(sel, specs, profile, workspace) || s.cfg.LearnedSkills != nil
	if !needPerSession {
		// Shared-engine fast path (today's behaviour, byte-identical). The labels are
		// the empty pair + default profile here (the empty-selector default profile is
		// exactly the no-per-session case), so setLabels persists nothing new — the
		// snapshot stays byte-identical to a pre-Phase-1 default session.
		sess, err := newCreatedSession(mintID(), mode, placement.Ref, limits, s.cfg.Now(), opts)
		if err != nil {
			return nil, fmt.Errorf("server: create session metadata: %w", err)
		}
		if err := setSessionLabels(sess, sel, profile, owner, s.rootAuthority(sess.Kind, carriedAuthority, carriedAuthorityBound)); err != nil {
			return nil, err
		}
		s.setTitleGenerationEligibility(sess, sel)
		if err := seedCarryover(sess, carrySnap); err != nil {
			return nil, err
		}
		return s.persistPlacedCreatedSession(ctx, sess, owner, retryRequest, placement)
	}

	return s.createPerSessionEngine(ctx, mintID, mode, workspace, limits, sel, specs, profile, carrySnap, owner, opts, carriedAuthority, carriedAuthorityBound, retryRequest, placement)
}

// createPerSessionEngine is the per-session-engine create branch, factored out
// of createSession so createSession stays under the gocyclo threshold. It
// enforces the engine-factory + registry-cap discipline (cheap pre-check, build
// outside the lock, authoritative re-check + register under the lock), seeds any
// carryover history BEFORE the first Store.Save, and on a persist failure
// evicts the reservation and tears the freshly-built engine down so a failed
// create leaks neither a slot nor a connection. See createSession for the
// profile-aware workspace rule and the carryover snapshot semantics.
//
//nolint:gocyclo // Creation keeps factory, authorization, broker ownership, registration, and teardown in one transaction.
func (s *Service) createPerSessionEngine(ctx context.Context, mintID func() session.SessionID, mode session.PermissionMode, workspace string, limits session.Limits, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, carrySnap []session.Message, owner *session.Principal, opts createSessionOpts, carriedAuthority session.Authority, carriedAuthorityBound bool, retryRequest *createRequest, placement *PlacementBinding) (*session.Session, error) {
	if opts.debugTargetID != "" {
		if s.cfg.DebugSessionEngine == nil {
			return nil, fmt.Errorf("%w: session debugging is not supported", ErrInvalidArgument)
		}
	} else if s.cfg.SessionEngine == nil && s.cfg.SessionEngineWithTools == nil {
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

	var res SessionEngineResult
	var err error
	var debugTarget *session.Session
	var broker *localBrokerAttachment
	var committed bool
	var id session.SessionID
	if opts.debugTargetID != "" {
		debugTarget, err = s.cfg.Store.Load(ctx, opts.debugTargetID)
		if err != nil || debugTarget == nil || s.authorizeSession(ctx, debugTarget) != nil {
			return nil, fmt.Errorf("%w: debug target is unavailable", ErrNotFound)
		}
		if debugTarget.Incarnation() != opts.debugTargetIncarnation {
			return nil, fmt.Errorf("%w: debug target incarnation changed during creation", ErrFailedPrecondition)
		}
		res, err = s.cfg.DebugSessionEngine(ctx, sel, profile, mode, opts.debugTargetID, session.DebugTargetFingerprint(debugTarget), debugTarget.Owner, opts.debugMCPServers, nil)
	} else {
		if s.cfg.MCPBroker != nil {
			id = mintID()
			unlockBroker := s.brokerMu.lock(id)
			defer unlockBroker()
			broker, err = s.openBrokerAttachment(ctx, id, "", false)
			if err != nil {
				return nil, err
			}
			defer s.finalizeBrokerAttachment(broker, &committed)
		}
		res, err = s.callSessionEngine(ctx, sel, specs, profile, workspace, mode, brokerTools(broker))
	}
	if err != nil {
		// Factory maps an unknown/unavailable provider to ErrInvalidArgument; any
		// error is propagated as-is for the caller to map to a status.
		return nil, err
	}
	eng, closeFn := res.Engine, res.Close
	// All-or-nothing client MCP on the wire path, before a non-broker id is minted
	// or anything is persisted: a caller that asked for tools must not be handed a
	// session quietly missing them. Teardown uses the same closeFn idiom as every
	// other rejection below, so a refused create leaks neither a connection nor a
	// registry slot.
	if err := verifyClientMCPMounted(specs, res.MountedClientMCP, opts.clientMCPStrict); err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, err
	}
	if id == "" {
		id = mintID()
	}
	sess, err := newCreatedSession(id, mode, placement.Ref, limits, s.cfg.Now(), opts)
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, fmt.Errorf("server: create session metadata: %w", err)
	}
	if broker != nil {
		sess.ExternalBinding = broker.attachment.Binding()
	}
	// Persist the neutral provider+model selector and the profile as write-once
	// creation labels on the aggregate, so a restarted process re-derives the SAME
	// per-session engine via the factory (rehydrateSession) instead of falling to the
	// default-provider floor / inferring the profile from the empty-workspace pun.
	if err := s.setPerSessionLabels(sess, sel, profile, owner, opts, res, brokerTools(broker), carriedAuthority, carriedAuthorityBound); err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, err
	}
	if debugTarget != nil {
		sess.DebugTargetFingerprint = session.DebugTargetFingerprint(debugTarget)
	}
	if placement != nil {
		sess.Placement = canonicalPlacementMetadata(*placement)
	}
	s.setTitleGenerationEligibility(sess, sel)
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
	if len(specs) > 0 {
		s.clientMCPSpecs[sess.ID] = append([]mcp.ServerConfig(nil), specs...)
	}
	s.mu.Unlock()

	persisted, perr := s.persistCreatedSession(ctx, sess, owner, retryRequest)
	if perr != nil || persisted != sess {
		// The engine was built and the slot reserved but this newly-built session
		// was not published: evict the slot and environment. On an idempotent
		// cross-service retry, persisted is the already-published winner.
		s.mu.Lock()
		delete(s.sessionEngines, sess.ID)
		delete(s.sessionEnvironments, sess.ID)
		delete(s.clientMCPSpecs, sess.ID)
		s.mu.Unlock()
		if closeFn != nil {
			_ = closeFn()
		}
		return persisted, perr
	}
	if broker != nil {
		commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
		commitErr := s.commitBrokerAttachment(commitCtx, id, broker)
		cancelCommit()
		if commitErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrInternal, commitErr)
		}
		committed = true
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
// skills.ToolName (internal/adapter/skills.NewTool), tools.ShellToolName
// (internal/adapter/tools.NewShellTool) — so the cap links to the registered name
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
		Bash:              has(tools.ShellToolName),
		Image:             pcaps.Image,
		Audio:             pcaps.Audio,
		Posture:           s.cfg.Posture,
		Worktrees:         s.placementDiscoveryAvailable(),
		Reflection:        s.cfg.ReflectSession != nil,
		LearningProposals: s.cfg.Proposals != nil,
		LearnedSkills:     s.cfg.LearnedSkills != nil,
		Scheduling:        s.scheduleStore() != nil,
		StorageHealth:     s.cfg.StorageManagementAuthorized != nil && (implementsStorageHealth(s.cfg.Store) || s.scheduleStore() != nil),
		StorageMigration:  s.cfg.StorageManagementAuthorized != nil && s.maintenanceMutationAvailable() && func() bool { _, ok := migrationStore(s.cfg.Store); return ok }(),
		StorageCleanup:    s.cfg.StorageManagementAuthorized != nil && s.maintenanceMutationAvailable() && supportsCleanupDelete(s.cfg.Store),
		ManualDream:       toProtoDreamCapabilities(s.ManualDreamCapabilities()),
		// Steer reads the SAME wired engine knob the runs consult (Deps.EnableSteer
		// via Engine.SteerEnabled) — the advertisement can never claim a steer
		// path the engine did not arm, and it is computed HERE, once, never
		// recomputed per sink (the CreateSession echo and the Session snapshot
		// re-hydration path both carry this one value).
		Steer: s.cfg.Engine != nil && s.cfg.Engine.SteerEnabled(),
		// Manual compaction uses the configured engine, or a per-session engine
		// derived under the same service construction semantics.
		ManualCompaction:    s.cfg.Engine != nil,
		SessionDebug:        s.cfg.DebugSessionEngine != nil,
		DebugMcp:            s.cfg.DebugMCP,
		WorkspaceEnrollment: s.cfg.WorkspaceEnrollment,
	}
}

// CompatibilityInfo returns the deployment's compatibility descriptor (ADR 0248): the
// API major, the operator-enabled capabilities, the build's supported feature
// identifiers, and the optional build/deployment labels.
//
// It exists so a client can answer "what is this server?" WITHOUT creating a
// probe session — ServerCapabilities otherwise rides CreateSessionResponse only,
// so discovery cost a session that then had to be cleaned up.
//
// The capabilities half REUSES s.capabilities() rather than recomputing a
// parallel projection. That is the load-bearing part: a second projection would
// drift from the CreateSession echo, and a client comparing the two would see a
// server contradicting itself about its own configuration.
//
// The two vocabularies stay SEPARATE by design. capabilities answers "what has
// this operator enabled?" and changes with operator config; features answers
// "what does this build implement?" and changes on upgrade. Folding one into the
// other makes a --no-bash deployment indistinguishable from version skew.
func (s *Service) CompatibilityInfo(ctx context.Context) *mecatlv1.GetCompatibilityInfoResponse {
	return &mecatlv1.GetCompatibilityInfoResponse{
		ApiMajor:     APIMajor,
		Capabilities: s.capabilitiesFor(ctx),
		Features:     serverFeatures(s.featureScope()),
		Deployment:   s.cfg.DeploymentID,
	}
}

// featureScope projects the deployment policy the feature registry filters on.
// It reads the SAME Config value the enforcement seam reads, which is what keeps
// the advertisement and the refusal from disagreeing.
func (s *Service) featureScope() FeatureScope {
	return FeatureScope{ClientMCPOnCreate: s.cfg.ClientMCPOnCreate}
}

// verifyClientMCPMounted enforces the wire path's ALL-OR-NOTHING client-MCP
// contract: every requested server must appear in the factory's mounted set.
//
// The problem it closes: connecting client MCP is best-effort in composition
// (mcp.NewManager keeps the servers that answered and drops the rest, and returns
// an error only when EVERY one fails). That is right for the ACP adapter, whose
// peer is the operator's own editor and for whom a degraded session beats no
// session. It is wrong for a wire caller, which sees none of composition's WARNs
// and would receive an ordinary session id for a session missing some or all of
// the tools it asked for — with nothing in the response to tell it apart from
// success. Silent partial success is the failure mode worth engineering against.
//
// strict is set only by WithClientMCP, so the ACP path is untouched. When strict
// and servers were requested, an EMPTY mounted set fails: a factory that reports
// nothing has mounted nothing as far as this check can tell, and a guarantee that
// a factory can opt out of by omitting a field is not a guarantee.
//
// The error names the SERVER NAMES that did not mount — client-supplied
// identifiers, which the caller already knows. It carries no URL and, per AC9.5,
// no header value: those are secret-shaped and never appear in an error.
func verifyClientMCPMounted(requested []mcp.ServerConfig, mounted []string, strict bool) error {
	if !strict || len(requested) == 0 {
		return nil
	}
	mountedSet := make(map[string]struct{}, len(mounted))
	for _, name := range mounted {
		mountedSet[name] = struct{}{}
	}
	var missing []string
	for _, spec := range requested {
		if _, ok := mountedSet[spec.Name]; !ok {
			missing = append(missing, spec.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d of %d requested server(s) did not connect: %s",
		ErrClientMCPUnreachable, len(missing), len(requested), strings.Join(missing, ", "))
}

// ClientMCPFromWire is the SINGLE enforcement seam for client-provided MCP
// servers arriving on a session-creating API request. Both wire transports call
// it; neither classifies an entry itself.
//
// Order is load-bearing, and it is the ordinary "400 before 501" shape:
//
//  1. CLASSIFY, unconditionally, through the shared mcp.PartitionClientServers —
//     the same validator the ACP surface uses. A stdio or sse entry is rejected
//     AS SUCH on every deployment, so "No stdio MCP, ever" (AGENTS.md) stays an
//     invariant of the request shape rather than a downstream consequence of a
//     policy flag that some future composition root might flip. Classification is
//     pure string work: it opens no connection and has no side effect, so running
//     it on a deployment that will refuse anyway costs nothing.
//  2. GATE on the deployment policy. A well-formed request that this deployment
//     does not accept is refused with the typed ErrClientMCPUnsupported
//     (UNIMPLEMENTED / 501), never silently dropped — a client whose servers were
//     quietly ignored would run a session it believes has tools it does not have.
//
// An empty list returns an EMPTY grant and no error on EVERY deployment: sending
// no MCP servers is not a use of the feature, so a TCP deployment must not fail an
// ordinary create that merely carries an empty repeated field.
//
// It returns a ClientMCPGrant rather than raw specs so that "these specs were
// classified and permitted" is enforced by the type system: WithClientMCP accepts
// nothing else, and the grant's field is unexported, so this function is the only
// place a non-empty one comes from.
//
// Header values never appear in the returned error (they are secret-shaped).
func (s *Service) ClientMCPFromWire(servers []mcp.ClientServer) (ClientMCPGrant, error) {
	if len(servers) == 0 {
		return ClientMCPGrant{}, nil
	}
	specs, err := mcp.PartitionClientServers(servers)
	if err != nil {
		return ClientMCPGrant{}, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	if !s.cfg.ClientMCPOnCreate {
		return ClientMCPGrant{}, fmt.Errorf("%w: this API surface is reachable over TCP; client-provided MCP servers require a UNIX-socket listener with HTTP disabled", ErrClientMCPUnsupported)
	}
	return ClientMCPGrant{specs: specs}, nil
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
func (s *Service) CreateSessionWithMCP(ctx context.Context, mode session.PermissionMode, limits session.Limits, specs []mcp.ServerConfig) (*session.Session, error) {
	// Thin wrapper over the generalized create path with the ZERO provider
	// selector and the DEFAULT profile: no specs uses the shared engine (today's
	// behaviour), specs build a per-session engine. The zero selector leaves the
	// per-session engine bound to the DEFAULT provider, matching the pre-S3 MCP
	// path exactly. (ACP carries no profile in P0 — every ACP session is the
	// default filesystem profile.)
	return s.createSession(ctx, mode, limits, ProviderSelector{}, specs, ProfileDefault, createSessionOpts{})
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
	unlockEntry := s.runEntryMu.lock(id)
	defer unlockEntry()
	s.closeSessionAuthorized(id)
}

// closeSessionAuthorized performs the authorization-settlement teardown. The
// caller must already hold runEntryMu for id: CloseSession itself acquires it
// for direct callers (e.g. the ACP disconnect path), while EndSession already
// holds it across its live-run precondition check and calls this directly to
// avoid re-locking the non-reentrant per-id mutex.
//
// A session this process already definitively lost (lostOwnership[id] set by
// onLeaseLost) skips the reaffirm/settle step as an optimization: it is closing
// this session anyway, so there is nothing to gain from a real re-Acquire just
// to immediately Release it again, and settleAuthorizationLocked is a no-op
// unless this process is mid external-authorization, which it cannot be once
// it has lost the lease. (acquireLease/reaffirmLease still fail fast on the
// tombstone for every OTHER caller — the one narrow exception is
// acquireMutationLeaseForStaleSettle/SettleIfStale, see lostOwnership's field
// doc comment — but skipping the round trip here is still correct and
// cheaper.) closeSessionLocal itself is already safe to call in this state —
// releaseLease guards on lease validity and never re-releases a hold onLeaseLost
// already invalidated, and it unconditionally clears the tombstone too, so
// CloseSession remains a recovery path regardless.
func (s *Service) closeSessionAuthorized(id session.SessionID) {
	s.mu.Lock()
	_, alreadyLost := s.lostOwnership[id]
	s.mu.Unlock()

	if !alreadyLost {
		if err := s.acquireLease(context.Background(), id); err != nil {
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "session close authorization settlement deferred",
				"session", string(id), "err", err.Error())
			return
		}
		if err := s.settleAuthorizationLocked(context.Background(), id); err != nil {
			return // settlement logged the persistence/authority failure; retain attachment + lease for retry.
		}
	}
	s.stopAuthorizationExpiry(id)
	unlockBroker := s.brokerMu.lock(id)
	defer unlockBroker()
	s.closeSessionLocal(id)
}

// closeSessionLocal releases only process-local ownership. The caller must hold
// brokerMu for id so no engine can borrow and install the attachment while it is
// being closed.
func (s *Service) closeSessionLocal(id session.SessionID) {
	// Release composition-owned session-scoped state first (e.g. the per-session
	// learned permission rules) so it never outlives the session, even if the
	// per-session engine teardown below is a no-op for this id.
	if s.cfg.OnCloseSession != nil {
		s.cfg.OnCloseSession(id)
	}
	s.mu.Lock()
	expiry := s.authorizationExpiry[id]
	delete(s.authorizationExpiry, id)
	se, ok := s.sessionEngines[id]
	if ok {
		delete(s.sessionEngines, id)
	}
	brokerAttachment := s.brokerAttachments[id]
	delete(s.brokerAttachments, id)
	// Drop any per-session environment override too: it closes over the (now
	// disconnecting) connection, so it must not outlive the session.
	delete(s.sessionEnvironments, id)
	delete(s.clientMCPSpecs, id)
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
	delete(s.lostOwnership, id)
	s.mu.Unlock()
	if expiry != nil && expiry.timer != nil {
		expiry.timer.Stop()
	}
	if ok && se.close != nil {
		if err := se.close(); err != nil {
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "per-session engine close failed")
		}
	}
	if brokerAttachment != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), engineCloseTimeout)
		_, err := brokerAttachment.Close(closeCtx)
		cancel()
		if err != nil {
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "MCP broker attachment close failed")
		}
	}
	// Stop the session's renewer and release its cross-process lease (cloud-native
	// Phase 4): the session is ending, so a competitor may now take it over. No-op
	// when no lease is wired or held.
	s.releaseLease(id)
}

// EndSession is the precondition-checked sibling of CloseSession: the
// surface-facing session-end entry for the gRPC/HTTP transports (the ACP adapter
// calls the void CloseSession directly on disconnect). It verifies the session
// exists and serializes against run admission. A locally registered run owns the
// session until its relay calls FinishRun, so close fails with
// ErrFailedPrecondition without releasing the lease or tearing down any local
// engine, policy, or environment. A persisted awaiting snapshot with no local run
// is not active ownership: teardown releases local resources while leaving the
// durable PendingAsk untouched. Closing an already-released (but still persisted)
// session remains idempotent. EndSession never deletes the persisted snapshot and
// never cancels a run; cancellation is an orthogonal operation.
func (s *Service) EndSession(ctx context.Context, id session.SessionID) error {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	_, live := s.runs[id]
	s.mu.Unlock()
	if live {
		return fmt.Errorf("%w: session has a local active run", ErrFailedPrecondition)
	}
	s.closeSessionAuthorized(id)
	return nil
}

// Close tears down all per-session engines' MCP managers. It is the Service's
// shutdown hook so a process exit does not leak any per-session MCP connection.
// It is safe to call multiple times.
//
//nolint:gocyclo // shutdown sequences multiple independent teardown phases in order; inherent.
func (s *Service) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	complete := s.shutdownComplete
	s.mu.Unlock()
	if complete {
		return
	}
	// Stop and join auxiliary title workers before tearing down their session
	// dependencies. A cancelled in-flight provider call records interruption.
	if s.titleCoordinator != nil {
		s.titleCoordinator.Close()
	}
	// Signal shutdown so in-flight runs (scheduled fires and foreground turns)
	// observe the cancellation and unwind. This fires BEFORE the scheduler stop
	// and before engine-close so runs unblock promptly rather than waiting on
	// the full shutdown sequence.
	s.shutdownCancel()

	s.prepareAuthorizationClose()

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
		rs.persistMu.Lock()
		awaiting := rs.awaiting.Load()
		rs.persistMu.Unlock()
		if awaiting {
			continue // resumable cross-process via the durable awaiting snapshot
		}
		if rs.run != nil {
			rs.run.Cancel()
		} else if rs.admissionCancel != nil {
			rs.admissionCancel()
		}
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
	// Stop every attached durable watch (ADR 0250) so no pump goroutine outlives
	// the Service, and refuse later attachments. A shutdown cancel is a CLEAN end,
	// not a gap: no append failed, so the stream simply ends and the client
	// reconnects with its cursor.
	s.closeWatches()

	s.mu.Lock()
	engines := s.sessionEngines
	s.sessionEngines = make(map[session.SessionID]*sessionEngine)
	brokerAttachments := s.brokerAttachments
	s.brokerAttachments = make(map[session.SessionID]brokercontract.Attachment)
	// Drop all per-session environment overrides on shutdown; they hold no resources
	// of their own (the underlying connection is closed separately) but must not
	// linger past the Service.
	s.sessionEnvironments = make(map[session.SessionID]tool.Environment)
	s.clientMCPSpecs = make(map[session.SessionID][]mcp.ServerConfig)
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
				if err := se.close(); err != nil {
					s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "per-session engine close failed during shutdown")
				}
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
	attachmentCtx, cancelAttachments := context.WithTimeout(context.Background(), engineCloseTimeout)
	var attachmentWG sync.WaitGroup
	for _, attachment := range brokerAttachments {
		attachmentWG.Add(1)
		go func() {
			defer attachmentWG.Done()
			if _, err := attachment.Close(attachmentCtx); err != nil {
				s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "MCP broker attachment close failed during shutdown")
			}
		}()
	}
	attachmentWG.Wait()
	cancelAttachments()

	// Stop every renewer and release every held cross-process lease on shutdown
	// (cloud-native Phase 4), so a restarted process can take the sessions over
	// without waiting out the TTL. Best-effort (detached short-timeout ctx).
	for _, id := range leasedIDs {
		s.releaseLease(id)
	}
	s.mu.Lock()
	s.shutdownComplete = true
	s.mu.Unlock()
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
	// Close admission and snapshot provisional cancellations under the same lock
	// used by promotion, so a run cannot cross from provisional to provider-start
	// after the drain gate wins.
	s.mu.Lock()
	s.draining.Store(true)
	var provisional []context.CancelFunc
	for _, st := range s.runs {
		if st.run == nil && st.admissionCancel != nil {
			provisional = append(provisional, st.admissionCancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range provisional {
		cancel()
	}
	// Arm the scheduler's drain gate too so no NEW fires start mid-tick during
	// shutdown (in-flight fires complete or are cancelled by Close's Stop).
	// The scheduler lives on the embedded scheduleManager (ADR 0076) as an
	// atomic pointer; nil-safe (no scheduler wired, or no manager at all).
	if m := s.schedMgr; m != nil {
		if sch := m.scheduler.Load(); sch != nil {
			sch.Drain()
		}
	}
}

func (s *Service) snapshotDrainState() (map[session.SessionID]*runState, []session.SessionID) {
	s.mu.Lock()
	runs := make(map[session.SessionID]*runState, len(s.runs))
	for id, st := range s.runs {
		runs[id] = st
	}
	leaseOnly := make([]session.SessionID, 0, len(s.heldLeases))
	for id := range s.heldLeases {
		if _, live := runs[id]; !live {
			leaseOnly = append(leaseOnly, id)
		}
	}
	s.mu.Unlock()

	// Do not hold s.mu while taking persistMu: Persist takes them in the opposite
	// order to revalidate registry identity after its potentially blocking save.
	for _, st := range runs {
		st.persistMu.Lock()
		if st.awaiting.Load() {
			st.preserveDurable.Store(true)
		}
		st.persistMu.Unlock()
	}
	return runs, leaseOnly
}

// GracefulDrain settles locally-owned runs after closing admission. Executing
// runs are cancelled and must be joined by their relay (FinishRun) before their
// terminal aggregate is persisted and their lease is explicitly released.
// Awaiting runs are cancelled only in memory: preserveDurable prevents the relay
// from replacing the already-persisted PendingAsk handoff point. A context
// timeout invalidates local mutation capability and stops renewal, but never
// explicitly releases an unjoined run's lease; TTL then governs takeover.
func (s *Service) GracefulDrain(ctx context.Context) error {
	s.Drain()

	runs, leaseOnly := s.snapshotDrainState()

	// Ownership with no local run is already settled (including an unattended
	// durable awaiting snapshot), so it can hand off immediately.
	for _, id := range leaseOnly {
		s.releaseLease(id)
	}
	for id, st := range runs {
		if st.run != nil {
			// The already-persisted awaiting snapshot is the handoff point. Deny
			// the engine's terminal cancellation save before cancelling it.
			if st.preserveDurable.Load() {
				s.cfg.MutationCapability.Invalidate(id)
			}
			st.run.Cancel()
		} else if st.admissionCancel != nil {
			st.admissionCancel()
		}
	}

	pending := make(map[session.SessionID]*runState, len(runs))
	settled := make(chan session.SessionID, len(runs))
	for id, st := range runs {
		pending[id] = st
		go func(id session.SessionID, done <-chan struct{}) {
			select {
			case <-done:
				settled <- id
			case <-ctx.Done():
			}
		}(id, st.settled)
	}
	settle := func(id session.SessionID) {
		st, ok := pending[id]
		if !ok {
			return
		}
		delete(pending, id)
		st.persistMu.Lock()
		preserveDurable := st.preserveDurable.Load()
		st.persistMu.Unlock()
		if !preserveDurable {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseAcquireTimeout)
			if s.mutationLeaseHeld(id) {
				if err := s.saveSession(saveCtx, st.sess); err != nil {
					s.cfg.Diagnostics.Log(saveCtx, port.LevelWarn, "drain persistence failed; prior durable state remains authoritative",
						"session", string(id), "err", err.Error())
				}
			}
			cancel()
		}
		s.releaseLease(id)
	}
	for len(pending) > 0 {
		select {
		case id := <-settled:
			settle(id)
		case <-ctx.Done():
			// A settlement may race the timeout. Recheck every pending lifecycle
			// directly before deciding which genuinely unjoined owners retain TTL.
			for id, st := range pending {
				select {
				case <-st.settled:
					settle(id)
				default:
				}
			}
			for pendingID := range pending {
				s.retainLeaseForTTL(pendingID)
			}
			return ctx.Err()
		}
	}
	return nil
}

// retainLeaseForTTL stops renewal and invalidates local mutation authority while
// leaving ownership unreleased for process-death/TTL takeover.
func (s *Service) retainLeaseForTTL(id session.SessionID) {
	s.mu.Lock()
	h := s.heldLeases[id]
	if h != nil && h.valid {
		h.valid = false
		s.cfg.MutationCapability.Invalidate(id)
	}
	s.mu.Unlock()
	if h != nil {
		h.cancel()
	}
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

// OwnershipEnforced reports whether caller ownership is active. Composition
// uses it only where ownerless persisted metadata must fail closed before
// reconstructing a caller context; resource decisions still flow through
// ownsResource/authorizeSession.
func (s *Service) OwnershipEnforced() bool {
	return s.cfg.OwnershipEnforced
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

func (s *Service) saveSession(ctx context.Context, sess *session.Session) error {
	return s.cfg.MutationCapability.GuardStore(s.cfg.Store).Save(ctx, sess)
}

func (s *Service) deleteSessionFamily(ctx context.Context, id session.SessionID, store port.PrunableStore) error {
	if !s.mutationLeaseHeld(id) {
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	return store.Delete(ctx, id)
}

// GetSession returns the persisted session under id, or ErrNotFound.
//
// Absence, foreign ownership, and a broken store are deliberately ONE
// caller-visible answer, so a caller cannot probe for another owner's ids. That
// concealment is owed to the CALLER only: an infrastructure failure is logged
// for the operator, because otherwise a storage outage is indistinguishable from
// mass deletion from both sides at once. A genuine not-found is the normal case
// and stays silent.
func (s *Service) GetSession(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil && !errors.Is(err, port.ErrSessionNotFound) {
		if s.cfg.OwnershipEnforced {
			class := port.ClassifySessionLoadFailure(err)
			// This target-free operator fact must not inherit request trace/baggage:
			// handlers may project context values into the final log record.
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "session load failed", "class", class.String(), "ownership", "enforced")
			if s.cfg.SessionLoadFailureMetric != nil {
				s.cfg.SessionLoadFailureMetric(class)
			}
		} else {
			s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session load failed; reported to the caller as absent",
				"session", string(id), "err", err.Error())
		}
	}
	if err != nil || s.authorizeSession(ctx, sess) != nil {
		if s.cfg.OwnershipEnforced {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	// The session ID is an opaque handle, so repairing malformed bytes here would
	// silently turn one persisted identity into another before protobuf mapping.
	if !utf8.ValidString(string(sess.ID)) {
		return nil, fmt.Errorf("%w: persisted session has an invalid UTF-8 id", ErrInternal)
	}
	return sess, nil
}

// WithAuthorizedSession serializes a caller-owned side effect with session run
// entry. It performs an ownership-only preflight before taking caller-selected
// coordination, then reloads and reauthorizes under runEntryMu immediately
// before effect. The callback must not call another operation that locks the
// same session id.
func (s *Service) WithAuthorizedSession(ctx context.Context, id session.SessionID, effect func(*session.Session) error) (*session.Session, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := effect(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// CompactSession applies one configured compaction pass to an owned main-chat
// session at a turn boundary. caller is the verified transport principal; it is
// bound to ctx only when the context has no principal, and a mismatch is rejected.
// The operation is serialized against run entry and cross-process mutations.
// A successful no-op neither saves nor appends events.
//
//nolint:gocyclo // explicit authorization, state, liveness, lease, and persistence gates stay ordered.
func (s *Service) CompactSession(ctx context.Context, id session.SessionID, caller *session.Principal) (agent.ManualCompactionResult, error) {
	contextCaller := session.PrincipalFromContext(ctx)
	if caller != nil && contextCaller != nil && !caller.SameIdentity(contextCaller) {
		return agent.ManualCompactionResult{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if caller != nil && contextCaller == nil {
		ctx = session.WithPrincipal(ctx, caller)
	}
	if isDelegationChildSessionID(id) {
		return agent.ManualCompactionResult{}, fmt.Errorf("%w: delegation-child sessions cannot be compacted directly", ErrFailedPrecondition)
	}
	absent, err := s.managementOwnershipPreflight(ctx, id, false)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	if absent {
		return agent.ManualCompactionResult{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}

	unlock := s.runEntryMu.lock(id)
	defer unlock()
	sess, _, err := s.managementTarget(ctx, id, false)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	if err := admitRunPurpose(sess, runPurposeChat); err != nil {
		return agent.ManualCompactionResult{}, err
	}
	if err := s.validatePersistedWorkspace(sess); err != nil {
		return agent.ManualCompactionResult{}, err
	}

	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	defer release()
	compactCtx, stopCompact, leaseHeld := s.mutationLeaseContext(ctx, id)
	defer stopCompact()
	sess, _, err = s.managementTarget(ctx, id, false)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	if err := admitRunPurpose(sess, runPurposeChat); err != nil {
		return agent.ManualCompactionResult{}, err
	}
	eng, _, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	result, err := eng.CompactSession(compactCtx, sess)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	if !result.Changed {
		return result, nil
	}
	// The storage port has no lease-token CAS, so this is not fencing: it is the
	// narrowest available pre-save loss check. Cancellation also lets a cooperative
	// long-running compactor stop as soon as the renewer declares loss.
	if !leaseHeld() {
		return agent.ManualCompactionResult{}, fmt.Errorf("%w: session lease was lost during compaction", ErrSessionLeasedElsewhere)
	}
	if err := s.saveSession(compactCtx, sess); err != nil {
		return agent.ManualCompactionResult{}, fmt.Errorf("%w: persist compacted session: %v", ErrInternal, err)
	}

	appendCtx := context.WithoutCancel(ctx)
	events := []session.Event{
		{Type: session.EvCompaction, Text: result.Summary},
		{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: result.Archive}},
	}
	for _, ev := range events {
		if err := s.appendEvent(appendCtx, id, ev); err != nil {
			s.cfg.Diagnostics.Log(appendCtx, port.LevelWarn, "manual compaction event append failed; compacted snapshot remains committed", "session", string(id), "event", string(ev.Type), "error", err)
		}
	}
	return result, nil
}

// RenameSession applies an explicit operator title change to an owned main
// session. Authorization, kind/state/liveness checks, and lease acquisition are
// serialized under the same per-session mutex used by prompt starts.
func (s *Service) RenameSession(ctx context.Context, id session.SessionID, title string) (*session.Session, error) {
	absent, err := s.managementOwnershipPreflight(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	_, absent, err = s.managementTarget(ctx, id, false)
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
	sess, absent, err := s.managementTarget(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err := sess.RenameTitle(title); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("%w: rename session: %v", ErrInternal, err)
	}
	s.publishTitle(context.WithoutCancel(ctx), sess)
	return sess, nil
}

// DeleteSession physically removes an owned main session and all store-managed
// sidecars. Absence and foreign ownership are both idempotent success, preventing
// deletion from becoming an ownership oracle. Infrastructure failures remain loud.
func (s *Service) DeleteSession(ctx context.Context, id session.SessionID) error {
	absent, err := s.managementOwnershipPreflight(ctx, id, true)
	if err != nil || absent {
		return err
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	_, absent, err = s.managementTargetAwaitingDrain(ctx, id, true)
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
	sess, absent, err := s.managementTargetAwaitingDrain(ctx, id, true)
	if err != nil || absent {
		return err
	}
	unlockBroker := s.brokerMu.lock(id)
	defer unlockBroker()
	if err := s.deleteSessionFamily(ctx, sess.ID, prunable); err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			// The durable record is already gone: still attempt broker cleanup
			// (best-effort) before reporting success, so a locally-retained
			// broker handle is never orphaned by an already-completed delete.
			if brokerErr := s.deleteBrokerSessionLocked(ctx, id); brokerErr != nil {
				s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker cleanup after already-deleted session failed",
					"session", string(id), "err", brokerErr.Error())
			}
			s.closeSessionLocal(id)
			return nil
		}
		if errors.Is(err, port.ErrPruneUnsupported) {
			return ErrSessionDeleteUnsupported
		}
		return fmt.Errorf("%w: delete session: %v", ErrInternal, err)
	}
	// The durable record is gone; broker cleanup is now best-effort. Reordered
	// deliberately (I-8): deleting broker state FIRST left an unrecoverable
	// partial-deletion window if the durable delete then failed — the snapshot
	// would survive pointing at broker state that no longer exists. Broker
	// state is process-local, so an orphaned entry here is a bounded leak, the
	// strictly safer failure direction.
	if err := s.deleteBrokerSessionLocked(ctx, id); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker cleanup after session delete failed",
			"session", string(id), "err", err.Error())
	}
	s.closeSessionLocal(id)
	return nil
}

var (
	errRetentionCandidateActive  = fmt.Errorf("%w: retention candidate is active", ErrFailedPrecondition)
	errRetentionCandidateChanged = fmt.Errorf("%w: retention candidate changed", ErrFailedPrecondition)
)

// DeleteSessionForRetentionCandidate removes one exact planner candidate while
// keeping the mandatory maintenance/run-entry lease exclusions held through the
// backend's atomic final metadata comparison and family deletion. Automatic and
// manual retention intentionally use the same exclusion posture.
func (s *Service) DeleteSessionForRetentionCandidate(ctx context.Context, candidate port.SessionDiscoveryMeta) error {
	unlock := s.runEntryMu.lock(candidate.ID)
	defer unlock()
	deleter, ok := s.cfg.Store.(port.ConditionalPrunableStore)
	if !ok {
		return ErrSessionDeleteUnsupported
	}
	if s.IsLive(candidate.ID) {
		return errRetentionCandidateActive
	}
	release, err := s.acquireMaintenanceMutationLease(ctx, candidate.ID)
	if err != nil {
		return err
	}
	defer release()
	if s.IsLive(candidate.ID) {
		return errRetentionCandidateActive
	}
	sess, err := s.cfg.Store.Load(ctx, candidate.ID)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return errRetentionCandidateChanged
		}
		return fmt.Errorf("%w: load retention candidate: %v", ErrInternal, err)
	}
	if !retentionCandidateMatches(sess, candidate) {
		return errRetentionCandidateChanged
	}
	unlockBroker := s.brokerMu.lock(candidate.ID)
	defer unlockBroker()
	if !s.mutationLeaseHeld(candidate.ID) {
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, candidate.ID)
	}
	deleted, err := deleter.DeleteSessionIfUnchanged(ctx, candidate)
	if err != nil {
		if errors.Is(err, port.ErrPruneUnsupported) {
			return ErrSessionDeleteUnsupported
		}
		return fmt.Errorf("%w: delete retention candidate: %v", ErrInternal, err)
	}
	if !deleted {
		return errRetentionCandidateChanged
	}
	// The durable record is gone; broker cleanup is now best-effort (I-8: see
	// deleteBrokerSessionLocked's doc comment for the ordering rationale).
	if err := s.deleteBrokerSessionLocked(ctx, candidate.ID); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker cleanup after retention delete failed",
			"session", string(candidate.ID), "err", err.Error())
	}
	s.closeSessionLocal(candidate.ID)
	return nil
}

func retentionCandidateMatches(sess *session.Session, candidate port.SessionDiscoveryMeta) bool {
	if sess == nil {
		return false
	}
	ownerMatches := candidate.Owner == nil && sess.Owner == nil || candidate.Owner != nil && candidate.Owner.SameIdentity(sess.Owner)
	return sess.ID == candidate.ID && ownerMatches && sess.Kind == candidate.Kind && sess.State == candidate.State &&
		sess.State != session.StateRunning && sess.State != session.StateAwaiting && sess.State != session.StateAuthorizing && sess.Kind != session.SessionKindUnknown &&
		session.ValidateSessionMetadata(sess.Kind, sess.Relationship) == nil
}

// DeleteSessionForRetention removes one session selected by the composition-owned
// automatic-retention policy. Unlike DeleteSession it is not restricted to main
// chats, but it still serializes against run entry, acquires the cross-process
// mutation lease, and revalidates durable taxonomy and lifecycle after acquiring
// that lease. It is an internal composition callback, never a wire operation.
func (s *Service) DeleteSessionForRetention(ctx context.Context, id session.SessionID) error {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	prunable, ok := s.cfg.Store.(port.PrunableStore)
	if !ok {
		return ErrSessionDeleteUnsupported
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil
		}
		return fmt.Errorf("%w: load retention candidate: %v", ErrInternal, err)
	}
	if sess == nil || sess.ID != id {
		return fmt.Errorf("%w: retention candidate identity mismatch", ErrInternal)
	}
	if err := session.ValidateSessionMetadata(sess.Kind, sess.Relationship); err != nil ||
		sess.Kind == session.SessionKindUnknown || sess.Kind == session.SessionKindMain && hasLegacyNonChatPrefix(id) {
		return fmt.Errorf("%w: retention candidate has no valid durable taxonomy", ErrFailedPrecondition)
	}
	if sess.State == session.StateRunning || sess.State == session.StateAwaiting || sess.State == session.StateAuthorizing || s.IsLive(id) {
		return fmt.Errorf("%w: retention candidate is active or awaiting approval", ErrFailedPrecondition)
	}
	unlockBroker := s.brokerMu.lock(id)
	defer unlockBroker()
	if err := s.deleteSessionFamily(ctx, id, prunable); err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			if brokerErr := s.deleteBrokerSessionLocked(ctx, id); brokerErr != nil {
				s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker cleanup after already-deleted retention candidate failed",
					"session", string(id), "err", brokerErr.Error())
			}
			s.closeSessionLocal(id)
			return nil
		}
		if errors.Is(err, port.ErrPruneUnsupported) {
			return ErrSessionDeleteUnsupported
		}
		return fmt.Errorf("%w: delete retention candidate: %v", ErrInternal, err)
	}
	// The durable record is gone; broker cleanup is now best-effort (I-8: see
	// deleteBrokerSessionLocked's doc comment for the ordering rationale).
	if err := s.deleteBrokerSessionLocked(ctx, id); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker cleanup after retention delete failed",
			"session", string(id), "err", err.Error())
	}
	s.closeSessionLocal(id)
	return nil
}

// managementOwnershipPreflight keeps foreign callers out of caller-selected
// per-session coordination. It deliberately checks ownership only and returns no
// aggregate: managementTarget must reload and reauthorize under runEntryMu before
// any mutation. The ownership-disabled compatibility path retains its historical
// single authoritative load.
func (s *Service) managementOwnershipPreflight(ctx context.Context, id session.SessionID, concealAbsence bool) (bool, error) {
	if !s.cfg.OwnershipEnforced {
		return false, nil
	}
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			if concealAbsence {
				return true, nil
			}
			return false, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return false, fmt.Errorf("%w: load session: %v", ErrInternal, err)
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		if concealAbsence {
			return true, nil
		}
		return false, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return false, nil
}

// managementSession performs the common management authorization and taxonomy
// validation without applying the generic idle-only management gate. The caller
// must hold runEntryMu for id.
func (s *Service) managementSession(ctx context.Context, id session.SessionID, concealAbsence bool) (*session.Session, bool, error) {
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
	return sess, false, nil
}

// errSessionActiveOrAwaiting is managementTarget's specific liveness-conflict
// sentinel, distinct from managementSession's other ErrFailedPrecondition
// causes (e.g. "session is not a main session") — callers that want to await
// the same terminal-but-draining grace promotedSteerRun already tolerates
// (see awaitRunDeregister) match on this exact sentinel via errors.Is, never
// the error's rendered text.
var errSessionActiveOrAwaiting = fmt.Errorf("%w: session is active or awaiting approval", ErrFailedPrecondition)

// managementTarget adds the generic idle-only eligibility gate used by
// management operations other than ClearSession. The caller must hold
// runEntryMu for id.
func (s *Service) managementTarget(ctx context.Context, id session.SessionID, concealAbsence bool) (*session.Session, bool, error) {
	sess, absent, err := s.managementSession(ctx, id, concealAbsence)
	if err != nil || absent {
		return nil, absent, err
	}
	if sess.State == session.StateRunning || sess.State == session.StateAwaiting || sess.State == session.StateAuthorizing || s.IsLive(id) {
		return nil, false, errSessionActiveOrAwaiting
	}
	return sess, false, nil
}

// managementTargetAwaitingDrain wraps managementTarget with the same
// terminal-but-still-draining tolerance promotedSteerRun already gives steer
// promotion (see awaitRunDeregister): a run's liveness registration
// deliberately outlives its terminal event by design (the relay needs to
// finish draining), so a caller that reacts to a terminal result the instant
// it observes one — any SDK client calling DeleteSession right after
// run.result() resolves, for example — can otherwise lose this race against
// managementTarget's single immediate IsLive check even though the session's
// durable state is already correctly terminal (terminateComplete saves
// before it emits). On the specific errSessionActiveOrAwaiting conflict,
// await the registry clearing (bounded by steerPromoteGrace) and retry once;
// any other error, or a conflict that does not clear within the grace, is
// surfaced unchanged — a genuinely in-flight session is not delayed beyond
// the same bound the existing steer-promotion path already accepts. The
// caller must hold runEntryMu for id, matching managementTarget's contract.
func (s *Service) managementTargetAwaitingDrain(ctx context.Context, id session.SessionID, concealAbsence bool) (*session.Session, bool, error) {
	sess, absent, err := s.managementTarget(ctx, id, concealAbsence)
	if !errors.Is(err, errSessionActiveOrAwaiting) {
		return sess, absent, err
	}
	if !s.awaitRunDeregister(ctx, id, steerPromoteGrace) {
		return nil, false, err
	}
	return s.managementTarget(ctx, id, concealAbsence)
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
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if mode == "" {
		return nil, fmt.Errorf("%w: mode is required", ErrInvalidArgument)
	}
	// Ownership is established before caller-selected coordination, then
	// revalidated under runEntryMu before and after lease acquisition.
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()

	// Prefer the live session the engine drives (if registered) so the change is
	// observed by the same object; otherwise reload the authoritative snapshot
	// after acquiring the lease.
	s.mu.Lock()
	st, live := s.runs[id]
	s.mu.Unlock()

	var sess *session.Session
	if live {
		sess = st.sess
		if err := s.authorizeSession(ctx, sess); err != nil {
			return nil, err
		}
	} else {
		loaded, loadErr := s.GetSession(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		sess = loaded
	}

	if err := sess.SetMode(mode); err != nil {
		// A mid-turn refusal from the aggregate is a client-sequencing error.
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := s.saveSession(ctx, sess); err != nil {
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
func (s *Service) validateCarryover(ctx context.Context, srcID session.SessionID, newProviderID string) ([]session.Message, *session.Principal, session.Authority, bool, error) {
	src, err := s.loadAndReopen(ctx, srcID)
	if err != nil {
		return nil, nil, session.Authority{}, false, err
	}
	if src.State == session.StateRunning || src.State == session.StateAwaiting || src.State == session.StateAuthorizing {
		return nil, nil, session.Authority{}, false, fmt.Errorf("%w: carryover requires a session at a turn boundary; source %q is %s", ErrFailedPrecondition, srcID, src.State)
	}
	// The SOURCE's owner travels with the carried history (ADR 0204 decision 4):
	// a fork is attributed to whoever owned the session it copied, never to the
	// caller doing the forking — otherwise fork is an ownership-laundering path
	// (copy someone else's session, become its owner). An ownerless source
	// yields an ownerless fork, never a fabricated one.
	srcOwner := src.Owner
	srcAuthority, srcAuthorityBound := src.BoundAuthority()
	return s.providerCarryoverSnapshot(src, newProviderID), srcOwner, srcAuthority, srcAuthorityBound, nil
}

func (s *Service) providerCarryoverSnapshot(src *session.Session, newProviderID string) []session.Message {
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
	if srcProv == newProv {
		return snap
	}
	stripped := session.StripProviderState(snap)
	if usesResponsesReplayIDs(newProv) {
		return synthesizeOpenAIItemIDs(stripped)
	}
	return stripped
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
	// Ownership is checked before caller-selected coordination, then revalidated
	// under runEntryMu before and after acquiring the mutation lease.
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	// Persisted workspace authority is enforced at the top of reopenLoadedSession.
	return s.reopenLoadedSession(ctx, sess)
}

// validatePersistedWorkspace keeps the run-entry call sites explicit while
// placement reattachment validates the exact persisted EnvironmentRef.
func (*Service) validatePersistedWorkspace(sess *session.Session) error {
	if !sess.EnvironmentRef.Valid() {
		return fmt.Errorf("%w: persisted session %q has no exact placement", ErrFailedPrecondition, sess.ID)
	}
	return nil
}

// validatePersistedSchedulePlacement rejects legacy or cross-scope schedule
// records before claim. Exact reauthorization still happens for every fire.
func (s *Service) validatePersistedSchedulePlacement(spec port.ScheduleSpec) error {
	if !spec.EnvironmentRef.Valid() || spec.PlacementScope == "" {
		return fmt.Errorf("%w: persisted schedule %q has no exact placement", ErrFailedPrecondition, spec.Name)
	}
	if PlacementScope(spec.PlacementScope) != s.schedulePlacementScope() {
		return fmt.Errorf("%w: persisted schedule %q placement scope changed", ErrFailedPrecondition, spec.Name)
	}
	return nil
}

// CanProcessSchedule reports whether a durable schedule is eligible to be
// claimed by this deployment's scheduler. Rejected legacy state is logged before
// the scheduler's claim fence so it cannot revive an off-root filesystem path.
func (s *Service) CanProcessSchedule(sched port.Schedule) bool {
	if err := s.validatePersistedSchedulePlacement(sched.Spec); err != nil {
		s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "scheduler: refusing schedule outside deployment workspace authority", "schedule", sched.Spec.Name, "err", err.Error())
		return false
	}
	return true
}

func (*Service) validateEnvironmentOverride(sess *session.Session, env, authorized tool.Environment) error {
	if env.Workspace() == nil || authorized.Workspace() == nil || env.Ref() != sess.EnvironmentRef ||
		authorized.Ref() != sess.EnvironmentRef || env.Workspace().Root() != authorized.Workspace().Root() {
		return fmt.Errorf("%w: environment override does not match the session's exact placement namespace", ErrFailedPrecondition)
	}
	return nil
}

// reopenLoadedSession applies the existing terminal-state recovery funnel to an
// already-ownership-authorized session. Run entry uses this form so its purpose
// gate can reject a session before recovery mutates or persists it.
//
// It is ALSO the structural choke point for persisted workspace authority (ADR
// 0237): every route that recovers a loaded session for use — loadAndReopen,
// startRunContent, ForkSession — passes through here, so validating the persisted
// root at the top means a new recovery route cannot silently skip the check the
// way ForkSession once did. The check is a pure lexical no-op under client-selected
// authority. Two routes still validate earlier on their own: startRunContent (to
// reject before its purpose gate and run-registry cleanup) and resumeFromAwaiting
// (which rejects terminal states and so bypasses this funnel entirely).
func (s *Service) reopenLoadedSession(ctx context.Context, sess *session.Session) (*session.Session, error) {
	if err := s.validatePersistedWorkspace(sess); err != nil {
		return nil, err
	}
	id := sess.ID
	// Repopulate the in-memory learned-rule store from the durable EventLog's
	// allow-always verdicts (cloud-native Phase 3b) BEFORE the run starts, so a
	// session that allow-always'd a tool before a restart does not re-ask. Done at
	// most once per id per process (a live session learns as it runs; a re-run only
	// re-derives idempotent rules). It reads the LOADED conversation to correlate the
	// verdicts, so it must run after GetSession and before the engine runs.
	s.maybeReplayApprovals(ctx, sess)
	if sess.State == session.StateFailed && sess.FailurePermanence() {
		// Capture permanence BEFORE Recover() clears it (resetToIdle sets
		// permanent=false). Store the pre-flight advisory so the relay can emit
		// an EvRecoverNotice before the next turn burns a provider call on the
		// same unrecoverable error. Use LoadOrStore so two concurrent loads of
		// the same session (under different surface adapters) still emit exactly
		// ONE notice. Keyed by the session id.
		s.recoverNotices.LoadOrStore(id, recoverNoticeText)
	}
	if err := s.repairTerminalState(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// repairTerminalState is the recover-if-terminal arm shared by loadAndReopen
// (the run-entry funnel) and StartRunContent's post-drain-grace re-repair:
// completed → Reopen, cancelled → Interrupt, failed →
// Recover (each history-repaired), persisted, so the matching Run never drives
// an illegal RecordUserPrompt-from-terminal. Awaiting, idle, and running are
// deliberate no-ops: awaiting is the preserved Phase-2 resume point (repairing
// it would clear its still-resolvable PendingAsk); idle is the target state;
// running is repaired ONLY by StartRunContent's crash-orphan Abandon arm, AFTER
// it holds the real lock/lease (issue #475) — never here.
func (s *Service) repairTerminalState(ctx context.Context, sess *session.Session) error {
	switch sess.State {
	case session.StateCompleted:
		if rerr := sess.Reopen(); rerr != nil {
			return fmt.Errorf("server: reopen session: %w", rerr)
		}
		if serr := s.saveSession(ctx, sess); serr != nil {
			return fmt.Errorf("server: persist reopened session: %w", serr)
		}
	case session.StateCancelled:
		if rerr := sess.Interrupt(); rerr != nil {
			return fmt.Errorf("server: interrupt session: %w", rerr)
		}
		if serr := s.saveSession(ctx, sess); serr != nil {
			return fmt.Errorf("server: persist interrupted session: %w", serr)
		}
	case session.StateFailed:
		if rerr := sess.Recover(); rerr != nil {
			return fmt.Errorf("server: recover session: %w", rerr)
		}
		if serr := s.saveSession(ctx, sess); serr != nil {
			return fmt.Errorf("server: persist recovered session: %w", serr)
		}
	}
	return nil
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
	// Re-mount client MCP on resume, re-deriving provider/model and profile from
	// persisted server-owned labels. EnvironmentRef is the sole placement identity;
	// privateWorkspace exactly reattaches it and never infers authority from a path.
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	profile := profileForSession(sess)
	// The exact persisted placement is the session's base namespace: the rebuilt
	// engine's child permission resolver pins to that reattached root. The MODE is
	// loaded session's persisted Mode (ADR 0030 Layer 3), so a session loaded into plan
	// mode mounts the plan model; builtForMode is stamped from the result so a later
	// in-process mode switch on this reloaded session triggers the CASE 1 rebuild.
	workspace, err := s.privateWorkspace(ctx, sess)
	if err != nil {
		return nil, err
	}
	res, err := s.cfg.SessionEngine(ctx, sel, specs, profile, workspace, sess.Mode)
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
	if profile == ProfileNoFS && (s.placementBinder == nil || !sess.EnvironmentRef.Valid()) {
		// A no-fs session's environment override is re-registered with the engine
		// under the same lock (the create-time discipline), so StartRun never
		// consults the shared factory with the empty root. It is a complete
		// shell-less Environment with an honest nofs ref.
		s.sessionEnvironments[id] = tool.MustEnvironment(sess.EnvironmentRef, nofs.New(), memledger.New(), nil)
	}
	s.clientMCPSpecs[id] = append([]mcp.ServerConfig(nil), specs...)
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
	generation := s.captureRunEntryGeneration(id)
	return s.startRunContent(ctx, id, text, parts, runPurposeChat, generation, false)
}

// StartInteractiveRunContent starts a public HTTP/gRPC run whose transport can
// present and control browser authorization. Non-interactive adapters must use
// StartRunContent so protected calls fail without parking.
func (s *Service) StartInteractiveRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content) (*agent.Run, error) {
	generation := s.captureRunEntryGeneration(id)
	return s.startRunContent(ctx, id, text, parts, runPurposeChat, generation, true)
}

// StartScheduledRunContent is the trusted scheduler-purpose entry. It admits
// explicitly-stamped scheduled sessions and the historical sched-- fallback for
// legacy unknown snapshots. It is intentionally absent from public transports;
// scheduler composition calls it directly.
func (s *Service) StartScheduledRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content) (*agent.Run, error) {
	generation := s.captureRunEntryGeneration(id)
	return s.startRunContent(ctx, id, text, parts, runPurposeScheduler, generation, false)
}

// RetryFailedRun resumes the failed model step from the persisted conversation state
// without submitting another prompt. Live system instructions and operator context are
// resolved again for the retry. The caller owns draining the returned run and calling
// FinishRun, exactly as for StartRunContent. Eligibility is derived exclusively from the
// persisted typed terminal metadata and is consumed only after every fallible setup step
// has succeeded and the recovered idle snapshot has been saved.
func (s *Service) RetryFailedRun(ctx context.Context, id session.SessionID) (*agent.Run, error) {
	generation := s.captureRunEntryGeneration(id)
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSession(ctx, id); err != nil {
			return nil, err
		}
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.validateRunEntryGeneration(id, generation); err != nil {
		return nil, err
	}

	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validatePersistedWorkspace(sess); err != nil {
		return nil, err
	}
	if err := admitRunPurpose(sess, runPurposeChat); err != nil {
		return nil, fmt.Errorf("%w: session %q is not eligible for chat retry", ErrFailedStepRetryIneligible, id)
	}
	if registered, ok := s.LookupRun(id); ok {
		if !sess.State.IsTerminal() {
			return nil, fmt.Errorf("%w: session %q already has an active run", ErrFailedStepRetryIneligible, id)
		}
		s.deregister(id, registered)
	}
	eligibleFailure, err := failedStepRetryEligibility(sess)
	if err != nil {
		return nil, fmt.Errorf("%w: session %q: %v", ErrFailedStepRetryIneligible, id, err)
	}

	st, admissionParent, err := s.beginRunAdmission(ctx, id, sess, false)
	if err != nil {
		return nil, err
	}
	promoted := false
	defer s.cleanupRunAdmission(id, st, &promoted)
	if err := s.acquireLease(admissionParent, id); err != nil {
		return nil, err
	}
	admissionCtx, stopAdmission, leaseHeld := s.mutationLeaseContext(admissionParent, id)
	defer func() {
		if !promoted {
			stopAdmission()
		}
	}()
	ctx = admissionCtx
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	// Approval replay reads the still-failed conversation and must complete before
	// preparation clears failed-state metadata.
	s.maybeReplayApprovals(ctx, sess)
	if err := s.prepareFailedStepRetry(ctx, sess, eligibleFailure); err != nil {
		return nil, err
	}

	if !leaseHeld() {
		return nil, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	ctx = memory.WithWorkspace(ctx, env.Workspace().Root())
	run, err := s.promoteRunAdmission(id, st, stopAdmission, func() *agent.Run {
		return engine.RetryFailedStep(ctx, sess, env)
	})
	if err != nil {
		return nil, err
	}
	promoted = true
	return run, nil
}

func failedStepRetryEligibility(sess *session.Session) (bool, error) {
	disposition, progress := sess.FailureMetadata()
	pendingDisposition, pendingProgress, pending := sess.FailedStepRetryPending()
	failed := sess.State == session.StateFailed && disposition == session.RetryDispositionRetryable &&
		(progress == session.StreamProgressPrecommit || progress == session.StreamProgressVisible)
	prepared := pending && (sess.State == session.StateIdle || sess.State == session.StateRunning) &&
		pendingDisposition == session.RetryDispositionRetryable &&
		(pendingProgress == session.StreamProgressPrecommit || pendingProgress == session.StreamProgressVisible)
	if failed || prepared {
		return failed, nil
	}
	return false, fmt.Errorf("state=%q disposition=%q progress=%q retry_pending=%t", sess.State, disposition, progress, pending)
}

func (s *Service) prepareFailedStepRetry(ctx context.Context, sess *session.Session, failed bool) error {
	if failed {
		if err := sess.PrepareFailedStepRetry(); err != nil {
			return fmt.Errorf("server: prepare failed-step retry session: %w", err)
		}
	} else if sess.State == session.StateRunning {
		// Lease ownership and runEntryMu prove a persisted running retry is orphaned.
		if err := sess.Abandon(); err != nil {
			return fmt.Errorf("server: abandon crashed failed-step retry: %w", err)
		}
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return fmt.Errorf("server: persist failed-step retry preparation: %w", err)
	}
	return nil
}

type runEntryGeneration uint64

func (s *Service) captureRunEntryGeneration(id session.SessionID) runEntryGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runEntryGeneration(s.runEntryGenerations[id])
}

// validateRunEntryGeneration must be called while runEntryMu for id is held.
func (s *Service) validateRunEntryGeneration(id session.SessionID, generation runEntryGeneration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateRunEntryGenerationLocked(id, generation)
}

func (s *Service) validateRunEntryGenerationLocked(id session.SessionID, generation runEntryGeneration) error {
	if runEntryGeneration(s.runEntryGenerations[id]) != generation {
		return fmt.Errorf("%w: session %q was cleared while the request waited for admission", ErrFailedPrecondition, id)
	}
	return nil
}

type runPurpose uint8

const (
	runPurposeChat runPurpose = iota
	runPurposeScheduler
	scheduleFireSessionPrefix = "sched--"
)

//nolint:gocyclo // run-entry funnel keeps repair, lease, engine-resolve, and launch in one ordered transaction; inherent.
func (s *Service) startRunContent(ctx context.Context, id session.SessionID, text string, parts []session.Content, purpose runPurpose, generation runEntryGeneration, canPresentAuthorization bool) (*agent.Run, error) {
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if text == "" && len(parts) == 0 {
		return nil, fmt.Errorf("%w: prompt text or parts is required", ErrInvalidArgument)
	}
	// With ownership enabled, prove ownership before entering caller-selected
	// per-session coordination. This is only a preflight: the session may change
	// before the lock is acquired, so the aggregate is deliberately discarded and
	// loaded again under the lock. The compatibility path retains its historical
	// single authoritative load.
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSession(ctx, id); err != nil {
			return nil, err
		}
	}
	// Serialize the authoritative run-entry transaction, including a fresh load,
	// purpose authorization, and terminal-state recovery. When enabled, the
	// preflight above keeps foreign callers out of this owner-correlated lock; this
	// reload prevents the preflight from becoming a durable grant.
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.validateRunEntryGeneration(id, generation); err != nil {
		return nil, err
	}
	// Authorize the exact id before revealing whether its metadata or legacy prefix
	// is runnable. Foreign, ownerless-under-enforcement, pruned, and absent ids all
	// remain the same ErrNotFound class.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	// Validate the persisted root EARLY — before the purpose gate and the run-
	// registry cleanup below — so an off-root session is rejected before any side
	// effect. reopenLoadedSession also enforces this (the shared choke point), so
	// this call is a deliberate earlier gate, not the sole defense.
	if err := s.validatePersistedWorkspace(sess); err != nil {
		return nil, err
	}
	if err := admitRunPurpose(sess, purpose); err != nil {
		return nil, err
	}
	if _, _, pending := sess.FailedStepRetryPending(); pending {
		return nil, fmt.Errorf("%w: session %q has a pending failed-step retry", ErrFailedPrecondition, id)
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
	// Register a cancellable provisional lifecycle before lease acquisition and
	// engine construction. Drain can now cancel admission even in the gap between
	// acquiring ownership and constructing the run.
	st, admissionParent, err := s.beginRunAdmission(ctx, id, sess, false)
	if err != nil {
		return nil, err
	}
	promoted := false
	defer s.cleanupRunAdmission(id, st, &promoted)
	// Cross-process single-writer gate: take the session lease after runEntryMu and
	// before terminal recovery can mutate or persist the aggregate.
	if err := s.acquireLease(admissionParent, id); err != nil {
		return nil, err
	}
	// Bind every fallible admission step to this exact hold. Renewal loss cancels
	// construction and the final revalidation below prevents provider/tool start.
	admissionCtx, stopAdmission, leaseHeld := s.mutationLeaseContext(admissionParent, id)
	defer func() {
		if !promoted {
			stopAdmission()
		}
	}()
	ctx = admissionCtx
	// Apply the unchanged reopen/interrupt/recover funnel only after the trusted
	// purpose and ownership gates and lease acquisition. Rejected kinds are never
	// mutated as a side effect of probing.
	sess, err = s.reopenLoadedSession(ctx, sess)
	if err != nil {
		return nil, err
	}
	// Hold the per-session run-entry lock across engine-resolve (which may REBUILD a
	// per-session engine for a mode→model change, ADR 0030 Layer 3) + run launch +
	// register. The lock was acquired before loading so the entire run-entry
	// transaction observes one authoritative snapshot.
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
	sess, interruptedAuthorization, err := s.interruptRestoredAuthorizationLocked(ctx, sess)
	if err != nil {
		return nil, err
	}
	// interruptRestoredAuthorizationLocked may reload/replace the aggregate
	// (e.g. to interrupt a restored authorizing session); st.sess was captured
	// by beginRunAdmission above from the PRE-repair load, so it must be kept
	// in sync or Persist/GracefulDrain would observe the stale object.
	st.sess = sess
	// interruptedContinuationOwned is non-nil only on the interruptedAuthorization
	// branch below; the flag it points to starts false and must flip true at the
	// exact moment a real Engine.Run takes ownership (BeginRun below), or the
	// deferred repair stays armed for every return in between.
	var interruptedContinuationOwned *bool
	if !interruptedAuthorization {
		if err := s.repairRunningSession(ctx, sess); err != nil {
			return nil, err
		}
	} else {
		// interruptRestoredAuthorizationLocked already durably saved sess
		// StateRunning, correct ONLY because this function is about to hand it
		// to a real Engine.Run below. Every return between here and that
		// handoff leaves the same durable StateRunning with no owning run —
		// the exact stranded-snapshot shape repairRunningSession exists for.
		owned := false
		interruptedContinuationOwned = &owned
		defer func() {
			if !*interruptedContinuationOwned {
				_ = s.repairRunningSession(context.WithoutCancel(ctx), sess)
			}
		}()
	}
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	// Mint this run's identity and stamp it on the aggregate BEFORE launching, so
	// the id is on the snapshot the moment the run can park awaiting an approval —
	// which is what lets a cross-process resume continue THE SAME run rather than
	// mint a second one (ADR 0249). Every prompt-entry path funnels through here
	// (StartRun, RetryFailedRun, scheduler fires, steer promotion), so this is the
	// one mint site for a new run.
	runID := newRunID()
	sess.BeginRun(runID)
	if !leaseHeld() {
		return nil, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	ctx = memory.WithWorkspace(ctx, env.Workspace().Root())
	run, err := s.promoteRunAdmission(id, st, stopAdmission, func() *agent.Run {
		return engine.Run(ctx, sess, env, agent.RunRequest{Text: text, Parts: parts, RunID: runID, CanPresentAuthorization: canPresentAuthorization})
	})
	if err != nil {
		return nil, err
	}
	// engine.Run is now actively driving sess in its own goroutine and owns its
	// persistence from here — the deferred repair above must stand down.
	if interruptedContinuationOwned != nil {
		*interruptedContinuationOwned = true
	}
	promoted = true
	return run, nil
}

func (s *Service) repairRunningSession(ctx context.Context, sess *session.Session) error {
	if sess.State != session.StateRunning {
		return nil
	}
	if err := sess.Abandon(); err != nil {
		return fmt.Errorf("server: abandon stale running session: %w", err)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return fmt.Errorf("server: persist abandoned session: %w", err)
	}
	return nil
}

// Steer routes an operator steer (mid-run injected input, issue #512) for a
// session to the right home. It is the Service-level routing decision the
// wire-facing steer handler drives: the steer NEVER drops silently.
//
//   - LIVE run: if a run is registered AND its steer inbox still accepts it,
//     the text enqueues to that run's steer inbox (the run drains it at the next
//     turn boundary) and the function reports the engine's authoritative outcome
//     (accepted / appended) with promoted=false.
//   - LOST TERMINAL RACE: no live run, or the live run's inbox already closed
//     (the engine reported too_late — the run went terminal behind the caller's
//     "still running" belief): the steer is PROMOTED into a fresh follow-up run
//     through the EXISTING hardened run-entry funnel — StartRunContent
//     (loadAndReopen + the lease + recover-if-completed / interrupt-if-cancelled
//     / recover-if-failed / abandon-if-crash-orphaned-running) — exactly like a
//     normal follow-up prompt, and reported as (agent.SteerTooLate, true).
//
// The returned promotedRun (non-nil only when promoted) is the registered
// follow-up run the caller must drain + FinishRun, exactly as StartRunContent's
// caller does. An unknown session id yields ErrNotFound (via the funnel); a
// terminal-state repair failure surfaces as the funnel's error.
//
// messageID is the client-minted correlation id of the Steer frame ("" when the
// caller supplied none). The engine parks it atomically with the pending content
// so the EvSteer drain echo carries the exact bundle watermark. The ACK-side echo
// is the caller's own frame field.
func (s *Service) Steer(ctx context.Context, id session.SessionID, text string, parts []session.Content, messageID, expectedRunID string) (agent.SteerOutcome, bool, *agent.Run, error) {
	generation := s.captureRunEntryGeneration(id)
	// Authorize before touching the in-memory registry or the run-entry funnel:
	// a steer injects caller input into a run / drives a follow-up, so a foreign
	// request must be absence-equivalent (ErrNotFound), mirroring Cancel/Approve.
	if _, err := s.GetSession(ctx, id); err != nil {
		return agent.SteerTooLate, false, nil, err
	}
	if err := validateSteerMessageID(messageID); err != nil {
		return agent.SteerTooLate, false, nil, err
	}
	// Live-run fast path: validate the exact lease hold and enqueue while holding
	// the service lock, ordering admission atomically against lease invalidation.
	s.mu.Lock()
	if err := s.validateRunEntryGenerationLocked(id, generation); err != nil {
		s.mu.Unlock()
		return agent.SteerTooLate, false, nil, err
	}
	st := s.runs[id]
	if st != nil && st.run != nil {
		if st.cancelling {
			s.mu.Unlock()
			return agent.SteerTooLate, false, nil, ErrNoActiveRun
		}
		if s.cfg.SessionLease != nil && !s.leaseDisabled {
			h := s.heldLeases[id]
			if h == nil || !h.valid || h.ctx.Err() != nil {
				s.mu.Unlock()
				return agent.SteerTooLate, false, nil, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
			}
		}
		run := st.run
		if err := checkExpectedRun(expectedRunID, run.RunID()); err != nil {
			s.mu.Unlock()
			return agent.SteerTooLate, false, nil, err
		}
		outcome, err := run.EnqueueSteerWithMessageID(text, parts, messageID)
		s.mu.Unlock()
		if err == nil && outcome != agent.SteerTooLate {
			return outcome, false, nil, nil
		}
		if err != nil {
			return outcome, false, nil, fmt.Errorf("server: steer enqueue: %w", err)
		}
	} else {
		s.mu.Unlock()
	}
	// Terminal race → promote through the run-entry funnel. An unknown id or an
	// unrepaired-terminal-state error surfaces here rather than ever dropping.
	//
	// Drain-grace: the too_late steer usually arrives in
	// the TERMINATE WINDOW — the original run went terminal (its inbox closed)
	// but its relay is still draining, so the run is still REGISTERED and a bare
	// StartRunContent would hit the funnel's IsLive / StateRunning guards and
	// drop the steer with a bare too_late ack. The promote path is the ONE place
	// that must wait out that drain: only a terminal-but-still-registered run
	// can deregister within steerPromoteGrace (a genuinely in-flight run's relay
	// drains continuously, so the lapse correctly refuses it), and ONLY the
	// promoted steer pays the wait — the shared funnel stays byte-identical, so
	// a concurrent legitimate prompt on the same live session is never wrongly
	// delayed.
	// STRICT STEER (ADR 0249): a caller that named a specific run did NOT ask to
	// start a different one. Promotion is the right default for an unqualified
	// steer — the operator meant "say this to the agent", and a fresh follow-up
	// run says it — but it is the wrong answer for "say this to run X", where X
	// has already ended. Refusing is the honest outcome, and it is what lets an
	// SDK offer a steer that never surprises a caller with an extra run.
	//
	// The guard is here rather than at the top because the live path above may
	// still succeed: expected_run_id only forbids PROMOTION, it does not forbid
	// steering the run it names.
	if expectedRunID != "" {
		return agent.SteerTooLate, false, nil, checkExpectedRun(expectedRunID, "")
	}
	promotedRun, err := s.promotedSteerRun(ctx, id, text, parts, generation)
	if err != nil {
		return agent.SteerTooLate, false, nil, err
	}
	return agent.SteerTooLate, true, promotedRun, nil
}

// promotedSteerRun is Service.Steer's promote path: try the funnel
// once, and only on a liveness conflict await the original run's deregister
// (bounded by steerPromoteGrace) then retry — so the just-terminal,
// still-draining run clears and the follow-up drives through the hardened
// funnel instead of dropping the steer. Holding the wait HERE — never inside
// the shared StartRunContent — keeps the concurrent-live-prompt contract
// byte-identical: only the promoted steer waits out a drain, so a prompt on a
// genuinely-live session is not slowed by the grace. An unknown session id
// surfaces ErrNotFound from the first funnel call.
func (s *Service) promotedSteerRun(ctx context.Context, id session.SessionID, text string, parts []session.Content, generation runEntryGeneration) (*agent.Run, error) {
	run, err := s.startRunContent(ctx, id, text, parts, runPurposeChat, generation, false)
	if err == nil {
		s.notifySteerPromotionRegistered()
		return run, nil // no live run blocked the entry — promoted immediately
	}
	if !errors.Is(err, ErrFailedPrecondition) {
		return nil, err // not a liveness conflict — surface it (e.g. ErrNotFound)
	}
	// Liveness conflict: the original run is still registered. If it is the
	// terminal-but-draining run the grace exists for, it clears within
	// steerPromoteGrace; a run still registered at the lapse is genuinely
	// in-flight (its relay drains continuously), so the lapse refuses the
	// promotion. Only a terminal-but-still-registered run can possibly
	// deregister inside the grace, so the wait is near-zero after the relay
	// finished and correctly bounds the refusal.
	if !s.awaitRunDeregister(ctx, id, steerPromoteGrace) {
		return nil, err // the run is genuinely in-flight — refuse the promotion
	}
	// Registry cleared: the original relay finished and the run's final terminal
	// state is durable. Drive the follow-up through the hardened funnel, which
	// now sees the terminal state and reopens it.
	run, err = s.startRunContent(ctx, id, text, parts, runPurposeChat, generation, false)
	if err == nil {
		s.notifySteerPromotionRegistered()
	}
	return run, err
}

func (s *Service) notifySteerPromotionRegistered() {
	s.mu.Lock()
	notify := s.steerPromotionRegistered
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// CancelSteer retracts the session's live run's PENDING (un-drained) steer,
// reporting the engine's authoritative outcome (retracted / none_pending). It is
// the Service-level owner of the steer_cancel route —
// the wire handler drives THIS (mirror of how Cancel routes through the
// Service), so the live-run lookup stays single-owner and the deferred HTTP/SSE
// steer endpoint reuses the same Service decision rather than re-deriving it. A
// steer that already drained at a turn boundary is ordinary recorded history and
// cannot be retracted (the engine reports none_pending then: the drain won). A
// session with no live run reports none_pending (there is no inbox to retract
// from — the steer that would be pending is already lost with its run, the
// best-effort in-memory contract the docs/acceptance/steer-while-running.md
// Scenario-2 contract records). expectedRunID has the same optional strictness
// as the other run controls: when set, no absent or replacement run may be
// touched. The wire's steer_cancel message_id never crosses the Service (the
// ack-side echo is the caller's own frame field), so the signature stays
// correlation-id-free.
func (s *Service) CancelSteer(ctx context.Context, id session.SessionID, expectedRunID string) (agent.SteerOutcome, error) {
	// Authorize before the registry read, mirroring Cancel: a foreign request is
	// absence-equivalent (ErrNotFound), never a peek at another caller's inbox.
	if _, err := s.GetSession(ctx, id); err != nil {
		return agent.SteerNonePending, err
	}
	s.mu.Lock()
	st := s.runs[id]
	if st == nil || st.run == nil {
		s.mu.Unlock()
		if expectedRunID != "" {
			return agent.SteerNonePending, checkExpectedRun(expectedRunID, "")
		}
		return agent.SteerNonePending, nil
	}
	run := st.run
	if err := checkExpectedRun(expectedRunID, run.RunID()); err != nil {
		s.mu.Unlock()
		return agent.SteerNonePending, err
	}
	outcome, err := run.CancelSteer()
	s.mu.Unlock()
	if err != nil {
		return outcome, fmt.Errorf("server: steer cancel: %w", err)
	}
	return outcome, nil
}

// maxSteerMessageIDRunes bounds a client-minted message id before it enters the
// engine bundle and every downstream log, event, or diagnostic echo.
const maxSteerMessageIDRunes = 64

func validateSteerMessageID(messageID string) error {
	if utf8.RuneCountInString(messageID) > maxSteerMessageIDRunes {
		return fmt.Errorf("%w: message_id exceeds %d characters", ErrInvalidArgument, maxSteerMessageIDRunes)
	}
	return nil
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
		if (kind == session.SessionKindMain || kind == session.SessionKindDebug) && !hasLegacyNonChatPrefix(sess.ID) {
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
// for a rehydrated model-routed or no-FS session. Every path exactly reattaches
// the persisted EnvironmentRef; neither path follows the current default.
//
// Resolution order: exact placement reattachment validates the durable binding first.
// An authorized per-session environment overlay (for example ACP buffers) may then be
// used only when its identity and namespace match that binding. A per-session engine
// (client MCP, provider/model routing, no-fs, or non-default placement root) is
// preferred; otherwise the shared engine is used.
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
	attribution := s.ResolvedModel(id)
	sess.SetUsageAttribution(attribution.ProviderID, attribution.ModelID)
	engine := s.cfg.Engine
	s.mu.Lock()
	se, hasEngine := s.sessionEngines[id]
	envOverride, hasEnvOverride := s.sessionEnvironments[id]
	s.mu.Unlock()
	if !sess.EnvironmentRef.Valid() {
		return nil, tool.Environment{}, ErrInvalidPlacementSelection
	}
	verified, err := s.ReattachPlacement(ctx, sess.EnvironmentRef)
	if err != nil {
		return nil, tool.Environment{}, err
	}
	if s.cfg.SessionReadLedger != nil {
		ledger := s.cfg.SessionReadLedger(id)
		if ledger == nil {
			return nil, tool.Environment{}, fmt.Errorf("%w: session read-ledger factory returned nil", ErrConfig)
		}
		verified.Environment, err = tool.NewEnvironment(verified.Ref, verified.Environment.Workspace(), ledger, verified.Environment.CommandRunner())
		if err != nil {
			return nil, tool.Environment{}, fmt.Errorf("%w: bind session read ledger: %v", ErrConfig, err)
		}
	}
	verifiedPlacement := &verified
	if hasEnvOverride {
		if err := s.validateEnvironmentOverride(sess, envOverride, verifiedPlacement.Environment); err != nil {
			return nil, tool.Environment{}, err
		}
	}
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
		st := s.runs[id]
		live := st != nil && st.run != nil
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
	placementNeedsEngine := !hasEngine && !s.needsRehydration(sess) &&
		sess.EnvironmentRef.Kind != session.EnvKindNoFS &&
		verifiedPlacement.Environment.Workspace().Root() != s.cfg.SharedEngineRoot
	if !hasEngine && (s.needsRehydration(sess) || placementNeedsEngine) {
		// RESTART REHYDRATION (issue #55, widened in the cloud-native Phase 1): a
		// PERSISTED session that needed a PER-SESSION engine — a non-default
		// provider/model selector, OR the no-fs profile — has its engine + (for no-fs)
		// its environment override living only in process memory; after a restart both
		// are gone. Without rehydration the session would silently DEGRADE onto the
		// shared engine: a no-fs session would gain the wrong FS-capable tool surface,
		// and a selector session could run the wrong model. Rebuild the same engine
		// through the factory path used at creation, reading persisted selector and
		// profile labels.
		var err error
		se, err = s.rehydrateSession(ctx, sess)
		if err != nil {
			return nil, tool.Environment{}, err
		}
		hasEngine = true
		// Placement environments are never restored from the override registry;
		// every ordinary run reattaches a fresh provider binding below.
	}
	if hasEngine {
		engine = se.engine
	}
	if hasEnvOverride {
		// The override was authorized and namespace-matched before any engine
		// selection or factory call above could observe it. ACP buffer and no-FS
		// overrides are complete environments: the creator supplied the accurate ref
		// and the correct (possibly nil) CommandRunner. Use them directly; never guess
		// a ref, namespace, or runner from the override's presence.
		return engine, envOverride, nil
	}
	return engine, verifiedPlacement.Environment, nil
}

// sessionNeedsPerFactory reports whether a CreateSession with the given inputs
// must route through the per-session engine factory (rather than the shared
// engine fast path). It is the single expression behind needPerSession in
// createSession, extracted so createSession stays under the cyclomatic cap. The
// workspace is compared with the verified shared-engine policy root; a mismatch
// routes through the factory so children pin
// their resolver to the session root. This comparison is unconditional for
// filesystem-capable placements: a rootless shared deployment must not run a
// custom non-empty namespace with rootless policy collaborators. The
// DefaultModelPending arm (issue #262 review finding 1) routes EVERY
// zero-selector session through the factory when the shared engine booted
// with an unresolved intent-driven default model, so the per-session build
// resolves it at session-build time instead of freezing "".
func (s *Service) sessionNeedsPerFactory(sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string) bool {
	return sel != (ProviderSelector{}) || len(specs) > 0 || profile == ProfileNoFS ||
		s.cfg.DefaultModelPending || workspace != s.cfg.SharedEngineRoot
}

// needsRehydration reports whether a loaded session with no live per-session engine
// must rebuild one before running. Persisted profile/provider/model/reasoning labels,
// debug/learned-skill scope, and unresolved default-model intent drive this decision.
// Placement does not: engineAndEnvironmentFor always exactly reattaches EnvironmentRef,
// then separately compares the verified live root with SharedEngineRoot to decide whether
// placement affinity needs a per-session engine.
func (s *Service) needsRehydration(sess *session.Session) bool {
	return s.cfg.MCPBroker != nil || s.cfg.LearnedSkills != nil ||
		sess.Kind == session.SessionKindDebug ||
		sess.Profile == string(ProfileNoFS) ||
		sess.ProviderID != "" || sess.ModelID != "" ||
		sess.ReasoningEffort != "" ||
		s.cfg.DefaultModelPending
}

// profileForSession reconstructs the tool-surface profile from server-owned durable
// state. The explicit profile label is primary; an exact no-FS EnvironmentRef also
// requires the no-FS catalog so profile metadata cannot widen its placement authority.
// No path, workspace emptiness, or current deployment default participates.
func profileForSession(sess *session.Session) SessionProfile {
	switch {
	case sess.Profile == string(ProfileNoFS):
		return ProfileNoFS
	case sess.Profile == "" && sess.EnvironmentRef.Kind == session.EnvKindNoFS:
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
	if sess.Kind == session.SessionKindDebug {
		if sess.Profile != string(ProfileNoFS) || sess.EnvironmentRef.Kind != session.EnvKindNoFS || sess.Relationship.DebugTargetID == "" || sess.DebugTargetFingerprint == "" {
			return nil, fmt.Errorf("%w: persisted debug session %q has invalid no-fs metadata", ErrInvalidArgument, sess.ID)
		}
		if s.cfg.DebugSessionEngine == nil {
			return nil, fmt.Errorf("%w: persisted debug session %q cannot be rehydrated (no debug-session engine factory configured)", ErrInvalidArgument, sess.ID)
		}
	} else if s.cfg.SessionEngine == nil {
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
//
//nolint:gocyclo // Explicit validation, rebuild, broker, capacity, and rollback gates stay ordered.
func (s *Service) buildAndRegisterSessionEngine(ctx context.Context, sess *session.Session, sel ProviderSelector, profile SessionProfile, mode session.PermissionMode, replace bool) (*sessionEngine, error) {
	return s.buildAndRegisterSessionEngineWithBrokerTools(ctx, sess, sel, profile, mode, replace, nil, false)
}

//nolint:gocyclo // rehydration keeps validation, factory selection, broker, capacity, and rollback gates ordered; inherent.
func (s *Service) buildAndRegisterSessionEngineWithBrokerTools(ctx context.Context, sess *session.Session, sel ProviderSelector, profile SessionProfile, mode session.PermissionMode, replace bool, exactTools []tool.Tool, useExactTools bool) (*sessionEngine, error) {
	id := sess.ID
	unlockBroker := s.brokerMu.lock(id)
	defer unlockBroker()
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
	var res SessionEngineResult
	var err error
	var broker *localBrokerAttachment
	var brokerCommitted bool
	// The session's original client-supplied MCP specs, if any: a rebuild must
	// carry them forward or client-provided MCP tools silently disappear (they
	// are otherwise threaded through only once, at session creation/load).
	s.mu.Lock()
	specs := s.clientMCPSpecs[id]
	s.mu.Unlock()
	if sess.Kind == session.SessionKindDebug {
		target, loadErr := s.cfg.Store.Load(ctx, sess.Relationship.DebugTargetID)
		if loadErr != nil || target == nil || sess.DebugTargetFingerprint == "" || !sess.Relationship.DebugTargetIncarnation.Valid() ||
			target.Incarnation() != sess.Relationship.DebugTargetIncarnation ||
			session.DebugTargetFingerprint(target) != sess.DebugTargetFingerprint ||
			s.cfg.OwnershipEnforced && session.PrincipalScopeHash(target.Owner) != session.PrincipalScopeHash(sess.Owner) ||
			s.authorizeSession(ctx, target) != nil {
			return nil, fmt.Errorf("%w: debug target is stale or inaccessible", ErrNotFound)
		}
		res, err = s.cfg.DebugSessionEngine(ctx, sel, profile, mode, sess.Relationship.DebugTargetID, sess.DebugTargetFingerprint, target.Owner, sess.DebugMCPServers, sess.DebugMCPTools)
	} else if useExactTools {
		workspace, workspaceErr := s.privateWorkspace(ctx, sess)
		if workspaceErr != nil {
			return nil, workspaceErr
		}
		res, err = s.callSessionEngine(ctx, sel, specs, profile, workspace, mode, append([]tool.Tool(nil), exactTools...))
	} else {
		broker, err = s.openBrokerAttachment(ctx, id, sess.ExternalBinding, true)
		if err != nil {
			return nil, err
		}
		defer s.finalizeBrokerAttachment(broker, &brokerCommitted)
		workspace, workspaceErr := s.privateWorkspace(ctx, sess)
		if workspaceErr != nil {
			return nil, workspaceErr
		}
		res, err = s.callSessionEngine(ctx, sel, specs, profile, workspace, mode, brokerTools(broker))
	}
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
		// from under the in-flight run. A provisional entry owned by the serialized
		// run admission is not live yet and is exactly what this rebuild prepares.
		// A run PARKED for authorization (RunOutcomeAuthorizationPending) is the one
		// deliberate exception, mirroring registerPrepared's own carve-out: nothing
		// reads the engine while parked, and continueGrantedAuthorizationLocked needs
		// exactly this rebuild — with the freshly authenticated tool catalogue —
		// before it resumes that same parked call.
		st := s.runs[id]
		if st != nil && st.run != nil && st.run.Outcome() != agent.RunOutcomeAuthorizationPending {
			s.mu.Unlock()
			if se.close != nil {
				_ = se.close()
			}
			return nil, fmt.Errorf("%w: cannot rebuild engine for session %q mid-run (mode change must be deferred to a turn boundary)", ErrInvalidArgument, id)
		}
	}
	s.sessionEngines[id] = se
	if profile == ProfileNoFS && (s.placementBinder == nil || !sess.EnvironmentRef.Valid()) {
		// Re-register the no-fs environment override under the SAME lock as the engine
		// (the create-time discipline), so the run below — and every later run —
		// resolves its environment here and never consults the shared factory with the
		// empty root. It is a complete shell-less Environment with an honest nofs ref.
		// A selector session with a real workspace needs no override: the run-entry
		// seam builds its environment from the shared factory as usual.
		s.sessionEnvironments[id] = tool.MustEnvironment(sess.EnvironmentRef, nofs.New(), memledger.New(), nil)
	}
	s.mu.Unlock()
	if broker != nil {
		commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
		commitErr := s.commitBrokerAttachment(commitCtx, id, broker)
		cancelCommit()
		if commitErr != nil {
			s.mu.Lock()
			if s.sessionEngines[id] == se {
				if replace && hadPrior {
					s.sessionEngines[id] = prior
				} else {
					delete(s.sessionEngines, id)
				}
				delete(s.sessionEnvironments, id)
			}
			s.mu.Unlock()
			if se.close != nil {
				_ = se.close()
			}
			return nil, fmt.Errorf("%w: %v", ErrInternal, commitErr)
		}
		brokerCommitted = true
	}
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
	if !ok || st.run == nil {
		return nil, false
	}
	return st.run, true
}

// IsLive reports whether a top-level Service run or an engine-owned delegation
// child is currently in flight in this process. The two registries share one
// predicate so stale reconciliation and every destructive maintenance path see
// the same process-local exclusion. Cross-process liveness is protected by the
// separately configured SessionLease.
func (s *Service) IsLive(id session.SessionID) bool {
	s.mu.Lock()
	_, topLevel := s.runs[id]
	s.mu.Unlock()
	return topLevel || s.cfg.SessionLiveness != nil && s.cfg.SessionLiveness.IsLive(id)
}

// steerPromoteGrace bounds the drain-grace promotedSteerRun waits for a
// just-terminal run's relay to FinishRun-deregister it —
// the promoted steer must not bounce off a liveness guard into the drop-and-ack
// path while the run's terminal relay drain is still completing. The value is
// conservatively long (2s) so a backlogged relay comfortably finishes; a run
// still registered when it lapses is genuinely in-flight (a busy run's relay
// drains continuously, so only a terminal-but-still-registered run can possibly
// deregister inside the grace).
const steerPromoteGrace = 2 * time.Second

// steerPromotePoll is the poll quantum awaitRunDeregister re-checks the
// registration between — short enough that the promoted entry sees the cleared
// registry promptly, long enough that s.mu is not hot-spun under the wait.
const steerPromotePoll = 20 * time.Millisecond

// awaitRunDeregister blocks until no run is registered for id (the in-flight
// registry observation StartRunContent's liveness guards read), the grace
// lapses, or ctx is cancelled, returning true when the registry cleared. It is
// a pure registry OBSERVATION (poll, never mutation): it never abandons or
// re-homes a live run's session — the only safe read that lets the promoted
// steer wait out a terminal-but-still-draining run without racing it.
func (s *Service) awaitRunDeregister(ctx context.Context, id session.SessionID, grace time.Duration) bool {
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	tick := time.NewTicker(steerPromotePoll)
	defer tick.Stop()
	for {
		if !s.IsLive(id) {
			return true
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// approveLiveRun routes an in-stream verdict through the Service-owned lease gate.
// The service mutex orders the approval with renewal-loss invalidation; a surfaced
// child ask is still addressed through its parent run's approval router.
func (s *Service) approveLiveRun(id session.SessionID, target *agent.Run, askID string, verdict session.ApprovalVerdict, expectedRunID string) error {
	if s.draining.Load() {
		return fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		return ErrNoActiveRun
	}
	return s.approveRunState(id, st, target, askID, verdict, expectedRunID)
}

// approveRunState crosses the persistence barrier for the runState captured by
// approveLiveRun, then proves that exact state is still registered before it
// signals the run. The explicit captured state keeps the post-barrier registry
// identity proof in one place.
func (s *Service) approveRunState(id session.SessionID, st *runState, target *agent.Run, askID string, verdict session.ApprovalVerdict, expectedRunID string) error {
	// Persist and a control signal must be one ordered transaction. Persist takes
	// persistMu before revalidating under s.mu, so retain that lock order here.
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id] != st || st.run == nil || st.run != target || st.cancelling {
		return ErrNoActiveRun
	}
	if s.cfg.SessionLease != nil && !s.leaseDisabled {
		h := s.heldLeases[id]
		if h == nil || !h.valid || h.ctx.Err() != nil {
			return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
		}
	}
	if err := checkExpectedRun(expectedRunID, target.RunID()); err != nil {
		return err
	}
	// Set before waking the run. If permission.ask is still buffered, its relay
	// observes this marker and skips the now-stale awaiting snapshot.
	st.resolvedAskID = askID
	target.Approve(askID, verdict)
	return nil
}

// Approve resolves the paused permission ask on the session's in-flight run with
// the client's three-way verdict (deny / allow-once / allow-always).
//
// SAME-PROCESS path FIRST: a live registered run resolves the ask over its
// in-memory channel while this process still holds the session lease. On a
// LookupRun MISS — typically the
// process that parked the ask died and a different process now serves the Approve —
// it falls to resumeFromAwaiting (cloud-native Phase 2): if the persisted session is
// in StateAwaiting it loads the snapshot, rebuilds the engine, re-enters the loop AT
// the ask, applies the verdict, and drives to completion; the caller relays the
// returned run's events (the resumed run is registered like any other). A
// non-awaiting (idle/completed/cancelled/failed) session stays terminal and yields
// ErrNoActiveRun; an unknown session yields ErrNotFound. The returned run, when
// non-nil, is the resumed run the wire adapter must drain + FinishRun.
func (s *Service) Approve(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict) error {
	_, err := s.ApproveRun(ctx, id, askID, verdict, "")
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
// runs EXACTLY ONCE. The common live-run case takes the service lock only long
// enough to order approval against lease-loss invalidation.
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
func (s *Service) ApproveRun(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict, expectedRunID string) (*agent.Run, error) {
	generation := s.captureRunEntryGeneration(id)
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	// Authorize before reading the in-memory registry: a mismatch must be
	// indistinguishable from a missing handle and cannot signal a live run.
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	// Fast path: resolve a live local ask only while this process still owns its
	// session mutation capability. Keep the service lock through the registry
	// resolution so lease-loss invalidation and approval are ordered: whichever
	// wins the lock wins, and a verdict can never enter after declared loss.
	s.mu.Lock()
	if err := s.validateRunEntryGenerationLocked(id, generation); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	st, ok := s.runs[id]
	if ok {
		if st.cancelling {
			s.mu.Unlock()
			return nil, ErrNoActiveRun
		}
		if st.run == nil {
			resumeAdmission := st.resumeAdmission
			s.mu.Unlock()
			if resumeAdmission {
				return s.resumeFromAwaiting(ctx, id, askID, verdict, expectedRunID, generation)
			}
			return nil, ErrNoActiveRun
		}
		run := st.run
		s.mu.Unlock()
		// Compare against and signal the run that would ACTUALLY receive the
		// verdict. approveLiveRun revalidates the registry + lease after crossing
		// the awaiting-persistence barrier.
		if err := s.approveLiveRun(id, run, askID, verdict, expectedRunID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	s.mu.Unlock()
	return s.resumeFromAwaiting(ctx, id, askID, verdict, expectedRunID, generation)
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
//
//nolint:gocyclo // Approval resume keeps generation, lock ordering, lease, and exact-once launch in one transaction.
func (s *Service) resumeFromAwaiting(ctx context.Context, id session.SessionID, askID string, verdict session.ApprovalVerdict, expectedRunID string, generation runEntryGeneration) (*agent.Run, error) {
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	unlock := s.resumeMu.lock(id)
	defer unlock()
	entryUnlock := s.runEntryMu.lock(id)
	defer entryUnlock()
	if err := s.validateRunEntryGeneration(id, generation); err != nil {
		return nil, err
	}

	// Re-check under the locks: a concurrent resume that won the race has registered a
	// live run. Route this verdict only while its local lease capability remains
	// valid, using the same service-lock ordering as ApproveRun's fast path.
	s.mu.Lock()
	st, ok := s.runs[id]
	if ok {
		run := st.approvalRun()
		s.mu.Unlock()
		if run == nil {
			return nil, ErrNoActiveRun
		}
		if err := s.approveLiveRun(id, run, askID, verdict, expectedRunID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	s.mu.Unlock()

	// ADR 0030 Layer 3 note: engineAndEnvironmentFor's mode→model rebuild (CASE 1) is a
	// NO-OP here. SetMode is rejected from StateAwaiting by the aggregate, so a parked
	// session's Mode cannot have changed since its engine was built — se.builtForMode ==
	// sess.Mode always holds, and the stale-mode branch never fires. (A restart-parked
	// awaiting session is rehydrated on its persisted Mode first, so the rebuilt engine's
	// builtForMode matches too.) The model is fixed for the resumed turn.

	// GetSession (read-only snapshot load): ErrNotFound for an unknown session. We do
	// NOT use loadAndReopen here — its job is to drive completed/cancelled/failed back
	// to idle for a NEW prompt, exactly the terminal states this seam must REJECT.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validatePersistedWorkspace(sess); err != nil {
		return nil, err
	}
	// On this path the persisted session IS the run — it parked awaiting the ask
	// and no live *agent.Run exists — so the stored id is the authoritative answer
	// to "which run would this verdict resolve?" (ADR 0249).
	if err := checkExpectedRun(expectedRunID, sess.RunID()); err != nil {
		return nil, err
	}
	if sess.State != session.StateAwaiting {
		// Awaiting is the only state Phase 2 makes non-terminal. Everything else stays
		// stranded-for-Approve as before (last-write-wins / nothing to resume).
		return nil, ErrNoActiveRun
	}
	st, admissionParent, err := s.beginRunAdmission(ctx, id, sess, true)
	if err != nil {
		return nil, err
	}
	promoted := false
	defer s.cleanupRunAdmission(id, st, &promoted)
	// Cross-process single-writer gate (cloud-native Phase 4): the resumed run is a
	// run-entry like any other, so it acquires the session lease too — a competing
	// process that took over this evicted session must refuse the resume.
	if err := s.acquireLease(admissionParent, id); err != nil {
		return nil, err
	}
	admissionCtx, stopAdmission, leaseHeld := s.mutationLeaseContext(admissionParent, id)
	defer func() {
		if !promoted {
			stopAdmission()
		}
	}()
	ctx = admissionCtx
	engine, env, err := s.engineAndEnvironmentFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !leaseHeld() {
		return nil, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	ctx = memory.WithWorkspace(ctx, env.Workspace().Root())
	run, err := s.promoteRunAdmission(id, st, stopAdmission, func() *agent.Run {
		return engine.ResumeApproval(ctx, sess, env, askID, verdict)
	})
	if err != nil {
		return nil, err
	}
	promoted = true
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
	generation := s.captureRunEntryGeneration(id)
	return s.approvePlan(ctx, id, targetMode, note, generation)
}

func (s *Service) approvePlan(ctx context.Context, id session.SessionID, targetMode session.PermissionMode, note string, generation runEntryGeneration) (<-chan session.Event, error) {
	if s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	// Authorize and load before reading the in-memory registry. A foreign caller
	// must not learn that a run exists or trigger any live-run side effect.
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	// (1) A live run means an approve-mid-run: reject. The operator must use the
	// Converse ResumeApproval frame for a live run, not this atomic RPC.
	if _, ok := s.LookupRun(id); ok {
		return nil, fmt.Errorf("%w: session %q has a live run (use the Converse resume_approval frame for an in-flight run)", ErrNotAwaitingPlan, id)
	}
	// (2) Validate the plan-originated precondition and read the askID from the
	// authorized snapshot. resumeFromAwaiting reloads and revalidates under the
	// admission locks before any work can start.
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
	// ApprovePlan is an atomic RPC addressed at the session, not at a run: the
	// caller approves THE PLAN this session is parked on, and the askID is read
	// off the snapshot rather than supplied. There is no caller expectation to
	// enforce, so it passes no expected run id.
	resumed, rerr := s.resumeFromAwaiting(ctx, id, ask.AskID, verdict, "", generation)
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
		cont, cerr := s.startRunContent(ctx, id, proceed, nil, runPurposeChat, generation, false)
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
func (s *Service) Cancel(ctx context.Context, id session.SessionID, expectedRunID string) error {
	// Authorize before reading the in-memory registry: cancellation is a live
	// signal and a foreign request must be absence-equivalent.
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.runs[id]
	if st != nil && st.run != nil {
		run := st.run
		s.mu.Unlock()
		return s.cancelLiveRun(id, run, expectedRunID)
	}
	s.mu.Unlock()
	return s.noActiveRun(ctx, id)
}

// cancelLiveRun orders a cancellation signal against permission-ask
// persistence. It is shared by unary and stream controls so a detached run can
// never resume its aggregate while the relay snapshots StateAwaiting.
func (s *Service) cancelLiveRun(id session.SessionID, target *agent.Run, expectedRunID string) error {
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		return ErrNoActiveRun
	}
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id] != st || st.run == nil || st.run != target || st.cancelling {
		return ErrNoActiveRun
	}
	if s.cfg.SessionLease != nil && !s.leaseDisabled {
		h := s.heldLeases[id]
		if h == nil || !h.valid || h.ctx.Err() != nil {
			return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
		}
	}
	if err := checkExpectedRun(expectedRunID, target.RunID()); err != nil {
		return err
	}
	// Set before waking the run. Any permission.ask already in the event buffer
	// is historical once cancellation wins and must not trigger an awaiting save.
	st.cancelSignaled = true
	target.Cancel()
	return nil
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
	// Serialize the durable-awaiting admission and marker resolution with drain's
	// lifecycle snapshot. Drain either sees the successful awaiting marker or
	// waits for a failed save and treats the run as non-awaiting.
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	s.mu.Lock()
	current := s.runs[id] == st
	s.mu.Unlock()
	if !current || st.preserveDurable.Load() {
		return
	}
	if !s.mutationLeaseHeld(id) {
		return
	}
	s.persistRun(ctx, id, st)
}

// persistPermissionAsk is Persist with control-event correlation. A detached
// relay can observe permission.ask after an approval/cancellation already won;
// in that ordering the aggregate is resuming and must not be read or persisted.
func (s *Service) persistPermissionAsk(ctx context.Context, id session.SessionID, askID string) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return
	}
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	s.mu.Lock()
	current := s.runs[id] == st
	s.mu.Unlock()
	if !current || st.preserveDurable.Load() || st.cancelSignaled {
		return
	}
	if st.resolvedAskID == askID && askID != "" {
		return
	}
	// A different ask proves any prior resolution marker is obsolete.
	st.resolvedAskID = ""
	if !s.mutationLeaseHeld(id) {
		return
	}
	s.persistRun(ctx, id, st)
}

func (s *Service) persistRun(ctx context.Context, id session.SessionID, st *runState) {
	// Save FIRST, then mark awaiting on success (H1 ordering): the flag must be
	// set only after the durable StateAwaiting snapshot has actually landed, so
	// Close (which skips cancelling awaiting runs) never skips a run whose
	// resumable snapshot was never persisted. Set-before-save would let Close
	// skip a run whose Save then fails, losing the resume point.
	err := s.saveSession(ctx, st.sess)
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
	if st.sess.TitleRevision != st.titleRevision {
		s.publishTitle(context.WithoutCancel(ctx), st.sess)
		st.titleRevision = st.sess.TitleRevision
	}
	// Title work is submitted only after the completed chat snapshot (including
	// the ingress-captured source) is durable. Submission is non-blocking.
	if st.sess.State == session.StateCompleted && st.sess.TitleGeneration == session.TitleGenerationPending && len(st.sess.TitleSourcePrompts()) > 0 {
		s.submitTitleGeneration(id)
	}
}

// completeRelay persists a terminal run before the relay removes its registry
// entry. It is deliberately internal: authorization occurred at run entry, while
// this late completion must retain dead-client persistence without a request
// principal.
func (s *Service) completeRelay(ctx context.Context, id session.SessionID, run *agent.Run) {
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if ok && st.run == run && (st.sess.State == session.StateCompleted || st.sess.State == session.StateCancelled || st.sess.State == session.StateFailed) {
		st.persistMu.Lock()
		defer st.persistMu.Unlock()
		s.persistRun(ctx, id, st)
	}
}

// finishRelayRun is the one terminal path for wire relays: persist first so a
// disconnected client cannot lose the terminal snapshot, then release the run.
func (s *Service) finishRelayRun(ctx context.Context, id session.SessionID, run *agent.Run) {
	s.completeRelay(ctx, id, run)
	s.deregister(id, run)
}

// appendEvent durably records one projected relay event to the configured
// EventLog. A nil EventLog is a no-op. The caller owns failure diagnostics so a
// run-scoped recorder can make the warning sticky while continuing later
// attempts.
//
// It is ALSO the SINGLE site that stamps session.Event.Actor (ADR 0204 decision
// 5): the attribution is derive-at-append, read from the CONTEXT PRINCIPAL — the
// verified caller who drove this request — so the loop stays storage- and
// identity-agnostic and every emit site leaves Actor nil. Callers pass a
// cancel-detached ctx (context.WithoutCancel), which preserves the context VALUES
// and therefore the caller. A request with no verified caller leaves it nil —
// absence is never fabricated. Do not add a second stamping path.
func (s *Service) appendEvent(ctx context.Context, id session.SessionID, ev session.Event) error {
	if s.cfg.EventLog == nil {
		return nil
	}
	if !s.mutationLeaseHeld(id) {
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
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
	// The cursor seam (ADR 0250) is used when the backend offers it, because THIS
	// is where a durable position is assigned. AC7.8 is a structural claim, not a
	// performance one: the position must be minted at the persistence chokepoint
	// and nowhere else — never at an emit site in the loop, which stays
	// storage-agnostic and never imports port.CursorEventLog at all.
	//
	// "Exactly one append per event" means one append per appended RECORD. The
	// RunEventRecorder deliberately COALESCES streaming text deltas into bounded
	// chunks before they reach here, so a turn of N delta events legitimately
	// becomes one record; what must never happen is the same record being appended
	// twice, or a second write path minting a rival position.
	//
	// The returned cursor is deliberately DISCARDED. Readers get their positions
	// from the log itself via ReadAfter, which is exactly what lets a watcher in
	// another process follow this append; remembering it here would create a second
	// source of truth that only the appending replica could see.
	if log := s.cursorLog; log != nil {
		if _, err := log.AppendEvent(ctx, id, ev); err != nil {
			s.noteAppendGap(ctx, id, log, err)
			return err
		}
		return nil
	}
	return s.cfg.EventLog.Append(ctx, id, ev)
}

// relayEvent applies the SHARED per-event relay discipline (cloud-native Phase
// 3a/3b + the plan-mode auto-approve observer, issue #206 Wave 6a) that every
// event-relay loop (gRPC Converse, gRPC ApprovePlan, HTTP relayRunSSE, HTTP
// relayEventsSSE) must run for EACH observed event, BEFORE the call site's own
// wire write. It returns forward=true when the event should be sent on the
// client wire, forward=false when it is log-only (consumed by the durable log
// ONLY, NOT relayed to the client). It performs, in order:
//
//  1. Observe the event through the run-scoped durable recorder. It buffers
//     streaming deltas and durably flushes them before this event when it is a
//     boundary; client liveness never gates observation, so the post-disconnect
//     tail still includes the terminal EvResult.
//  2. skip the client wire for the seven log-only kinds (EvApproval,
//     EvCompactionArchive, EvUserPrompt, EvNetworkAttempt, EvRequestManifest,
//     EvAuthorizationRequired, EvAuthorizationResolved) — recorded above but
//     NOT forwarded.
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
func (s *Service) relayEvent(ctx context.Context, id session.SessionID, ev session.Event, autoApprove bool, recorder *RunEventRecorder) (forward bool) {
	recorder.Observe(ev)
	// A non-ask event means the run is PROGRESSING (a tool result, a turn end, a
	// verdict, the terminal EvResult) — it is no longer parked awaiting. Clear the
	// runState.awaiting flag so Close's cancel loop does not skip a resumed-mid-
	// stream run (which would leave it StateRunning in the store on shutdown). The
	// flag is (re)set by Persist when EvPermissionAsk parks the run again. Race-free:
	// the atomic is mutated here on the relay thread and only read by Close. EvPermissionAsk
	// itself is handled below (Persist sets the flag), so it is excluded from this clear.
	if ev.Type != session.EvPermissionAsk {
		s.mu.Lock()
		st := s.runs[id]
		s.mu.Unlock()
		if st != nil {
			st.persistMu.Lock()
			st.awaiting.Store(false)
			st.persistMu.Unlock()
		}
	}
	// EvApproval (3a), EvCompactionArchive (3b), and EvUserPrompt (ADR 0038) are
	// consumed by the durable log ONLY — appended above but NOT relayed to the
	// client wire. EvNetworkAttempt (ADR 0255) and EvRequestManifest are the
	// remaining isPublicEvent exclusions. authorization.required/resolved ARE
	// relayed — cmd/mecatui/client/msgs.go decodes them into MCPAuthorizationMsg,
	// the client's only signal to show the MCP-authorization modal.
	if !isPublicEvent(ev) || ev.Type == session.EvApproval || ev.Type == session.EvCompactionArchive || ev.Type == session.EvUserPrompt {
		return false
	}
	if ev.Type == session.EvPermissionAsk {
		askID := ""
		if ev.Ask != nil {
			askID = ev.Ask.AskID
		}
		s.persistPermissionAsk(ctx, id, askID)
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
	generation := s.captureRunEntryGeneration(id)
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
		// Live run: deliver the verdict through the Service-owned lifecycle gate.
		// Clear may have marked this exact run cancelling after LookupRun; in that
		// case refuse both the verdict and its continuation.
		if err := s.approveLiveRun(id, run, ev.Ask.AskID, session.VerdictAllowOnce, ""); err != nil {
			s.cfg.Diagnostics.Log(ctx, port.LevelWarn,
				"plan_mode_auto_approve: auto-approve failed (ask stays parked)",
				"session", string(id), "err", err.Error())
			return
		}
		// The mode flips at the terminal boundary. A continuation run MUST then proceed — a headless auto-approve
		// has no operator to re-prompt, so leaving the session idle (completed at
		// StopPlanApproved) is useless. This mirrors the cross-process path
		// (ApprovePlan's atomic continuation) so BOTH live and cross-process
		// auto-approve end with an execution run, not a parked-completed session.
		// It stays composition-side: the loop only terminates StopPlanApproved +
		// flips the mode; THIS goroutine drives the continuation via the SAME
		// StartRunContent path ApprovePlan uses (loadAndReopen → execute model)
		// carrying agent.PlanApprovedProceedText.
		// Drive the continuation run in the background. The relay that owns the
		// ORIGINAL run's client stream drains the StopPlanApproved terminal; this
		// goroutine waits for the session to reach a terminal state (the verdict
		// terminated the live run) then starts the continuation, draining ITS
		// events to the durable log (appendEvent) so it never wedges. The
		// continuation's events are NOT relayed to the original client stream
		// (same discipline as the cross-process path's drain goroutine).
		go s.autoApproveContinuation(ctx, id, generation)
		return
	}
	// Cross-process: the run is dead, the session is parked in the store. Drive
	// the EXISTING ApprovePlan path (resumeFromAwaiting → continuation run).
	events, err := s.approvePlan(ctx, id, session.ModeDefault, "auto-approved: no human reviewed this plan", generation)
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
		recorder := NewRunEventRecorder(context.WithoutCancel(ctx), s, id)
		defer recorder.Close()
		for ev := range events {
			// Drain to completion — the continuation run's events are not relayed
			// to a client here (the client's stream is the original run's), but
			// the run must not wedge behind a full channel.
			recorder.Observe(ev)
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
func (s *Service) autoApproveContinuation(ctx context.Context, id session.SessionID, generation runEntryGeneration) {
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
	cont, cerr := s.startRunContent(logCtx, id, proceed, nil, runPurposeChat, generation, false)
	if cerr != nil {
		s.cfg.Diagnostics.Log(logCtx, port.LevelWarn,
			"plan_mode_auto_approve: continuation run failed to start",
			"session", string(id), "err", cerr.Error())
		return
	}
	recorder := NewRunEventRecorder(logCtx, s, id)
	for ev := range cont.Events() {
		recorder.Observe(ev)
	}
	recorder.Close()
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

// acquireMutationLeaseForStaleSettle is acquireMutationLease's counterpart for
// SettleIfStale ONLY (the composition-level stale-session reconcile sweep,
// issue #475). Every OTHER caller of acquireLease/acquireMutationLease
// deliberately fails fast forever once lostOwnership[id] is set — once this
// process has been told it lost a session's lease, it must never quietly
// resume acting as owner without an explicit CloseSession, even if the
// backend would technically permit a fresh Acquire (TestADR_0294_
// AwaitingLeaseLossRetractsLocalAskPreservesSnapshot and the Scenario5/7
// session-affinity-and-handoff tests pin this: a stale owner must stay
// refused even when no successor ever actually took the lease over).
//
// SettleIfStale is different: its caller is authorized as the system
// stale-reconciler (staleReconcileAuthorized, internal/adapter/server/
// stale_maintenance.go), reachable only from the composition-level sweep
// goroutine (internal/app/session_reconcile.go) — never from a gRPC/HTTP
// request, since admissiblePrincipal (authn.go) rejects any externally
// authenticated principal carrying the internal issuer or the system grant
// type (pinned by TestCallerIdentityEdgeRejectsMalformedPrincipal's "system
// grant"/"internal issuer sys" cases). That caller has already independently
// verified, via SessionStale's age-horizon-first test plus a local IsLive
// check, that id is a genuine crash orphan — never a live handoff in
// progress. For exactly that narrow, pre-verified case a real re-Acquire is
// safe: flocklease.Renew's ErrLeaseHeld does not distinguish "a real
// competitor took it" from "this record simply expired because a renew
// landed late" (a missed tick, GC pause, backend blip), so once real time has
// passed the record may simply be free again — and that is the only way a
// session recovered by the sweep (never closed, so closeSessionLocal's
// tombstone-clear is never reached) becomes re-acquirable short of a process
// restart. A genuine live successor still correctly refuses this via
// ErrLeaseHeld below, so this narrows the fail-fast; it does not weaken the
// exclusion. It deliberately skips acquireLease's drain gate (unlike every
// other caller): repairing an already-crash-orphaned session is cleanup, not
// a new admission, so a shutting-down replica settling one before it exits is
// safe and desirable, not something Drain() needs to steer away from.
func (s *Service) acquireMutationLeaseForStaleSettle(ctx context.Context, id session.SessionID) (func(), error) {
	s.mu.Lock()
	_, preHeld := s.heldLeases[id]
	s.mu.Unlock()
	if err := s.acquireLeaseCore(ctx, id, true); err != nil {
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

func (s *Service) mutationLeaseHeld(id session.SessionID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.SessionLease == nil || s.leaseDisabled {
		return true
	}
	h := s.heldLeases[id]
	return h != nil && h.valid && h.ctx.Err() == nil
}

func (s *Service) mutationLeaseContext(parent context.Context, id session.SessionID) (context.Context, func(), func() bool) {
	s.mu.Lock()
	h := s.heldLeases[id]
	leasingRequired := s.cfg.SessionLease != nil && !s.leaseDisabled
	s.mu.Unlock()
	if !leasingRequired {
		return parent, func() {}, func() bool { return true }
	}
	if h == nil {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}, func() bool { return false }
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(h.ctx, cancel)
	cleanup := func() {
		stop()
		cancel()
	}
	stillHeld := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.heldLeases[id] == h && h.valid && h.ctx.Err() == nil
	}
	return ctx, cleanup, stillHeld
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
	// paths are gated uniformly. Live approval/control paths perform their own
	// drain and exact-held-capability checks because they do not reacquire here.
	if s.draining.Load() {
		return fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	return s.reaffirmLease(ctx, id)
}

// reaffirmLease is acquireLease's body MINUS the new-run-entry drain gate. It
// exists for close/shutdown authorization settlement (prepareAuthorizationClose),
// which reaffirms a lease this process may already hold for ids discovered from
// its OWN bookkeeping (heldLeases/brokerAttachments/authorizationExpiry) — never
// a fresh admission — so the drain gate (which exists to refuse NEW run-entries)
// must not reject it: Close() legitimately runs after Drain() has armed.
func (s *Service) reaffirmLease(ctx context.Context, id session.SessionID) error {
	return s.acquireLeaseCore(ctx, id, false)
}

// acquireLeaseCore is the Acquire -> classify -> install-and-renew sequence
// shared by reaffirmLease (bypassTombstone=false: every normal caller —
// run-entry, ApproveRun's awaiting-resume, RenameSession/DeleteSession/other
// management mutations) and acquireMutationLeaseForStaleSettle
// (bypassTombstone=true: SettleIfStale ONLY). bypassTombstone is the ONE
// safety-relevant axis the two policies differ on — whether a prior
// definitive-loss tombstone (lostOwnership[id], set by onLeaseLost) hard-refuses
// before ever attempting a real Acquire, or is treated as stale evidence that
// deserves a genuine re-Acquire attempt. See reaffirmLease's and
// acquireMutationLeaseForStaleSettle's doc comments for why each policy is
// correct for its callers; do not change this parameter's meaning without
// re-reading both.
func (s *Service) acquireLeaseCore(ctx context.Context, id session.SessionID, bypassTombstone bool) error {
	if s.cfg.SessionLease == nil {
		return nil
	}
	s.mu.Lock()
	if s.leaseDisabled {
		s.mu.Unlock()
		return nil
	}
	if _, lost := s.lostOwnership[id]; lost && !bypassTombstone {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
	}
	if h, held := s.heldLeases[id]; held {
		valid := h.valid
		s.mu.Unlock()
		if valid {
			return nil // already ours for this session; acquire only on first entry.
		}
		if !bypassTombstone {
			return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
		}
		// bypassTombstone: an invalid held entry here is stale local bookkeeping
		// deliberately left behind by onLeaseLost's preserveAwaiting branch
		// (heldLeases[id] is NOT deleted there, only marked invalid). Fall through
		// to a real Acquire instead of permanently refusing — the post-Acquire
		// install below replaces it rather than mistaking it for a live winner.
	} else {
		s.mu.Unlock()
	}

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
		s.cfg.MutationCapability.Disable()
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
	// our just-started renewer for the duplicate. The dup check only collapses
	// against a VALID existing entry — a stale invalid one (the
	// bypassTombstone fall-through case above) must not be mistaken for a live
	// winner, or the freshly Acquired lease would be silently dropped without
	// ever being released.
	renewCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	if bypassTombstone {
		// The backend just proved id is free/ours again — any earlier
		// definitive-loss tombstone no longer applies to this now-verified-orphaned
		// session.
		delete(s.lostOwnership, id)
	}
	if existing, dup := s.heldLeases[id]; dup && existing.valid {
		s.mu.Unlock()
		cancel()
		return nil
	}
	h := &heldLease{lease: lease, ctx: renewCtx, cancel: cancel, valid: true}
	s.cfg.MutationCapability.Grant(id)
	s.heldLeases[id] = h
	s.mu.Unlock()
	go s.renewLoop(renewCtx, id, h)
	return nil
}

// renewLoop refreshes the held lease for id on a ticker until renewCtx is
// cancelled (CloseSession / shutdown). The renewer is OWNED BY Service -- the
// loop never imports port.SessionLease (the storage-agnostic discipline).
//
// GENERATION IDENTITY: the heldLease pointer captured at startup identifies this
// renewer generation. Every completion rechecks that heldLeases[id] is still that
// exact pointer before updating state or declaring loss, so a delayed backend call
// can never affect a CloseSession/reacquire successor.
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
func (s *Service) renewLoop(renewCtx context.Context, id session.SessionID, expected *heldLease) {
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
			s.mu.Lock()
			if s.heldLeases[id] != expected {
				s.mu.Unlock()
				return
			}
			lease := expected.lease
			s.mu.Unlock()

			rCtx, rCancel := context.WithTimeout(renewCtx, renewTimeout)
			refreshed, err := s.cfg.SessionLease.Renew(rCtx, lease)
			rCancel()

			// Renew implementations may complete after cancellation. In all cases the
			// captured pointer, not merely the session id, is the generation fence.
			s.mu.Lock()
			current := s.heldLeases[id] == expected
			if current && err == nil {
				expected.lease = refreshed
			}
			s.mu.Unlock()
			if !current {
				return
			}

			switch {
			case errors.Is(err, context.Canceled):
				return // shutdown / close raced the tick.
			case errors.Is(err, port.ErrLeaseHeld):
				// Definitive loss: a competitor holds it now.
				s.onLeaseLost(renewCtx, id, expected, err)
				return
			case err != nil:
				// Transient/infra fault: keep the run unless we are within one renew
				// interval of expiry (the next tick would land past it).
				if s.cfg.Now().Add(s.cfg.LeaseRenewInterval).Before(lease.Expiry) {
					continue // still have headroom; retry next tick.
				}
				s.onLeaseLost(renewCtx, id, expected, err)
				return
			}
		}
	}
}

// onLeaseLost handles a declared lease loss only when expected remains the
// current hold. Local mutation capability is invalidated before the owning run
// is stopped. For a durably parked awaiting run, its exact local ask is withdrawn
// before cancellation; cancellation can then unwind only in memory and cannot
// overwrite the durable awaiting handoff point. The lifecycle record remains until
// its relay drains and FinishRun performs identity-safe removal. A lightweight
// lost-owner tombstone prevents this stale Service from reacquiring the session.
// The successor now owns durable state, so any process-local authorization expiry
// timer and local broker transaction are also stopped/invalidated here; the
// durable snapshot itself is never mutated. Do not acquire runEntryMu here: lease
// loss cancels operations that may be holding it, so waiting for that lock would
// deadlock their cancellation.
func (s *Service) onLeaseLost(ctx context.Context, id session.SessionID, expected *heldLease, cause error) {
	s.stopAuthorizationExpiry(id)
	s.invalidateLocalAuthorization(context.WithoutCancel(ctx), id)
	var run *agent.Run
	var admissionCancel context.CancelFunc
	var leaseCancel context.CancelFunc
	var lease port.Lease
	var askID string
	var preserveAwaiting bool
	s.mu.Lock()
	if s.heldLeases[id] != expected || !expected.valid {
		s.mu.Unlock()
		return
	}
	expected.valid = false
	s.lostOwnership[id] = struct{}{}
	s.cfg.MutationCapability.Invalidate(id)
	leaseCancel = expected.cancel
	lease = expected.lease
	if st := s.runs[id]; st != nil {
		run = st.run
		admissionCancel = st.admissionCancel
		preserveAwaiting = st.awaiting.Load()
		if preserveAwaiting {
			if ask, pending := st.sess.PendingAsk(); pending {
				askID = ask.AskID
			}
		}
	}
	if !preserveAwaiting {
		delete(s.heldLeases, id)
	}
	s.mu.Unlock()
	if run != nil && askID != "" {
		run.RetractPermissionAsk(askID)
	}
	if leaseCancel != nil {
		leaseCancel()
	}
	if admissionCancel != nil {
		admissionCancel()
	}
	if run != nil {
		run.Cancel()
	}
	if !preserveAwaiting {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), leaseAcquireTimeout)
		defer releaseCancel()
		if err := s.cfg.SessionLease.Release(releaseCtx, lease); err != nil {
			s.cfg.Diagnostics.Log(releaseCtx, port.LevelWarn, "session lease release failed after loss",
				"session", string(id), "owner", s.cfg.LeaseOwner, "err", err.Error())
		}
	}
	// Emit diagnostics only after cancellation has been signalled, so observers
	// never see a loss report while the stale lifecycle is still admissible.
	s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "lost session lease; cancelling run",
		"session", string(id), "owner", s.cfg.LeaseOwner, "err", cause.Error())
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
	valid := false
	removeOnReturn := false
	if ok {
		lease = h.lease // guarded snapshot of the latest token/expiry.
		valid = h.valid
		h.valid = false
		s.cfg.MutationCapability.Invalidate(id)
		delete(s.heldLeases, id)
		if st := s.runs[id]; st != nil {
			st.removeCapabilityOnSettle = valid
		} else {
			removeOnReturn = valid
		}
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	h.cancel() // stop the renewer first.
	if !valid {
		return // declared loss: TTL/takeover owns transition; never release stale ownership.
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), leaseAcquireTimeout)
	defer cancel()
	if err := s.cfg.SessionLease.Release(ctx, lease); err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session lease release failed",
			"session", string(id), "owner", s.cfg.LeaseOwner, "err", err.Error())
	}
	if removeOnReturn {
		// No stale run reference remains, so normal teardown can forget the
		// invalidation rather than accumulating a per-session tombstone.
		s.cfg.MutationCapability.Remove(id)
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
	if !staleReconcileAuthorized(ctx) {
		return false, ErrManagementUnauthorized
	}
	// Authorization precedes caller-selected coordination. Revalidation after
	// runEntryMu and the maintenance mutation lease prevents an advisory stale
	// scan from becoming a durable grant.
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		return false, fmt.Errorf("server: load session for stale settle: %w", err)
	}
	if s.cfg.OwnershipEnforced && sess.Owner == nil {
		return false, nil
	}
	if !staleMaintenanceSessionCandidate(sess) {
		return false, nil
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if s.IsLive(id) {
		return false, nil
	}
	release, err := s.acquireMutationLeaseForStaleSettle(ctx, id)
	if err != nil {
		return false, err
	}
	defer release()
	sess, err = s.cfg.Store.Load(ctx, id)
	if err != nil {
		return false, fmt.Errorf("server: reload session for stale settle: %w", err)
	}
	if s.cfg.OwnershipEnforced && sess.Owner == nil || !staleMaintenanceSessionCandidate(sess) || s.IsLive(id) {
		return false, nil
	}
	if err := sess.Abandon(); err != nil {
		return false, fmt.Errorf("server: abandon stale running session: %w", err)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return false, fmt.Errorf("server: persist abandoned session: %w", err)
	}
	return true, nil
}

func (s *Service) cleanupRunAdmission(id session.SessionID, st *runState, promoted *bool) {
	if *promoted {
		return
	}
	st.admissionCancel()
	s.removeRunState(id, st)
}

// beginRunAdmission installs a cancellable provisional lifecycle before lease
// acquisition or engine construction. resumeAdmission marks the awaiting-resume
// path so concurrent approvals wait for its resumeMu transaction to promote.
// The caller holds runEntryMu for id.
func (s *Service) beginRunAdmission(parent context.Context, id session.SessionID, sess *session.Session, resumeAdmission bool) (*runState, context.Context, error) {
	ctx, cancel := context.WithCancel(parent)
	st := &runState{sess: sess, admissionCancel: cancel, settled: make(chan struct{}), resumeAdmission: resumeAdmission, titleRevision: sess.TitleRevision}
	s.mu.Lock()
	if s.draining.Load() {
		s.mu.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if _, exists := s.runs[id]; exists {
		s.mu.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("%w: session %q already has an active run", ErrFailedPrecondition, id)
	}
	s.runs[id] = st
	s.mu.Unlock()
	return st, ctx, nil
}

func (s *Service) removeRunState(id session.SessionID, st *runState) {
	removeCapability := false
	var stopRunContext context.CancelFunc
	s.mu.Lock()
	if s.runs[id] == st {
		delete(s.runs, id)
		stopRunContext = st.runContextStop
		st.runContextStop = nil
		st.settledOnce.Do(func() { close(st.settled) })
		removeCapability = st.removeCapabilityOnSettle
		if h := s.heldLeases[id]; h != nil && !h.valid {
			delete(s.heldLeases, id)
			removeCapability = true
		}
	}
	s.mu.Unlock()
	if stopRunContext != nil {
		stopRunContext()
	}
	if removeCapability {
		s.cfg.MutationCapability.Remove(id)
	}
}

// promoteRunAdmission atomically validates the exact hold and drain gate while
// launching the engine. Holding s.mu orders launch against loss and drain.
func (s *Service) promoteRunAdmission(id session.SessionID, st *runState, stop context.CancelFunc, launch func() *agent.Run) (*agent.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id] != st || st.cancelling || s.draining.Load() {
		return nil, fmt.Errorf("%w: %q", ErrUnavailable, id)
	}
	if s.cfg.SessionLease != nil && !s.leaseDisabled {
		h := s.heldLeases[id]
		if h == nil || !h.valid || h.ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, id)
		}
	}
	run := launch()
	st.run = run
	st.runContextStop = stop
	st.admissionCancel = nil
	return run, nil
}

// deregister removes the in-flight run for id (only if it is still the one
// recorded, so a later run for the same session is never clobbered).
func (s *Service) deregister(id session.SessionID, run *agent.Run) {
	s.mu.Lock()
	st := s.runs[id]
	matches := st != nil && st.run == run
	s.mu.Unlock()
	if matches {
		s.removeRunState(id, st)
	}
}

// FinishRun removes run from the in-flight registry for id. It is the EXPORTED
// counterpart of register that every wire adapter must call (typically via
// `defer`) once it has finished draining run.Events(), so a completed run does
// not leak in the registry. It is idempotent and only removes the entry if run
// is still the one recorded (a later run for the same session is never
// clobbered), so it is safe to call unconditionally after a drain.
func (s *Service) FinishRun(id session.SessionID, run *agent.Run) {
	s.mu.Lock()
	st, ok := s.runs[id]
	s.mu.Unlock()
	if !ok || st.run != run {
		return
	}
	parked := run.Outcome() == agent.RunOutcomeAuthorizationPending
	var pending session.PendingAuthorization
	var pendingOK bool
	if parked && st.sess != nil {
		pending, pendingOK = st.sess.PendingAuthorization()
	}
	s.removeRunState(id, st)
	if parked {
		s.scheduleAuthorizationExpiry(id, pending, pendingOK)
	}
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
	partition, partitionErr := s.skillPartition(ctx, "")
	if s.cfg.BeginSkillPublication != nil && partitionErr == nil {
		unlock := s.cfg.BeginSkillPublication(partition)
		defer unlock()
	}
	if s.cfg.PublishLearnedSkills != nil && partitionErr == nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillPublicationTimeout)
		_ = s.cfg.PublishLearnedSkills(publishCtx, partition)
		cancel()
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

// --- Worktree discovery (provider-private) -----------------------------------

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
	// TitleMetadata is the bounded source-free title lifecycle projection.
	TitleMetadata session.TitlePayload
	// TokenUsage is the canonical durable accounting ledger for this row.
	TokenUsage map[session.UsageKind]session.TokenUsage
	// Placement is bounded display-only placement metadata.
	Placement session.PlacementMetadata
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
	Generation         string `json:"g"`
	Scope              string `json:"s"`
	Continuation       string `json:"c"`
}

func encodeInventoryCursor(cursor *port.SessionMetadataCursor) (string, error) {
	if cursor == nil {
		return "", nil
	}
	data, err := json.Marshal(inventoryCursor{
		ModifiedAtUnixNano: cursor.ModifiedAt.UnixNano(), SessionID: string(cursor.ID),
		Generation: cursor.Generation, Scope: cursor.Scope, Continuation: cursor.Continuation,
	})
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
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.SessionID == "" || cursor.Generation == "" || cursor.Scope == "" || cursor.Continuation == "" {
		return nil, fmt.Errorf("%w: invalid session inventory cursor", ErrInvalidArgument)
	}
	return &port.SessionMetadataCursor{
		ModifiedAt: time.Unix(0, cursor.ModifiedAtUnixNano), ID: session.SessionID(cursor.SessionID),
		Generation: cursor.Generation, Scope: cursor.Scope, Continuation: cursor.Continuation,
	}, nil
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
		utf8.ValidString(relationship.MemberName) &&
		utf8.ValidString(string(relationship.DebugTargetID))
}

func validSessionIdentityMetadata(id session.SessionID, relationship session.SessionRelationship) bool {
	return id != "" && utf8.ValidString(string(id)) && validSessionRelationshipUTF8(relationship)
}

func metadataKeyAfter(row port.SessionDiscoveryMeta, cursor *port.SessionMetadataCursor) bool {
	return row.ModifiedAt.Before(cursor.ModifiedAt) ||
		(row.ModifiedAt.Equal(cursor.ModifiedAt) && row.ID > cursor.ID)
}

func validMetadataCursor(cursor *port.SessionMetadataCursor) bool {
	return cursor != nil && cursor.ID != "" && utf8.ValidString(string(cursor.ID)) &&
		cursor.Generation != "" && cursor.Scope != "" && cursor.Continuation != ""
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
		if len(page.Sessions) == 0 || !validMetadataCursor(page.NextCursor) {
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
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) || errors.Is(err, port.ErrSessionMetadataCursorRestart) {
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
		Owner:           meta.Owner.Clone(), Kind: kind, Relationship: meta.Relationship,
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
			summary.TitleMetadata = titlePayload(sess)
			summary.TokenUsage = sess.TokenUsageSnapshot()
			summary.Placement = sess.Placement
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
