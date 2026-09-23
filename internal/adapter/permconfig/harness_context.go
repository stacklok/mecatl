package permconfig

import (
	"fmt"

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
	for _, entry := range []struct {
		name string
		kind HarnessContextKind
	}{
		{"instructions", s.Kinds.Instructions},
		{"commands", s.Kinds.Commands},
		{"rules", s.Kinds.Rules},
		{"skills", s.Kinds.Skills},
		{"agent_defs", s.Kinds.AgentDefs},
	} {
		if entry.kind.Mode != "combine" && entry.kind.Mode != "replace" {
			return fmt.Errorf("harness_context.kinds.%s.mode must be combine or replace", entry.name)
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
