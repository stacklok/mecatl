package permconfig

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

//nolint:revive,staticcheck // Exact CLI retry message is a published contract.
var errProviderConfigurationChanged = errors.New("Configuration changed while this command was running; no changes were made. Review the file and retry.")

//nolint:gocyclo // AST-preserving update covers add, replace, and removal explicitly.
func mutateProviderMap(data []byte, update ProviderMapUpdate) ([]byte, bool, error) {
	cfg, err := parseYAML(data)
	if err != nil {
		return nil, false, errors.New("settings document is invalid")
	}
	if err := checkProviderMapPreconditions(cfg, update); err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if update.Definition == nil {
			return data, true, nil
		}
		out := []byte(providerMapStaticEntry(update.Provider, *update.Definition).String())
		if update.Definition.Auth.Method == providerAuthOIDC && update.OIDCCredentialStore != nil {
			out = append(out, []byte("\ncredential_store:\n"+oidcCredentialStoreText(*update.OIDCCredentialStore))...)
		}
		if err := ValidateYAML(out); err != nil {
			return nil, false, errors.New("updated settings document is invalid")
		}
		return out, false, nil
	}
	if err := ValidateYAML(data); err != nil {
		return nil, false, errors.New("settings document is invalid")
	}
	doc, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return nil, false, errors.New("settings document is invalid or ambiguous")
	}
	providersNode, err := uniqueDefaultValue(doc.Mapping(), "providers")
	if err != nil {
		return nil, false, err
	}
	if providersNode == nil {
		if update.Definition == nil {
			return data, true, nil
		}
		doc.Mapping().Values = append(doc.Mapping().Values, providerMapStaticEntry(update.Provider, *update.Definition))
	} else {
		providers, ok := providersNode.(*ast.MappingNode)
		if !ok {
			return nil, false, errors.New("providers must be a mapping")
		}
		current, err := uniqueDefaultValue(providers, update.Provider)
		if err != nil {
			return nil, false, err
		}
		if update.Definition == nil {
			if current == nil {
				return data, true, nil
			}
			for i, entry := range providers.Values {
				key, _ := defaultString(entry.Key)
				if key == update.Provider {
					providers.Values = append(providers.Values[:i], providers.Values[i+1:]...)
					break
				}
			}
			if len(providers.Values) == 0 {
				for _, entry := range doc.Mapping().Values {
					key, _ := defaultString(entry.Key)
					if key == "providers" {
						_ = entry.Replace(defaultStaticEntry("providers: {}\n").Value)
						break
					}
				}
			}
		} else if current != nil {
			for _, entry := range providers.Values {
				key, _ := defaultString(entry.Key)
				if key == update.Provider {
					_ = entry.Replace(providerMapEntry(update.Provider, *update.Definition).Value)
					break
				}
			}
		} else {
			if len(providers.Values) == 0 {
				for _, entry := range doc.Mapping().Values {
					key, _ := defaultString(entry.Key)
					if key == "providers" {
						_ = entry.Replace(providerMapStaticEntry(update.Provider, *update.Definition).Value)
						break
					}
				}
			} else {
				entry := providerMapEntry(update.Provider, *update.Definition)
				currentColumn := providers.Values[0].Key.GetToken().Position.Column
				entryColumn := entry.Key.GetToken().Position.Column
				if currentColumn > entryColumn {
					entry.AddColumn(currentColumn - entryColumn)
				}
				providers.Values = append(providers.Values, entry)
			}
		}
	}
	if update.Definition != nil && update.Definition.Auth.Method == providerAuthOIDC && update.OIDCCredentialStore != nil {
		if err := ensureOIDCCredentialStore(doc, *update.OIDCCredentialStore); err != nil {
			return nil, false, err
		}
	}
	if update.RemoveOIDCCredentialStore != nil {
		if err := removeOIDCCredentialStore(doc, *update.RemoveOIDCCredentialStore); err != nil {
			return nil, false, err
		}
	}
	out := []byte(doc.String())
	if err := ValidateYAML(out); err != nil {
		return nil, false, errors.New("updated settings document is invalid")
	}
	return out, false, nil
}

func checkProviderMapPreconditions(cfg Config, update ProviderMapUpdate) error {
	if update.ExpectedAbsent && update.ExpectedDefinition != nil {
		return errors.New("provider update has conflicting preconditions")
	}
	current, exists := cfg.Providers[update.Provider]
	if update.ExpectedAbsent && exists {
		return errProviderConfigurationChanged
	}
	if update.ExpectedDefinition != nil && (!exists || !sameProviderDefinition(current, *update.ExpectedDefinition)) {
		return errProviderConfigurationChanged
	}
	if update.ExpectedOIDCCredentialStoreAbsent && cfg.CredentialStore != nil && cfg.CredentialStore.OIDC != nil {
		return errProviderConfigurationChanged
	}
	if update.RemoveOIDCCredentialStore != nil {
		if cfg.CredentialStore == nil || cfg.CredentialStore.OIDC == nil || !sameOIDCCredentialStore(*cfg.CredentialStore.OIDC, *update.RemoveOIDCCredentialStore) {
			return errProviderConfigurationChanged
		}
	}
	return nil
}

func sameProviderDefinition(a, b ProviderDefinition) bool {
	if a.BaseURL != b.BaseURL || a.DefaultModel != b.DefaultModel || a.APIFlavor != b.APIFlavor || a.Auth.Method != b.Auth.Method {
		return false
	}
	if a.Auth.OIDC == nil || b.Auth.OIDC == nil {
		return a.Auth.OIDC == nil && b.Auth.OIDC == nil
	}
	aScopes, aErr := llmendpoint.NormalizeScopes(a.Auth.OIDC.Scopes)
	bScopes, bErr := llmendpoint.NormalizeScopes(b.Auth.OIDC.Scopes)
	return aErr == nil && bErr == nil &&
		a.Auth.OIDC.Issuer == b.Auth.OIDC.Issuer &&
		a.Auth.OIDC.ClientID == b.Auth.OIDC.ClientID &&
		a.Auth.OIDC.ResourceAudience == b.Auth.OIDC.ResourceAudience &&
		slices.Equal(aScopes, bScopes) &&
		a.Auth.OIDC.IssuerTrust == b.Auth.OIDC.IssuerTrust &&
		a.Auth.OIDC.GatewayTrust == b.Auth.OIDC.GatewayTrust
}

func sameOIDCCredentialStore(a, b OIDCCredentialStore) bool {
	aHome, aErr := filepath.Abs(a.Home)
	bHome, bErr := filepath.Abs(b.Home)
	return aErr == nil && bErr == nil && filepath.Clean(aHome) == filepath.Clean(bHome) && a.Key == b.Key
}

func ensureOIDCCredentialStore(doc *yamldiag.Document, store OIDCCredentialStore) error {
	credentialStore, err := uniqueDefaultValue(doc.Mapping(), "credential_store")
	if err != nil {
		return err
	}
	if credentialStore == nil {
		doc.Mapping().Values = append(doc.Mapping().Values, credentialStoreStaticEntry(store))
		return nil
	}
	section, ok := credentialStore.(*ast.MappingNode)
	if !ok {
		return errors.New("credential_store must be a mapping")
	}
	existing, err := uniqueDefaultValue(section, providerAuthOIDC)
	if err != nil {
		return err
	}
	if existing == nil {
		section.Values = append(section.Values, oidcCredentialStoreEntry(store))
	}
	return nil
}

func removeOIDCCredentialStore(doc *yamldiag.Document, expected OIDCCredentialStore) error {
	cfg, err := parseYAML([]byte(doc.String()))
	if err != nil {
		return errors.New("updated settings document is invalid")
	}
	if cfg.CredentialStore == nil || cfg.CredentialStore.OIDC == nil || !sameOIDCCredentialStore(*cfg.CredentialStore.OIDC, expected) {
		return errProviderConfigurationChanged
	}
	for _, definition := range cfg.Providers {
		if definition.Auth.Method == providerAuthOIDC {
			return nil
		}
	}
	credentialStore, err := uniqueDefaultValue(doc.Mapping(), "credential_store")
	if err != nil || credentialStore == nil {
		return err
	}
	section, ok := credentialStore.(*ast.MappingNode)
	if !ok {
		return errors.New("credential_store must be a mapping")
	}
	existing, err := uniqueDefaultValue(section, providerAuthOIDC)
	if err != nil || existing == nil {
		return err
	}
	for i, entry := range section.Values {
		key, _ := defaultString(entry.Key)
		if key == providerAuthOIDC {
			section.Values = append(section.Values[:i], section.Values[i+1:]...)
			break
		}
	}
	if len(section.Values) == 0 {
		for _, entry := range doc.Mapping().Values {
			key, _ := defaultString(entry.Key)
			if key == "credential_store" {
				_ = entry.Replace(defaultStaticEntry("credential_store: {}\n").Value)
				break
			}
		}
	}
	return nil
}

func credentialStoreStaticEntry(store OIDCCredentialStore) *ast.MappingValueNode {
	return defaultStaticEntry("credential_store:\n" + oidcCredentialStoreText(store))
}

func oidcCredentialStoreEntry(store OIDCCredentialStore) *ast.MappingValueNode {
	section, ok := credentialStoreStaticEntry(store).Value.(*ast.MappingNode)
	if !ok || len(section.Values) != 1 {
		panic("invalid static credential store mapping")
	}
	return section.Values[0]
}

func oidcCredentialStoreText(store OIDCCredentialStore) string {
	var text strings.Builder
	text.WriteString("  oidc:\n    home: ")
	text.WriteString(defaultQuote(store.Home))
	text.WriteString("\n    key:\n      source: ")
	text.WriteString(defaultQuote(store.Key.Source))
	if store.Key.KeyEnv != "" {
		text.WriteString("\n      key_env: ")
		text.WriteString(defaultQuote(store.Key.KeyEnv))
	}
	text.WriteByte('\n')
	return text.String()
}

func providerMapEntry(name string, definition ProviderDefinition) *ast.MappingValueNode {
	providers, ok := providerMapStaticEntry(name, definition).Value.(*ast.MappingNode)
	if !ok || len(providers.Values) != 1 {
		panic("invalid static provider settings mapping")
	}
	return providers.Values[0]
}

func providerMapStaticEntry(name string, definition ProviderDefinition) *ast.MappingValueNode {
	var text strings.Builder
	text.WriteString("providers:\n  ")
	text.WriteString(defaultQuote(name))
	text.WriteString(":\n    base_url: ")
	text.WriteString(defaultQuote(definition.BaseURL))
	text.WriteString("\n    default_model: ")
	text.WriteString(defaultQuote(definition.DefaultModel))
	text.WriteString("\n    api_flavor: ")
	text.WriteString(defaultQuote(definition.APIFlavor))
	text.WriteString("\n    auth:\n      method: ")
	text.WriteString(defaultQuote(definition.Auth.Method))
	if definition.Auth.OIDC != nil {
		oidc := definition.Auth.OIDC
		text.WriteString("\n      oidc:\n        issuer: ")
		text.WriteString(defaultQuote(oidc.Issuer))
		text.WriteString("\n        client_id: ")
		text.WriteString(defaultQuote(oidc.ClientID))
		if oidc.ResourceAudience != "" {
			text.WriteString("\n        resource_audience: ")
			text.WriteString(defaultQuote(oidc.ResourceAudience))
		}
		text.WriteString("\n        scopes: [")
		for i, scope := range oidc.Scopes {
			if i > 0 {
				text.WriteString(", ")
			}
			text.WriteString(defaultQuote(scope))
		}
		text.WriteString("]\n        issuer_trust:\n          policy: ")
		text.WriteString(defaultQuote(oidc.IssuerTrust.Policy))
		if oidc.IssuerTrust.CABundle != "" {
			text.WriteString("\n          ca_bundle: ")
			text.WriteString(defaultQuote(oidc.IssuerTrust.CABundle))
		}
		text.WriteString("\n        gateway_trust:\n          policy: ")
		text.WriteString(defaultQuote(oidc.GatewayTrust.Policy))
		if oidc.GatewayTrust.CABundle != "" {
			text.WriteString("\n          ca_bundle: ")
			text.WriteString(defaultQuote(oidc.GatewayTrust.CABundle))
		}
	}
	text.WriteByte('\n')
	return defaultStaticEntry(text.String())
}
