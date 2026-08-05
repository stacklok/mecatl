package app

import (
	"context"
	"path/filepath"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/hashutil"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/soul"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// soulEnv is the PATH-RESOLUTION environment used to resolve the conventional
// user-scoped soul (<xdg>/mecatl/soul.md). It defaults to the real process
// environment; tests override it (with t.Cleanup-style restoration) so the
// user-soul precedence checks run fully offline against a faked XDG/home — never the
// developer's real ~/.config. It is used only to compute a path string (no write).
var soulEnv = xdgconfig.OSEnv

// soulselect.go is the composition-layer SOUL PROVENANCE + TRUST GATE (issue #14,
// Phase 3, Item 2). The soul is fenced DATA, never a permission scope, so this is
// NOT routed through engine/governance — but it REUSES the issue-#13 trust gate
// (Config.TrustProject) so an imported/project-sourced soul is governed by the
// EXACT same operator gesture that gates a project's ALLOW permission rules. As of
// the Workspace-Trust feature (Phase 1), Config.TrustProject carries the FOLDED
// TrustDecision (trust.go): the --trust-project flag OR a settings.yaml
// `trustedWorkspaces:` declaration. A declared-trusted workspace therefore honours
// a project soul exactly as --trust-project does — through this same gate, never a
// bypass.
//
// Two soul PROVENANCES:
//   - USER: the conventional user-scoped <xdg>/mecatl/soul.md (fallback
//     ~/.config/mecatl/soul.md), or an explicit --soul-file PATH. ALWAYS trusted —
//     it is the operator's own file, NEVER trust-gated (correction (C)).
//   - PROJECT: a discovered <workspace>/.mecatl/soul.md (parallel to
//     .mecatl/settings.yaml and to how RootAssembler discovers AGENTS.md/CLAUDE.md).
//     This is the imported/project-provenance soul. UNTRUSTED by default: it
//     contributes NOTHING unless --trust-project is set.
//
// PRECEDENCE = USER-WINS (single identity anchor; MVP, NOT a merge): when a
// user-scoped soul is present it is used and the project soul is IGNORED. The
// project soul is used ONLY when (a) --trust-project is set AND (b) no user-scoped
// soul is present. Two simultaneous soul blocks are explicitly avoided (a
// contradiction + a double injection surface).
//
// An untrusted project soul is dropped SILENTLY-BUT-LOGGED (slog.Warn, mirroring
// applyTrustGate's report posture), never an error.

// projectSoulSubpath is the conventional project-scoped soul file relative to the
// workspace root, i.e. <workspace>/.mecatl/soul.md. It mirrors permconfig's
// projectFileMecatl (".mecatl/settings.yaml") path convention — the soul lives in
// the SAME per-project .mecatl/ dir as the project permission config.
const projectSoulSubpath = ".mecatl/soul.md"

// soulProvenance records WHERE a selected soul originated, for Item 3's read-only
// TUI inspector. It is a composition-level concern: the soul adapter is a pure
// loader (it neither knows nor cares about provenance), and engine/prompt stays
// trust-unaware (the SoulSource interface is unchanged).
type soulProvenance int

const (
	// soulNone means no soul was selected (none present, or an untrusted project
	// soul was dropped, or --no-soul).
	soulNone soulProvenance = iota
	// soulUser means the selected soul is the user-scoped one (always trusted).
	soulUser
	// soulProject means the selected soul is the project-scoped
	// <workspace>/.mecatl/soul.md (only selected when --trust-project is set).
	soulProject
	// soulDriver means the selected soul came from a remote soul-source driver
	// (--soul-source-url, Phase C1). Operator-configured infrastructure: it
	// occupies the USER slot in the precedence and is always trusted; the drift
	// baseline is SKIPPED for it (sidecar-file machinery — the driver sits
	// behind the operator's own auth), so --soul-strict/--approve-soul are
	// no-ops for this provenance.
	soulDriver
)

func (p soulProvenance) String() string {
	switch p {
	case soulUser:
		return "user"
	case soulProject:
		return "project"
	case soulDriver:
		return "driver"
	default:
		return "none"
	}
}

// soulMeta is the composition-level metadata ABOUT the selected soul, produced HERE
// (internal/app) and intended for consumption by Item 3's read-only TUI inspector
// (which is OUT of scope for Item 2 — no proto, no RPC, no TUI yet). It carries no
// behaviour; it is a value snapshot of the selection outcome. The zero value
// (Present:false, Provenance:soulNone) means "no soul this run".
type soulMeta struct {
	// Present is true when a soul fragment WILL be contributed this run.
	Present bool
	// Provenance is where the selected soul came from (User|Project|None).
	Provenance soulProvenance
	// Trusted reflects whether the selected soul's provenance is trusted. A user
	// soul is always trusted; a project soul is trusted only with --trust-project.
	Trusted bool
	// Drifted is true when the selected soul's content hash differs from its
	// recorded baseline (Item 1). It is informational: by default a drifted soul
	// still loads (Present stays true) unless --soul-strict dropped it.
	Drifted bool
	// SHA256 is the content hash of the selected soul's clean body, or "" for none.
	SHA256 string
	// Size is the byte length of the selected soul's clean body, or 0 for none.
	Size int
	// PermEffect is the resolved effect of the synthetic "soul:apply" action for this
	// run (issue #14). It is informational (for the slog narration + future use): on a
	// Deny or Ask the soul is WITHHELD (Present stays false) and this records WHY. The
	// zero value ("") means the gate was not consulted (legacy/nil-gate path) or
	// resolved to Allow. There is no proto/RPC/TUI surface for it this pass.
	PermEffect governance.Effect
}

// soulGate resolves the permission effect of the synthetic "soul:apply" action
// (issue #14). It is the composition-layer seam the soul load-gate consults BEFORE
// running the USER/project selection: it shares the SAME governance evaluator +
// file-config resolver the real tool policy uses, so a `soul:apply` rule in
// .mecatl/settings.yaml or the user settings is honoured exactly like a tool rule
// (project rules gated by --trust-project, resolved against cfg.Workspace). It is an
// injectable seam so tests can drive each effect with a fake; the real binding is
// buildSoulGate.
type soulGate interface {
	// Effect returns the resolved governance.Effect for the "soul:apply" action.
	Effect() governance.Effect
}

// soulGateFunc adapts a function to soulGate (the lightweight-closure idiom used
// elsewhere in this package, e.g. commandListerFunc).
type soulGateFunc func() governance.Effect

// Effect implements soulGate.
func (f soulGateFunc) Effect() governance.Effect { return f() }

// buildSoulGate constructs the real soul load-gate: it evaluates the synthetic
// "soul:apply" action against the SAME ruleset (mainRules), the SAME evaluator
// options (mainEvaluatorOptions — the soul gates the MAIN engine, so it carries
// the AudienceMain pin: a `subagent:`-block rule naming soul:apply must never
// bind it), and the SAME build-once file-config resolver (cfg.permResolver —
// the ONE instance Build constructed right after the trust fold; never a second
// permconfig.New, which would be a second cache and discovery pass). Project
// ALLOW rules stay gated behind --trust-project inside that resolver, resolved
// against cfg.Workspace via a READ-ONLY osfs workspace (exactly like the real
// per-session policy, which resolves against each session's root).
//
// A nil/unopenable workspace (or a nil resolver — config off) simply means no
// project-scoped soul:apply rule applies — selection then falls back to the
// built-in floor Allow (the default-on posture). The gate never fails: it is a
// pure read of config the process already trusts. Direct-call tests must fold
// cfg.permResolver themselves (buildPermResolver) — exactly what Build does.
func buildSoulGate(cfg Config) soulGate {
	rules := mainRules(cfg)
	eval := governance.NewEvaluator(rules, mainEvaluatorOptions(cfg)...)
	resolver := cfg.permResolver
	return soulGateFunc(func() governance.Effect {
		var ws tool.WorkspaceReader
		if cfg.Workspace != "" {
			if w, err := osfs.NewWorkspace(cfg.Workspace); err == nil {
				ws = w
			}
			// FAIL-OPEN-TO-FLOOR BY DESIGN: an unopenable workspace leaves ws nil, so the
			// resolver yields only user/CLI rules (no project soul:apply rule) and the gate
			// falls back to the built-in floor Allow. This is deliberate: a user-scoped soul
			// should still load when the PROJECT workspace can't be read. It is distinct
			// from a configured Deny/Ask, which DOES withhold (those are real rules, honoured).
		}
		var extra []governance.Rule
		if resolver != nil {
			extra = resolver.Resolve(context.Background(), ws)
		}
		// planMode=false: the soul is build-time data, not a mutating tool call; plan
		// mode does not apply. EvaluateWith folds the floor Allow with any config
		// rule deny-dominantly, so a config Ask/Deny on soul:apply still wins.
		return eval.EvaluateWith(SoulApplyAction, nil, false, extra).Effect
	})
}

// selectSoulSource owns the full Item-2 selection policy: USER-wins precedence, the
// project-soul trust gate, and the Item-1 drift check applied to WHICHEVER soul
// wins. It returns the chosen prompt.SoulSource (nil when nothing is contributed)
// plus the soulMeta snapshot for Item 3.
//
// It is fail-soft end to end: every candidate is loaded through the SAME soul.Store
// discipline (byte cap, injection scan, fence reject), and an absent/rejected soul
// simply yields no fragment. The drift baseline (checkSoulDrift) runs against the
// SELECTED soul's path only — never both — so there is no double-baseline.
//
// BEFORE any selection it consults the soul load-gate (issue #14): the synthetic
// "soul:apply" governance action, resolved through the SAME evaluator/resolver the
// real tool policy uses. Allow ⇒ proceed; Deny ⇒ withhold (no fragment); Ask ⇒
// withhold-with-warn (the soul is applied at build time with no interactive gate, so
// Ask cannot be satisfied — set soul:apply→allow to apply it). gate may be nil in
// tests/legacy callers, which is treated as Allow (the default-on posture).
//
// PROJECT-SOUL DOUBLE-GATE (Workspace-Trust MUST-FIX 3). The PROJECT soul is admitted
// only if BOTH gates pass — a logical AND:
//  1. TRUST (provenance): cfg.TrustProject (the folded TrustDecision) must be true,
//     else the project soul is withheld (line ~266). "May this repo set a persona at
//     all?"
//  2. soul:apply (policy): even on a trusted repo, soul:apply may Deny/Ask, which
//     withholds. "Does the operator's permission config permit applying a soul this
//     run?"
//
// The two are INDEPENDENT and both must pass. The soul:apply consultation above runs
// first as an early-out (it also governs the always-trusted USER soul, which is never
// trust-gated). For the PROJECT soul the trust gate is the dominant provenance check:
// a project's OWN soul:apply ALLOW rule is itself trust-gated inside buildSoulGate
// (project permission rules only resolve when trusted), so an untrusted project can
// never grant itself soul:apply — the AND holds in every case. An untrusted project
// soul is withheld irrespective of soul:apply; a trusted project soul still obeys an
// explicit soul:apply Deny/Ask. No change to buildSoulGate's internals.
func selectSoulSource(cfg Config, io baselineIO, gate soulGate) (prompt.SoulSource, soulMeta) {
	if cfg.NoSoul {
		cfg.diag().Log(context.Background(), port.LevelInfo, "soul DISABLED (--no-soul)")
		return nil, soulMeta{}
	}

	// Soul load-gate: consult the "soul:apply" permission BEFORE selecting a source.
	// A nil gate (tests/legacy) defaults to Allow — the built-in floor posture.
	if gate != nil {
		switch eff := gate.Effect(); eff {
		case governance.Deny:
			cfg.diag().Log(context.Background(), port.LevelWarn, "soul: withheld by permission policy (soul:apply → deny)")
			return nil, soulMeta{PermEffect: governance.Deny}
		case governance.Ask:
			cfg.diag().Log(context.Background(), port.LevelWarn, "soul: soul:apply resolved to Ask, but the soul is applied at build time with no interactive gate; withholding this run — set soul:apply→allow to apply it")
			return nil, soulMeta{PermEffect: governance.Ask}
		default:
			// Allow (or unrecognised, fail-safe to the default-on posture): proceed.
			_ = eff
		}
	}

	// USER-slot candidate first (always trusted). The remote DRIVER (Phase C1,
	// --soul-source-url) OCCUPIES the user slot when configured — mutually
	// exclusive with --soul-file (validateDriverConfig), and the conventional
	// user file is NOT consulted (one user-slot source, never two). Otherwise
	// the user file: an explicit --soul-file is a user-scoped override of the
	// conventional path; either way it is the operator's own file.
	if cfg.SoulSourceURL != "" {
		if src, meta, selected := selectDriverSoul(cfg); selected {
			return src, meta
		}
		// No usable driver persona (or an unreachable driver on a non-probed
		// test/legacy path — Build's probe is the FATAL gate): like "no user
		// soul", consider the project soul below.
	} else {
		userStore := soul.NewWithEnv(soul.Options{Path: cfg.SoulPath, Diagnostics: cfg.diag()}, soulEnv)
		userRes, _ := userStore.LoadWithMeta(context.Background())
		if userRes.Body != "" {
			// USER-WINS: a present user soul is selected; the project soul is ignored.
			if cfg.SoulPath != "" {
				cfg.diag().Log(context.Background(), port.LevelInfo, "soul ENABLED (user provenance, read-only persona); permission: soul:apply allow (built-in default, overridable to ask/deny via settings)", "path", cfg.SoulPath)
			} else {
				cfg.diag().Log(context.Background(), port.LevelInfo, "soul ENABLED (user provenance, read-only persona); permission: soul:apply allow (built-in default, overridable to ask/deny via settings)",
					"path", "conventional <xdg>/mecatl/soul.md")
			}
			drifted := checkSoulDrift(cfg.diag(), io, userStore.ResolvedPath(), userRes.SHA256, cfg.ApproveSoul)
			if drifted && cfg.SoulStrict {
				cfg.diag().Log(context.Background(), port.LevelWarn, "soul: drifted persona refused (--soul-strict); no soul fragment this run",
					"path", userStore.ResolvedPath(), "provenance", soulUser.String())
				return nil, soulMeta{}
			}
			return userStore, soulMeta{
				Present:    true,
				Provenance: soulUser,
				Trusted:    true,
				Drifted:    drifted,
				SHA256:     userRes.SHA256,
				Size:       userRes.Size,
			}
		}
	}

	// No user-slot soul. Consider the PROJECT soul: <workspace>/.mecatl/soul.md.
	// Resolution is per the build-time cfg.Workspace (the single-workspace embedded
	// server). An empty workspace means there is no project soul to discover.
	if cfg.Workspace == "" {
		cfg.diag().Log(context.Background(), port.LevelInfo, "soul: no fragment (no user soul present; no workspace to discover a project soul)")
		return nil, soulMeta{}
	}
	projectPath := filepath.Join(cfg.Workspace, projectSoulSubpath)
	projectStore := soul.NewWithEnv(soul.Options{Path: projectPath, Diagnostics: cfg.diag()}, soulEnv)
	projRes, _ := projectStore.LoadWithMeta(context.Background())
	if projRes.Body == "" {
		// No project soul on disk (or it was rejected by the loader's discipline).
		cfg.diag().Log(context.Background(), port.LevelInfo, "soul: no fragment (no user or project soul present; fail-soft)")
		return nil, soulMeta{}
	}

	// A project soul EXISTS. It is ingested only when the project tier is admitted —
	// trusted AND the ingestion grant (projectIngestionAdmitted). A not-admitted
	// project soul is dropped silently-but-LOGGED (Warn), never an error.
	if !projectIngestionAdmitted(cfg) {
		cfg.diag().Log(context.Background(), port.LevelWarn, "soul: a project-sourced soul was discovered but is NOT INGESTED (untrusted workspace or project ingestion not granted); dropping it (no fragment). Pass --trust-project (on a headless root) or run --posture auto on an interactive root to honour a soul discovered in this repo (only for a repo you trust)",
			"path", projectPath)
		return nil, soulMeta{Provenance: soulProject, Trusted: false, SHA256: projRes.SHA256, Size: projRes.Size}
	}

	cfg.diag().Log(context.Background(), port.LevelInfo, "soul ENABLED (PROJECT provenance, read-only persona; trusted via --trust-project); permission: soul:apply allow (built-in default, overridable to ask/deny via settings)", "path", projectPath)
	drifted := checkSoulDrift(cfg.diag(), io, projectStore.ResolvedPath(), projRes.SHA256, cfg.ApproveSoul)
	if drifted && cfg.SoulStrict {
		cfg.diag().Log(context.Background(), port.LevelWarn, "soul: drifted persona refused (--soul-strict); no soul fragment this run",
			"path", projectPath, "provenance", soulProject.String())
		return nil, soulMeta{Provenance: soulProject, Trusted: true}
	}
	return projectStore, soulMeta{
		Present:    true,
		Provenance: soulProject,
		Trusted:    true,
		Drifted:    drifted,
		SHA256:     projRes.SHA256,
		Size:       projRes.Size,
	}
}

// selectDriverSoul resolves the USER-slot DRIVER candidate (--soul-source-url,
// Phase C1): dial through the build-scoped conn cache (Build's probe already
// owns the once-guarded close), load + re-validate the body through the
// grpcdriver client (which applies the full soul.ValidateBody discipline —
// a driver is never trusted to sanitize), and select it when usable.
//
//   - Provenance soulDriver, Trusted true: the driver is operator-configured
//     infrastructure (the Phase-B trust tier), exactly like the operator's own
//     user file.
//   - Drift baseline SKIPPED: the baseline is sidecar-FILE machinery keyed on
//     a local path; the driver sits behind the operator's own auth. One INFO
//     line records the skip; --soul-strict/--approve-soul are documented
//     no-ops for this provenance.
//   - selected=false (a dial fault on a non-probed path, or an
//     empty/rejected body) falls through to the PROJECT soul — the same
//     precedence as an absent user soul. Fail-soft, never an error.
func selectDriverSoul(cfg Config) (prompt.SoulSource, soulMeta, bool) {
	conn, _, err := cfg.drivers().dial(cfg, cfg.SoulSourceURL)
	if err != nil {
		cfg.diag().Log(context.Background(), port.LevelWarn, "soul: dialing the soul-source driver failed; no driver soul this run",
			"target", cfg.SoulSourceURL, "err", err)
		return nil, soulMeta{}, false
	}
	src := grpcdriver.NewSoulSource(conn, grpcdriver.SoulOptions{Diagnostics: cfg.diag()})
	// Load once for presence + the snapshot hash/size (the client logs its own
	// fail-soft WARN on a runtime fault and re-validates the body).
	body, _ := src.Load(context.Background())
	if body == "" {
		cfg.diag().Log(context.Background(), port.LevelInfo, "soul: the soul-source driver serves no usable persona; considering a project soul (fail-soft)",
			"target", cfg.SoulSourceURL)
		return nil, soulMeta{}, false
	}
	cfg.diag().Log(context.Background(), port.LevelInfo, "soul ENABLED (driver provenance, read-only persona); permission: soul:apply allow (built-in default, overridable to ask/deny via settings)",
		"target", cfg.SoulSourceURL)
	cfg.diag().Log(context.Background(), port.LevelInfo, "soul: drift baseline SKIPPED for driver provenance (--soul-strict/--approve-soul are no-ops; the driver is operator-run infrastructure behind its own auth)")
	return src, soulMeta{
		Present:    true,
		Provenance: soulDriver,
		Trusted:    true,
		SHA256:     hashutil.SHA256Hex([]byte(body)),
		Size:       len(body),
	}, true
}
