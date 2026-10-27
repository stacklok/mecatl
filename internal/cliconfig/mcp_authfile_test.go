package cliconfig

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestSettingsSelectsPreliminaryPathWithoutReread(t *testing.T) {
	for _, missing := range []bool{false, true} {
		reads := 0
		env := envWithAuth("/config/mecatl/auth.yaml", "")
		env.ReadFile = func(string) ([]byte, error) {
			reads++
			if missing {
				return nil, os.ErrNotExist
			}
			return []byte("mcp:\n  github:\n    token: original\n"), nil
		}
		source := newCredentialSource(nil, env, nil)
		source.acquire()
		source.SetAPIKeyFile("/config/mecatl/auth.yaml")
		for range 2 {
			_, providerErr := source.ResolveProviders(nil)
			token, secretErr := source.Lookup(MCPBearerSecret, "github")
			if missing {
				if providerErr == nil || !errors.Is(secretErr, ErrSecretSource) {
					t.Fatal("explicit selection lost cached missing-file failure")
				}
			} else if providerErr != nil || secretErr != nil || token != "original" {
				t.Fatal("same-path selection lost preliminary snapshot")
			}
		}
		if reads != 1 {
			t.Fatal("same-path settings selection reread file")
		}
	}
}

func TestSelectedCredentialSnapshotAcquiredOnce(t *testing.T) {
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
			return []byte("providers:\n  openai:\n    api_key: original-provider\nmcp:\n  exact:\n    token: original-mcp\n"), nil
		},
	}
	source := newCredentialSource(nil, env, nil)
	source.SetAPIKeyFile("/selected/auth.yaml")
	first, err := source.ResolveProviders(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ResolveProviders(nil)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := source.Lookup(MCPBearerSecret, "exact"); err != nil || token != "original-mcp" {
		t.Fatal("MCP did not share selected snapshot")
	}
	if token, err := source.Lookup(MCPBearerSecret, "Exact"); !errors.Is(err, ErrSecretReference) || token != "" {
		t.Fatal("uppercase reference accepted")
	}
	if reads != 1 || first.OpenAI != "original-provider" || second.OpenAI != first.OpenAI {
		t.Fatal("selected credential snapshot was reread or changed")
	}
}

func TestSelectedCredentialSnapshotCachesFailure(t *testing.T) {
	for _, body := range []string{"mcp: [malformed-private-sentinel", "providers:\n  openai-codex:\n    api_key: ignored-private-sentinel\n"} {
		reads := 0
		env := envWithAuth("/selected/auth.yaml", "")
		env.ReadFile = func(string) ([]byte, error) { reads++; return []byte(body), nil }
		source := newCredentialSource(nil, env, nil)
		source.SetAPIKeyFile("/selected/auth.yaml")
		for range 2 {
			if _, err := source.ResolveProviders(nil); err == nil {
				t.Fatal("invalid selected file accepted")
			}
			if token, err := source.Lookup(MCPBearerSecret, "exact"); token != "" || !errors.Is(err, ErrSecretSource) {
				t.Fatal("MCP lost selected-source failure provenance")
			}
		}
		if reads != 1 {
			t.Fatal("failed selected source was reread")
		}
	}
}

func TestStaticSecretEnvironmentSelectionAndSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		environ []string
		want    string
		wantErr error
		reads   int
	}{
		{"absent", nil, "file-token", nil, 1},
		{"present", []string{"MECATL_MCP_TOKEN_GITHUB=env-token"}, "env-token", nil, 1},
		{"opaque whitespace", []string{"MECATL_MCP_TOKEN_GITHUB= \topaque\t token"}, " \topaque\t token", nil, 1},
		{"empty", []string{"MECATL_MCP_TOKEN_GITHUB="}, "", ErrSecretUnavailable, 1},
		{"blank", []string{"MECATL_MCP_TOKEN_GITHUB= \t"}, "", ErrSecretUnavailable, 1},
		{"control", []string{"MECATL_MCP_TOKEN_GITHUB=private\rvalue"}, "", ErrSecretUnavailable, 1},
		{"NUL", []string{"MECATL_MCP_TOKEN_GITHUB=private\x00value"}, "", ErrSecretUnavailable, 1},
		{"DEL", []string{"MECATL_MCP_TOKEN_GITHUB=private\x7fvalue"}, "", ErrSecretUnavailable, 1},
		{"edge space", []string{"MECATL_MCP_TOKEN_GITHUB=private "}, "", ErrSecretUnavailable, 1},
		{"edge tab", []string{"MECATL_MCP_TOKEN_GITHUB=private\t"}, "", ErrSecretUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			env := envWithAuth("/config/mecatl/auth.yaml", "")
			env.ReadFile = func(string) ([]byte, error) { reads++; return []byte("mcp:\n  github:\n    token: file-token\n"), nil }
			source := newCredentialSource(nil, env, tc.environ)
			for range 2 {
				token, err := source.Lookup(MCPBearerSecret, "github")
				if token != tc.want || !errors.Is(err, tc.wantErr) {
					t.Fatalf("wrong selection/error category: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("secret in error")
				}
				// Neither a newly present ambient value nor rotation changes this view.
				t.Setenv("MECATL_MCP_TOKEN_GITHUB", "rotated")
				if len(tc.environ) > 0 {
					tc.environ[0] = "MECATL_MCP_TOKEN_GITHUB=rotated"
				}
			}
			if reads != tc.reads {
				t.Fatalf("reads = %d, want %d", reads, tc.reads)
			}
		})
	}
}

func TestStaticSecretReferenceBoundary(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "")
	env.ReadFile = func(string) ([]byte, error) { t.Fatal("invalid reference acquired file"); return nil, nil }
	source := newCredentialSource(nil, env, nil)
	for _, ref := range []string{"", "GitHub", "../github", "github-x", "é", "MECATL_MCP_TOKEN_GITHUB"} {
		if _, err := source.Lookup(MCPBearerSecret, ref); !errors.Is(err, ErrSecretReference) {
			t.Fatal("invalid ref accepted")
		}
	}
	if _, err := source.Lookup(SecretKind("unknown"), "github"); !errors.Is(err, ErrSecretReference) {
		t.Fatal("unknown kind accepted")
	}
}

func TestAcquisitionWarningSurvivesProviderSanitization(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "providers:\n  openai-codex:\n    api_key: ignored\nmcp:\n  github:\n    token: valid\n")
	flags := &ProviderFlags{}
	keys := flags.resolve(env, time.Time{})
	source := NewCredentialSource(flags, keys)
	loader := NewProviderCredentialResolver(source, keys)
	for range 2 {
		if _, _, err := loader.Load(nil); err != nil {
			t.Fatal("provider permissive projection changed")
		}
		if _, err := source.Lookup(MCPBearerSecret, "github"); !errors.Is(err, ErrSecretSource) {
			t.Fatal("acquisition warning lost after sanitization")
		}
	}
}
