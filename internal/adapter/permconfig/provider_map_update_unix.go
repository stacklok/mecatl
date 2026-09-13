//go:build linux || darwin

package permconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

var providerMapUpdateTestHook func(string) error

// UpdateProviderMap adds, replaces, or removes exactly one custom providers entry.
// It preserves unrelated settings mappings and uses an atomic, checked replacement.
// A nil update.Definition removes update.Provider.
//
//nolint:gocyclo // The linear preserving-write protocol keeps outcome classification explicit.
func UpdateProviderMap(ctx context.Context, path string, update ProviderMapUpdate) (state authfile.CommitState, result error) {
	if !providerIDPattern.MatchString(update.Provider) || isReservedProviderID(update.Provider) {
		return authfile.CommitNotApplied, errors.New("settings provider update: invalid or reserved provider")
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	parent, leaf, err := canonicalSettingsParent(path)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings provider update: open parent")
	}
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil || !settingsPrivateDir(&parentStat) {
		_ = unix.Close(parentFD)
		return authfile.CommitNotApplied, errors.New("settings provider update: parent must be an owner-only directory")
	}
	defer func() {
		if closeErr := unix.Close(parentFD); closeErr != nil && result == nil {
			if state == authfile.CommitDurable {
				state, result = authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings provider update: close parent after replacement")
			} else {
				state, result = authfile.CommitNotApplied, errors.New("settings provider update: close parent")
			}
		}
	}()

	before, err := readSettingsTarget(parentFD, leaf)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	out, noop, err := mutateProviderMap(before.data, update)
	if err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	if noop {
		return authfile.CommitNoop, nil
	}
	if len(out) > maxConfigBytes {
		return authfile.CommitNotApplied, errors.New("settings provider update: output exceeds size limit")
	}
	tempLeaf, tempFD, err := createSettingsTemp(parentFD)
	if err != nil {
		return authfile.CommitNotApplied, errors.New("settings provider update: create temporary file")
	}
	tempOpen := true
	defer func() {
		if tempOpen {
			_ = unix.Close(tempFD)
		}
		_ = unix.Unlinkat(parentFD, tempLeaf, 0)
	}()
	if err := writeSettingsAll(tempFD, out); err != nil || unix.Fsync(tempFD) != nil {
		return authfile.CommitNotApplied, errors.New("settings provider update: write temporary file")
	}
	if providerMapUpdateTestHook != nil {
		if err := providerMapUpdateTestHook("after-temp-sync"); err != nil {
			return authfile.CommitNotApplied, errors.New("settings provider update: prepare replacement")
		}
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	if err := unix.Close(tempFD); err != nil {
		return authfile.CommitNotApplied, errors.New("settings provider update: close temporary file")
	}
	tempOpen = false
	if providerMapUpdateTestHook != nil {
		if err := providerMapUpdateTestHook("before-compare"); err != nil {
			return authfile.CommitNotApplied, errors.New("settings provider update: prepare comparison")
		}
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	current, err := readSettingsTarget(parentFD, leaf)
	if err != nil || !sameSettingsTarget(before, current) {
		//nolint:revive,staticcheck // Exact CLI retry message is a published contract.
		return authfile.CommitNotApplied, errors.New("Configuration changed while this command was running; no changes were made. Review the file and retry.")
	}
	if err := ctx.Err(); err != nil {
		return authfile.CommitNotApplied, fmt.Errorf("settings provider update: %w", err)
	}
	if err := unix.Renameat(parentFD, tempLeaf, parentFD, leaf); err != nil {
		return authfile.CommitNotApplied, errors.New("settings provider update: replace target")
	}
	if providerMapUpdateTestHook != nil {
		if err := providerMapUpdateTestHook("after-rename"); err != nil {
			return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings provider update: sync parent after replacement")
		}
	}
	if err := unix.Fsync(parentFD); err != nil {
		return authfile.CommitReplacementAppliedDurabilityUnknown, errors.New("settings provider update: sync parent after replacement")
	}
	return authfile.CommitDurable, nil
}

//nolint:gocyclo // AST-preserving update covers add, replace, and removal explicitly.
func mutateProviderMap(data []byte, update ProviderMapUpdate) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("providers: {}\n")
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
				providers.Values = append(providers.Values, providerMapEntry(update.Provider, *update.Definition))
			}
		}
	}
	if update.Definition != nil && update.Definition.Auth.Method == providerAuthOIDC && update.OIDCCredentialStore != nil {
		if err := ensureOIDCCredentialStore(doc, *update.OIDCCredentialStore); err != nil {
			return nil, false, err
		}
	}
	out := []byte(doc.String())
	if err := ValidateYAML(out); err != nil {
		return nil, false, errors.New("updated settings document is invalid")
	}
	return out, false, nil
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
