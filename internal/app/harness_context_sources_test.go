package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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

type listedMissCommands struct{}

func (listedMissCommands) List(context.Context) ([]prompt.Command, error) {
	return []prompt.Command{{Name: "review"}}, nil
}
func (listedMissCommands) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}

func TestHarnessCommandExpansionContinuesAfterConfiguredMiss(t *testing.T) {
	binding := &resolvedCommandBinding{
		policy: harnessKindPolicy{sources: []HarnessSourceID{"driver", "local"}, mode: harnessModeCombine, exclude: map[string]map[HarnessSourceID]struct{}{}, overrides: map[string]harnessOverride{}},
		sources: []boundCommandSource{
			{id: "driver", binding: listedMissCommands{}},
			{id: "local", binding: &hcCommands{values: map[string]string{"review": "LOCAL-WINNER"}}},
		},
	}
	out, ok, err := binding.Expand(t.Context(), "/review")
	if err != nil || !ok || out != "LOCAL-WINNER" {
		t.Fatalf("configured miss chain = %q,%v,%v", out, ok, err)
	}
}

func TestHarnessReplaceStopsBeforeLowerSource(t *testing.T) {
	kinds := harnessEmptyKinds()
	replace := permconfig.HarnessContextKind{Sources: []string{"high", "low"}, Mode: "replace"}
	kinds.Commands, kinds.Rules, kinds.Skills, kinds.AgentDefs = replace, replace, replace, replace
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"high", "low"}, Kinds: kinds})
	cfg.harnessScope = &HarnessSourceScope{}
	var lower atomic.Int32
	lowerErr := errors.New("lower source must not be consulted")
	for _, id := range []HarnessSourceID{"high", "low"} {
		isLow := id == "low"
		cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{ID: id, Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			if isLow {
				lower.Add(1)
				return nil, nil, lowerErr
			}
			return &hcCommands{values: map[string]string{"chosen": "high"}}, nil, nil
		}})
		cfg.HarnessRulesSources = append(cfg.HarnessRulesSources, HarnessSourceRegistration[prompt.RulesSource]{ID: id, Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
			if isLow {
				lower.Add(1)
				return nil, nil, lowerErr
			}
			return frozenHarnessRules{rules: []prompt.Rule{{Name: "chosen", Body: "high"}}}, nil, nil
		}})
		cfg.HarnessSkillSources = append(cfg.HarnessSkillSources, HarnessSourceRegistration[tool.SkillSource]{ID: id, Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.SkillSource, func() error, error) {
			if isLow {
				lower.Add(1)
				return nil, nil, lowerErr
			}
			return markedHarnessSkills{SkillSource: sourceconformance.NewFixtureSource(), marker: "high"}, nil, nil
		}})
		cfg.HarnessAgentDefSources = append(cfg.HarnessAgentDefSources, HarnessSourceRegistration[tool.AgentDefSource]{ID: id, Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
			if isLow {
				lower.Add(1)
				return nil, nil, lowerErr
			}
			return &resolvedAgentSource{defs: []tool.AgentDef{{Name: "chosen", Body: "high"}}}, nil, nil
		}})
	}
	cfg = harnessResolveSnapshots(t, cfg)
	resolver, err := buildConfiguredCommandResolver(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.(*harnessCommandResolver).Close()
	binding, release, err := resolver.Borrow(t.Context(), "replace", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if out, ok, err := binding.Expand(t.Context(), "/chosen"); err != nil || !ok || out != "high" {
		t.Fatalf("selected command = %q,%v,%v", out, ok, err)
	}
	if lower.Load() != 0 {
		t.Fatalf("replace mode consulted %d lower source backends", lower.Load())
	}

	var lowerProcess atomic.Int32
	processRegs := []HarnessSourceRegistration[server.CommandSourceBinding]{
		{ID: "high", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return &hcCommands{values: map[string]string{"chosen": "process-high"}}, nil, nil
		}},
		{ID: "low", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			lowerProcess.Add(1)
			return nil, nil, lowerErr
		}},
	}
	processResolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"high", "low"}, mode: harnessModeReplace, exclude: map[string]map[HarnessSourceID]struct{}{}}, processRegs)
	if err != nil {
		t.Fatalf("unused lower process source aborted selection: %v", err)
	}
	processResolver.Close()
	if lowerProcess.Load() != 0 {
		t.Fatalf("replace mode bound lower process source %d times", lowerProcess.Load())
	}
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

type mixedOriginSkills struct{ tool.SkillSource }

func (s mixedOriginSkills) ListSkills(ctx context.Context) ([]tool.SkillMeta, error) {
	metas, err := s.SkillSource.ListSkills(ctx)
	for i := range metas {
		origins := []tool.SkillOrigin{tool.SkillOriginUser, tool.SkillOriginProject, tool.SkillOriginExplicit}
		metas[i].Origin = origins[i%len(origins)]
	}
	return metas, err
}

func TestHarnessPreserveAllowedRetainsTrustedMixedOrigins(t *testing.T) {
	kinds := harnessEmptyKinds()
	selected := permconfig.HarnessContextKind{Sources: []string{"mixed"}, Mode: "combine"}
	kinds.Rules, kinds.Skills, kinds.AgentDefs = selected, selected, selected
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"mixed"}, Kinds: kinds})
	cfg.HarnessRulesSources = []HarnessSourceRegistration[prompt.RulesSource]{{ID: "mixed", Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{"user", "project"}}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
		return frozenHarnessRules{rules: []prompt.Rule{{Name: "user", Origin: prompt.RuleOriginUser}, {Name: "project", Origin: prompt.RuleOriginProject}}}, nil, nil
	}}}
	cfg.HarnessSkillSources = []HarnessSourceRegistration[tool.SkillSource]{{ID: "mixed", Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{"user", "project", "explicit"}}, Bind: func(context.Context, HarnessSourceScope) (tool.SkillSource, func() error, error) {
		return mixedOriginSkills{SkillSource: sourceconformance.NewFixtureSource()}, nil, nil
	}}}
	cfg.HarnessAgentDefSources = []HarnessSourceRegistration[tool.AgentDefSource]{{ID: "mixed", Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{"user", "project", "explicit"}}, Bind: func(context.Context, HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
		return &resolvedAgentSource{defs: []tool.AgentDef{{Name: "user", Origin: tool.AgentOriginUser}, {Name: "project", Origin: tool.AgentOriginProject}, {Name: "explicit", Origin: tool.AgentOriginExplicit}}}, nil, nil
	}}}
	cfg = harnessResolveSnapshots(t, cfg)
	rules, err := cfg.harnessRules.ListRules(t.Context())
	if err != nil || len(rules) != 2 || rules[0].Origin == rules[1].Origin {
		t.Fatalf("preserved rules = %+v, %v", rules, err)
	}
	metas, err := cfg.harnessSkills.ListSkills(t.Context())
	if err != nil || len(metas) < 3 || metas[0].Origin == metas[1].Origin || metas[1].Origin == metas[2].Origin {
		t.Fatalf("preserved skill catalog = %+v, %v", metas, err)
	}
	defs, err := cfg.harnessAgentDefs.ListAgentDefs(t.Context())
	if err != nil || len(defs) != 3 || defs[0].Origin == defs[1].Origin || defs[1].Origin == defs[2].Origin {
		t.Fatalf("preserved agent catalog = %+v, %v", defs, err)
	}

	bad := cfg
	bad.HarnessAgentDefSources = []HarnessSourceRegistration[tool.AgentDefSource]{{ID: "mixed", Provenance: HarnessProvenancePolicy{PreserveAllowed: []string{"user"}}, Bind: func(context.Context, HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
		return &resolvedAgentSource{defs: []tool.AgentDef{{Name: "forged", Origin: tool.AgentOriginDriver}}}, nil, nil
	}}}
	bad.harnessAgentDefs = nil
	if err := resolveProcessHarnessSnapshots(t.Context(), &bad); err == nil || !strings.Contains(err.Error(), "disallowed provenance") {
		t.Fatalf("disallowed payload origin was promoted: %v", err)
	}
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
