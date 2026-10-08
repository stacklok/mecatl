package mcpbroker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpsecretfile"
)

func TestClientSecretFileValidationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, make([]byte, mcpsecretfile.MaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "missing"), "empty": empty, "oversized": oversized} {
		t.Run(name, func(t *testing.T) {
			if err := validateClientSecretFile(path); err == nil {
				t.Fatal("accepted invalid client secret file")
			}
		})
	}
}

func TestToolHiveOAuthUsesClientSecretFile(t *testing.T) {
	profile := protectedToolHiveProfile("private")
	construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example")
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	if got, want := construction.upstreams[0].OAuth2Config.ClientSecretFile, profile.OAuth.ClientSecretFile; got != want {
		t.Fatalf("client secret file = %q, want %q", got, want)
	}
	if got := construction.upstreams[0].OAuth2Config.ClientSecretEnvVar; got != "" {
		t.Fatalf("client secret environment = %q, want empty", got)
	}
	if got := construction.upstreams[0].OAuth2Config.TokenEndpointAuthMethod; got != "" {
		t.Fatalf("token endpoint auth method = %q, want empty", got)
	}
}

func TestToolHiveOAuthUsesClientSecretEnv(t *testing.T) {
	profile := protectedToolHiveProfile("private")
	profile.OAuth.ClientSecretFile, profile.OAuth.ClientSecretEnv = "", "MECATL_TEST_CLIENT_SECRET"
	construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example")
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	if cfg := construction.upstreams[0].OAuth2Config; cfg.ClientSecretEnvVar != "MECATL_TEST_CLIENT_SECRET" || cfg.ClientSecretFile != "" {
		t.Fatalf("oauth2 secret sources = env %q file %q", cfg.ClientSecretEnvVar, cfg.ClientSecretFile)
	}
	profile.OAuth.AuthorizationEndpoint, profile.OAuth.TokenEndpoint, profile.OAuth.Issuer = "", "", "https://issuer.example"
	upstream, err := toolHiveUpstream(profile, "private", "https://broker.example")
	if err != nil {
		t.Fatalf("toolHiveUpstream: %v", err)
	}
	if cfg := upstream.OIDCConfig; cfg.ClientSecretEnvVar != "MECATL_TEST_CLIENT_SECRET" || cfg.ClientSecretFile != "" {
		t.Fatalf("oidc secret sources = env %q file %q", cfg.ClientSecretEnvVar, cfg.ClientSecretFile)
	}
}

func TestCompileOAuthRouteResolvesSecretEnv(t *testing.T) {
	t.Setenv("MECATL_TEST_ROUTE_CLIENT_SECRET", "fixture")
	config := protectedConfig("https://accounts.example/token")
	config.Routes[0].Auth.OAuth.Client.Preregistered.SecretFile = ""
	config.Routes[0].Auth.OAuth.Client.Preregistered.SecretEnv = "MECATL_TEST_ROUTE_CLIENT_SECRET"
	route, err := compileOAuthRoute(config.CallbackURL, config.Routes[0])
	if err != nil {
		t.Fatalf("compileOAuthRoute: %v", err)
	}
	got, err := route.resolveClientSecret(t.Context(), defaultOAuthRuntimeOptions())
	if err != nil || got != "fixture" {
		t.Fatalf("resolveClientSecret = (%q, %v)", got, err)
	}
}
