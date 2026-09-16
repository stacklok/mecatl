package cliconfig

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestOperatorDefinedLLMProviders_Scenario2_SeparateAuthFileKeys(t *testing.T) {
	defs := permconfig.ProviderDefinitions{
		"one": {ID: "one", Auth: permconfig.ProviderAuth{Method: "api_key"}},
		"two": {ID: "two", Auth: permconfig.ProviderAuth{Method: "api_key"}},
	}
	path := "/config/mecatl/auth.yaml"
	env := envWithAuth(path, "providers:\n  one:\n    api_key: key-one\n  two:\n    api_key: key-two\n")
	resolved, err := ResolveProviderCredentials(nil, defs, env)
	if err != nil {
		t.Fatalf("ResolveProviderCredentials: %v", err)
	}
	if resolved.CustomAPIKey("one") != "key-one" || resolved.CustomAPIKey("two") != "key-two" {
		t.Fatalf("custom credentials were not distinct: %+v", resolved)
	}
	if strings.Contains(resolved.AuthFileWarning, "key-") {
		t.Fatalf("credential leaked to warning: %q", resolved.AuthFileWarning)
	}
}

func TestOperatorDefinedLLMProviders_Scenario2_AvailabilityFollowsAuthMethod(t *testing.T) {
	defs := permconfig.ProviderDefinitions{
		"keyed":     {ID: "keyed", Auth: permconfig.ProviderAuth{Method: "api_key"}},
		"anonymous": {ID: "anonymous", Auth: permconfig.ProviderAuth{Method: "none"}},
	}
	resolved, err := ResolveProviderCredentials(nil, defs, envWithAuth("/config/mecatl/auth.yaml", "providers: {}\n"))
	if err != nil {
		t.Fatalf("ResolveProviderCredentials: %v", err)
	}
	if resolved.CustomAvailable("keyed") {
		t.Fatal("api_key provider was available without an auth-file record")
	}
	if !resolved.CustomAvailable("anonymous") {
		t.Fatal("none provider was unavailable without an auth-file record")
	}
}

func TestInvariant_custom_provider_auth_bootstrap_single_source(t *testing.T) {
	defs := permconfig.ProviderDefinitions{"custom": {ID: "custom", Auth: permconfig.ProviderAuth{Method: "api_key"}}}
	reads := 0
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			if key == "CUSTOM_API_KEY" {
				return "must-not-be-read"
			}
			return ""
		},
		ReadFile: func(string) ([]byte, error) {
			reads++
			return []byte("providers:\n  custom:\n    api_key: from-file\n"), nil
		},
	}
	resolved, err := ResolveProviderCredentials(nil, defs, env)
	if err != nil {
		t.Fatalf("ResolveProviderCredentials: %v", err)
	}
	if reads != 1 || resolved.CustomAPIKey("custom") != "from-file" {
		t.Fatalf("bootstrap must read auth.yaml once and use it exclusively: reads=%d resolved=%+v", reads, resolved)
	}
}

func TestInvariant_custom_provider_authfile_strict(t *testing.T) {
	defs := permconfig.ProviderDefinitions{"known": {ID: "known", Auth: permconfig.ProviderAuth{Method: "api_key"}}}
	_, err := ResolveProviderCredentials(nil, defs, envWithAuth("/config/mecatl/auth.yaml", "providers:\n  old-provider:\n    api_key: secret\n"))
	if err == nil {
		t.Fatal("unknown custom auth provider was accepted")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "old-provider") {
		t.Fatalf("strict auth error leaked file data: %v", err)
	}
}

func TestProviderCredentialResolverFailsClosedForMalformedConventionalAuthWithCustomProviders(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "providers: [malformed")
	flags := &ProviderFlags{}
	keys := flags.resolve(env, time.Now())
	if keys.AuthFileWarning == "" {
		t.Fatal("malformed conventional auth file did not produce the stock-provider warning")
	}
	resolver := NewProviderCredentialResolver(flags, keys)
	if _, _, err := resolver.Load(nil); err != nil {
		t.Fatalf("stock-provider-only startup lost warning-only behavior: %v", err)
	}
	definitions := permconfig.ProviderDefinitions{"custom": {ID: "custom", Auth: permconfig.ProviderAuth{Method: "api_key"}}}
	if _, _, err := resolver.Load(definitions); err == nil {
		t.Fatal("custom provider accepted malformed conventional auth file")
	}
}

func TestResolveProviderCredentialsReportsUnknownProviderWarningSafely(t *testing.T) {
	const path = "/config/mecatl/auth.yaml"
	const staleID = "old-provider"
	defs := permconfig.ProviderDefinitions{
		"renamed-provider": {ID: "renamed-provider", Auth: permconfig.ProviderAuth{Method: "api_key"}},
	}
	_, err := ResolveProviderCredentials(nil, defs, envWithAuth(path, "providers:\n  "+staleID+":\n    api_key: secret\n"))
	if err == nil {
		t.Fatal("unknown custom auth provider was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, path) {
		t.Fatalf("error = %q, want auth-file path", message)
	}
	if !strings.Contains(message, "renamed-provider") {
		t.Fatalf("error = %q, want configured provider ID", message)
	}
	if strings.Contains(message, staleID) || strings.Contains(message, "secret") {
		t.Fatalf("error = %q, leaked stale provider ID or credential", message)
	}
}

func envWithAuth(path, contents string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			return ""
		},
		ReadFile: func(got string) ([]byte, error) {
			if got == path {
				return []byte(contents), nil
			}
			return nil, nil
		},
	}
}
