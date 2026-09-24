package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func harnessPolicyConfig(t *testing.T, policy permconfig.HarnessContextSection) Config {
	t.Helper()
	data, err := yaml.Marshal(map[string]any{"harness_context": policy})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, UseMock: true, Headless: true, TrustProject: true, PermissionConfigs: []string{file}}
}
func harnessEmptyKinds() permconfig.HarnessContextKinds {
	empty := permconfig.HarnessContextKind{Mode: "combine"}
	return permconfig.HarnessContextKinds{Instructions: empty, Commands: empty, Rules: empty, Skills: empty, AgentDefs: empty}
}
func harnessResolveSnapshots(t *testing.T, cfg Config) Config {
	t.Helper()
	cfg.permResolver = buildPermResolver(cfg)
	if _, err := validateHarnessPolicy(cfg); err != nil {
		t.Fatal(err)
	}
	if err := resolveProcessHarnessSnapshots(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.harnessContextClose != nil {
		t.Cleanup(cfg.harnessContextClose)
	}
	return cfg
}

type markedHarnessSkills struct {
	tool.SkillSource
	marker string
	absent bool
}

func (s markedHarnessSkills) ListSkills(ctx context.Context) ([]tool.SkillMeta, error) {
	if s.absent {
		return nil, nil
	}
	metas, err := s.SkillSource.ListSkills(ctx)
	for i := range metas {
		metas[i].Description = s.marker
		metas[i].Origin = tool.SkillOriginUser
	}
	return metas, err
}
func (s markedHarnessSkills) SkillBody(ctx context.Context, name string) (string, error) {
	if s.absent {
		return "", tool.ErrSkillNotFound
	}
	body, err := s.SkillSource.SkillBody(ctx, name)
	return s.marker + ":" + body, err
}
func (s markedHarnessSkills) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	if s.absent {
		return nil, tool.ErrSkillNotFound
	}
	return s.SkillSource.ListSkillAssets(ctx, name)
}
func (s markedHarnessSkills) ReadSkillAsset(ctx context.Context, name, asset string) ([]byte, error) {
	if s.absent {
		return nil, tool.ErrSkillAssetNotFound
	}
	return s.SkillSource.ReadSkillAsset(ctx, name, asset)
}

func TestADR_0357_HarnessContext_Scenario5_PerKindOverrideResolution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing string
		want    string
	}{{"earlier non-replaced blocks", "", "b"}, {"no blocker", "b", "c"}, {"absent winner", "c", "a"}} {
		t.Run(tc.name, func(t *testing.T) {
			kinds := harnessEmptyKinds()
			ordered := []string{"a", "b", "c"}
			kinds.Commands = permconfig.HarnessContextKind{Sources: ordered, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{{Name: "review", Winner: "c", Replaces: []string{"a"}}}}
			kinds.Skills = permconfig.HarnessContextKind{Sources: ordered, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{{Name: "review", Winner: "c", Replaces: []string{"a"}}}}
			kinds.Rules = permconfig.HarnessContextKind{Sources: ordered, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{{Name: "rule", Winner: "c", Replaces: []string{"a"}}}}
			kinds.AgentDefs = permconfig.HarnessContextKind{Sources: ordered, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{{Name: "agent", Winner: "c", Replaces: []string{"a"}}}}
			cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: ordered, Kinds: kinds})
			for _, id := range ordered {
				missing := id == tc.missing
				marker := id
				cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: HarnessSourceID(id), Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
					values := map[string]string{}
					if !missing {
						values["review"] = marker
					}
					return &hcCommands{values: values}, nil, nil
				}})
				cfg.HarnessRulesSources = append(cfg.HarnessRulesSources, HarnessSourceRegistration[prompt.RulesSource]{ID: HarnessSourceID(id), Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
					var rules []prompt.Rule
					if !missing {
						rules = []prompt.Rule{{Name: "rule", Body: marker, Origin: prompt.RuleOriginUser}}
					}
					return frozenHarnessRules{rules: rules}, nil, nil
				}})
				cfg.HarnessSkillSources = append(cfg.HarnessSkillSources, HarnessSourceRegistration[tool.SkillSource]{ID: HarnessSourceID(id), Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.SkillSource, func() error, error) {
					return markedHarnessSkills{SkillSource: sourceconformance.NewFixtureSource(), marker: marker, absent: missing}, nil, nil
				}})
				cfg.HarnessAgentDefSources = append(cfg.HarnessAgentDefSources, HarnessSourceRegistration[tool.AgentDefSource]{ID: HarnessSourceID(id), Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
					var defs []tool.AgentDef
					if !missing {
						defs = []tool.AgentDef{{Name: "agent", Description: marker, Body: marker, Origin: tool.AgentOriginUser}}
					}
					return &resolvedAgentSource{defs: defs}, nil, nil
				}})
			}
			cfg = harnessResolveSnapshots(t, cfg)
			rules, err := cfg.harnessRules.ListRules(t.Context())
			if err != nil || len(rules) != 1 || rules[0].Body != tc.want || rules[0].Origin != prompt.RuleOriginDriver {
				t.Fatalf("rules=%v,%v", rules, err)
			}
			defs, err := cfg.harnessAgentDefs.ListAgentDefs(t.Context())
			if err != nil || len(defs) != 1 || defs[0].Body != tc.want || defs[0].Origin != tool.AgentOriginDriver {
				t.Fatalf("defs=%v,%v", defs, err)
			}
			metas, err := cfg.harnessSkills.ListSkills(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, meta := range metas {
				if meta.Name == "review" {
					found = true
					if meta.Description != tc.want || meta.Origin != tool.SkillOriginDriver {
						t.Fatalf("skill metadata=%v", meta)
					}
				}
			}
			if !found {
				t.Fatal("skill absent")
			}
			body, err := cfg.harnessSkills.SkillBody(t.Context(), "review")
			if err != nil || !strings.HasPrefix(body, tc.want+":") {
				t.Fatalf("skill body=%q,%v", body, err)
			}
			resolver, err := buildConfiguredCommandResolver(t.Context(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resolver.(*harnessCommandResolver).Close()
			binding, release, err := resolver.Borrow(t.Context(), "s", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			commands, err := binding.List(t.Context())
			if err != nil || len(commands) != 1 || commands[0].Name != "review" {
				t.Fatalf("commands=%v,%v", commands, err)
			}
			expanded, ok, err := binding.Expand(t.Context(), "/review")
			if err != nil || !ok || expanded != tc.want {
				t.Fatalf("expanded=%q,%v,%v", expanded, ok, err)
			}
		})
	}
	t.Run("invalid declarations", func(t *testing.T) {
		ids := map[HarnessSourceID]struct{}{"a": {}, "b": {}, "c": {}}
		for _, override := range []permconfig.HarnessContextOverride{
			{Name: "x", Winner: "c", Replaces: []string{"c"}},
			{Name: "x", Winner: "c", Replaces: []string{"a", "a"}},
			{Name: "x", Winner: "missing", Replaces: []string{"a"}},
			{Name: "x", Winner: "c", Replaces: []string{"missing"}},
			{Name: "x", Winner: "a", Replaces: []string{"b"}},
			{Name: "x", Winner: "c"},
		} {
			if _, err := compileHarnessKind("commands", permconfig.HarnessContextKind{Sources: []string{"a", "b", "c"}, Mode: "combine", Overrides: []permconfig.HarnessContextOverride{override}}, ids, ids, true); err == nil {
				t.Fatalf("accepted invalid override %+v", override)
			}
		}
	})
}

func TestADR_0357_HarnessContext_Scenario3_ProvenanceAndSourceContracts(t *testing.T) {
	kinds := harnessEmptyKinds()
	policy := permconfig.HarnessContextKind{Sources: []string{"fixture"}, Mode: "combine"}
	kinds.Rules = policy
	kinds.Skills = policy
	kinds.AgentDefs = policy
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"fixture"}, Kinds: kinds})
	cfg.HarnessRulesSources = []HarnessSourceRegistration[prompt.RulesSource]{{ID: "fixture", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
		return sourceconformance.NewRuleFixtureSource(), nil, nil
	}}}
	cfg.HarnessSkillSources = []HarnessSourceRegistration[tool.SkillSource]{{ID: "fixture", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.SkillSource, func() error, error) {
		return sourceconformance.NewFixtureSource(), nil, nil
	}}}
	cfg.HarnessAgentDefSources = []HarnessSourceRegistration[tool.AgentDefSource]{{ID: "fixture", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
		return sourceconformance.NewAgentFixtureSource(), nil, nil
	}}}
	cfg = harnessResolveSnapshots(t, cfg)
	t.Run("rules conformance", func(t *testing.T) {
		sourceconformance.RunRulesSource(t, func(*testing.T) prompt.RulesSource { return cfg.harnessRules })
	})
	t.Run("skills conformance", func(t *testing.T) {
		sourceconformance.RunSkillSource(t, func(*testing.T) tool.SkillSource { return cfg.harnessSkills })
	})
	t.Run("agent conformance", func(t *testing.T) {
		sourceconformance.RunAgentSource(t, func(*testing.T) tool.AgentDefSource { return cfg.harnessAgentDefs })
	})
	t.Run("snapshot and fail-soft rules", func(t *testing.T) {
		before, err := cfg.harnessRules.ListRules(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		after, err := cfg.harnessRules.ListRules(t.Context())
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("snapshot drift")
		}
		a := prompt.RulesAssembler{Src: frozenHarnessRules{err: errors.New("backend failed")}}
		messages, err := a.Assemble(t.Context())
		if err != nil || len(messages) != 0 {
			t.Fatal("existing rule fail-soft semantics changed")
		}
	})
	for _, invalid := range []HarnessProvenancePolicy{{}, {Fixed: "driver", PreserveAllowed: []string{"user"}}, {Fixed: "deployment"}, {PreserveAllowed: []string{"user", "user"}}} {
		if err := validateHarnessProvenance(invalid); err == nil {
			t.Fatalf("accepted invalid provenance %+v", invalid)
		}
	}
}
