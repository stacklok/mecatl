package cliconfig

import (
	"errors"
	"sort"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
)

// ProviderCredentialResolver adapts the command root's one immutable credential
// snapshot to app.Build's composition-owned provider-credential seam.
type ProviderCredentialResolver struct {
	flags *ProviderFlags
	keys  ResolvedCredentials
}

// NewProviderCredentialResolver constructs a loader over an already-resolved command
// snapshot. Load performs no filesystem or environment I/O.
func NewProviderCredentialResolver(flags *ProviderFlags, keys ResolvedCredentials) *ProviderCredentialResolver {
	return &ProviderCredentialResolver{flags: flags, keys: keys}
}

// SetAPIKeyFile applies the composition-resolved credential_store.api_key.file.
func (r *ProviderCredentialResolver) SetAPIKeyFile(path string) {
	if r == nil || r.flags == nil {
		return
	}
	r.flags.SetAPIKeyFile(path)
}

// Load validates custom auth IDs only after Build has resolved the operator
// definitions, then returns a detached immutable-by-convention credential snapshot.
func (r *ProviderCredentialResolver) Load(definitions permconfig.ProviderDefinitions) (app.ProviderCredentials, interface{ Close() error }, error) {
	keys := r.keys
	if r.flags != nil && r.flags.apiKeyFile != "" {
		var err error
		keys, err = ResolveProviderCredentials(r.flags, definitions, xdgconfig.OSEnv)
		if err != nil {
			return app.ProviderCredentials{}, nil, err
		}
	}
	known := append([]string{}, knownAuthProviders...)
	for id := range definitions {
		known = append(known, id)
	}
	sort.Strings(known)
	if len(definitions) > 0 && r.flags != nil && r.flags.authSnapshotReady && r.flags.authSnapshot == nil && r.flags.authSnapshotWarn != "" {
		return app.ProviderCredentials{}, nil, errors.New(r.flags.authSnapshotWarn)
	}
	if r.flags != nil && r.flags.authSnapshotReady && r.flags.authSnapshot != nil {
		if warning := r.flags.authSnapshot.ValidateKnown(known); warning != "" {
			return app.ProviderCredentials{}, nil, errors.New(warning)
		}
	}

	credentials := app.ProviderCredentials{
		OpenAIKey: keys.OpenAI, OpenRouterKey: keys.OpenRouter,
		AnthropicKey: keys.Anthropic, OpenCodeKey: keys.OpenCode,
		OpenAICodexCredential: keys.OpenAICodex,
		CustomProviderAPIKeys: make(map[string]string, len(definitions)),
	}
	for id, definition := range definitions {
		if definition.Auth.Method == "api_key" {
			key := keys.CustomAPIKey(id)
			if key == "" && r.flags != nil && r.flags.authSnapshot != nil {
				key = r.flags.authSnapshot.APIKey(id)
			}
			if key != "" {
				credentials.CustomProviderAPIKeys[id] = key
			}
		}
	}
	return credentials, nil, nil
}
