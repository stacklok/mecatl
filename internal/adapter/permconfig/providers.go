package permconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

const providerHTTPS = "https"

var providerIDPattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

var reservedProviderIDs = map[string]struct{}{
	"anthropic": {}, "mock": {}, "openai": {}, "openai-codex": {}, "openrouter": {}, "opencode": {}, "toolhive": {},
}

var builtinOverrideIDs = map[string]struct{}{
	"anthropic": {}, "openai": {}, "openrouter": {}, "opencode": {},
}

// ProviderDefinitions is the strict, operator-owned custom provider map.
type ProviderDefinitions map[string]ProviderDefinition

// ProviderDefinition is one custom provider's non-secret registry definition.
type ProviderDefinition struct {
	ID           string
	BaseURL      string       `yaml:"base_url"`
	DefaultModel string       `yaml:"default_model"`
	APIFlavor    string       `yaml:"api_flavor"`
	Auth         ProviderAuth `yaml:"auth"`
}

// ProviderAuth controls the closed custom-provider authentication vocabulary.
type ProviderAuth struct {
	Method string `yaml:"method"`
}

// ProviderOverrides is the strict operator-owned built-in endpoint map.
type ProviderOverrides map[string]ProviderOverride

// ProviderOverride is one eligible built-in's endpoint override.
type ProviderOverride struct {
	BaseURL string `yaml:"base_url"`
}

// UnmarshalYAML decodes a strict map of custom provider definitions.
func (p *ProviderDefinitions) UnmarshalYAML(node ast.Node) error {
	mapping, ok := permconfigMapping(node)
	if !ok {
		return errors.New("providers: expected mapping")
	}
	out := make(ProviderDefinitions, len(mapping.Values))
	for _, entry := range mapping.Values {
		id, ok := permconfigMappingKey(entry.Key)
		if !ok || !providerIDPattern.MatchString(id) || isReservedProviderID(id) {
			return errors.New("providers: invalid or reserved provider id")
		}
		if _, exists := out[id]; exists {
			return errors.New("providers: duplicate provider id")
		}
		var definition ProviderDefinition
		if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(entry.Value, &definition); err != nil {
			return fmt.Errorf("providers: invalid provider definition: %w", err)
		}
		definition.ID = id
		out[id] = definition
	}
	*p = out
	return nil
}

// UnmarshalYAML decodes and validates one strict custom provider definition.
func (p *ProviderDefinition) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry", map[string]any{
		"base_url": &p.BaseURL, "default_model": &p.DefaultModel, "api_flavor": &p.APIFlavor, "auth": &p.Auth,
	}); err != nil {
		return err
	}
	if err := validProviderURL(p.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(p.DefaultModel) == "" {
		return errors.New("providers entry: default_model is required")
	}
	switch p.APIFlavor {
	case "openai-responses", "openai-chat-completions", "anthropic-messages":
	default:
		return errors.New("providers entry: unsupported api_flavor")
	}
	return nil
}

// UnmarshalYAML decodes the closed custom-provider authentication method.
func (a *ProviderAuth) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry auth", map[string]any{"method": &a.Method}); err != nil {
		return err
	}
	switch a.Method {
	case "", "none":
		a.Method = "none"
	case "api_key":
	default:
		return errors.New("providers entry auth: unsupported method")
	}
	return nil
}

// UnmarshalYAML decodes a strict map of eligible built-in endpoint overrides.
func (p *ProviderOverrides) UnmarshalYAML(node ast.Node) error {
	mapping, ok := permconfigMapping(node)
	if !ok {
		return errors.New("provider_overrides: expected mapping")
	}
	out := make(ProviderOverrides, len(mapping.Values))
	for _, entry := range mapping.Values {
		id, stringID := permconfigMappingKey(entry.Key)
		if !stringID {
			return errors.New("provider_overrides: unsupported built-in provider")
		}
		if _, ok := builtinOverrideIDs[id]; !ok {
			return errors.New("provider_overrides: unsupported built-in provider")
		}
		if _, exists := out[id]; exists {
			return errors.New("provider_overrides: duplicate provider")
		}
		var override ProviderOverride
		if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(entry.Value, &override); err != nil {
			return fmt.Errorf("provider_overrides: invalid override: %w", err)
		}
		out[id] = override
	}
	*p = out
	return nil
}

// UnmarshalYAML decodes and validates one strict endpoint override.
func (p *ProviderOverride) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "provider override", map[string]any{"base_url": &p.BaseURL}); err != nil {
		return err
	}
	return validProviderURL(p.BaseURL)
}

func isReservedProviderID(id string) bool {
	_, ok := reservedProviderIDs[id]
	return ok
}

func validProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != providerHTTPS || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("provider URL must be an HTTPS URL without credentials, query, or fragment")
	}
	return nil
}
