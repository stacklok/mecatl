package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// posture.go is the COMPOSITION-LAYER operator-POSTURE resolver: the graduated
// trust/automation ladder that subsumes the historical --yolo / --trust-project
// switches into ONE ordered tier (strict < trusted < auto < yolo). Like trust.go
// it is a pure composition concern — engine/governance stays session-free and
// posture-unaware; posture only COMPOSES the derived knobs (the allow-all rule,
// the evaluator's loose-substitution option, and the root-aware project-trust
// floor) and every decision still goes THROUGH governance.Evaluate, never a bypass.
//
// The ladder (owner-approved), and the knobs each tier derives in applyPosture:
//
//	posture | AllowAllTools | main loose-subst | child loose-subst     | TrustProject floor
//	--------|---------------|------------------|----------------------|-------------------
//	strict  | false         | false            | false                | (trust sources only)
//	trusted | false         | false            | false                | interactive-only
//	auto    | true          | true             | false (child def ON) | interactive-only
//	yolo    | true          | true             | TRUE  (child def OFF)| interactive-only
//
// Issue #359. TrustProject is the ONE effective positive workspace-trust
// decision after resolveTrust. It gates project-tier ingestion through the named
// projectIngestionAdmitted seam and directly gates the read-only subagent shell
// (git worktree operations against the repo's .git, the issue-#40 fork-time-RCE
// surface). The fail-safe headless default is achieved by making the posture
// ladder root-aware: a HEADLESS root never raises TrustProject from posture, while
// explicit/declarative/remembered trust can still make it true. An INTERACTIVE
// root keeps the historical trusted/auto/yolo floor. Thus an untrusted headless
// repo gets neither steering nor a read-only child shell, while every legitimate
// trust source admits both.
//
// "child loose-subst" is the NEW Config.LooseChildSubstitution: under yolo a
// child/subagent/branch engine ALSO loosens the built-in substitution Ask floor
// (so a $()/backtick/heredoc command auto-runs), turning OFF the child
// prompt-injection defense — the deliberate behaviour change yolo now carries.
// strict/trusted/auto keep that defense ON (childEvaluatorOptions omits
// WithLooseSubstitution), so only yolo lets a child auto-run a substitution.
//
// PostureStrict is iota-zero by design: a zero/unrecognised value fails CLOSED to
// the safest tier, the structural backstop that mirrors the rest of the harness's
// fail-closed posture (an unknown --posture WARNs and lands here, never refusing
// boot).

// Posture is the graduated operator trust/automation tier. Higher = more
// automation, less prompting. Zero value (PostureStrict) is the fail-closed
// default.
type Posture int

// ORDER IS CONTRACT: comparisons depend on it. The tiers are ordered
// strict < trusted < auto < yolo and code RELIES on the integer ordering — the
// alias MAX-fold (ResolveAliasPosture), the ceiling clamp, narratePosture's `>=`
// flags, and the root-refusal's `>= PostureAuto` all use `<`/`>=` directly. Adding
// a tier or reordering these breaks those silently; TestPostureIotaOrderingIsContract
// pins it. Posture stays an int (not a string-backed type) precisely so these
// ordered comparisons are cheap and obvious; the string token is only its
// serialization (String/parsePosture).
const (
	// PostureStrict is the DEFAULT (iota zero = fail-closed backstop): no allow-all,
	// no substitution loosening anywhere, project trust left at the operator's own
	// --trust-project. Every mutate prompts.
	PostureStrict Posture = iota
	// PostureTrusted raises the project-trust floor (honour a discovered project's
	// ALLOW rules) but loosens NOTHING else — it is the alias --trust-project maps to.
	PostureTrusted
	// PostureAuto is the recommended UNATTENDED default: allow-all (main + children),
	// main substitution loosened, project trust raised — but the child
	// prompt-injection defense stays ON (a child's $()/backtick/heredoc still resolves
	// through the child-ask model, never auto-run).
	PostureAuto
	// PostureYolo is truly-off, GATE-FREE: everything PostureAuto does, PLUS the child
	// substitution floor is loosened (LooseChildSubstitution) — child prompt-injection
	// defense OFF, $()/backtick/heredoc auto-run in subagents/branches. Isolated,
	// ephemeral, single-tenant deployments only.
	PostureYolo
)

// String renders a Posture as its CLI/wire token.
func (p Posture) String() string {
	switch p {
	case PostureTrusted:
		return "trusted"
	case PostureAuto:
		return "auto"
	case PostureYolo:
		return "yolo"
	default:
		return "strict"
	}
}

// ParsePosture maps a CLI --posture token to a Posture for the composition roots
// (cmd/mecated, cmd/mecatui), which set Config.Posture from the flag string. It is
// the EXPORTED alias of parsePosture so the cmd layer never re-implements the
// strict/trusted/auto/yolo grammar (empty/whitespace/unknown fail CLOSED to
// PostureStrict; the cmd WARNs on an unknown value via IsKnownPostureToken).
func ParsePosture(s string) Posture { return parsePosture(s) }

// IsKnownPostureToken reports whether s is a recognised posture token (so the cmd
// layer can WARN on an unknown --posture value before it silently fails closed to
// strict). Empty is "known" (the unset default).
func IsKnownPostureToken(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "strict", "trusted", "auto", "yolo":
		return true
	default:
		return false
	}
}

// ResolveAliasPosture folds the explicit --posture tier with the --yolo /
// --trust-project ALIASES, MAX-tier: --yolo raises to yolo, --trust-project raises to
// >=trusted, and an alias can only RAISE (never lower an explicit higher --posture).
// It is the ONE place the alias grammar lives — both cmd roots (their CLI-only
// pre-check / refusal) AND the composition-layer resolvePosture call it, so the
// security-relevant fold cannot drift between sites (the same "ONE grammar, cmd never
// re-implements" discipline as ParsePosture). It is PURE: no YAML, no diagnostics — the
// WARN for an alias raising above an explicit lower flag lives in resolvePosture, which
// has the cfg/diag context.
func ResolveAliasPosture(flag Posture, yolo, trustProject bool) Posture {
	resolved := flag
	if yolo && PostureYolo > resolved {
		resolved = PostureYolo
	}
	if trustProject && PostureTrusted > resolved {
		resolved = PostureTrusted
	}
	return resolved
}

// PostureRefusalReason returns a non-nil error when a posture that GRANTS allow-all
// (auto or yolo — both waive the built-in mutate-ask floor) is requested while the
// process is PRIVILEGED (running as root WITHOUT a declared sandbox). It is the ONE
// definition of the root/no-sandbox refusal, shared by the cmd-layer fast-path and
// (authoritatively) by Build, which calls it AFTER applyPosture so an operator-YAML
// `posture:` tier cannot escape the refusal the CLI flags always hit. strict and
// trusted are NEVER refused (they suppress no prompt). The privileged predicate is
// computed in the cmd layer (it owns the os.Geteuid / MECATL_SANDBOX reads) and
// threaded in as a bool, keeping os out of internal/app.
func PostureRefusalReason(p Posture, privileged bool) error {
	if p >= PostureAuto && privileged {
		return fmt.Errorf("posture %q refused: running as root (euid 0) without a declared sandbox; set MECATL_SANDBOX=1 (or IS_SANDBOX=1) to affirm an isolated, disposable environment", p.String())
	}
	return nil
}

// parsePosture maps a CLI/YAML token to a Posture. Empty / whitespace / unknown
// fail CLOSED to PostureStrict (the caller WARNs on an unknown value but never
// refuses boot).
func parsePosture(s string) Posture {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trusted":
		return PostureTrusted
	case "auto":
		return PostureAuto
	case "yolo":
		return PostureYolo
	default:
		return PostureStrict
	}
}

// postureNoCeiling is the sentinel "no ceiling" value for resolvePosture's ceiling
// parameter: a Posture above every real tier so min(resolved, ceiling) is a no-op.
// A future managed-scope ceiling (NOT v1) passes a real tier here to clamp.
const postureNoCeiling = Posture(1 << 30)

// resolvePosture folds, MAX-tier across every source, the explicit --posture flag,
// the --yolo / --trust-project ALIASES, and the operator-global settings.yaml
// `posture:` key (already folded onto cfg.Posture by foldOperatorPosture before
// this runs — CLI out-ranks YAML because the explicit flag and the aliases are
// taken from cfg here and an alias can only RAISE). Resolution is MAX-tier so an
// alias raises but never silently downgrades an explicit lower --posture; when an
// alias raised above an explicit lower --posture flag, it WARNs (the operator's
// explicit choice was superseded by an alias they also passed).
//
// ceiling clamps the result with a final min(resolved, ceiling); pass
// postureNoCeiling for the v1 unbounded behaviour. Ceiling ENFORCEMENT (a managed
// scope) is out of v1 — the parameter exists now so adding it is a one-line caller
// change, not a signature churn.
func resolvePosture(cfg Config, ceiling Posture) Posture {
	explicit := cfg.Posture // the folded --posture flag + operator-YAML value
	// The alias MAX-fold lives in ONE place (ResolveAliasPosture) so the cmd roots and
	// composition cannot drift on the security-relevant grammar.
	resolved := ResolveAliasPosture(explicit, cfg.AllowAllTools, cfg.TrustProject)

	// WARN when an alias raised the tier ABOVE an explicit lower --posture flag (the
	// operator's explicit choice was superseded by an alias they also passed). The WARN
	// lives here, not in the pure helper, because it needs the cfg/diag context.
	if cfg.PostureFlagSet && resolved > explicit {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"posture: an alias (--yolo/--trust-project) raised the effective posture above the explicit --posture value",
			"explicit", explicit.String(), "effective", resolved.String())
	}

	if resolved > ceiling {
		resolved = ceiling
	}
	return resolved
}

// applyPosture maps the resolved tier to the derived composition knobs per the
// ladder table: AllowAllTools (the allow-all rule, main + children),
// LooseChildSubstitution (child substitution loosening, yolo only), and the
// project-trust floor. The floor is raised for trusted/auto/yolo on INTERACTIVE
// roots only; a HEADLESS root's ladder never raises TrustProject. Explicit trust
// survives because this function only raises. It MUST run BEFORE resolveTrust so
// the trust fold and permission resolver see the posture-derived interactive
// floor.
func applyPosture(cfg Config) Config {
	switch cfg.Posture {
	case PostureYolo:
		cfg.AllowAllTools = true
		cfg.LooseChildSubstitution = true
		// TrustProject floor: INTERACTIVE-only. On a HEADLESS root the ladder does
		// NOT raise TrustProject — the fail-safe default (an untrusted clone gets no
		// ingestion and no subagent shell without an explicit --trust-project).
		if !cfg.Headless {
			cfg.TrustProject = true
		}
	case PostureAuto:
		cfg.AllowAllTools = true
		cfg.LooseChildSubstitution = false
		if !cfg.Headless {
			cfg.TrustProject = true
		}
	case PostureTrusted:
		if !cfg.Headless {
			cfg.TrustProject = true
		}
	default: // PostureStrict: derive nothing; leave the operator's own flags.
	}
	return cfg
}

// foldOperatorPosture merges the OPERATOR-TIER `posture:` YAML scalar (read by the
// permconfig resolver from the user-global + CLI tiers ONLY — never the project
// file, which is IGNORED with a WARN: a malicious repo's .mecatl/settings.yaml
// posture: yolo must never be honoured) onto cfg.Posture. A CLI --posture
// (cfg.PostureFlagSet) OUT-RANKS the YAML value. It is a no-op when no operator-tier
// posture: key was configured. cfg is taken and returned by value (Build holds a
// local cfg).
func foldOperatorPosture(cfg Config) Config {
	if cfg.PostureFlagSet {
		return cfg // CLI wins; YAML cannot lower or raise an explicit flag.
	}
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	yamlPosture := strings.TrimSpace(res.OperatorPosture())
	if yamlPosture == "" {
		return cfg
	}
	cfg.Posture = parsePosture(yamlPosture)
	cfg.diag().Log(context.Background(), port.LevelWarn,
		"posture: the settings.yaml posture: key is DEPRECATED; use permissionMode: instead (ADR 0365)",
		"posture", yamlPosture)
	return cfg
}

// ResolveAuthoritativePosture computes the posture tier Build resolves: it folds the
// OPERATOR-TIER `posture:` YAML scalar (CLI --posture out-ranks), then resolves the
// tier (aliases + ceiling clamp). It does NOT fold workspace trust — that is Build's
// job, surfaced by the `operator posture` startup diagnostic (narratePosture) after
// resolveTrust. Callers that need only the tier (mecatui embedded posture, the
// mecated fast-path refusal) use this; callers that need the full root-aware trust
// decision drive the real Build and read its structured diagnostic.
func ResolveAuthoritativePosture(cfg Config) Posture {
	cfg.permResolver = buildPermResolver(cfg)
	// Fold the permission-mode token first (ADR 0365), so an operator-YAML-only
	// permissionMode: auto/yolo reaches the cmd fast paths. An invalid token is
	// Build's to report; the pre-check falls back to the deprecated surface.
	if folded, err := foldPermissionMode(cfg); err == nil {
		cfg = folded
	} else {
		cfg = foldOperatorPosture(cfg)
	}
	return resolvePosture(cfg, postureNoCeiling)
}

// narratePosture logs the resolved posture and authoritative, fully-folded
// workspace trust as one build-once composition fact. Build calls it only after
// resolveTrust, so it cannot contradict the following workspace-trust narration.
// It is the sole observation/debug surface for the resolved posture + per-defence
// state; there is no separate print-and-exit CLI.
func narratePosture(diag port.Diagnostics, p Posture, trustProject bool) {
	diag.Log(context.Background(), port.LevelInfo, "operator posture",
		"posture", p.String(),
		"allow_all", p >= PostureAuto,
		"main_loose_substitution", p >= PostureAuto,
		"child_loose_substitution", p == PostureYolo,
		"project_ingestion", trustProject,
		"trust_project", trustProject)
}
