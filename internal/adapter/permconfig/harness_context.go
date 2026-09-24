package permconfig

import (
	"errors"

	"github.com/goccy/go-yaml/ast"
)

const harnessContextNameKey = "name"

func (s *HarnessContextSection) strictFields() map[string]any {
	return map[string]any{"enabled_sources": &s.EnabledSources, "kinds": &s.Kinds}
}

// UnmarshalYAML decodes a strict harness_context mapping.
func (s *HarnessContextSection) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "harness_context", s.strictFields()); err != nil {
		return err
	}
	for _, kind := range []HarnessContextKind{s.Kinds.Instructions, s.Kinds.Commands, s.Kinds.Rules, s.Kinds.Skills, s.Kinds.AgentDefs} {
		if kind.Mode != "combine" && kind.Mode != "replace" {
			return errors.New("all five harness kinds require a valid mode")
		}
	}
	return nil
}
func (k *HarnessContextKinds) strictFields() map[string]any {
	return map[string]any{"instructions": &k.Instructions, "commands": &k.Commands, "rules": &k.Rules, "skills": &k.Skills, "agent_defs": &k.AgentDefs}
}

// UnmarshalYAML decodes the five strict kind mappings.
func (k *HarnessContextKinds) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "harness_context.kinds", k.strictFields())
}
func (k *HarnessContextKind) strictFields() map[string]any {
	return map[string]any{"sources": &k.Sources, "mode": &k.Mode, "exclude": &k.Exclude, "overrides": &k.Overrides}
}

// UnmarshalYAML decodes one strict content-kind policy.
func (k *HarnessContextKind) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "harness_context.kinds.*", k.strictFields())
}
func (e *HarnessContextExclude) strictFields() map[string]any {
	return map[string]any{"source": &e.Source, harnessContextNameKey: &e.Name}
}

// UnmarshalYAML decodes one strict exclusion declaration.
func (e *HarnessContextExclude) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "harness_context.kinds.*.exclude[]", e.strictFields())
}
func (o *HarnessContextOverride) strictFields() map[string]any {
	return map[string]any{harnessContextNameKey: &o.Name, "winner": &o.Winner, "replaces": &o.Replaces}
}

// UnmarshalYAML decodes one strict named override declaration.
func (o *HarnessContextOverride) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "harness_context.kinds.*.overrides[]", o.strictFields())
}
