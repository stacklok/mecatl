package cliconfig

import (
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestInvariant_provider_credentials_auth_snapshot(t *testing.T) {
	reads := 0
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			return ""
		},
		ReadFile: func(path string) ([]byte, error) {
			reads++
			if path == "/config/mecatl/auth.yaml" {
				return []byte("providers:\n  gateway:\n    api_key: snapshot-key\n"), nil
			}
			return nil, nil
		},
	}
	flags := &ProviderFlags{}
	keys := flags.resolve(env, time.Time{})
	loader := NewProviderCredentialResolver(flags, keys)
	definitions := permconfig.ProviderDefinitions{"gateway": {
		ID: "gateway", Auth: permconfig.ProviderAuth{Method: "api_key"},
	}}
	profile, _, err := loader.Load(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("auth.yaml reads = %d, want exactly one immutable snapshot", reads)
	}
	if got := profile.CustomProviderAPIKeys["gateway"]; got != "snapshot-key" {
		t.Fatalf("custom API key = %q, want snapshot value", got)
	}
	for _, field := range []string{"OpenAIBaseURL", "OpenRouterBaseURL", "AnthropicBaseURL", "OpenCodeBaseURL"} {
		if _, ok := reflect.TypeOf(profile).FieldByName(field); ok {
			t.Errorf("ProviderCredentials must not contain endpoint field %s", field)
		}
	}
}
