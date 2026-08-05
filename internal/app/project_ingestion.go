package app

// project_ingestion.go is the COMPOSITION-LAYER project-tier-INGESTION admission
// seam (issue #359 redesign). It is a peer of posture.go/trust.go: a pure
// composition concern that engine/ stays unaware of. The admission decision is the
// conjunction of TWO positive grants:
//
//   - the WORKSPACE-TRUST fold (cfg.TrustProject — flag/declared/remembered; on
//     INTERACTIVE roots the posture ladder also raises it at trusted/auto/yolo,
//     collapsed by resolveTrust; on a HEADLESS root the ladder does NOT raise it,
//     the fail-safe default); and
//   - the INGESTION grant (cfg.ProjectIngestionGranted — root-aware, raised by
//     applyPosture: an explicit --trust-project opts in on BOTH roots, and the
//     interactive ladder grants it at auto/yolo; a HEADLESS root does NOT grant it
//     via the ladder, so a dark factory over a freshly-cloned untrusted repo ingests
//     NONE of the repo's steering unless the operator explicitly passes
//     --trust-project).
//
// On a HEADLESS root the two grants are COINCIDENT (both effective only via an
// explicit --trust-project); on an INTERACTIVE root the ladder grants both at
// auto/yolo. The read-only subagent/member shell is gated on cfg.TrustProject
// directly (the operator vouches for the repo's `.git` — the issue-#40
// fork-time-RCE surface), NOT on this ingestion grant.
//
// The two-grant design replaces the former suppressor pin: the
// negative knob had no caller once headless became opt-in and interactive followed
// the ladder.

// projectIngestionAdmitted is the SINGLE expression for "admit the project's
// repo-resident steering" — trust AND the ingestion grant. Every ingestion
// consumer (permconfig ALLOW rules, project soul, agent defs, skills, rules, slash
// commands, the git snapshot, and AGENTS.md/CLAUDE.md) reads THIS, never
// cfg.TrustProject directly. The shell gate (buildSandboxedCommandRunner) reads
// cfg.TrustProject (see build.go) — it is NOT an ingestion consumer.
func projectIngestionAdmitted(cfg Config) bool {
	return cfg.TrustProject && cfg.ProjectIngestionGranted
}
