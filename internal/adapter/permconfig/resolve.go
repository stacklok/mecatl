package permconfig

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// Compile-time assertion that *Resolver satisfies the policy's RuleResolver port.
var _ permpolicy.RuleResolver = (*Resolver)(nil)

// Conventional permission-config locations. Project files are discovered under
// the session workspace root; user-global files under the XDG config dir / home.
// The project tier splits into SHARED (checked-in) and LOCAL (gitignored,
// personal), mirroring the Scope ordering ScopeLocalProject > ScopeSharedProject.
const (
	// projectFileMecatl is the SHARED (checked-in) project YAML config.
	projectFileMecatl = ".mecatl/settings.yaml"
	// projectFileMecatlLocal is the LOCAL (gitignored, personal) project YAML config.
	projectFileMecatlLocal = ".mecatl/settings.local.yaml"
	// projectFileClaude is the Claude-Code-compatible SHARED project settings file.
	projectFileClaude = ".claude/settings.json"
	// projectFileClaudeLocal is the Claude-Code-compatible LOCAL project settings file.
	projectFileClaudeLocal = ".claude/settings.local.json"
	// UserSettingsRelPath is the user-level (operator-tier) YAML config location,
	// relative to the XDG config base (joined under <XDG_CONFIG_HOME>/...). It is the
	// SINGLE definition of where the operator settings.yaml lives: the resolver READS
	// it here (loadUserRules), and `mecated config init` WRITES it via this same const
	// (re-exported through internal/configgen), so the read and write paths can never
	// resolve different files.
	UserSettingsRelPath = "mecatl/settings.yaml"
	// userSubdirMecatl is the historical internal alias for UserSettingsRelPath.
	userSubdirMecatl = UserSettingsRelPath // joined under <config>/...
	// userSubdirClaude is the Claude-Code-compatible user-level settings file,
	// rooted at the home directory (~/.claude/settings.json).
	userSubdirClaude = ".claude/settings.json"
)

// projectSource is one discovered project-config file: its workspace-relative
// path, the config scope it loads at, and whether it is a Claude-imported JSON
// (vs a mecatl YAML). The discovery list is fixed; whether each file EXISTS is
// resolved per workspace via ws.Stat.
type projectSource struct {
	path   string
	scope  governance.Scope
	claude bool
}

// Options configures a Resolver's discovery posture (issue #13).
type Options struct {
	// Conventional turns on auto-discovery of the project-level conventional files
	// (the shared/local .mecatl + .claude files) and the user-global files. Default
	// false keeps discovery fully off (only ExplicitFiles, if any, are loaded).
	Conventional bool
	// ImportClaude turns on importing Claude-Code settings.json (project + user),
	// with the lossy fail-safe table applied (see claudeimport.go). Default false.
	ImportClaude bool
	// TrustProject, when true, honours a project's ALLOW rules (shared AND local —
	// both are project-supplied). When false (the default — the safe stance) a
	// project's allows are DROPPED while its deny/ask rules are still honoured.
	// User-global and explicit (CLI) config is always trusted regardless.
	TrustProject bool
	// ExplicitFiles are operator-pointed YAML config files (e.g. from a repeatable
	// --permission-config flag), loaded at ScopeCLI (the HIGHEST config precedence,
	// fully trusted) regardless of Conventional. Read from the host filesystem at
	// construction.
	ExplicitFiles []string
	// Diagnostics is the operational-logging sink for the lossy import report
	// (demoted/inert/dropped specs) and the per-file fail-soft skip lines. nil is
	// tolerated: newWithEnv defaults it to port.NopDiagnostics so the resolver stays
	// silent rather than nil-panicking. The composition layer injects the shared sink.
	Diagnostics port.Diagnostics
}

// fileStamp fingerprints a single config file's on-disk state (mod time + size)
// so the cache can detect a mid-process edit. A missing file stamps as exists=false
// — so a config CREATED after the first Resolve also invalidates the cache (not
// just an edit to an existing one).
type fileStamp struct {
	exists  bool
	modUnix int64
	size    int64
}

// cacheEntry is a resolved per-root result plus the fingerprint of the project
// files it was built from. Resolve revalidates the fingerprint on every hit and
// rebuilds when any stamp changed (mtime/size), so a deny added (or a file
// created/removed) mid-process takes effect on the next call — not at restart.
type cacheEntry struct {
	rules  []governance.Rule
	stamps map[string]fileStamp // keyed by the project-relative file path
	// projectModels is the SANITIZED project-tier models: block captured during the
	// SAME resolution that built rules/stamps (ADR 0030 Phase 4): slots/aliases/default
	// only (the allowlist: key is stripped — non-wideable), and ONLY when the project is
	// trusted AND an operator allowlist exists. nil when the project carried no honoured
	// models: block. It lives on the cacheEntry so it invalidates with the project rules
	// on the SAME mtime/size fingerprint (ProjectModelBindings is a pure read of it).
	projectModels *ModelsSection
}

// Resolver discovers and caches file-based permission rules per workspace root
// (issue #13). It implements permpolicy.RuleResolver: the policy calls Resolve on
// every Evaluate, so the resolver caches — discovery is file I/O. The cache is
// keyed by ws.Root() with a sync.RWMutex; each entry is REVALIDATED against the
// config files' mtime/size on every hit, so the cache speeds up the common
// unchanged case without going stale on an edit (matching the re-probe stance of
// the MCP source prober and command lister).
//
// The USER-GLOBAL + explicit (CLI) rules are root-independent and resolved ONCE at
// construction (they are the operator's own, fully trusted). Only the PROJECT
// rules are re-resolved per root, gated by TrustProject.
type Resolver struct {
	opts    Options
	env     xdgconfig.ResolveEnv
	sources []projectSource  // the fixed project-file discovery list
	diag    port.Diagnostics // operational-logging sink; never nil after newWithEnv

	// userRules are the root-independent user-global + explicit (CLI) rules,
	// computed once at construction. Always fully trusted.
	userRules []governance.Rule

	operatorHarnessContext    *HarnessContextSection
	operatorHarnessContextErr error

	// operatorGuardrails is the OPERATOR-TIER guardrails config (issue #27), read
	// ONCE at construction from the user-global + explicit (CLI) tiers ONLY. A
	// project-tier file's guardrails: block is deliberately IGNORED (a project repo
	// weakening/disabling a checker is a security DOWNGRADE) — loadProjectRules WARNs
	// when it sees one. nil when no operator-tier file carried a guardrails: section.
	// CLI (explicit files) out-ranks user-global.
	operatorGuardrails *GuardrailsSection

	// operatorPosture is the OPERATOR-TIER posture: scalar (issue: posture ladder),
	// read ONCE at construction from the user-global + CLI tiers ONLY. A project-tier
	// file's posture: key is deliberately IGNORED (a project repo raising the
	// automation posture is a security DOWNGRADE — loadProjectRules WARNs when it sees
	// one). Empty when no operator-tier file carried a posture: scalar. CLI (explicit
	// files) out-ranks user-global (first-non-empty keeps CLI).
	operatorPosture string

	// operatorReasoningEffort is the OPERATOR-TIER reasoning-effort: scalar (ADR
	// 0055), read ONCE at construction from the user-global + CLI tiers ONLY. A
	// project-tier file's reasoning-effort: key is deliberately IGNORED (operator-
	// tier only, for consistency with posture — loadProjectRules WARNs
	// when it sees one). Empty when no operator-tier file carried a reasoning-effort:
	// scalar. CLI (explicit files) out-ranks user-global (first-non-empty keeps CLI).
	operatorReasoningEffort string

	// operatorPlanModeAutoApprove is the OPERATOR-TIER plan-mode-auto-approve: bool
	// (issue #206 Wave 6a), read ONCE at construction from the user-global + CLI
	// tiers ONLY. A project-tier file's plan-mode-auto-approve: key is deliberately
	// IGNORED (a project repo enabling autonomous plan approval is a security
	// DOWNGRADE — loadProjectRules WARNs when it sees one). false when no
	// operator-tier file carried the key. CLI (explicit files) out-ranks user-global
	// (first-non-empty keeps CLI).
	operatorPlanModeAutoApprove bool

	// operatorLearning is the first operator-tier learning subtree. Project values
	// are evaluated separately because they may only tighten this ceiling.
	operatorLearning *LearningSection

	// operatorSteer is the OPERATOR-TIER steer: scalar (steer-while-running, issue
	// #512), read ONCE at construction from the user-global + CLI tiers ONLY. A
	// project-tier file's steer: key is deliberately IGNORED (operator-tier only,
	// for consistency with posture — loadProjectRules WARNs when it sees one).
	// operatorSteerSet records whether ANY operator-tier file carried the key, so
	// ABSENT is distinguishable from an explicit steer: false (the knob is an
	// opt-OUT of a default-ON feature, so the composition layer needs the
	// presence bit, not just the bool). CLI (explicit files) out-ranks user-global
	// (first-present keeps CLI).
	operatorSteer    bool
	operatorSteerSet bool

	// operatorModels is the OPERATOR-TIER models: subtree (ADR 0030), read ONCE at
	// construction from the user-global + CLI tiers ONLY (the SOLE capture path is
	// captureModels from loadUserRules; there is no second capture path). It carries
	// the operator's own slots/aliases/default AND the non-wideable Allowlist cap that
	// gates the PROJECT-tier bindings (Phase 4). A project-tier file's models: block is
	// honoured only WITHIN this Allowlist on a trusted workspace (loadProjectRules) —
	// when the Allowlist is empty the project block stays WARN-ignored (the opt-in).
	// nil when no operator-tier file carried a models: section. CLI (explicit files)
	// out-ranks user-global (first-non-nil keeps CLI).
	operatorModels *ModelsSection

	// operatorOpenRouter is the OPERATOR-TIER openrouter: subtree (issue #480), read
	// ONCE at construction from the user-global + CLI tiers ONLY (the SOLE capture
	// path is captureOpenRouter from loadUserRules — mirroring captureModels). A
	// project-tier file's openrouter: block is IGNORED with a WARN in loadProjectRules
	// (steering requests to a downstream provider is an operator spend/compliance
	// decision). nil when no operator-tier file carried an openrouter: section. CLI
	// (explicit files) out-ranks user-global (first-non-nil keeps CLI).
	operatorOpenRouter *OpenRouterSection

	// operatorTelemetry is the OPERATOR-TIER telemetry: subtree, read ONCE at
	// construction from the user-global + CLI tiers ONLY (the SOLE capture path
	// is captureTelemetry from loadUserRules — mirroring captureOpenRouter). A
	// project-tier file's telemetry: block is IGNORED with a WARN in
	// loadProjectRules. nil when no operator-tier file carried a telemetry:
	// section. CLI (explicit files) out-ranks user-global (first-non-nil keeps
	// CLI).
	operatorTelemetry *TelemetrySection

	// operatorMCP is the first complete operator-tier mcp: subtree. Explicit CLI
	// files are visited before user-global settings, so precedence is whole-block,
	// first-non-nil; project mcp blocks are warning-only and never captured.
	operatorMCP *MCPSection

	// operatorRetention is the first complete operator-tier retention block.
	operatorRetention    *RetentionSection
	operatorRetentionErr error

	// operatorStorageManagement is the first complete operator-tier authority
	// block. Parse failures are retained so composition fails closed at startup.
	operatorStorageManagement    *StorageManagementSection
	operatorStorageManagementErr error
	operatorCommandRunner        *CommandRunnerSection
	operatorSystemPrompt         *SystemPromptSection
	operatorCommandRunnerErr     error
	operatorTemporaryStorage     *TemporaryStorageSection
	operatorTemporaryStorageErr  error

	// operatorProviders and operatorProviderOverrides are immutable operator-tier
	// provider configuration captured once at resolver construction.
	operatorProviders         ProviderDefinitions
	operatorProviderOverrides ProviderOverrides
	operatorCredentialStore   *CredentialStoreSection
	operatorProviderConfigErr error

	mu    sync.RWMutex
	cache map[string]*cacheEntry // keyed by ws.Root()
}

// OperatorProviders returns the operator-tier custom provider definitions and
// endpoint overrides. Both maps are immutable snapshots; the returned error records
// a strict operator configuration parse failure involving either section.
func (r *Resolver) OperatorProviders() (ProviderDefinitions, ProviderOverrides, error) {
	if r == nil {
		return nil, nil, nil
	}
	return r.operatorProviders, r.operatorProviderOverrides, r.operatorProviderConfigErr
}

// OperatorCredentialStore returns the operator-tier credential-store configuration.
func (r *Resolver) OperatorCredentialStore() *CredentialStoreSection {
	if r == nil {
		return nil
	}
	return r.operatorCredentialStore
}

// OperatorStorageManagement returns the immutable operator-tier management
// authority block and any strict parse failure that would otherwise disable it.
func (r *Resolver) OperatorStorageManagement() (*StorageManagementSection, error) {
	if r == nil {
		return nil, nil
	}
	return r.operatorStorageManagement, r.operatorStorageManagementErr
}

// OperatorCredentialEnvironmentNames returns the effective configured environment
// credential references. Values are names only and the returned slice is a copy.
func (r *Resolver) OperatorCredentialEnvironmentNames() []string {
	if r == nil {
		return nil
	}
	var names []string
	if store := r.operatorCredentialStore; store != nil && store.OIDC != nil {
		names = append(names, store.OIDC.Key.KeyEnv)
	}
	if mcp := r.operatorMCP; mcp != nil {
		for _, server := range mcp.Servers {
			auth := server.Auth
			if auth.StaticBearer != nil {
				names = append(names, auth.StaticBearer.TokenEnv)
			}
			if auth.OAuth == nil {
				continue
			}
			if p := auth.OAuth.Client.Preregistered; p != nil {
				names = append(names, p.SecretEnv)
			}
			if p := auth.OAuth.Credentials.Local; p != nil {
				names = append(names, p.KeyEnv)
			}
			if p := auth.OAuth.Credentials.Environment; p != nil {
				names = append(names, p.CredentialEnv)
			}
		}
	}
	out := names[:0]
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return append([]string(nil), out...)
}

// OperatorCommitCoauthor returns the optional operator setting. Nil means absent,
// so later composition can retain its enabled-by-default behavior.
func (r *Resolver) OperatorCommitCoauthor() *bool {
	if r == nil || r.operatorSystemPrompt == nil {
		return nil
	}
	return r.operatorSystemPrompt.CommitCoauthor
}

// OperatorCommandRunner returns the immutable effective operator-tier command-runner policy.
func (r *Resolver) OperatorCommandRunner() (*CommandRunnerSection, error) {
	if r == nil {
		return nil, nil
	}
	return r.operatorCommandRunner, r.operatorCommandRunnerErr
}

// OperatorTemporaryStorage returns the immutable operator-tier command temporary
// storage policy and any strict parse failure that would otherwise disable it.
func (r *Resolver) OperatorTemporaryStorage() (*TemporaryStorageSection, error) {
	if r == nil {
		return nil, nil
	}
	return r.operatorTemporaryStorage, r.operatorTemporaryStorageErr
}

// HarnessContextError reports an explicit operator policy that could not be parsed.
func (r *Resolver) HarnessContextError() error {
	if r == nil {
		return nil
	}
	return r.operatorHarnessContextErr
}

// OperatorHarnessContext returns the strict operator-tier harness source policy.
func (r *Resolver) OperatorHarnessContext() *HarnessContextSection {
	if r == nil {
		return nil
	}
	return r.operatorHarnessContext
}

// OperatorGuardrails returns the operator-tier guardrails config (user-global + CLI
// only), or nil when none was configured. It is the SOLE accessor the composition
// layer uses to read guardrails from config — by construction it never returns a
// project-tier block (decision 3: operator-tier-only).
func (r *Resolver) OperatorGuardrails() *GuardrailsSection {
	if r == nil {
		return nil
	}
	return r.operatorGuardrails
}

// OperatorPosture returns the operator-tier posture: scalar (user-global + CLI
// only), or "" when none was configured. It is the SOLE accessor the composition
// layer uses to read posture from config — by construction it never returns a
// project-tier value (a project posture: is ignored with a WARN in loadProjectRules).
func (r *Resolver) OperatorPosture() string {
	if r == nil {
		return ""
	}
	return r.operatorPosture
}

// OperatorReasoningEffort returns the operator-tier reasoning-effort: scalar
// (user-global + CLI only), or "" when none was configured (ADR 0055). It is the
// SOLE accessor the composition layer uses to read reasoning-effort from config —
// by construction it never returns a project-tier value (a project reasoning-effort:
// is ignored with a WARN in loadProjectRules). nil-safe. Mirrors OperatorPosture().
func (r *Resolver) OperatorReasoningEffort() string {
	if r == nil {
		return ""
	}
	return r.operatorReasoningEffort
}

// OperatorPlanModeAutoApprove returns the operator-tier plan-mode-auto-approve:
// bool (user-global + CLI only), or false when none was configured (issue #206
// Wave 6a). It is the SOLE accessor the composition layer uses to read the flag
// from config — by construction it never returns a project-tier value (a project
// plan-mode-auto-approve: is ignored with a WARN in loadProjectRules). nil-safe.
// Mirrors OperatorPosture().
func (r *Resolver) OperatorPlanModeAutoApprove() bool {
	if r == nil {
		return false
	}
	return r.operatorPlanModeAutoApprove
}

// OperatorLearning returns the complete operator-tier learning policy. Callers
// must treat it as immutable.
func (r *Resolver) OperatorLearning() *LearningSection {
	if r == nil {
		return nil
	}
	return r.operatorLearning
}

// OperatorLearningMode returns the operator-tier learning mode token, or empty
// when no learning subtree was configured.
func (r *Resolver) OperatorLearningMode() string {
	if r == nil || r.operatorLearning == nil {
		return ""
	}
	return strings.TrimSpace(r.operatorLearning.Mode)
}

// ProjectLearningModes returns project-tier mode tokens in precedence order.
// Composition applies them only as autonomy ceilings (off < review < auto).
func (r *Resolver) ProjectLearningModes(ws tool.WorkspaceReader) []string {
	settings := r.ProjectLearningSettings(ws)
	modes := make([]string, 0, len(settings))
	for _, setting := range settings {
		if token := strings.TrimSpace(setting.Mode); token != "" {
			modes = append(modes, token)
		}
	}
	return modes
}

// ProjectLearningSettings returns strict project learning subtrees in precedence
// order. Composition applies only mode and sensitivity as tighten-only ceilings;
// Automatic is operator-only and ignored with a warning.
func (r *Resolver) ProjectLearningSettings(ws tool.WorkspaceReader) []*LearningSection {
	if r == nil || ws == nil {
		return nil
	}
	var result []*LearningSection
	for _, src := range r.sources {
		if src.claude {
			continue
		}
		data, err := ws.Read(context.Background(), src.path)
		if err != nil {
			continue
		}
		cfg, err := parseYAML(data)
		if err == nil && cfg.Learning != nil {
			result = append(result, cfg.Learning)
		}
	}
	return result
}

// OperatorSteer returns the OPERATOR-TIER steer: bool (user-global + CLI only) and
// whether ANY operator-tier file carried the key (steer-while-running, issue #512).
// It is the SOLE accessor the composition layer uses to read the knob from config —
// by construction it never returns a project-tier value (a project steer: is ignored
// with a WARN in loadProjectRules). The presence bit matters because the knob is an
// opt-OUT of a DEFAULT-ON feature: absent (present=false) means composition keeps the
// default; an explicit steer: false (present=true, value=false) disables it. nil-safe.
// Mirrors OperatorPosture().
func (r *Resolver) OperatorSteer() (value, present bool) {
	if r == nil {
		return false, false
	}
	return r.operatorSteer, r.operatorSteerSet
}

// OperatorModelSlots returns the operator-tier models: subtree (user-global + CLI
// only), or nil when none was configured. It is the SOLE accessor the composition
// layer uses to read per-slot model config from disk — by construction it never
// returns a project-tier block (a project models: is ignored with a WARN in
// loadProjectRules). nil-safe.
func (r *Resolver) OperatorModelSlots() *ModelsSection {
	if r == nil {
		return nil
	}
	return r.operatorModels
}

// OperatorOpenRouter returns the operator-tier openrouter: subtree (user-global +
// CLI only), or nil when none was configured (issue #480). It is the SOLE accessor
// the composition layer uses to read OpenRouter downstream-provider routing from
// config — by construction it never returns a project-tier block (a project
// openrouter: is ignored with a WARN in loadProjectRules). nil-safe. Mirrors
// OperatorModelSlots().
func (r *Resolver) OperatorOpenRouter() *OpenRouterSection {
	if r == nil {
		return nil
	}
	return r.operatorOpenRouter
}

// OperatorProductMetricsEnabled returns the operator-tier
// telemetry.productMetrics.enabled: value (user-global + CLI only), or nil
// when none was configured. It is the SOLE accessor composition uses to
// read the product-metrics opt-out from config — by construction it never
// returns a project-tier value (a project telemetry: block is ignored with
// a WARN in loadProjectRules). nil-safe. Mirrors OperatorOpenRouter().
func (r *Resolver) OperatorProductMetricsEnabled() *bool {
	if r == nil || r.operatorTelemetry == nil || r.operatorTelemetry.ProductMetrics == nil {
		return nil
	}
	return r.operatorTelemetry.ProductMetrics.Enabled
}

// OperatorMCP returns the complete operator-tier mcp subtree, or nil when absent.
// It is metadata only and can never originate from project settings.
func (r *Resolver) OperatorMCP() *MCPSection {
	if r == nil {
		return nil
	}
	return r.operatorMCP
}

// OperatorRetention returns the immutable operator-tier retention block.
func (r *Resolver) OperatorRetention() (*RetentionSection, error) {
	if r == nil {
		return nil, nil
	}
	return r.operatorRetention, r.operatorRetentionErr
}

// OperatorModelPolicy returns the operator-tier models: subtree (user-global + CLI
// only), or nil when none was configured (ADR 0030 Phase 4). It is the accessor the
// composition layer reads the operator ALLOWLIST and the operator DEFAULT from — the
// non-wideable cap that gates project-tier bindings. It reads the SAME operatorModels
// backing field as OperatorModelSlots (allowlist+default+slots+aliases all ride the
// one operator models: block, captured once via captureModels); there is no second
// capture path. nil-safe. It mirrors OperatorGuardrails()/OperatorPosture().
func (r *Resolver) OperatorModelPolicy() *ModelsSection {
	if r == nil {
		return nil
	}
	return r.operatorModels
}

// ProjectModelBindings returns the SANITIZED project-tier models: block for the given
// workspace (ADR 0030 Phase 4): slots/aliases/default only (the allowlist: key is
// stripped at capture — non-wideable), captured ONLY when the project was trusted AND
// an operator allowlist exists. It is a pure read of the per-root cacheEntry; a cold
// root is resolved first (the same revalidated-cache path Resolve uses), so the
// trust/allowlist decision was already applied at CAPTURE time (loadProjectRules) — this
// accessor adds no policy. Returns nil when ws is nil, the resolver is nil, or the
// project carried no honoured models: block.
func (r *Resolver) ProjectModelBindings(ws tool.WorkspaceReader) *ModelsSection {
	if r == nil || ws == nil {
		return nil
	}
	// Warm the cache (and apply the capture-time trust/allowlist gate) if cold/stale,
	// reusing the SAME revalidated-cache path as Resolve so the projectModels capture
	// stays in lockstep with the rules on the file fingerprint.
	r.Resolve(context.Background(), ws)
	root := ws.Root()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.cache[root]; ok {
		return entry.projectModels
	}
	return nil
}

// New constructs a Resolver from opts, reading the user-global + explicit (CLI)
// rules once (project rules are read lazily per root in Resolve). It logs the
// import report (demoted/inert/dropped specs) so lossy outcomes are observable. It
// never fails: an unreadable/malformed user file is logged and skipped (the
// process must still start), matching the rest of the composition's fail-soft
// posture. Returns nil when opts requests no sources at all, so callers can pass
// the result straight to permpolicy.NewPolicyWithResolver (a nil resolver = off).
func New(opts Options) *Resolver {
	return NewWithEnv(opts, xdgconfig.OSEnv)
}

// NewWithEnv constructs a Resolver using env for user-global discovery. It is for
// composition callers that need to provide an isolated environment; New preserves
// the production binding to xdgconfig.OSEnv.
func NewWithEnv(opts Options, env xdgconfig.ResolveEnv) *Resolver {
	return newWithEnv(opts, env)
}

// newWithEnv is NewWithEnv's private implementation, also used by package tests.
func newWithEnv(opts Options, env xdgconfig.ResolveEnv) *Resolver {
	if !opts.Conventional && len(opts.ExplicitFiles) == 0 {
		return nil
	}
	diag := opts.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	r := &Resolver{
		opts:    opts,
		env:     env,
		sources: projectSources(opts),
		diag:    diag,
		cache:   make(map[string]*cacheEntry),
	}
	var report Report
	r.userRules = r.loadUserRules(&report)
	r.logReport(&report, "user-global")
	return r
}

// projectSources returns the fixed, ordered list of project-config files to probe
// per workspace, given the discovery posture. The LOCAL files (gitignored,
// personal) load at the higher-precedence ScopeLocalProject; the SHARED
// (checked-in) files at ScopeSharedProject. Claude files are included only when
// ImportClaude is set.
func projectSources(opts Options) []projectSource {
	if !opts.Conventional {
		return nil
	}
	srcs := []projectSource{
		{path: projectFileMecatlLocal, scope: governance.ScopeLocalProject},
		{path: projectFileMecatl, scope: governance.ScopeSharedProject},
	}
	if opts.ImportClaude {
		srcs = append(srcs,
			projectSource{path: projectFileClaudeLocal, scope: governance.ScopeLocalProject, claude: true},
			projectSource{path: projectFileClaude, scope: governance.ScopeSharedProject, claude: true},
		)
	}
	return srcs
}

// Resolve returns the rules that apply to the given workspace: the project rules
// (gated by TrustProject) plus the root-independent user/CLI rules. A nil ws
// yields only the user/CLI rules (no project to discover). The per-root cache is
// revalidated against the config files' mtime/size, so a mid-process edit takes
// effect on the next call. It satisfies permpolicy.RuleResolver.
func (r *Resolver) Resolve(_ context.Context, ws tool.WorkspaceReader) []governance.Rule {
	if ws == nil {
		return r.userRules
	}
	root := ws.Root()

	// Fast path: a cache hit whose fingerprint still matches the files on disk.
	stamps := r.stampSources(ws)
	r.mu.RLock()
	entry, ok := r.cache[root]
	r.mu.RUnlock()
	if ok && stampsEqual(entry.stamps, stamps) {
		return entry.rules
	}

	// Miss or stale: reload the project rules + the (gated) project models and re-stamp.
	project, projModels := r.loadProjectRules(ws)
	merged := make([]governance.Rule, 0, len(project)+len(r.userRules))
	merged = append(merged, project...)
	merged = append(merged, r.userRules...)

	r.mu.Lock()
	r.cache[root] = &cacheEntry{rules: merged, stamps: stamps, projectModels: projModels}
	r.mu.Unlock()
	return merged
}

// stampSources fingerprints every discovered project-config file under ws via
// ws.Stat (the read-only seam — never a write), so a changed/created/removed file
// invalidates the cache. A stat error other than "exists" is treated as
// exists=false (e.g. a not-found file), which is the correct revalidation signal.
func (r *Resolver) stampSources(ws tool.WorkspaceReader) map[string]fileStamp {
	stamps := make(map[string]fileStamp, len(r.sources))
	for _, src := range r.sources {
		fi, err := ws.Stat(context.Background(), src.path)
		if err != nil {
			stamps[src.path] = fileStamp{exists: false}
			continue
		}
		stamps[src.path] = fileStamp{exists: true, modUnix: fi.ModTime.UnixNano(), size: fi.Size}
	}
	return stamps
}

// stampsEqual reports whether two stamp maps are identical (same keys, same
// exists/mtime/size), i.e. no config file changed since the cached resolution.
func stampsEqual(a, b map[string]fileStamp) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}

// loadProjectRules discovers and parses every project-config file under ws.Root()
// (read THROUGH the workspace, the same session-scoped seam the tools use), tags
// each at its tier scope (local > shared), applies the trust gate, and logs the
// import report. It is fail-soft PER FILE: an unreadable/malformed file is logged
// and skipped, so a bad shared YAML never suppresses a good local/Claude file.
func (r *Resolver) loadProjectRules(ws tool.WorkspaceReader) ([]governance.Rule, *ModelsSection) {
	var report Report
	var rules []governance.Rule
	var projectModels *ModelsSection

	for _, src := range r.sources {
		data, err := ws.Read(context.Background(), src.path)
		if err != nil {
			continue // absent file: nothing to load (not an error)
		}
		if src.claude {
			imported, ierr := importClaude(data, src.scope, &report)
			if ierr != nil {
				r.diag.Log(context.Background(), port.LevelWarn, "permission config: project claude settings unparseable; skipping",
					"file", src.path, "root", ws.Root(), "err", ierr)
				continue
			}
			rules = append(rules, imported...)
			continue
		}
		r.warnProjectHarnessContext(data, src.path, ws.Root())
		cfg, perr := parseYAMLForTier(data, true)
		if perr != nil {
			// The skip drops the WHOLE file — its DENY/ASK rules included, so a
			// typo LOOSENS policy. Name the lost per-effect counts (best-effort
			// lenient re-read) so the operator sees how much tightening vanished.
			deny, ask, allow, counted := lostRuleCounts(data)
			r.diag.Log(context.Background(), port.LevelWarn, "permission config: project YAML invalid; skipping (its rules are LOST, deny/ask included)",
				"file", src.path, "root", ws.Root(), "err", perr,
				"lost_deny", deny, "lost_ask", ask, "lost_allow", allow, "counts_known", counted)
			continue
		}
		// Provider configuration is OPERATOR-TIER ONLY.
		if cfg.Providers != nil {
			r.diag.Log(context.Background(), port.LevelWarn, "providers: IGNORING project-tier providers block (operator-tier only)", "file", src.path, "root", ws.Root())
		}
		if cfg.ProviderOverrides != nil {
			r.diag.Log(context.Background(), port.LevelWarn, "provider_overrides: IGNORING project-tier provider_overrides block (operator-tier only)", "file", src.path, "root", ws.Root())
		}
		// Guardrails are OPERATOR-TIER ONLY (issue #27, decision 3): a project file's
		// guardrails block is ignored because letting a project weaken or disable a
		// security checker would reverse the usual tighten-only project gate.
		if cfg.Guardrails != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"guardrails: IGNORING a project-tier guardrails: block (operator-tier only — a project repo cannot configure/disable a security checker; set guardrails in your user-global settings.yaml or via --guardrails-model)",
				"file", src.path, "root", ws.Root())
		}
		// Posture is OPERATOR-TIER ONLY (the fail-closed core of the posture ladder): a
		// project file's posture: scalar is IGNORED with a loud WARN. Honouring it would
		// let a malicious repo RAISE the automation posture (e.g. posture: yolo to
		// auto-run substitutions in subagents) — a security DOWNGRADE the tighten-only
		// project gate forbids (it reverses here, exactly like guardrails).
		if strings.TrimSpace(cfg.Posture) != "" {
			r.diag.Log(context.Background(), port.LevelWarn,
				"posture: IGNORING a project-tier posture: scalar (operator-tier only — a project repo cannot raise the automation posture; set posture in your user-global settings.yaml or via --posture)",
				"file", src.path, "root", ws.Root())
		}
		// ReasoningEffort is OPERATOR-TIER ONLY (ADR 0055), for consistency with
		// posture: a project file's reasoning-effort: scalar is IGNORED
		// with a loud WARN. It is a cost/quality preference, not a security control,
		// but keeping it operator-tier-only matches the established pattern and prevents
		// a project from silently changing the model's reasoning spend.
		if strings.TrimSpace(cfg.ReasoningEffort) != "" {
			r.diag.Log(context.Background(), port.LevelWarn,
				"reasoning-effort: IGNORING a project-tier reasoning-effort: scalar (operator-tier only — set reasoning-effort in your user-global settings.yaml or via --reasoning-effort)",
				"file", src.path, "root", ws.Root())
		}
		// PlanModeAutoApprove is OPERATOR-TIER ONLY (issue #206 Wave 6a), for
		// consistency with posture/guardrails: a project file's plan-mode-auto-approve:
		// key is IGNORED with a loud WARN. Enabling it from a project repo would let a
		// repo enable autonomous plan approval — a security DOWNGRADE (the same
		// operator-tier-only discipline as guardrails/posture).
		if cfg.PlanModeAutoApprove {
			r.diag.Log(context.Background(), port.LevelWarn,
				"plan-mode-auto-approve: IGNORING a project-tier plan-mode-auto-approve: key (operator-tier only — a project repo cannot enable autonomous plan approval; set plan-mode-auto-approve in your user-global settings.yaml or via --plan-mode-auto-approve)",
				"file", src.path, "root", ws.Root())
		}
		// Steer is OPERATOR-TIER ONLY (issue #512), for consistency with posture: a
		// project file's steer: key is IGNORED with a loud WARN. The harness's
		// operator surface is not a project repo's to flip in either direction.
		if cfg.Steer != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"steer: IGNORING a project-tier steer: key (operator-tier only — a project repo cannot change the mid-run steer surface; set steer in your user-global settings.yaml or via --no-steer)",
				"file", src.path, "root", ws.Root())
		}
		// OpenRouter downstream-provider routing is OPERATOR-TIER ONLY (issue #480): a
		// project file's openrouter: block is IGNORED with a loud WARN. Steering requests
		// to a particular downstream inference provider is a spend/compliance/capability
		// decision the operator owns — the same operator-only discipline as
		// default_provider/allowlist/router (a project repo cannot pick the downstream).
		if cfg.OpenRouter != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"openrouter: IGNORING a project-tier openrouter: block (operator-tier only — a project repo cannot steer the OpenRouter downstream provider; set openrouter in your user-global settings.yaml)",
				"file", src.path, "root", ws.Root())
		}
		if cfg.Telemetry != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"telemetry: IGNORING a project-tier telemetry: block (operator-tier only — a project repo cannot change a user's own product-metrics opt-out in either direction; set telemetry in your user-global settings.yaml)",
				"file", src.path, "root", ws.Root())
		}
		if cfg.MCP != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"mcp: IGNORING a project-tier mcp: block (operator-tier only — a project repo cannot configure global MCP servers)",
				"file", src.path, "root", ws.Root())
		}
		if cfg.Retention != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"retention: IGNORING a project-tier retention block (operator-tier only; projects cannot weaken cleanup protection)",
				"file", src.path, "root", ws.Root())
		}
		r.warnProjectSystemPrompt(cfg.SystemPrompt, src.path, ws.Root())
		r.warnProjectCommandRunner(cfg.CommandRunner, src.path, ws.Root())
		if cfg.TemporaryStorage != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"temporary_storage: IGNORING a project-tier temporary_storage block (operator-tier only; projects cannot redirect command temporary storage or alter cleanup retention)",
				"file", src.path, "root", ws.Root())
		}
		if cfg.StorageManagement != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"storage_management: IGNORING a project-tier authority block (operator-tier only)",
				"file", src.path, "root", ws.Root())
		}
		// models: is project-overridable WITHIN AN OPERATOR ALLOWLIST (ADR 0030 Phase 4),
		// otherwise IGNORED. captureProjectModels applies the full gate (allowlist key
		// stripped + WARN; opt-in by operator allowlist; trust gate) and merges the
		// honoured slots/aliases/default across project files (local > shared by load
		// order — first non-empty wins per field/key). It WARNs precisely on each
		// not-honoured reason. Outside an operator allowlist this is byte-identical to the
		// pre-Phase-4 WARN-ignore.
		if cfg.Models != nil {
			projectModels = r.captureProjectModels(ws, src.path, cfg.Models, projectModels)
		}
		rules = append(rules, rulesFromConfig(cfg, src.scope, &report)...)
	}

	rules = r.applyTrustGate(rules, &report)
	r.logReport(&report, "project("+ws.Root()+")")
	return rules, projectModels
}

func (r *Resolver) warnProjectHarnessContext(data []byte, file, root string) {
	if !hasTopLevelKey(data, "harness_context") {
		return
	}
	r.diag.Log(context.Background(), port.LevelWarn, "harness_context: IGNORING project-tier block (operator-tier only)", "file", file, "root", root)
}

func (r *Resolver) warnProjectCommandRunner(section *CommandRunnerSection, file, root string) {
	if section == nil {
		return
	}
	r.diag.Log(context.Background(), port.LevelWarn,
		"command_runner: IGNORING a project-tier command_runner block (operator-tier only; configure it in user-global settings.yaml or an explicit operator file)",
		"file", file, "root", root)
}

func (r *Resolver) warnProjectSystemPrompt(section *SystemPromptSection, file, root string) {
	if section == nil {
		return
	}
	r.diag.Log(context.Background(), port.LevelWarn,
		"system_prompt: IGNORING a project-tier system_prompt block (operator-tier only; configure it in user-global settings.yaml or an explicit operator file)",
		"file", file, "root", root)
}

// captureProjectModels applies the ADR 0030 Phase 4 gate to ONE project-tier models:
// block and merges its honoured bindings onto acc (the running per-root accumulator
// across the local→shared file order; first-non-empty wins, so the higher-precedence
// LOCAL file's binding is kept). It is the SINGLE choke point for the project-tier
// trust/allowlist decision:
//
//  1. An allowlist: key in a PROJECT block is non-wideable — STRIP it with a WARN (a
//     project cannot widen its own cap). The rest of the block is still considered.
//  2. The rest is honoured ONLY when an operator allowlist EXISTS (the opt-in: no
//     operator allowlist ⇒ project models stay WARN-ignored, byte-identical to today)
//     AND the workspace is TRUSTED (the SAME r.opts.TrustProject gate as project allow
//     rules — an untrusted repo's models: is dropped). When not honoured it WARNs,
//     distinguishing the reason (no operator allowlist (opt-in) vs untrusted workspace).
//  3. The honoured slots/aliases/default are sanitized (allowlist always nil) and merged
//     onto acc. The allowlist-MEMBERSHIP cap itself (resolve-then-check each binding
//     against the canonical set) is applied in COMPOSITION (foldProjectModelBindings),
//     because it needs the operator-merged alias map to canonicalize — permconfig only
//     captures the raw project bindings within the trust/opt-in gate.
func (r *Resolver) captureProjectModels(ws tool.WorkspaceReader, file string, block *ModelsSection, acc *ModelsSection) *ModelsSection {
	// (1) A project-tier allowlist: is non-wideable — strip + WARN, but keep the rest.
	if len(block.Allowlist) > 0 {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.allowlist (operator-tier only — a project cannot widen its own cap; set the allowlist in your user-global settings.yaml)",
			"file", file, "root", ws.Root())
	}

	// (1b) A project-tier router: is OPERATOR-TIER ONLY (ADR 0031) — strip + WARN, but
	// keep the rest. The semantic model-router taxonomy is an autonomous-spend/capability
	// decision the operator owns (like the allowlist); a project must not define which
	// models its delegated tasks route to. captureProjectModels never copies Router onto
	// acc, so the strip is the WARN — the field is structurally dropped.
	if block.Router != nil {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.router (operator-tier only — the semantic model-router taxonomy is an operator decision; set it in your user-global settings.yaml)",
			"file", file, "root", ws.Root())
	}

	// (1c) A project-tier default_provider: is OPERATOR-TIER ONLY (the SAME operator-only
	// captureModels discipline as the allowlist/router) — strip + WARN, but keep the rest.
	// The deployment-wide default provider id is an operator decision (it mirrors the
	// --default-provider flag); a project must not declare its own default provider.
	// captureProjectModels never copies DefaultProvider onto acc, so the strip is the WARN
	// — the field is structurally dropped.
	if block.DefaultProvider != "" {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.default_provider (operator-tier only — the deployment-wide default provider is an operator decision; set it in your user-global settings.yaml or pass --default-provider)",
			"file", file, "root", ws.Root())
	}

	// (1d) A project-tier subagent: is OPERATOR-TIER ONLY (issue #288 — the SAME
	// operator-only captureModels discipline as default_provider/allowlist/router) —
	// strip + WARN, but keep the rest. The def-less child-default model is an operator
	// decision (it mirrors the --subagent-model flag); a project must not declare which
	// model its delegated children run on. captureProjectModels never copies Subagent
	// onto acc, so the strip is the WARN — the field is structurally dropped.
	if block.Subagent != "" {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.subagent (operator-tier only — the child-default model is an operator decision; set it in your user-global settings.yaml or pass --subagent-model)",
			"file", file, "root", ws.Root())
	}
	if len(block.ContextWindows) > 0 {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.context_windows (operator-tier only — set context windows in your user-global settings.yaml)",
			"file", file, "root", ws.Root())
	}

	if value, ok := block.Slots["guardrail"]; ok && value.ExplicitProvider {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING project-tier models.slots.guardrail provider object (operator-tier only)",
			"file", file, "root", ws.Root())
	}

	// (2) Opt-in by operator allowlist, then trust-gated.
	op := r.operatorModels
	if op == nil || len(op.Allowlist) == 0 {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING a project-tier models: block (no operator models.allowlist configured — set one in your user-global settings.yaml to opt into project-overridable, allowlist-capped model bindings)",
			"file", file, "root", ws.Root())
		return acc
	}
	if !r.opts.TrustProject {
		r.diag.Log(context.Background(), port.LevelWarn,
			"models: IGNORING a project-tier models: block (untrusted workspace — pass --trust-project or add this repo to trustedWorkspaces to honour its allowlisted model bindings)",
			"file", file, "root", ws.Root())
		return acc
	}

	// (3) Honoured: merge the sanitized bindings (allowlist stripped; first-non-empty
	// wins per field, so a LOCAL file's binding out-ranks the SHARED file's).
	if acc == nil {
		acc = &ModelsSection{}
	}
	if block.Default != "" && acc.Default == "" {
		acc.Default = block.Default
	}
	acc.Slots = mergeFirstWinsSlots(acc.Slots, block.Slots)
	acc.Aliases = mergeFirstWins(acc.Aliases, block.Aliases)
	return acc
}

func mergeFirstWinsSlots(dst, src ModelSlots) ModelSlots {
	if dst == nil && len(src) > 0 {
		dst = make(ModelSlots, len(src))
	}
	for key, value := range src {
		if value.ExplicitProvider {
			continue
		}
		if _, exists := dst[key]; !exists {
			dst[key] = value
		}
	}
	return dst
}

// mergeFirstWins copies src entries into dst, keeping any key dst already holds (the
// higher-precedence file wins, since loadProjectRules visits local before shared). A nil
// dst is lazily allocated only when src has entries; a nil/empty src returns dst as-is.
func mergeFirstWins(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
	return dst
}

// applyTrustGate drops project ALLOW rules when the project is untrusted, keeping
// every DENY and ASK rule (they only tighten). A dropped allow is reported so the
// operator sees that an untrusted project's grant was ignored. When TrustProject
// is set the rules pass through unchanged. It applies to BOTH project tiers
// (shared and local) — both are project-supplied.
func (r *Resolver) applyTrustGate(rules []governance.Rule, report *Report) []governance.Rule {
	if r.opts.TrustProject {
		return rules
	}
	kept := rules[:0:0]
	for _, rule := range rules {
		if rule.Effect == governance.Allow {
			report.addDropped(specOf(rule), "project allow dropped (project not trusted; pass --trust-project to honour it)")
			continue
		}
		kept = append(kept, rule)
	}
	return kept
}

// loadUserRules reads the explicit (CLI) operator files at ScopeCLI plus the
// root-independent user-global config (XDG/home) at ScopeUser, all fully trusted.
// Read from the host filesystem via the injectable env (NOT a workspace — these
// live outside any session root). Fail-soft per file.
func (r *Resolver) captureOperatorParseError(data []byte, err error) {
	if hasTopLevelKey(data, "harness_context") && r.operatorHarnessContextErr == nil {
		r.operatorHarnessContextErr = errors.New("operator harness_context configuration is invalid")
	}
	if (hasTopLevelKey(data, "providers") || hasTopLevelKey(data, "provider_overrides") || hasTopLevelKey(data, "credential_store")) && r.operatorProviderConfigErr == nil {
		r.operatorProviderConfigErr = errors.New("operator provider configuration is invalid")
	}
	if hasTopLevelKey(data, "retention") && r.operatorRetentionErr == nil {
		r.operatorRetentionErr = err
	}
	if hasTopLevelKey(data, "storage_management") && r.operatorStorageManagementErr == nil {
		r.operatorStorageManagementErr = err
	}
	if hasTopLevelKey(data, "command_runner") && r.operatorCommandRunnerErr == nil {
		r.operatorCommandRunnerErr = err
	}
	if hasTopLevelKey(data, "temporary_storage") && r.operatorTemporaryStorageErr == nil {
		r.operatorTemporaryStorageErr = err
	}
}

func (r *Resolver) loadUserRules(report *Report) []governance.Rule {
	var rules []governance.Rule

	// Explicit operator files (always loaded, regardless of Conventional) at the
	// HIGHEST config scope (ScopeCLI), so a CLI rule out-ranks a project/user rule
	// of the same effect. Operator-supplied -> fully trusted (not gated).
	for _, path := range r.opts.ExplicitFiles {
		if path == "" {
			continue
		}
		data, err := r.env.ReadFile(path)
		if err != nil {
			r.diag.Log(context.Background(), port.LevelWarn, "permission config: explicit file unreadable; skipping", "file", path, "err", err)
			continue
		}
		cfg, perr := parseYAML(data)
		if perr != nil {
			r.captureOperatorParseError(data, perr)
			deny, ask, allow, counted := lostRuleCounts(data)
			r.diag.Log(context.Background(), port.LevelWarn, "permission config: explicit file invalid; skipping (its rules are LOST, deny/ask included)",
				"file", path, "err", perr,
				"lost_deny", deny, "lost_ask", ask, "lost_allow", allow, "counts_known", counted)
			continue
		}
		rules = append(rules, rulesFromConfig(cfg, governance.ScopeCLI, report)...)
		// Operator-tier guardrails (issue #27): CLI files out-rank user-global, so the
		// FIRST CLI file with a guardrails: block wins (first-non-nil keeps CLI).
		r.captureHarnessContext(cfg.HarnessContext)
		r.captureGuardrails(cfg.Guardrails)
		// Operator-tier posture: same first-non-empty-keeps-CLI discipline as guardrails.
		r.capturePosture(cfg.Posture)
		// Operator-tier reasoning-effort (ADR 0055): same discipline as posture.
		r.captureReasoningEffort(cfg.ReasoningEffort)
		// Operator-tier plan-mode-auto-approve (issue #206 Wave 6a): same discipline as posture.
		r.capturePlanModeAutoApprove(cfg.PlanModeAutoApprove)
		// Operator-tier learning: same first-non-nil-keeps-CLI discipline.
		r.captureLearning(cfg.Learning)
		// Operator-tier steer (issue #512): same discipline as posture.
		r.captureSteer(cfg.Steer)
		// Operator-tier models: same first-non-nil-keeps-CLI discipline (ADR 0030).
		r.captureModels(cfg.Models)
		// Operator-tier openrouter: same first-non-nil-keeps-CLI discipline (issue #480).
		r.captureOpenRouter(cfg.OpenRouter)
		// Operator-tier telemetry (opt-out product metrics): same
		// first-non-nil-keeps-CLI discipline as openrouter.
		r.captureTelemetry(cfg.Telemetry)
		// Operator-tier MCP profiles: capture the complete first block; never field-merge.
		r.captureMCP(cfg.MCP)
		r.captureRetention(cfg.Retention)
		r.captureStorageManagement(cfg.StorageManagement)
		r.captureSystemPrompt(cfg.SystemPrompt)
		r.captureCommandRunner(cfg.CommandRunner)
		r.captureProviders(cfg.Providers, cfg.ProviderOverrides, cfg.CredentialStore)
	}

	if !r.opts.Conventional {
		return rules
	}

	// User-global YAML under the XDG config dir.
	if cfgDir := xdgconfig.UserConfigDir(r.env); cfgDir != "" {
		path := filepath.Join(cfgDir, userSubdirMecatl)
		if data, err := r.env.ReadFile(path); err == nil {
			if cfg, perr := parseYAML(data); perr != nil {
				r.captureOperatorParseError(data, perr)
				deny, ask, allow, counted := lostRuleCounts(data)
				r.diag.Log(context.Background(), port.LevelWarn, "permission config: user YAML invalid; skipping (its rules are LOST, deny/ask included)",
					"file", path, "err", perr,
					"lost_deny", deny, "lost_ask", ask, "lost_allow", allow, "counts_known", counted)
			} else {
				rules = append(rules, rulesFromConfig(cfg, governance.ScopeUser, report)...)
				// User-global guardrails: captured only if no higher CLI file already did.
				r.captureHarnessContext(cfg.HarnessContext)
				r.captureGuardrails(cfg.Guardrails)
				// User-global posture: captured only if no higher CLI file already did.
				r.capturePosture(cfg.Posture)
				// User-global reasoning-effort (ADR 0055): same discipline as posture.
				r.captureReasoningEffort(cfg.ReasoningEffort)
				// User-global plan-mode-auto-approve (issue #206 Wave 6a): same discipline as posture.
				r.capturePlanModeAutoApprove(cfg.PlanModeAutoApprove)
				// User-global learning: captured only if no higher CLI file already did.
				r.captureLearning(cfg.Learning)
				// User-global steer (issue #512): same discipline as posture.
				r.captureSteer(cfg.Steer)
				// User-global models: captured only if no higher CLI file already did.
				r.captureModels(cfg.Models)
				// User-global openrouter: captured only if no higher CLI file already did.
				r.captureOpenRouter(cfg.OpenRouter)
				// Operator-tier telemetry (opt-out product metrics): same
				// first-non-nil-keeps-CLI discipline as openrouter.
				r.captureTelemetry(cfg.Telemetry)
				// User-global MCP: captured only if no higher CLI file already did.
				r.captureMCP(cfg.MCP)
				r.captureRetention(cfg.Retention)
				r.captureStorageManagement(cfg.StorageManagement)
				r.captureSystemPrompt(cfg.SystemPrompt)
				r.captureCommandRunner(cfg.CommandRunner)
				r.captureTemporaryStorage(cfg.TemporaryStorage)
				r.captureProviders(cfg.Providers, cfg.ProviderOverrides, cfg.CredentialStore)
			}
		}
	}

	// User-global Claude settings.json (when importing).
	if r.opts.ImportClaude {
		if home, err := r.env.UserHomeDir(); err == nil && home != "" {
			path := filepath.Join(home, userSubdirClaude)
			if data, rerr := r.env.ReadFile(path); rerr == nil {
				if imported, ierr := importClaude(data, governance.ScopeUser, report); ierr != nil {
					r.diag.Log(context.Background(), port.LevelWarn, "permission config: user claude settings unparseable; skipping", "file", path, "err", ierr)
				} else {
					rules = append(rules, imported...)
				}
			}
		}
	}

	return rules
}

// captureProviders records the first complete operator provider snapshot. Explicit
// files precede user-global settings, so the command-line operator tier wins.
func (r *Resolver) captureProviders(definitions ProviderDefinitions, overrides ProviderOverrides, store *CredentialStoreSection) {
	if r.operatorProviders == nil && definitions != nil {
		r.operatorProviders = definitions
	}
	if r.operatorProviderOverrides == nil && overrides != nil {
		r.operatorProviderOverrides = overrides
	}
	if r.operatorCredentialStore == nil && store != nil {
		r.operatorCredentialStore = store
	}
}

func (r *Resolver) captureHarnessContext(s *HarnessContextSection) {
	if s == nil || r.operatorHarnessContext != nil {
		return
	}
	r.operatorHarnessContext = s
}

// captureGuardrails records the FIRST operator-tier guardrails: block seen during
// construction (CLI files are parsed before user-global, so CLI wins on first-non-
// nil). It is called only from loadUserRules — the operator (user-global + CLI)
// tiers — never from loadProjectRules, so a project file can never supply guardrails
// (decision 3: operator-tier-only).
func (r *Resolver) captureGuardrails(g *GuardrailsSection) {
	if g == nil || r.operatorGuardrails != nil {
		return
	}
	r.operatorGuardrails = g
}

// capturePosture records the FIRST operator-tier posture: scalar seen during
// construction (CLI files are parsed before user-global, so CLI wins on
// first-non-empty). It is called only from loadUserRules — the operator (user-
// global + CLI) tiers — never from loadProjectRules, so a project file can never
// supply posture (the fail-closed core). A whitespace-only value is treated as
// absent.
func (r *Resolver) capturePosture(p string) {
	if r.operatorPosture != "" {
		return
	}
	if strings.TrimSpace(p) == "" {
		return
	}
	r.operatorPosture = strings.TrimSpace(p)
}

// captureReasoningEffort records the FIRST operator-tier reasoning-effort: scalar
// seen during construction (CLI files are parsed before user-global, so CLI wins
// on first-non-empty). It is called only from loadUserRules — the operator
// (user-global + CLI) tiers — never from loadProjectRules, so a project file can
// never supply reasoning-effort (operator-tier only, for consistency with
// posture — ADR 0055). A whitespace-only value is treated as absent.
func (r *Resolver) captureReasoningEffort(p string) {
	if r.operatorReasoningEffort != "" {
		return
	}
	if strings.TrimSpace(p) == "" {
		return
	}
	r.operatorReasoningEffort = strings.TrimSpace(p)
}

func (r *Resolver) captureLearning(s *LearningSection) {
	if s == nil || r.operatorLearning != nil {
		return
	}
	r.operatorLearning = s
}

// capturePlanModeAutoApprove records the FIRST operator-tier plan-mode-auto-approve:
// bool seen during construction (CLI files are parsed before user-global, so CLI
// wins on first-non-zero). It is called only from loadUserRules — the operator
// (user-global + CLI) tiers — never from loadProjectRules, so a project file can
// never supply it (operator-tier only, for consistency with posture/guardrails —
// issue #206 Wave 6a).
func (r *Resolver) capturePlanModeAutoApprove(p bool) {
	if r.operatorPlanModeAutoApprove {
		return
	}
	r.operatorPlanModeAutoApprove = p
}

// captureSteer records the FIRST operator-tier steer: scalar seen during
// construction (CLI files are parsed before user-global, so CLI wins on
// first-present). It is called only from loadUserRules — the operator (user-global
// + CLI) tiers — never from loadProjectRules, so a project file can never supply it
// (operator-tier only, for consistency with posture — issue #512). A nil *bool
// (the key absent) is a no-op; a non-nil value records BOTH the value and the
// presence bit (the knob is an opt-OUT, so presence is load-bearing).
func (r *Resolver) captureSteer(s *bool) {
	if s == nil || r.operatorSteerSet {
		return
	}
	r.operatorSteer = *s
	r.operatorSteerSet = true
}

// captureModels records the FIRST operator-tier models: block seen during
// construction (CLI files are parsed before user-global, so CLI wins on
// first-non-nil). It is called only from loadUserRules — the operator (user-global
// + CLI) tiers — never from loadProjectRules, so this OPERATOR block (the allowlist
// cap + the operator's own slots/aliases/default) can only come from an operator-tier
// file. A PROJECT file's models: block is NOT captured here — it is honoured (within
// the operator allowlist, on a trusted workspace) by the SEPARATE captureProjectModels
// path, which feeds cacheEntry.projectModels, never operatorModels (ADR 0030 Phase 4).
func (r *Resolver) captureModels(m *ModelsSection) {
	if m == nil || r.operatorModels != nil {
		return
	}
	r.operatorModels = m
}

// captureOpenRouter records the FIRST operator-tier openrouter: block seen during
// loadUserRules (CLI files out-rank user-global, so first-non-nil keeps CLI).
// Mirrors captureModels (issue #480).
func (r *Resolver) captureOpenRouter(s *OpenRouterSection) {
	if s == nil || r.operatorOpenRouter != nil {
		return
	}
	r.operatorOpenRouter = s
}

// captureTelemetry records the FIRST operator-tier telemetry: block seen
// during loadUserRules (CLI files out-rank user-global, so first-non-nil
// keeps CLI). Mirrors captureOpenRouter.
func (r *Resolver) captureTelemetry(s *TelemetrySection) {
	if s == nil || r.operatorTelemetry != nil {
		return
	}
	r.operatorTelemetry = s
}

// captureMCP records the first complete operator-tier mcp block. It is called
// only by loadUserRules, whose explicit-files-before-user order defines precedence.
func (r *Resolver) captureMCP(s *MCPSection) {
	if s == nil || r.operatorMCP != nil {
		return
	}
	r.operatorMCP = s
}

func (r *Resolver) captureRetention(s *RetentionSection) {
	if s == nil || r.operatorRetention != nil {
		return
	}
	r.operatorRetention = s
}

func (r *Resolver) captureSystemPrompt(s *SystemPromptSection) {
	if s == nil || r.operatorSystemPrompt != nil {
		return
	}
	r.operatorSystemPrompt = s
}

func (r *Resolver) captureCommandRunner(s *CommandRunnerSection) {
	if s == nil || r.operatorCommandRunner != nil {
		return
	}
	r.operatorCommandRunner = s
}

func (r *Resolver) captureTemporaryStorage(s *TemporaryStorageSection) {
	if s == nil || r.operatorTemporaryStorage != nil {
		return
	}
	r.operatorTemporaryStorage = s
}

func (r *Resolver) captureStorageManagement(s *StorageManagementSection) {
	if s == nil || r.operatorStorageManagement != nil {
		return
	}
	r.operatorStorageManagement = s
}

// specOf reconstructs a human-readable "Tool(pattern)" spec from a rule, for the
// drop report. A tool-wide rule (empty pattern) renders as the bare tool name.
func specOf(rule governance.Rule) string {
	if rule.Pattern == "" {
		return rule.Tool
	}
	return rule.Tool + "(" + rule.Pattern + ")"
}

// logReport logs the lossy outcome category and harness-authored reason without
// forwarding a rule spec from configuration into diagnostics.
func (r *Resolver) logReport(report *Report, origin string) {
	if report.Empty() {
		return
	}
	for _, e := range report.Demoted {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule demoted", "origin", origin, "reason", e.Reason)
	}
	for _, e := range report.Inert {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule inert", "origin", origin, "reason", e.Reason)
	}
	for _, e := range report.Dropped {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule dropped", "origin", origin, "reason", e.Reason)
	}
}
