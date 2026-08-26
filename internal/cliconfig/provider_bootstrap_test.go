package cliconfig

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// TestOperatorDefinedLLMProviders_Scenario5_CompositionRoots pins API-key and no-auth
// provider credentials across every server-owning root. Mecatui connect mode never builds an
// app.Config and therefore never constructs one.
func TestOperatorDefinedLLMProviders_Scenario5_CompositionRoots(t *testing.T) {
	testProviderCredentialsRoots(t)
}

// TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots pins the same root set's
// injected profile-loader path.
func TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots(t *testing.T) {
	testProviderCredentialsRoots(t)
}

func testProviderCredentialsRoots(t *testing.T) {
	t.Helper()
	definitions := permconfig.ProviderDefinitions{
		"keyed":     {ID: "keyed", Auth: permconfig.ProviderAuth{Method: "api_key"}},
		"anonymous": {ID: "anonymous", Auth: permconfig.ProviderAuth{Method: "none"}},
	}
	for _, root := range []string{"mecated", "embedded-mecatui", "mecatequi", "mecak8s"} {
		t.Run(root, func(t *testing.T) {
			reads := 0
			env := xdgconfig.ResolveEnv{
				Getenv: func(key string) string {
					if key == "XDG_CONFIG_HOME" {
						return "/config"
					}
					return ""
				},
				ReadFile: func(string) ([]byte, error) {
					reads++
					return []byte("providers:\n  keyed:\n    api_key: keyed-secret\n"), nil
				},
			}
			flags := &ProviderFlags{}
			keys := flags.resolve(env, time.Time{})
			loader := NewProviderCredentialResolver(flags, keys)
			profile, lifecycle, err := loader.Load(definitions)
			if err != nil {
				t.Fatal(err)
			}
			if lifecycle != nil {
				t.Fatal("API-key profile unexpectedly owns a lifecycle")
			}
			if reads != 1 {
				t.Fatalf("auth.yaml reads = %d, want 1", reads)
			}
			if profile.CustomProviderAPIKeys["keyed"] != "keyed-secret" {
				t.Fatal("custom API key was not retained")
			}
			if _, ok := profile.CustomProviderAPIKeys["anonymous"]; ok {
				t.Fatal("no-auth provider received a key")
			}
		})
	}
}
