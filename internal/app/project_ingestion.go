package app

// project_ingestion.go is the COMPOSITION-LAYER project-tier-INGESTION admission
// seam (issue #359 redesign). It is a peer of posture.go/trust.go: a pure
// composition concern that engine/ stays unaware of. The admission decision is the
// conjunction of TWO independent positive grants:
//
//   - the WORKSPACE-TRUST fold (cfg.TrustProject — flag/declared/remembered, raised
//     by the posture ladder at trusted/auto/yolo and collapsed by resolveTrust); and
//   - the INGESTION grant (cfg.ProjectIngestionGranted — root-aware, raised by
//     applyPosture: an explicit --trust-project opts in on BOTH roots, and the
//     interactive ladder grants it at auto/yolo; a HEADLESS root does NOT grant it
//     via the ladder, so a dark factory over a freshly-cloned untrusted repo ingests
//     NONE of the repo's steering unless the operator explicitly passes
//     --trust-project).
//
// The two-axis design replaces the former suppressor pin: the
// negative knob had no caller once headless became opt-in and interactive followed
// the ladder. The read-only subagent/member shell is a SEPARATE axis
// (cfg.SubagentShellGranted, posture >= auto on both roots) and does NOT ride
// TrustProject.

// projectIngestionAdmitted is the SINGLE expression for "admit the project's
// repo-resident steering" — trust AND the ingestion grant. Every ingestion
// consumer (permconfig ALLOW rules, project soul, agent defs, skills, rules, slash
// commands, the git snapshot, and AGENTS.md/CLAUDE.md) reads THIS, never
// cfg.TrustProject directly. The four shell consumers read cfg.SubagentShellGranted
// instead (see build.go) — they are NOT ingestion consumers.
func projectIngestionAdmitted(cfg Config) bool {
	return cfg.TrustProject && cfg.ProjectIngestionGranted
}
