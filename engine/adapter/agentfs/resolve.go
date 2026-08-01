package agentfs

import (
	"path/filepath"

	"github.com/stacklok/mecatl/engine/tool"
)

// Conventional agent-def sub-paths (Claude-Code-style "extra paths"). A def is
// laid out as <conventional-dir>/<name>.md.
const (
	// ProjectDirMecatl is the project-level agents dir under the workspace.
	ProjectDirMecatl = ".mecatl/agents"
	// ProjectDirClaude is the Claude-Code-compatible project-level agents dir.
	ProjectDirClaude = ".claude/agents"
	// userSubdirMecatl is the user-level agents sub-path under the XDG config dir.
	userSubdirMecatl = "mecatl/agents"
	// userSubdirClaude is the Claude-Code-compatible user-level agents sub-path,
	// rooted at the home directory (~/.claude/agents).
	userSubdirClaude = ".claude/agents"
)

// ResolveOptions configures the known-path resolver. The zero value resolves
// NOTHING (no explicit paths, conventional set OFF) — discovery stays strictly
// opt-in unless the operator asks for it, since a def body steers the model.
type ResolveOptions struct {
	// Explicit are operator-configured directories (e.g. from a repeatable
	// --agents-dir flag), highest precedence, in the order given. Always honoured
	// regardless of Conventional.
	Explicit []string
	// Conventional, when true, adds the built-in conventional project- and
	// user-level locations (lower precedence than Explicit). Default false: a
	// strict opt-in.
	Conventional bool
	// Workspace is the session workspace root used to resolve the project-level
	// conventional paths. Only consulted when Conventional is true and non-empty.
	Workspace string
	// IncludeProjectTier, when true, admits the PROJECT-tier conventional locations
	// (<workspace>/.mecatl/agents, <workspace>/.claude/agents). The composition layer
	// sets it false when the workspace is UNTRUSTED (Workspace-Trust feature, Phase
	// 2a) so a cloned repo's project agent defs cannot steer the model before the
	// operator trusts it; the user-tier and explicit sources stay active regardless.
	// It gates ONLY the project tier. Mirrors skills.ResolveOptions.IncludeProjectTier.
	//
	// ZERO-VALUE NOTE: false by default; callers set it explicitly. Composition uses
	// the folded trust bool. Only Conventional==true makes the project tier eligible.
	IncludeProjectTier bool
}

// ResolveSources builds the ORDERED, highest-precedence-first Source list from the
// conventional locations plus any explicit paths, ready to hand to NewMultiSource.
// The precedence is:
//
//	explicit (--agents-dir, in flag order)                   [highest]
//	  > project: <workspace>/.mecatl/agents, <workspace>/.claude/agents
//	    > user: $XDG_CONFIG_HOME/mecatl/agents (or ~/.config/mecatl/agents),
//	            ~/.claude/agents                                  [lowest]
//
// so a project def overrides a personal one of the same name, and an explicit def
// overrides both. Each location becomes a labelled DirSource; missing directories
// are harmless. When Conventional is false only the Explicit paths are included.
func ResolveSources(opts ResolveOptions) []AgentSource {
	return resolveSourcesEnv(opts, OSEnv)
}

// resolveSourcesEnv is ResolveSources with an injectable environment, for tests.
// Each source carries its admission TIER (AgentDef.Origin on the port) — a
// closed label, never a location.
func resolveSourcesEnv(opts ResolveOptions, env ResolveEnv) []AgentSource {
	var sources []AgentSource

	for _, dir := range opts.Explicit {
		if dir == "" {
			continue
		}
		sources = append(sources, DirSource{Dir: dir, Label: "explicit", Tier: tool.AgentOriginExplicit})
	}

	if !opts.Conventional {
		return sources
	}

	// Project tier — withheld when the workspace is untrusted (IncludeProjectTier=false,
	// Phase 2a). The user-tier sources below are NEVER gated.
	if opts.Workspace != "" && opts.IncludeProjectTier {
		sources = append(sources,
			DirSource{Dir: filepath.Join(opts.Workspace, ProjectDirMecatl), Label: "project(.mecatl)", Tier: tool.AgentOriginProject},
			DirSource{Dir: filepath.Join(opts.Workspace, ProjectDirClaude), Label: "project(.claude)", Tier: tool.AgentOriginProject},
		)
	}

	if cfg := UserConfigDir(env); cfg != "" {
		sources = append(sources, DirSource{Dir: filepath.Join(cfg, userSubdirMecatl), Label: "user(xdg)", Tier: tool.AgentOriginUser})
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		sources = append(sources, DirSource{Dir: filepath.Join(home, userSubdirClaude), Label: "user(.claude)", Tier: tool.AgentOriginUser})
	}

	return sources
}
