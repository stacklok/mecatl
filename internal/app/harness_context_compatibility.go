package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Existing flags register logical compatibility sources, not an execution backend.
func registerHarnessCompatibility(cfg *Config) {
	resolver, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || resolver.OperatorHarnessContext() == nil {
		return
	}
	local := *cfg
	cfg.HarnessInstructionSources = append(cfg.HarnessInstructionSources, HarnessSourceRegistration[prompt.InstructionAssembler]{ID: harnessLocalSource, Provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		if local.Workspace == "" {
			return prompt.NewMultiAssembler(), nil, nil
		}
		source, err := osfs.NewWorkspace(local.Workspace)
		if err != nil {
			return nil, nil, err
		}
		return prompt.RootAssembler{Source: source}, nil, nil
	}})
	commandTier := harnessProjectTier
	if cfg.CommandsDir != "" {
		commandTier = harnessExplicitTier
	}
	cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: harnessLocalSource, Provenance: HarnessProvenancePolicy{Fixed: commandTier}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		binding, err := bindHarnessLocalCommands(local)
		return binding, nil, err
	}})
	cfg.HarnessRulesSources = append(cfg.HarnessRulesSources, HarnessSourceRegistration[prompt.RulesSource]{ID: harnessLocalSource, Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{harnessUserTier, harnessProjectTier}}, Bind: func(ctx context.Context, _ HarnessSourceScope) (prompt.RulesSource, func() error, error) {
		source := resolveRulesSeam(ctx, local)
		if source == nil {
			source = frozenHarnessRules{}
		}
		return source, nil, nil
	}})
	cfg.HarnessSkillSources = append(cfg.HarnessSkillSources, HarnessSourceRegistration[tool.SkillSource]{ID: harnessLocalSource, Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{harnessExplicitTier, harnessProjectTier, harnessUserTier}}, Bind: func(ctx context.Context, _ HarnessSourceScope) (tool.SkillSource, func() error, error) {
		seam := resolveFSSkillSeam(ctx, local)
		if seam.source == nil {
			return &resolvedSkillSource{}, nil, nil
		}
		return seam.source, nil, nil
	}})
	cfg.HarnessAgentDefSources = append(cfg.HarnessAgentDefSources, HarnessSourceRegistration[tool.AgentDefSource]{ID: harnessLocalSource, Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{harnessExplicitTier, harnessProjectTier, harnessUserTier}}, Bind: func(ctx context.Context, _ HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
		return &resolvedAgentSource{defs: resolveAgentRegistry(ctx, local).List()}, nil, nil
	}})
	// The reserved skill command view is resolved inside each binding against its
	// already-composed skills, including principal-scoped skills.
	cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: "skills", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return prompt.NoopExpander{}, nil, nil
	}})
	if cfg.CommandSourceURL != "" {
		cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: harnessDriverTier, Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(ctx context.Context, _ HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			conn, closeConn, err := local.drivers().dial(local, local.CommandSourceURL)
			if err != nil {
				return nil, nil, err
			}
			source := grpcdriver.NewCommandSource(conn, grpcdriver.CommandOptions{Diagnostics: local.diag()})
			cleanup := func() error { closeConn(); return nil }
			if err := source.Probe(ctx); err != nil {
				return nil, cleanup, err
			}
			return prompt.NewSourceExpander(source), cleanup, nil
		}})
	}
	if cfg.SkillSourceURL != "" {
		cfg.HarnessSkillSources = append(cfg.HarnessSkillSources, HarnessSourceRegistration[tool.SkillSource]{ID: harnessDriverTier, Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(ctx context.Context, _ HarnessSourceScope) (tool.SkillSource, func() error, error) {
			seam, err := resolveDriverSkillSeam(ctx, local, nil)
			cleanup := func() error {
				if seam.close != nil {
					seam.close()
				}
				return nil
			}
			return seam.source, cleanup, err
		}})
	}
	if cfg.AgentSourceURL != "" {
		cfg.HarnessAgentDefSources = append(cfg.HarnessAgentDefSources, HarnessSourceRegistration[tool.AgentDefSource]{ID: harnessDriverTier, Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(ctx context.Context, _ HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
			registry, closeSource, err := resolveAgentSeam(ctx, local)
			cleanup := func() error {
				if closeSource != nil {
					closeSource()
				}
				return nil
			}
			if err != nil {
				return nil, cleanup, err
			}
			return &resolvedAgentSource{defs: registry.List()}, cleanup, nil
		}})
	}
	if cfg.MCPPrompts {
		holder := &harnessMCPCommands{}
		cfg.harnessMCPCommands = holder
		cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: "mcp", Provenance: HarnessProvenancePolicy{Fixed: harnessDriverTier}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return holder, nil, nil
		}})
	}
}

func bindHarnessLocalCommands(cfg Config) (server.CommandSourceBinding, error) {
	if cfg.CommandsDir != "" {
		root := cfg.CommandsDir
		if !filepath.IsAbs(root) {
			if cfg.Workspace == "" {
				return nil, fmt.Errorf("relative command source requires a configured project source root")
			}
			root = filepath.Join(cfg.Workspace, root)
		}
		source, err := osfs.NewWorkspace(root)
		if err != nil {
			return nil, err
		}
		return prompt.NewDirCommandExpander(source, "."), nil
	}
	if !cfg.EnableCommands || cfg.Workspace == "" || !projectIngestionAdmitted(cfg) {
		return prompt.NoopExpander{}, nil
	}
	source, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	return prompt.NewDirCommandExpander(source), nil
}

type harnessMCPCommands struct{ provider mcp.Provider }

func (s *harnessMCPCommands) List(ctx context.Context) ([]prompt.Command, error) {
	if s.provider == nil {
		return nil, nil
	}
	entries, err := s.provider.ListPrompts(ctx, "")
	if err != nil {
		return nil, nil
	}
	out := make([]prompt.Command, 0, len(entries))
	for _, entry := range entries {
		name := "mcp__" + entry.Server + "__" + entry.Name
		if !prompt.ValidCommandName(name) {
			continue
		}
		out = append(out, prompt.Command{Name: name, Description: entry.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *harnessMCPCommands) Expand(ctx context.Context, input string) (string, bool, error) {
	return mcp.NewPromptExpander(s.provider).Expand(ctx, input)
}
