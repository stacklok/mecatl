package cliconfig

import (
	"errors"
	"sort"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
)

// ProviderCredentialResolver projects providers from the shared source owner.
type ProviderCredentialResolver struct {
	source *CredentialSource
	keys   ResolvedCredentials
}

// NewProviderCredentialResolver returns a resolver backed by source and keys.
func NewProviderCredentialResolver(source *CredentialSource, keys ResolvedCredentials) *ProviderCredentialResolver {
	return &ProviderCredentialResolver{source: source, keys: keys}
}

// SetAPIKeyFile selects the auth file before credentials are loaded.
func (r *ProviderCredentialResolver) SetAPIKeyFile(path string) {
	r.source.SetAPIKeyFile(path)
}

// Load resolves credentials for the given provider definitions.
func (r *ProviderCredentialResolver) Load(definitions permconfig.ProviderDefinitions) (app.ProviderCredentials, interface{ Close() error }, error) {
	s := r.source
	s.mu.Lock()
	s.finalized = true
	reselected := s.reselected
	s.mu.Unlock()
	keys := r.keys
	if reselected || keys.source == nil {
		var err error
		keys, err = s.ResolveProviders(definitions)
		if err != nil {
			return app.ProviderCredentials{}, nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquire()
	known := append([]string{}, knownAuthProviders...)
	for id := range definitions {
		known = append(known, id)
	}
	sort.Strings(known)
	if len(definitions) > 0 && s.file == nil && s.warning != "" {
		return app.ProviderCredentials{}, nil, errors.New(s.warning)
	}
	if warning := s.file.ValidateKnown(known); warning != "" {
		s.strictWarning = joinCredentialWarnings(s.strictWarning, warning)
		return app.ProviderCredentials{}, nil, errors.New(warning)
	}
	if keys.searchErr != nil {
		return app.ProviderCredentials{}, nil, keys.searchErr
	}
	credentials := app.ProviderCredentials{
		ExaAPIKey: keys.Exa, BraveAPIKey: keys.Brave, WebSearchAPIKey: keys.WebSearch,
		OpenAIKey: keys.OpenAI, OpenRouterKey: keys.OpenRouter,
		AnthropicKey: keys.Anthropic, OpenCodeKey: keys.OpenCode,
		OpenAICodexCredential: keys.OpenAICodex,
		CustomProviderAPIKeys: make(map[string]string, len(definitions)),
	}
	for id, definition := range definitions {
		if definition.Auth.Method == "api_key" {
			if key := s.file.APIKey(id); key != "" {
				credentials.CustomProviderAPIKeys[id] = key
			}
		}
	}
	return credentials, nil, nil
}
