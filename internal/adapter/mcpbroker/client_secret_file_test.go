package mcpbroker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestClientSecretFileValidationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, make([]byte, maxOAuthClientSecretBytes+1), 0o600); err != nil {
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
	if got := construction.upstreams[0].OAuth2Config.TokenEndpointAuthMethod; got != oauthproto.TokenEndpointAuthMethodClientSecretBasic {
		t.Fatalf("token endpoint auth method = %q, want client_secret_basic", got)
	}
}
