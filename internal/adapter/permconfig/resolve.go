package permconfig

import (
	"context"
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

	// operatorOutputEconomy is the OPERATOR-TIER output-economy: scalar (ADR 0041),
	// read ONCE at construction from the user-global + CLI tiers ONLY. A project-tier
	// file's output-economy: key is deliberately IGNORED (operator-tier only, for
	// consistency with posture/guardrails — loadProjectRules WARNs when it sees one).
	// Empty when no operator-tier file carried an output-economy: scalar. CLI
	// (explicit files) out-ranks user-global (first-non-empty keeps CLI).
	operatorOutputEconomy string

	// operatorReasoningEffort is the OPERATOR-TIER reasoning-effort: scalar (ADR
	// 0055), read ONCE at construction from the user-global + CLI tiers ONLY. A
	// project-tier file's reasoning-effort: key is deliberately IGNORED (operator-
	// tier only, for consistency with posture/output-economy — loadProjectRules WARNs
	// when it sees one). Empty when no operator-tier file carried a reasoning-effort:
	// scalar. CLI (explicit files) out-ranks user-global (first-non-empty keeps CLI).
	operatorReasoningEffort string

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

	// operatorSchedules is the OPERATOR-TIER schedules: subtree (issue #233, Phase
	// 2b), read ONCE at construction from the user-global + CLI tiers ONLY. A
	// project-tier file's schedules: block is deliberately IGNORED (a project repo
	// registering schedules is an operator decision — the same operator-tier-only
	// discipline as guardrails/posture) — loadProjectRules WARNs when it sees one.
	// nil when no operator-tier file carried a schedules: section. CLI (explicit
	// files) out-ranks user-global (first-non-nil keeps CLI).
	operatorSchedules *SchedulesSection

	mu    sync.RWMutex
	cache map[string]*cacheEntry // keyed by ws.Root()
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

// OperatorOutputEconomy returns the operator-tier output-economy: scalar (user-global
// + CLI only), or "" when none was configured (ADR 0041). It is the SOLE accessor the
// composition layer uses to read output-economy from config — by construction it never
// returns a project-tier value (a project output-economy: is ignored with a WARN in
// loadProjectRules). nil-safe.
func (r *Resolver) OperatorOutputEconomy() string {
	if r == nil {
		return ""
	}
	return r.operatorOutputEconomy
}

// OperatorReasoningEffort returns the operator-tier reasoning-effort: scalar
// (user-global + CLI only), or "" when none was configured (ADR 0055). It is the
// SOLE accessor the composition layer uses to read reasoning-effort from config —
// by construction it never returns a project-tier value (a project reasoning-effort:
// is ignored with a WARN in loadProjectRules). nil-safe. Mirrors
// OperatorOutputEconomy().
func (r *Resolver) OperatorReasoningEffort() string {
	if r == nil {
		return ""
	}
	return r.operatorReasoningEffort
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

// OperatorSchedules returns the operator-tier schedules: subtree (user-global + CLI
// only), or nil when none was configured (issue #233, Phase 2b). It is the SOLE
// accessor the composition layer uses to read declared schedules from config — by
// construction it never returns a project-tier block (a project schedules: is
// ignored with a WARN in loadProjectRules). nil-safe. Mirrors
// OperatorGuardrails().
func (r *Resolver) OperatorSchedules() *SchedulesSection {
	if r == nil {
		return nil
	}
	return r.operatorSchedules
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
	return newWithEnv(opts, xdgconfig.OSEnv)
}

// newWithEnv is New with an injectable environment, for tests.
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
		cfg, perr := parseYAML(data)
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
		// Guardrails are OPERATOR-TIER ONLY (issue #27, decision 3): a project file's
		// guardrails: block is IGNORED with a loud WARN. Honouring it would let a
		// project repo weaken or disable a security checker — a downgrade the usual
		// tighten-only project gate does NOT permit (it reverses here: project config
		// can only TIGHTEN permissions, but a guardrail relaxation is a LOOSENING).
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
				"file", src.path, "root", ws.Root(), "ignored_value", strings.TrimSpace(cfg.Posture))
		}
		// OutputEconomy is OPERATOR-TIER ONLY (ADR 0041), for consistency with posture/
		// guardrails: a project file's output-economy: scalar is IGNORED with a loud
		// WARN. It is a style/cost preference, not a security control, but keeping it
		// operator-tier-only matches the established pattern and prevents a project
		// from silently changing agent output behavior; a project can still influence
		// prose style via AGENTS.md.
		if strings.TrimSpace(cfg.OutputEconomy) != "" {
			r.diag.Log(context.Background(), port.LevelWarn,
				"output-economy: IGNORING a project-tier output-economy: scalar (operator-tier only — set output-economy in your user-global settings.yaml or via --output-economy)",
				"file", src.path, "root", ws.Root(), "ignored_value", strings.TrimSpace(cfg.OutputEconomy))
		}
		// ReasoningEffort is OPERATOR-TIER ONLY (ADR 0055), for consistency with
		// posture/output-economy: a project file's reasoning-effort: scalar is IGNORED
		// with a loud WARN. It is a cost/quality preference, not a security control,
		// but keeping it operator-tier-only matches the established pattern and prevents
		// a project from silently changing the model's reasoning spend.
		if strings.TrimSpace(cfg.ReasoningEffort) != "" {
			r.diag.Log(context.Background(), port.LevelWarn,
				"reasoning-effort: IGNORING a project-tier reasoning-effort: scalar (operator-tier only — set reasoning-effort in your user-global settings.yaml or via --reasoning-effort)",
				"file", src.path, "root", ws.Root(), "ignored_value", strings.TrimSpace(cfg.ReasoningEffort))
		}
		// Schedules are OPERATOR-TIER ONLY (issue #233, Phase 2b), for consistency
		// with guardrails/posture: a project file's schedules: block is IGNORED with a
		// loud WARN. Honouring it would let a project repo register schedules that fire
		// agent runs — an operator decision a project repo must not make (the same
		// operator-tier-only discipline as guardrails: a project cannot mint autonomous
		// agent runs). It reverses the usual tighten-only gate exactly like guardrails.
		if cfg.Schedules != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"schedules: IGNORING a project-tier schedules: block (operator-tier only — a project repo cannot register schedules; set schedules in your user-global settings.yaml)",
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
	acc.Slots = mergeFirstWins(acc.Slots, block.Slots)
	acc.Aliases = mergeFirstWins(acc.Aliases, block.Aliases)
	return acc
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
			deny, ask, allow, counted := lostRuleCounts(data)
			r.diag.Log(context.Background(), port.LevelWarn, "permission config: explicit file invalid; skipping (its rules are LOST, deny/ask included)",
				"file", path, "err", perr,
				"lost_deny", deny, "lost_ask", ask, "lost_allow", allow, "counts_known", counted)
			continue
		}
		rules = append(rules, rulesFromConfig(cfg, governance.ScopeCLI, report)...)
		// Operator-tier guardrails (issue #27): CLI files out-rank user-global, so the
		// FIRST CLI file with a guardrails: block wins (first-non-nil keeps CLI).
		r.captureGuardrails(cfg.Guardrails)
		// Operator-tier posture: same first-non-empty-keeps-CLI discipline as guardrails.
		r.capturePosture(cfg.Posture)
		// Operator-tier output-economy (ADR 0041): same discipline as posture.
		r.captureOutputEconomy(cfg.OutputEconomy)
		// Operator-tier reasoning-effort (ADR 0055): same discipline as posture.
		r.captureReasoningEffort(cfg.ReasoningEffort)
		// Operator-tier models: same first-non-nil-keeps-CLI discipline (ADR 0030).
		r.captureModels(cfg.Models)
		// Operator-tier schedules (issue #233, Phase 2b): same first-non-nil-keeps-CLI discipline.
		r.captureSchedules(cfg.Schedules)
	}

	if !r.opts.Conventional {
		return rules
	}

	// User-global YAML under the XDG config dir.
	if cfgDir := xdgconfig.UserConfigDir(r.env); cfgDir != "" {
		path := filepath.Join(cfgDir, userSubdirMecatl)
		if data, err := r.env.ReadFile(path); err == nil {
			if cfg, perr := parseYAML(data); perr != nil {
				deny, ask, allow, counted := lostRuleCounts(data)
				r.diag.Log(context.Background(), port.LevelWarn, "permission config: user YAML invalid; skipping (its rules are LOST, deny/ask included)",
					"file", path, "err", perr,
					"lost_deny", deny, "lost_ask", ask, "lost_allow", allow, "counts_known", counted)
			} else {
				rules = append(rules, rulesFromConfig(cfg, governance.ScopeUser, report)...)
				// User-global guardrails: captured only if no higher CLI file already did.
				r.captureGuardrails(cfg.Guardrails)
				// User-global posture: captured only if no higher CLI file already did.
				r.capturePosture(cfg.Posture)
				// User-global output-economy (ADR 0041): same discipline as posture.
				r.captureOutputEconomy(cfg.OutputEconomy)
				// User-global reasoning-effort (ADR 0055): same discipline as posture.
				r.captureReasoningEffort(cfg.ReasoningEffort)
				// User-global models: captured only if no higher CLI file already did.
				r.captureModels(cfg.Models)
				// User-global schedules (issue #233, Phase 2b): captured only if no higher CLI file already did.
				r.captureSchedules(cfg.Schedules)
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

// captureOutputEconomy records the FIRST operator-tier output-economy: scalar seen
// during construction (CLI files are parsed before user-global, so CLI wins on
// first-non-empty). It is called only from loadUserRules — the operator (user-global
// + CLI) tiers — never from loadProjectRules, so a project file can never supply
// output-economy (operator-tier only, for consistency with posture/guardrails). A
// whitespace-only value is treated as absent.
func (r *Resolver) captureOutputEconomy(p string) {
	if r.operatorOutputEconomy != "" {
		return
	}
	if strings.TrimSpace(p) == "" {
		return
	}
	r.operatorOutputEconomy = strings.TrimSpace(p)
}

// captureReasoningEffort records the FIRST operator-tier reasoning-effort: scalar
// seen during construction (CLI files are parsed before user-global, so CLI wins
// on first-non-empty). It is called only from loadUserRules — the operator
// (user-global + CLI) tiers — never from loadProjectRules, so a project file can
// never supply reasoning-effort (operator-tier only, for consistency with
// posture/output-economy — ADR 0055). A whitespace-only value is treated as absent.
func (r *Resolver) captureReasoningEffort(p string) {
	if r.operatorReasoningEffort != "" {
		return
	}
	if strings.TrimSpace(p) == "" {
		return
	}
	r.operatorReasoningEffort = strings.TrimSpace(p)
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

// captureSchedules records the FIRST operator-tier schedules: block seen during
// construction (CLI files are parsed before user-global, so CLI wins on first-non-
// nil). It is called only from loadUserRules — the operator (user-global + CLI)
// tiers — never from loadProjectRules, so a project file can never supply
// schedules (issue #233, Phase 2b: operator-tier-only, mirroring guardrails).
// first-non-nil means a CLI `schedules:` block replaces (does not merge with) the
// user-global one — a list, unlike a struct, has no natural merge.
func (r *Resolver) captureSchedules(s *SchedulesSection) {
	if s == nil || r.operatorSchedules != nil {
		return
	}
	r.operatorSchedules = s
}

// specOf reconstructs a human-readable "Tool(pattern)" spec from a rule, for the
// drop report. A tool-wide rule (empty pattern) renders as the bare tool name.
func specOf(rule governance.Rule) string {
	if rule.Pattern == "" {
		return rule.Tool
	}
	return rule.Tool + "(" + rule.Pattern + ")"
}

// logReport logs the lossy outcomes (demoted/inert/dropped specs) of a load at the
// given origin, so an operator can see exactly what was weakened or ignored. A
// clean report logs nothing.
func (r *Resolver) logReport(report *Report, origin string) {
	if report.Empty() {
		return
	}
	for _, e := range report.Demoted {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule demoted", "origin", origin, "spec", e.Spec, "reason", e.Reason)
	}
	for _, e := range report.Inert {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule inert", "origin", origin, "spec", e.Spec, "reason", e.Reason)
	}
	for _, e := range report.Dropped {
		r.diag.Log(context.Background(), port.LevelWarn, "permission config: rule dropped", "origin", origin, "spec", e.Spec, "reason", e.Reason)
	}
}
