package app

import (
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// no_project_trust.go is the COMPOSITION-LAYER project-tier-INGESTION suppression
// seam (issue #359). It is a peer of posture.go/trust.go: a pure composition concern
// that engine/ stays unaware of. The single knob Config.NoProjectIngest suppresses
// ONLY the ingestion of a repo's repo-resident steering (permconfig ALLOW rules,
// project soul, agent defs, skills, rules, slash commands, the git snapshot, and
// AGENTS.md/CLAUDE.md) — it NEVER touches Config.TrustProject, so the read-only
// subagent-shell trust gate keeps reading the real trust decision.
//
// The motivating deployment is a scheduler (mecatequi/mecak8s) running over a
// freshly-cloned UNTRUSTED repo: the operator passes --trust-project so the read-only
// subagent shell is built (the child needs a shell to explore), but does NOT want the
// repo's own AGENTS.md / .mecatl rules / agents / skills / soul steering the model —
// the scheduler supplies its own --instructions. --no-project-trust is that pin.

// ingestProjectTier is the SINGLE expression for "admit the project's repo-resident
// steering" — trust AND not suppressed. Every ingestion consumer reads THIS, never
// cfg.TrustProject directly. The subagent-shell trust gate (build.go ~4460) is NOT an
// ingestion consumer and keeps reading cfg.TrustProject.
func ingestProjectTier(cfg Config) bool { return cfg.TrustProject && !cfg.NoProjectIngest }

// foldOperatorNoProjectTrust merges the OPERATOR-TIER no-project-trust: YAML bool
// (read by the permconfig resolver from the user-global + CLI tiers ONLY — never the
// project file, which is IGNORED with a WARN) onto cfg.NoProjectIngest. A CLI
// --no-project-trust (cfg.NoProjectTrustFlagSet) OUT-RANKS the YAML value. It is a
// no-op when no operator-tier key was configured. Mirrors foldOperatorPosture /
// foldOperatorPlanModeAutoApprove. cfg is taken and returned by value (Build holds a
// local cfg).
func foldOperatorNoProjectTrust(cfg Config) Config {
	if cfg.NoProjectTrustFlagSet {
		return cfg // CLI wins; YAML cannot lower or raise an explicit flag.
	}
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	if res.OperatorNoProjectTrust() {
		cfg.NoProjectIngest = true
	}
	return cfg
}
