package rulesfs

import (
	"path/filepath"

	"github.com/stacklok/mecatl/engine/prompt"
)

// Conventional rule sub-paths (Claude-Code-style "extra paths"). A rule is
// laid out as <conventional-dir>/<name>.md.
const (
	// ProjectDirMecatl is the project-level rules dir under the workspace.
	ProjectDirMecatl = ".mecatl/rules"
	// ProjectDirClaude is the Claude-Code-compatible project-level rules dir.
	ProjectDirClaude = ".claude/rules"
	// userSubdirMecatl is the user-level rules sub-path under the XDG config dir.
	userSubdirMecatl = "mecatl/rules"
	// userSubdirClaude is the Claude-Code-compatible user-level rules sub-path,
	// rooted at the home directory (~/.claude/rules).
	userSubdirClaude = ".claude/rules"
)

// ResolveOptions configures the known-path resolver. There is NO explicit tier
// (issue #329 operator amendment: conventional discovery is the ONLY admission
// path, always-on in production). Conventional exists so tests can disable the
// lanes; the zero value resolves NOTHING.
type ResolveOptions struct {
	// Conventional, when true, admits the built-in conventional project- and
	// user-level locations. Always true in production; the field exists so a
	// test can resolve a single lane (or none) in isolation.
	Conventional bool
	// Workspace is the session workspace root used to resolve the project-level
	// conventional paths. Only consulted when Conventional is true and non-empty.
	Workspace string
	// IncludeProjectTier, when true, admits the PROJECT-tier conventional locations
	// (<workspace>/.mecatl/rules, <workspace>/.claude/rules). The composition layer
	// sets it false when the workspace is UNTRUSTED (the Workspace-Trust fold) so
	// a cloned repo's project rules cannot steer the model before the operator
	// trusts it; the user-tier sources stay active regardless. It gates ONLY the
	// project tier. Mirrors agentfs.ResolveOptions.IncludeProjectTier.
	//
	// ZERO-VALUE NOTE: false by default; callers set it explicitly. Composition
	// uses the folded trust bool. Only Conventional==true makes the project tier
	// eligible.
	IncludeProjectTier bool
}

// ResolveSources builds the ORDERED, highest-precedence-first Source list from
// the conventional locations, ready to hand to NewMultiSource. The precedence
// is:
//
//	project: <workspace>/.mecatl/rules, <workspace>/.claude/rules   [highest]
//	  > user: $XDG_CONFIG_HOME/mecatl/rules (or ~/.config/mecatl/rules),
//	          ~/.claude/rules                                       [lowest]
//
// so a project rule overrides a personal one of the same name. Each location
// becomes a labelled DirSource; missing directories are harmless. When
// Conventional is false NO sources are included (the test-isolation lane).
func ResolveSources(opts ResolveOptions) []RuleSource {
	return resolveSourcesEnv(opts, OSEnv)
}

// resolveSourcesEnv is ResolveSources with an injectable environment, for tests.
// Each source carries its admission TIER (prompt.Rule.Origin on the port) — a
// closed label, never a location.
func resolveSourcesEnv(opts ResolveOptions, env ResolveEnv) []RuleSource {
	var sources []RuleSource

	if !opts.Conventional {
		return sources
	}

	// Project tier — withheld when the workspace is untrusted
	// (IncludeProjectTier=false). The user-tier sources below are NEVER gated.
	if opts.Workspace != "" && opts.IncludeProjectTier {
		sources = append(sources,
			DirSource{Dir: filepath.Join(opts.Workspace, ProjectDirMecatl), Label: "project(.mecatl)", Tier: prompt.RuleOriginProject},
			DirSource{Dir: filepath.Join(opts.Workspace, ProjectDirClaude), Label: "project(.claude)", Tier: prompt.RuleOriginProject},
		)
	}

	if cfg := UserConfigDir(env); cfg != "" {
		sources = append(sources, DirSource{Dir: filepath.Join(cfg, userSubdirMecatl), Label: "user(xdg)", Tier: prompt.RuleOriginUser})
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		sources = append(sources, DirSource{Dir: filepath.Join(home, userSubdirClaude), Label: "user(.claude)", Tier: prompt.RuleOriginUser})
	}

	return sources
}
