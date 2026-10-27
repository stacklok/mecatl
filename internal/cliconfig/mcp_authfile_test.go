package cliconfig

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestSelectedCredentialSnapshotAcquiredOnce(t *testing.T) {
	flags := &ProviderFlags{}
	flags.SetAPIKeyFile("/selected/auth.yaml")
	reads := 0
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			return ""
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != "/selected/auth.yaml" {
				t.Fatal("read unselected file")
			}
			reads++
			if reads > 1 {
				return []byte("providers: {}\n"), nil
			}
			return []byte("providers:\n  openai:\n    api_key: original-provider\nmcp:\n  Exact:\n    token: original-mcp\n"), nil
		},
	}
	first, err := ResolveProviderCredentials(flags, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveProviderCredentials(flags, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := flags.MCPToken("Exact"); err != nil || token != "original-mcp" {
		t.Fatal("MCP did not share selected snapshot")
	}
	if token, err := flags.MCPToken("exact"); !errors.Is(err, ErrMCPProfileSecret) || token != "" {
		t.Fatal("MCP lookup was not exact")
	}
	if reads != 1 || first.OpenAI != "original-provider" || second.OpenAI != first.OpenAI {
		t.Fatal("selected credential snapshot was reread or changed")
	}
}

func TestSelectedCredentialSnapshotCachesFailure(t *testing.T) {
	for _, body := range []string{"mcp: [malformed-private-sentinel", "providers:\n  openai-codex:\n    api_key: ignored-private-sentinel\n"} {
		flags := &ProviderFlags{}
		flags.SetAPIKeyFile("/selected/auth.yaml")
		reads := 0
		env := envWithAuth("/selected/auth.yaml", "")
		env.ReadFile = func(string) ([]byte, error) {
			reads++
			return []byte(body), nil
		}
		if _, err := ResolveProviderCredentials(flags, nil, env); err == nil {
			t.Fatal("invalid selected file accepted")
		}
		if _, err := ResolveProviderCredentials(flags, nil, env); err == nil {
			t.Fatal("cached file failure ignored")
		}
		if token, err := flags.MCPToken("Exact"); token != "" || !errors.Is(err, ErrMCPProfileStore) {
			t.Fatal("MCP lost selected-source failure provenance")
		}
		if reads != 1 {
			t.Fatal("failed selected source was reread")
		}
	}
}
