package app

// project_ingestion.go is the COMPOSITION-LAYER project-tier ingestion
// admission seam (issue #359). TrustProject is the ONE effective positive
// workspace-trust decision after resolveTrust folds the root-aware posture floor
// with explicit, declarative, and remembered trust. The named helper remains the
// single semantic seam every project-tier consumer uses, preserving one audit
// point and a future split point without inventing a second synchronized grant.

// projectIngestionAdmitted reports whether project-resident steering may be
// ingested. Every ingestion consumer (permission ALLOW/model bindings, project
// soul, agent defs and project agent memory, skills, rules, slash commands, git
// snapshot, and AGENTS.md/CLAUDE.md) reads this helper. The read-only worktree
// shell also reads cfg.TrustProject directly because both decisions intentionally
// express the same operator vouch for the workspace and its .git.
func projectIngestionAdmitted(cfg Config) bool {
	// Local project trust cannot authorize source ingestion for a remote
	// deployment; its explicit no-FS attenuation has no project source either.
	return cfg.TrustProject && !cfg.RemoteExecution
}

// projectIngestionAdmittedForRoot binds the single project-ingestion decision to
// its configured canonical launch root. Session-selected alternate roots never
// inherit a trust decision made for cfg.Workspace.
func projectIngestionAdmittedForRoot(cfg Config, root string) bool {
	return projectIngestionAdmitted(cfg) && root != "" && root == cfg.Workspace
}
