package permconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
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
	// Native is non-nil only for a definition normalized from llm.endpoints.
	// Legacy providers.auth.method remains unchanged.
	Native *NativeEndpointIdentity `yaml:"-"`
}

// LLMSection is the strict operator-owned native endpoint facade.
type LLMSection struct {
	CredentialHome string                    `yaml:"credential_home"`
	CredentialKey  NativeCredentialKey       `yaml:"credential_key"`
	Endpoints      NativeEndpointDefinitions `yaml:"endpoints"`
}

// NativeCredentialKey selects encryption-key custody for the shared credential home.
// The zero value preserves the existing keyring default.
type NativeCredentialKey struct {
	Source string `yaml:"source"`
	KeyEnv string `yaml:"key_env,omitempty"`
}

// Validate rejects unknown sources and incompatible environment references.
func (k NativeCredentialKey) Validate() error {
	switch k.Source {
	case "", "keyring":
		if k.KeyEnv != "" {
			return errors.New("llm.credential_key: keyring source forbids key_env")
		}
	case "environment":
		return validateMCPSecretRef("llm.credential_key.key_env", k.KeyEnv)
	default:
		return errors.New("llm.credential_key: unsupported source")
	}
	return nil
}

// UnmarshalYAML validates the closed, explicit key-source configuration.
func (k *NativeCredentialKey) UnmarshalYAML(node ast.Node) error {
	*k = NativeCredentialKey{}
	if err := decodeStrictMapping(node, "llm.credential_key", map[string]any{
		"source": &k.Source, "key_env": &k.KeyEnv,
	}); err != nil {
		return err
	}
	if k.Source == "" {
		return errors.New("llm.credential_key: source is required")
	}
	if k.Source == "keyring" {
		// Reject even an empty or null key_env, not just nonempty values.
		if err := decodeStrictMapping(node, "llm.credential_key", map[string]any{"source": &k.Source}); err != nil {
			return err
		}
	}
	return k.Validate()
}

// NativeEndpointDefinitions is the strict endpoint-ID keyed facade.
type NativeEndpointDefinitions map[string]NativeEndpointDefinition

// NativeEndpointDefinition is one validated native endpoint before it is
// normalized into ProviderDefinition.
type NativeEndpointDefinition struct {
	ID           string      `yaml:"-"`
	Protocol     string      `yaml:"protocol"`
	URL          string      `yaml:"url"`
	DefaultModel string      `yaml:"default_model"`
	OIDC         NativeOIDC  `yaml:"oidc"`
	IssuerTrust  NativeTrust `yaml:"issuer_trust"`
	GatewayTrust NativeTrust `yaml:"gateway_trust"`
}

// NativeEndpointIdentity carries native credential identity inputs on the
// existing provider-definition path.
type NativeEndpointIdentity struct {
	CredentialHome string
	CredentialKey  NativeCredentialKey
	OIDC           NativeOIDC
	IssuerTrust    NativeTrust
	GatewayTrust   NativeTrust
}

// NativeOIDC is the strict authorization-server configuration.
type NativeOIDC struct {
	Issuer           string   `yaml:"issuer"`
	ClientID         string   `yaml:"client_id"`
	ResourceAudience string   `yaml:"resource_audience,omitempty"`
	Scopes           []string `yaml:"scopes"`
}

// NativeTrust is one independently validated TLS trust identity.
type NativeTrust struct {
	Policy   string `yaml:"policy"`
	CABundle string `yaml:"ca_bundle,omitempty"`
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

// UnmarshalYAML decodes and canonicalizes the native endpoint facade.
func (l *LLMSection) UnmarshalYAML(node ast.Node) error {
	l.CredentialKey = NativeCredentialKey{Source: "keyring"}
	if err := decodeStrictMapping(node, "llm", map[string]any{
		"credential_home": &l.CredentialHome,
		"credential_key":  &l.CredentialKey,
		"endpoints":       &l.Endpoints,
	}); err != nil {
		return err
	}
	if len(l.Endpoints) == 0 {
		return nil
	}
	if strings.TrimSpace(l.CredentialHome) == "" {
		return errors.New("llm: credential_home is required when endpoints are configured")
	}
	home, err := filepath.Abs(l.CredentialHome)
	if err != nil {
		return errors.New("llm: invalid credential_home")
	}
	l.CredentialHome = filepath.Clean(home)
	return nil
}

// UnmarshalYAML decodes a strict map of native endpoint definitions.
func (n *NativeEndpointDefinitions) UnmarshalYAML(node ast.Node) error {
	mapping, ok := permconfigMapping(node)
	if !ok {
		return errors.New("llm.endpoints: expected mapping")
	}
	out := make(NativeEndpointDefinitions, len(mapping.Values))
	for _, entry := range mapping.Values {
		id, ok := permconfigMappingKey(entry.Key)
		if !ok || !providerIDPattern.MatchString(id) || isReservedProviderID(id) {
			return errors.New("llm.endpoints: invalid or reserved endpoint id")
		}
		if _, exists := out[id]; exists {
			return errors.New("llm.endpoints: duplicate endpoint id")
		}
		var definition NativeEndpointDefinition
		if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(entry.Value, &definition); err != nil {
			return fmt.Errorf("llm.endpoints: invalid endpoint definition: %w", err)
		}
		definition.ID = id
		out[id] = definition
	}
	*n = out
	return nil
}

// UnmarshalYAML validates one strict native endpoint.
func (n *NativeEndpointDefinition) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "llm endpoint", map[string]any{
		"protocol": &n.Protocol, "url": &n.URL, "default_model": &n.DefaultModel,
		"oidc": &n.OIDC, "issuer_trust": &n.IssuerTrust, "gateway_trust": &n.GatewayTrust,
	}); err != nil {
		return err
	}
	if n.Protocol != "openai-responses" {
		return errors.New("llm endpoint: unsupported protocol")
	}
	canonical, err := llmendpoint.CanonicalGatewayURL(n.URL)
	if err != nil {
		return errors.New("llm endpoint: invalid URL")
	}
	n.URL = canonical
	if strings.TrimSpace(n.DefaultModel) == "" {
		return errors.New("llm endpoint: default_model is required")
	}
	if !llmendpoint.ValidIssuer(n.OIDC.Issuer) || strings.TrimSpace(n.OIDC.ClientID) == "" || (n.OIDC.ResourceAudience != "" && strings.TrimSpace(n.OIDC.ResourceAudience) == "") || len(n.OIDC.Scopes) == 0 {
		return errors.New("llm endpoint: oidc is required")
	}
	if err := llmendpoint.ValidateTrust(llmendpoint.Trust{Policy: n.IssuerTrust.Policy, CABundle: n.IssuerTrust.CABundle}); err != nil {
		return errors.New("llm endpoint: issuer_trust is required")
	}
	if err := llmendpoint.ValidateTrust(llmendpoint.Trust{Policy: n.GatewayTrust.Policy, CABundle: n.GatewayTrust.CABundle}); err != nil {
		return errors.New("llm endpoint: gateway_trust is required")
	}
	return nil
}

// UnmarshalYAML validates the exact native OIDC identity fields.
func (o *NativeOIDC) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "llm endpoint oidc", map[string]any{
		"issuer": &o.Issuer, "client_id": &o.ClientID,
		"resource_audience": &o.ResourceAudience, "scopes": &o.Scopes,
	}); err != nil {
		return err
	}
	if !llmendpoint.ValidIssuer(o.Issuer) || strings.TrimSpace(o.ClientID) == "" || (o.ResourceAudience != "" && strings.TrimSpace(o.ResourceAudience) == "") {
		return errors.New("llm endpoint oidc: required identity field is invalid")
	}
	scopes, err := llmendpoint.NormalizeScopes(o.Scopes)
	if err != nil {
		return errors.New("llm endpoint oidc: invalid scopes")
	}
	o.Scopes = scopes
	return nil
}

// UnmarshalYAML validates one closed TLS trust policy.
func (t *NativeTrust) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "llm endpoint trust", map[string]any{
		"policy": &t.Policy, "ca_bundle": &t.CABundle,
	}); err != nil {
		return err
	}
	if err := llmendpoint.ValidateTrust(llmendpoint.Trust{Policy: t.Policy, CABundle: t.CABundle}); err != nil {
		return errors.New("llm endpoint trust: invalid policy or CA bundle")
	}
	return nil
}

// ProviderDefinitions converts the facade into the existing provider-definition
// input. Callers perform this once after operator-tier precedence is resolved.
func (l *LLMSection) ProviderDefinitions() ProviderDefinitions {
	if l == nil {
		return nil
	}
	out := make(ProviderDefinitions, len(l.Endpoints))
	for id, endpoint := range l.Endpoints {
		out[id] = ProviderDefinition{
			ID: id, BaseURL: endpoint.URL, DefaultModel: endpoint.DefaultModel,
			APIFlavor: endpoint.Protocol,
			Native: &NativeEndpointIdentity{
				CredentialHome: l.CredentialHome, CredentialKey: l.CredentialKey, OIDC: endpoint.OIDC,
				IssuerTrust: endpoint.IssuerTrust, GatewayTrust: endpoint.GatewayTrust,
			},
		}
	}
	return out
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
