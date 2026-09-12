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
	// Native is the composition-only projection used by the existing native OIDC runtime.
	Native *NativeEndpointIdentity `yaml:"-"`
}

// CredentialStoreSection holds the shared OIDC credential-store schema.
type CredentialStoreSection struct {
	OIDC *OIDCCredentialStore `yaml:"oidc"`
}

// OIDCCredentialStore selects the shared custody for OIDC provider credentials.
type OIDCCredentialStore struct {
	Home string              `yaml:"home"`
	Key  NativeCredentialKey `yaml:"key"`
}

// NativeCredentialKey selects encryption-key custody for the shared credential home.
type NativeCredentialKey struct {
	Source string `yaml:"source"`
	KeyEnv string `yaml:"key_env,omitempty"`
}

func (k NativeCredentialKey) Validate() error {
	switch k.Source {
	case "", "keyring":
		if k.KeyEnv != "" {
			return errors.New("credential_store.oidc.key: keyring source forbids key_env")
		}
	case "environment":
		return validateMCPSecretRef("credential_store.oidc.key.key_env", k.KeyEnv)
	default:
		return errors.New("credential_store.oidc.key: unsupported source")
	}
	return nil
}

func (k *NativeCredentialKey) UnmarshalYAML(node ast.Node) error {
	*k = NativeCredentialKey{}
	if err := decodeStrictMapping(node, "credential_store.oidc.key", map[string]any{"source": &k.Source, "key_env": &k.KeyEnv}); err != nil {
		return err
	}
	if k.Source == "" {
		return errors.New("credential_store.oidc.key: source is required")
	}
	if k.Source == "keyring" {
		if err := decodeStrictMapping(node, "credential_store.oidc.key", map[string]any{"source": &k.Source}); err != nil {
			return err
		}
	}
	return k.Validate()
}

func (s *CredentialStoreSection) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "credential_store", map[string]any{"oidc": newPermconfigNodePointer(&s.OIDC)}); err != nil {
		return err
	}
	if s.OIDC == nil {
		return errors.New("credential_store.oidc is required")
	}
	return nil
}

func (s *OIDCCredentialStore) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "credential_store.oidc", map[string]any{"home": &s.Home, "key": &s.Key}); err != nil {
		return err
	}
	if strings.TrimSpace(s.Home) == "" {
		return errors.New("credential_store.oidc.home is required")
	}
	home, err := filepath.Abs(s.Home)
	if err != nil {
		return errors.New("credential_store.oidc.home is invalid")
	}
	s.Home = filepath.Clean(home)
	return nil
}

// LLMSection is retained only for command-source compatibility. Settings parsing
// rejects the legacy llm mapping in favor of providers and credential_store.
type LLMSection struct {
	CredentialHome string                    `yaml:"credential_home"`
	CredentialKey  NativeCredentialKey       `yaml:"credential_key"`
	Endpoints      NativeEndpointDefinitions `yaml:"endpoints"`
}

type NativeEndpointDefinitions map[string]NativeEndpointDefinition

type NativeEndpointDefinition struct {
	ID           string      `yaml:"-"`
	Protocol     string      `yaml:"protocol"`
	URL          string      `yaml:"url"`
	DefaultModel string      `yaml:"default_model"`
	OIDC         NativeOIDC  `yaml:"oidc"`
	IssuerTrust  NativeTrust `yaml:"issuer_trust"`
	GatewayTrust NativeTrust `yaml:"gateway_trust"`
}

// NativeEndpointIdentity carries the unified OIDC provider identity on the
// existing composition path.
type NativeEndpointIdentity struct {
	CredentialHome string
	CredentialKey  NativeCredentialKey
	OIDC           NativeOIDC
	IssuerTrust    NativeTrust
	GatewayTrust   NativeTrust
}

type NativeOIDC struct {
	Issuer           string   `yaml:"issuer"`
	ClientID         string   `yaml:"client_id"`
	ResourceAudience string   `yaml:"resource_audience,omitempty"`
	Scopes           []string `yaml:"scopes"`
}

type NativeTrust struct {
	Policy   string `yaml:"policy"`
	CABundle string `yaml:"ca_bundle,omitempty"`
}

// ProviderAuth controls the closed custom-provider authentication vocabulary.
type ProviderAuth struct {
	Method string        `yaml:"method"`
	OIDC   *ProviderOIDC `yaml:"oidc,omitempty"`
}

// ProviderOIDC is the exact OIDC authentication schema for one provider.
type ProviderOIDC struct {
	Issuer           string      `yaml:"issuer"`
	ClientID         string      `yaml:"client_id"`
	ResourceAudience string      `yaml:"resource_audience,omitempty"`
	Scopes           []string    `yaml:"scopes"`
	IssuerTrust      NativeTrust `yaml:"issuer_trust"`
	GatewayTrust     NativeTrust `yaml:"gateway_trust"`
}

// ProviderOverrides is the strict operator-owned built-in endpoint map.
type ProviderOverrides map[string]ProviderOverride

type ProviderOverride struct {
	BaseURL string `yaml:"base_url"`
}

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

func (p *ProviderDefinition) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry", map[string]any{"base_url": &p.BaseURL, "default_model": &p.DefaultModel, "api_flavor": &p.APIFlavor, "auth": &p.Auth}); err != nil {
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

func (a *ProviderAuth) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry auth", map[string]any{"method": &a.Method, "oidc": newPermconfigNodePointer(&a.OIDC)}); err != nil {
		return err
	}
	switch a.Method {
	case "oidc":
		if a.OIDC == nil {
			return errors.New("providers entry auth.oidc is required")
		}
	case "api_key", "none":
		if err := decodeStrictMapping(node, "providers entry auth", map[string]any{"method": &a.Method}); err != nil {
			return err
		}
	default:
		return errors.New("providers entry auth: method must be api_key, oidc, or none")
	}
	return nil
}

func (o *ProviderOIDC) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry auth.oidc", map[string]any{"issuer": &o.Issuer, "client_id": &o.ClientID, "resource_audience": &o.ResourceAudience, "scopes": &o.Scopes, "issuer_trust": &o.IssuerTrust, "gateway_trust": &o.GatewayTrust}); err != nil {
		return err
	}
	if !llmendpoint.ValidIssuer(o.Issuer) || strings.TrimSpace(o.ClientID) == "" || (o.ResourceAudience != "" && strings.TrimSpace(o.ResourceAudience) == "") {
		return errors.New("providers entry auth.oidc: required identity field is invalid")
	}
	scopes, err := llmendpoint.NormalizeScopes(o.Scopes)
	if err != nil {
		return errors.New("providers entry auth.oidc: invalid scopes")
	}
	o.Scopes = scopes
	return nil
}

func (t *NativeTrust) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "providers entry auth.oidc trust", map[string]any{"policy": &t.Policy, "ca_bundle": &t.CABundle}); err != nil {
		return err
	}
	if err := llmendpoint.ValidateTrust(llmendpoint.Trust{Policy: t.Policy, CABundle: t.CABundle}); err != nil {
		return errors.New("providers entry auth.oidc trust: invalid policy or CA bundle")
	}
	return nil
}

func finalizeProviderDefinitions(definitions ProviderDefinitions, store *CredentialStoreSection) error {
	for id, definition := range definitions {
		if definition.Auth.Method != "oidc" {
			continue
		}
		if definition.APIFlavor != "openai-responses" {
			return errors.New("providers entry: oidc requires api_flavor openai-responses")
		}
		if store == nil || store.OIDC == nil {
			return errors.New("credential_store.oidc is required for OIDC providers")
		}
		oidc := definition.Auth.OIDC
		definition.Native = &NativeEndpointIdentity{CredentialHome: store.OIDC.Home, CredentialKey: store.OIDC.Key, OIDC: NativeOIDC{Issuer: oidc.Issuer, ClientID: oidc.ClientID, ResourceAudience: oidc.ResourceAudience, Scopes: append([]string(nil), oidc.Scopes...)}, IssuerTrust: oidc.IssuerTrust, GatewayTrust: oidc.GatewayTrust}
		definitions[id] = definition
	}
	return nil
}

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

func (p *ProviderOverride) UnmarshalYAML(node ast.Node) error {
	if err := decodeStrictMapping(node, "provider override", map[string]any{"base_url": &p.BaseURL}); err != nil {
		return err
	}
	return validProviderURL(p.BaseURL)
}

func isReservedProviderID(id string) bool { _, ok := reservedProviderIDs[id]; return ok }
func validProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != providerHTTPS || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("provider URL must be an HTTPS URL without credentials, query, or fragment")
	}
	return nil
}
