package app

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/workspacetrust"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// trust.go is the COMPOSITION-LAYER workspace-trust resolver (Workspace-Trust
// feature, Phase 1; MUST-FIX 2). It folds the per-invocation --trust-project flag
// and the declarative `trustedWorkspaces:` list (read from the user-global
// settings.yaml by the workspacetrust adapter) into ONE answer — a TrustDecision
// — produced once per process here in internal/app.
//
// The decision's Trusted bool becomes Config.TrustProject and feeds the named
// projectIngestionAdmitted seam plus the read-only worktree-shell gate. Thus
// explicit, declarative, remembered, and interactive-ladder trust all admit the
// same project authority set and shell. Adapters still take a plain bool. Trust is MONOTONIC-POSITIVE: it only ever GRANTS admission; it never
// suppresses a Deny or a configured Ask (those compose deny-dominantly in the
// evaluator regardless — see internal/adapter/permconfig).
//
// TrustDecision / TrustSource are COMPOSITION types, not domain or adapter types
// — governance/session/prompt/tool stay trust-unaware, exactly as they are for
// --trust-project today. Trust is a composition gate that FEEDS admission.

// TrustSource records WHY a workspace is (or is not) trusted, for the slog
// narration that mirrors the soul-selection narration (soulMeta).
type TrustSource int

const (
	// TrustNone means the workspace is not trusted this run.
	TrustNone TrustSource = iota
	// TrustFlag means trust came from the --trust-project one-shot flag.
	TrustFlag
	// TrustDeclared means the workspace's realpath is in the operator-authored
	// settings.yaml `trustedWorkspaces:` list.
	TrustDeclared
	// TrustRemembered is reserved for Phase 2 (the machine-written trust.yaml
	// registry). It is declared here so the value taxonomy is stable across
	// phases; Phase 1 never produces it.
	TrustRemembered
)

// String renders a TrustSource for the slog narration.
func (s TrustSource) String() string {
	switch s {
	case TrustFlag:
		return "flag"
	case TrustDeclared:
		return "declared"
	case TrustRemembered:
		return "remembered"
	default:
		return "none"
	}
}

// TrustDecision is the single composition-level answer to "is this workspace
// trusted, and why?" (MUST-FIX 2). Its Trusted field is the effective bool fed to
// the admission consumers; Source drives the log narration. Drifted is true when a
// remembered (trust.yaml) registry entry existed but its identity-anchor hash no
// longer matches the live anchor (Phase 2b) — a drifted entry FAILS SAFE to
// untrusted here (mecated has no prompt; Phase 2c's mecatui turns Drifted into a
// re-prompt).
type TrustDecision struct {
	// Trusted is the effective admission bool: honour the project's ALLOW rules
	// and project soul when true.
	Trusted bool
	// Source records why (for the slog narration).
	Source TrustSource
	// Drifted is true when a remembered registry entry existed but its anchor hash
	// mismatched the live identity anchor. When Drifted, Trusted is FALSE (fail-safe).
	Drifted bool
}

// trustEnv is the PATH-RESOLUTION environment the declarative-trust reader uses to
// locate the user-global settings.yaml. It defaults to the real process
// environment; tests override it (with t.Cleanup-style restoration) so the
// declarative-trust checks run fully offline against a faked XDG/home — never the
// developer's real ~/.config.
var trustEnv = xdgconfig.OSEnv

// resolveTrust folds, highest first, the per-invocation flag, the declarative list,
// and the machine-written registry into a TrustDecision for cfg.Workspace
// (MUST-FIX 2; R1.2 + R2.2):
//
//	--trust-project          ⇒ TrustFlag        (one-shot operator override)
//	trustedWorkspaces match  ⇒ TrustDeclared    (realpath-keyed; MUST-FIX 5.1)
//	trust.yaml entry, anchor  ⇒ TrustRemembered  (remembered + anchor MATCHES live)
//	  matches
//	trust.yaml entry, anchor  ⇒ TrustNone+Drifted (remembered but anchor DRIFTED —
//	  mismatches                                   fail-safe untrusted; 2c re-prompts)
//	otherwise                ⇒ TrustNone
//
// The flag and declared list win over a remembered entry for the SOURCE label
// (precedence), and they short-circuit BEFORE the registry/anchor are even read —
// an explicitly-trusted run never pays the anchor-hash cost and never drifts. A
// remembered entry only grants when its stored identity-anchor hash equals the
// LIVE anchor; a mismatch fails safe to untrusted with Drifted=true (the drift
// signal). Trust is monotonic-positive: no input can revoke a Deny or downgrade an
// Ask — they gate admission only. resolveTrust performs NO writes and never errors:
// a missing/corrupt settings.yaml or trust.yaml simply yields TrustNone (the
// workspacetrust reader fails safe).
func resolveTrust(cfg Config) TrustDecision {
	if cfg.TrustProject {
		return TrustDecision{Trusted: true, Source: TrustFlag}
	}
	if cfg.Workspace == "" {
		return TrustDecision{Trusted: false, Source: TrustNone}
	}

	reader := workspacetrust.NewWithEnv(trustEnv).WithDiagnostics(cfg.diag())
	if reader.IsDeclared(cfg.Workspace) {
		return TrustDecision{Trusted: true, Source: TrustDeclared}
	}

	// Remembered tier: consult the machine-written registry. Compute the live
	// identity-anchor hash ONCE and ask the registry whether a matching/drifted
	// entry exists. A remembered+undrifted entry grants TrustRemembered; a
	// remembered+drifted entry FAILS SAFE to untrusted (Drifted=true) — mecated has
	// no prompt, and Phase 2c's mecatui turns Drifted into a re-prompt.
	anchor := reader.AnchorHash(cfg.Workspace)
	remembered, drifted := reader.Remembered(cfg.Workspace, anchor)
	if remembered {
		if drifted {
			return TrustDecision{Trusted: false, Source: TrustNone, Drifted: true}
		}
		return TrustDecision{Trusted: true, Source: TrustRemembered}
	}
	return TrustDecision{Trusted: false, Source: TrustNone}
}

// ResolveTrust is the EXPORTED composition-level trust fold, for a SECOND
// composition root that must learn the trust decision BEFORE it calls Build —
// specifically the mecatui pre-TUI first-encounter prompt (Workspace-Trust Phase
// 2c). It returns the SAME TrustDecision Build computes internally (it delegates to
// the same unexported resolveTrust), so the prompt and Build never disagree.
//
// The prompt uses it to decide whether to fire: a TrustNone-with-authority or a
// Drifted decision prompts; an already-trusted (Flag/Declared/Remembered) decision
// does NOT. When the operator then answers "trust"/"trust-once", mecatui sets
// cfg.TrustProject=true so Build's own fold short-circuits to TrustFlag (trusted)
// — the prompt OUTCOME wins and Build does NOT re-resolve or re-prompt. When the
// operator declines (or the decision was already trusted declaratively), mecatui
// leaves cfg.TrustProject as-is and Build re-resolves to the SAME answer this call
// produced (idempotent: resolveTrust performs no writes and no prompt).
func ResolveTrust(cfg Config) TrustDecision { return resolveTrust(cfg) }

// HasProjectAuthority reports whether cfg.Workspace carries a project AUTHORITY
// SET worth gating behind the pre-TUI trust prompt (a project soul, project-tier
// agents/commands/skills, or a project settings.yaml with ALLOW rules). It is the
// composition-side wrapper over the workspacetrust adapter's presence probe, so the
// mecatui prompt asks ONLY when a trust grant would actually change what is
// admitted — a not-trusted repo with nothing to gate is never nagged. It reuses
// trustEnv only for symmetry; the probe itself walks the workspace tree (no XDG
// read), so it is fail-safe quiet (any IO/parse failure ⇒ "no authority").
func HasProjectAuthority(cfg Config) bool {
	if cfg.Workspace == "" {
		return false
	}
	return workspacetrust.NewWithEnv(trustEnv).WithDiagnostics(cfg.diag()).HasProjectAuthority(cfg.Workspace)
}

// RememberTrust persists a "trust" grant for cfg.Workspace to the machine-written
// trust.yaml registry, capturing the LIVE identity-anchor hash at trustedAt. It is
// the ONLY production caller of workspacetrust.Remember (Phase 2c), invoked by the
// mecatui prompt when the operator answers "trust" (persist). "trust-once" trusts
// the run WITHOUT calling this (nothing is persisted). trustedAt is injected by the
// caller (composition supplies time.Now via a clock seam) so tests are
// deterministic. A write failure is returned for the caller to log fail-soft — a
// failed persist must never abort startup; the run still proceeds trusted.
func RememberTrust(cfg Config, trustedAt time.Time) error {
	if cfg.Workspace == "" {
		return nil
	}
	reader := workspacetrust.NewWithEnv(trustEnv).WithDiagnostics(cfg.diag())
	return reader.Remember(cfg.Workspace, reader.AnchorHash(cfg.Workspace), trustedAt)
}

// narrateTrust logs the trust decision (mirroring the soulMeta narration): an Info
// line for the resolved state, so the why-trusted story is visible in the
// composition log exactly like the soul selection is. A DRIFTED decision (a
// remembered entry whose identity anchor changed) logs at Warn in the
// applyTrustGate drop-report style — the workspace's identity surface changed since
// it was trusted, so it has been re-gated to UNTRUSTED for this run (Phase 2c's
// mecatui will turn this into a re-prompt; mecated stays declarative).
func narrateTrust(diag port.Diagnostics, d TrustDecision, workspace string) {
	if d.Drifted {
		diag.Log(context.Background(), port.LevelWarn, "workspace trust: identity anchor DRIFTED since the workspace was trusted; re-gated to untrusted (run mecatui to re-confirm trust)",
			"trusted", d.Trusted,
			"source", d.Source.String(),
			"drifted", d.Drifted,
			"workspace", workspace)
		return
	}
	diag.Log(context.Background(), port.LevelInfo, "workspace trust",
		"trusted", d.Trusted,
		"source", d.Source.String(),
		"drifted", d.Drifted,
		"workspace", workspace)
}
