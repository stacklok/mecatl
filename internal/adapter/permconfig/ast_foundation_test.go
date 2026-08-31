package permconfig

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

type permconfigNodeUnmarshalProbe struct {
	Value   string
	Decoded bool
}

func (p *permconfigNodeUnmarshalProbe) UnmarshalYAML(node ast.Node) error {
	if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(node, &p.Value); err != nil {
		return err
	}
	p.Decoded = true
	return nil
}

type permconfigNestedNodeUnmarshalProbe struct {
	Field   permconfigNodeUnmarshalProbe            `yaml:"field"`
	Slice   []permconfigNodeUnmarshalProbe          `yaml:"slice"`
	Map     map[string]permconfigNodeUnmarshalProbe `yaml:"map"`
	Pointer *permconfigNodeUnmarshalProbe           `yaml:"pointer"`
}

func (p *permconfigNestedNodeUnmarshalProbe) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "nested probe", map[string]any{
		"field":   &p.Field,
		"slice":   &p.Slice,
		"map":     &p.Map,
		"pointer": newPermconfigNodePointer(&p.Pointer),
	})
}

func TestGoccyNodeUnmarshalerDecodesNestedSchemaValues(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseBytes([]byte("field: field\nslice: [slice]\nmap: {key: map}\npointer: pointer\n"), 0)
	if err != nil {
		t.Fatalf("parse goccy AST: %v", err)
	}
	var got permconfigNestedNodeUnmarshalProbe
	if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(file.Docs[0].Body, &got); err != nil {
		t.Fatalf("decode nested node unmarshaler: %v", err)
	}
	if !got.Field.Decoded || got.Field.Value != "field" {
		t.Fatalf("field = %#v, want decoded field", got.Field)
	}
	if len(got.Slice) != 1 || !got.Slice[0].Decoded || got.Slice[0].Value != "slice" {
		t.Fatalf("slice = %#v, want one decoded value", got.Slice)
	}
	if value, ok := got.Map["key"]; !ok || !value.Decoded || value.Value != "map" {
		t.Fatalf("map = %#v, want decoded key", got.Map)
	}
	if got.Pointer == nil || !got.Pointer.Decoded || got.Pointer.Value != "pointer" {
		t.Fatalf("pointer = %#v, want decoded pointer", got.Pointer)
	}

	file, err = parser.ParseBytes([]byte("pointer: null\n"), 0)
	if err != nil {
		t.Fatalf("parse null pointer AST: %v", err)
	}
	var nullPointer permconfigNestedNodeUnmarshalProbe
	if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(file.Docs[0].Body, &nullPointer); err != nil {
		t.Fatalf("decode null pointer: %v", err)
	}
	if nullPointer.Pointer != nil {
		t.Fatalf("null pointer = %#v, want nil", nullPointer.Pointer)
	}
}

func TestPermconfigASTFoundation_InspectsGoccyNodeShapesAndLocations(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseBytes([]byte("section:\n  item: value\n"), 0)
	if err != nil {
		t.Fatalf("parse goccy AST: %v", err)
	}
	root := file.Docs[0].Body
	mapping, ok := permconfigMapping(root)
	if !ok || mapping == nil {
		t.Fatal("mapping root was not recognized")
	}
	section, ok := permconfigMapping(mapping.Values[0].Value)
	if !ok || section == nil {
		t.Fatal("nested mapping was not recognized")
	}
	if _, ok := permconfigMapping(section.Values[0].Value); ok {
		t.Fatal("nested scalar was accepted as a mapping")
	}

	location := permconfigNodeLocation(section.Values[0].Value)
	if !location.HasLocation || location.Line != 2 || location.Column != 9 {
		t.Fatalf("location = %#v, want line 2, column 9", location)
	}
	if got := permconfigNodeLocation(nil); got.HasLocation || got.Line != 0 || got.Column != 0 {
		t.Fatalf("nil node location = %#v, want no invented coordinates", got)
	}
}

func TestGoccyYAMLMigration_Scenario2_PermconfigStrictLenientBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		document     string
		wantErr      string
		wantParseErr string
		check        func(t *testing.T, got Permissions)
	}{
		{
			name:     "null is the strict subtree zero value",
			document: "permissions: null\n",
			check: func(t *testing.T, got Permissions) {
				t.Helper()
				if len(got.Allow) != 0 || len(got.Ask) != 0 || len(got.Deny) != 0 {
					t.Fatalf("permissions = %#v, want zero value", got)
				}
			},
		},
		{
			name:     "unknown nested key remains strict",
			document: "permissions:\n  alow: [Read]\n",
			wantErr:  "permissions: unknown key \"alow\" (line 2)",
		},
		{
			name:         "duplicate nested key remains rejected",
			document:     "permissions:\n  allow: [Read]\n  allow: [Write]\n",
			wantParseErr: "mapping key \"allow\" already defined",
		},
		{
			name:     "known fields decode through the AST",
			document: "permissions:\n  allow: [Read]\n  subagent:\n    deny: [Write]\n",
			check: func(t *testing.T, got Permissions) {
				t.Helper()
				if strings.Join(got.Allow, ",") != "Read" || strings.Join(got.Subagent.Deny, ",") != "Write" {
					t.Fatalf("permissions = %#v, want parsed known fields", got)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := parser.ParseBytes([]byte(tc.document), 0)
			if tc.wantParseErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantParseErr) {
					t.Fatalf("parse error = %v, want %q", err, tc.wantParseErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse goccy AST: %v", err)
			}
			mapping, ok := permconfigMapping(file.Docs[0].Body)
			if !ok || len(mapping.Values) != 1 {
				t.Fatal("expected one top-level mapping entry")
			}
			var got Permissions
			err = got.UnmarshalYAML(mapping.Values[0].Value)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal permissions: %v", err)
			}
			tc.check(t, got)
		})
	}
}

// TestPermconfigNodeDecoderShapeInventory records the shape contract of every
// package-local goccy AST decoder. Semantic validation is intentionally outside
// this inventory: an empty mapping may fail required-field validation, but it
// must not fail as the wrong shape.
func TestPermconfigNodeDecoderShapeInventory(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		new  func() yaml.NodeUnmarshaler
	}{
		{"StorageManagementSection", func() yaml.NodeUnmarshaler { return &StorageManagementSection{} }},
		{"StorageManagementPrincipal", func() yaml.NodeUnmarshaler { return &StorageManagementPrincipal{} }},
		{"RetentionSection", func() yaml.NodeUnmarshaler { return &RetentionSection{} }},
		{"RetentionLimitSection", func() yaml.NodeUnmarshaler { return &RetentionLimitSection{} }},
		{"MCPSection", func() yaml.NodeUnmarshaler { return &MCPSection{} }},
		{"MCPServerProfile", func() yaml.NodeUnmarshaler { return &MCPServerProfile{} }},
		{"MCPAuthProfile", func() yaml.NodeUnmarshaler { return &MCPAuthProfile{} }},
		{"MCPStaticBearerProfile", func() yaml.NodeUnmarshaler { return &MCPStaticBearerProfile{} }},
		{"MCPOAuthProfile", func() yaml.NodeUnmarshaler { return &MCPOAuthProfile{} }},
		{"MCPOAuthClientProfile", func() yaml.NodeUnmarshaler { return &MCPOAuthClientProfile{} }},
		{"MCPPreregisteredClientProfile", func() yaml.NodeUnmarshaler { return &MCPPreregisteredClientProfile{} }},
		{"MCPCIMDClientProfile", func() yaml.NodeUnmarshaler { return &MCPCIMDClientProfile{} }},
		{"MCPOAuthCredentialProfile", func() yaml.NodeUnmarshaler { return &MCPOAuthCredentialProfile{} }},
		{"MCPLocalCredentialProfile", func() yaml.NodeUnmarshaler { return &MCPLocalCredentialProfile{} }},
		{"MCPEnvironmentCredentialProfile", func() yaml.NodeUnmarshaler { return &MCPEnvironmentCredentialProfile{} }},
		{"MCPOAuthNetworkProfile", func() yaml.NodeUnmarshaler { return &MCPOAuthNetworkProfile{} }},
		{"LearningSkillsSection", func() yaml.NodeUnmarshaler { return &LearningSkillsSection{} }},
		{"LearningAutomaticSection", func() yaml.NodeUnmarshaler { return &LearningAutomaticSection{} }},
		{"LearningSection", func() yaml.NodeUnmarshaler { return &LearningSection{} }},
		{"OpenRouterSection", func() yaml.NodeUnmarshaler { return &OpenRouterSection{} }},
		{"OpenRouterModelRoute", func() yaml.NodeUnmarshaler { return &OpenRouterModelRoute{} }},
		{"ContextWindows", func() yaml.NodeUnmarshaler { return &ContextWindows{} }},
		{"RouterSection", func() yaml.NodeUnmarshaler { return &RouterSection{} }},
		{"RouterCategory", func() yaml.NodeUnmarshaler { return &RouterCategory{} }},
		{"ModelsSection", func() yaml.NodeUnmarshaler { return &ModelsSection{} }},
		{"GuardrailsSection", func() yaml.NodeUnmarshaler { return &GuardrailsSection{} }},
		{"GuardrailRuleSpec", func() yaml.NodeUnmarshaler { return &GuardrailRuleSpec{} }},
		{"Permissions", func() yaml.NodeUnmarshaler { return &Permissions{} }},
		{"SubagentPermissions", func() yaml.NodeUnmarshaler { return &SubagentPermissions{} }},
		{"ProviderDefinitions", func() yaml.NodeUnmarshaler { return &ProviderDefinitions{} }},
		{"ProviderDefinition", func() yaml.NodeUnmarshaler { return &ProviderDefinition{} }},
		{"ProviderAuth", func() yaml.NodeUnmarshaler { return &ProviderAuth{} }},
		{"ProviderOverrides", func() yaml.NodeUnmarshaler { return &ProviderOverrides{} }},
		{"ProviderOverride", func() yaml.NodeUnmarshaler { return &ProviderOverride{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.new().UnmarshalYAML(permconfigTestNode(t, "value: {}\n")); err != nil && strings.Contains(strings.ToLower(err.Error()), "mapping") {
				t.Fatalf("mapping shape rejected: %v", err)
			}
			err := tc.new().UnmarshalYAML(permconfigTestNode(t, "value:\n  - attacker-provided: super-secret\n"))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "mapping") {
				t.Fatalf("sequence shape error = %v, want mapping-shape rejection", err)
			}
			if strings.Contains(err.Error(), "attacker-provided") || strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("shape error leaked YAML content: %q", err)
			}
		})
	}
}

func permconfigTestNode(t *testing.T, document string) ast.Node {
	t.Helper()
	file, err := parser.ParseBytes([]byte(document), 0)
	if err != nil {
		t.Fatalf("parse goccy node: %v", err)
	}
	return file.Docs[0].Body.(*ast.MappingNode).Values[0].Value
}
