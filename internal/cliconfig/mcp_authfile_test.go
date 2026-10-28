package cliconfig

import (
	"errors"
	"fmt"
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
			return []byte("mcp:\n  github:\n    api_key: original\n"), nil
		}
		source := newCredentialSource(nil, env)
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
			return []byte("providers:\n  openai:\n    api_key: original-provider\nmcp:\n  exact:\n    api_key: original-mcp\n"), nil
		},
	}
	source := newCredentialSource(nil, env)
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
		source := newCredentialSource(nil, env)
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
			env.ReadFile = func(string) ([]byte, error) {
				reads++
				return []byte("mcp:\n  github:\n    api_key: file-token\n"), nil
			}
			env.Environ = func() []string { return tc.environ }
			source := newCredentialSource(nil, env)
			for range 2 {
				token, err := source.Lookup(MCPBearerSecret, "github")
				if token != tc.want || !errors.Is(err, tc.wantErr) {
					t.Fatalf("wrong selection/error category: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("secret in error")
				}
			}
			if reads != tc.reads {
				t.Fatalf("reads = %d, want %d", reads, tc.reads)
			}
		})
	}
}

func TestCredentialSourceUsesInjectedEnviron(t *testing.T) {
	t.Setenv("MECATL_MCP_TOKEN_GITHUB", "ambient-token")
	for _, tc := range []struct {
		name    string
		environ func() []string
		want    string
	}{
		{"nil", nil, "file-token"},
		{"empty", func() []string { return nil }, "file-token"},
		{"injected", func() []string { return []string{"MECATL_MCP_TOKEN_GITHUB=injected-token"} }, "injected-token"},
	} {
		for _, providers := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/providers=%v", tc.name, providers), func(t *testing.T) {
				env := envWithAuth("/config/mecatl/auth.yaml", "mcp:\n  github:\n    api_key: file-token\n")
				env.Environ = tc.environ
				var keys ResolvedCredentials
				if providers {
					var err error
					keys, err = ResolveProviderCredentials(nil, nil, env)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					keys = (&ProviderFlags{}).resolve(env, time.Time{})
				}
				token, err := keys.source.Lookup(MCPBearerSecret, "github")
				if err != nil || token != tc.want {
					t.Fatalf("injected environment selection failed: %v", err)
				}
			})
		}
	}
}

func TestStaticSecretReferenceBoundary(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "")
	env.ReadFile = func(string) ([]byte, error) { t.Fatal("invalid reference acquired file"); return nil, nil }
	source := newCredentialSource(nil, env)
	for _, ref := range []string{"", "GitHub", "../github", "github-x", "é", "MECATL_MCP_TOKEN_GITHUB"} {
		if _, err := source.Lookup(MCPBearerSecret, ref); !errors.Is(err, ErrSecretReference) {
			t.Fatal("invalid ref accepted")
		}
	}
	if _, err := source.Lookup(SecretKind("unknown"), "github"); !errors.Is(err, ErrSecretReference) {
		t.Fatal("unknown kind accepted")
	}
}

func TestSearchSecretLookupPolicy(t *testing.T) {
	const kind = SearchAPIKeySecret
	for _, ref := range []string{"exa", "brave", "websearch"} {
		for _, tc := range []struct {
			name, body, envValue, want string
			wantErr                    error
		}{
			{"file", "search:\n  " + ref + ":\n    api_key: file-key\n", "", "file-key", nil},
			{"environment", "search:\n  " + ref + ":\n    api_key: file-key\n", "env-key", "env-key", nil},
			{"opaque environment", "search: {}\n", "key\rvalue ", "key\rvalue ", nil},
			{"optional", "search: {}\n", "", "", nil},
			{"sanitized provider warning", "providers:\n  openai-codex:\n    api_key: ignored\nsearch:\n  " + ref + ":\n    api_key: file-key\n", "", "file-key", nil},
			{"namespace isolation", "providers:\n  " + ref + ":\n    api_key: llm-key\nmcp:\n  " + ref + ":\n    api_key: mcp-key\n", "", "", nil},
			{"invalid source", "search: [private-sentinel", "", "", ErrSecretSource},
			{"environment overrides invalid source", "search: [private-sentinel", "env-key", "env-key", nil},
		} {
			t.Run(ref+"/"+tc.name, func(t *testing.T) {
				reads := 0
				env := envWithAuth("/config/mecatl/auth.yaml", tc.body)
				getenv, readFile := env.Getenv, env.ReadFile
				env.Getenv = func(name string) string {
					if name == strings.ToUpper(ref)+"_API_KEY" {
						return tc.envValue
					}
					return getenv(name)
				}
				// Search must not adopt MCP's captured environment policy.
				env.Environ = func() []string { return []string{strings.ToUpper(ref) + "_API_KEY=captured-poison"} }
				env.ReadFile = func(path string) ([]byte, error) { reads++; return readFile(path) }
				source := newCredentialSource(nil, env)
				for range 2 {
					key, err := source.Lookup(kind, ref)
					if key != tc.want || err != tc.wantErr {
						t.Fatalf("wrong search selection/error category: %v", err)
					}
				}
				if reads != 1 || !source.finalized {
					t.Fatal("public search lookup did not finalize one file acquisition")
				}
			})
		}
	}
	for _, explicit := range []bool{false, true} {
		env := envWithAuth("/config/mecatl/auth.yaml", "")
		env.ReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
		source := newCredentialSource(nil, env)
		if explicit {
			source.SetAPIKeyFile("/selected/auth.yaml")
		}
		key, err := source.Lookup(kind, "exa")
		if key != "" || (!explicit && err != nil) || (explicit && err != ErrSecretSource) {
			t.Fatal("search absence lost optional/source-failure distinction")
		}
	}
}

func TestSearchSecretReferenceBoundary(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "")
	env.ReadFile = func(string) ([]byte, error) { t.Fatal("invalid reference acquired file"); return nil, nil }
	source := newCredentialSource(nil, env)
	for _, ref := range []string{"", "Exa", "../exa", "exa-x", "é", "EXA_API_KEY", "private-sentinel"} {
		key, err := source.Lookup(SearchAPIKeySecret, ref)
		if key != "" || err != ErrSecretReference {
			t.Fatal("invalid search reference did not return fixed diagnostic")
		}
	}
	if key, err := source.Lookup(SecretKind("private-sentinel"), "exa"); key != "" || err != ErrSecretReference {
		t.Fatal("invalid kind did not return fixed diagnostic")
	}
	if source.finalized {
		t.Fatal("invalid lookup finalized source")
	}
}

func TestSearchAndMCPConcurrentLookupSharesSelectedSnapshot(t *testing.T) {
	const selected = "/selected/auth.yaml"
	reads := make(chan string, 4)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	env := envWithAuth(selected, "")
	env.ReadFile = func(path string) ([]byte, error) {
		reads <- path
		<-release
		return []byte("search:\n  exa:\n    api_key: search-key\nmcp:\n  exa:\n    api_key: mcp-key\n"), nil
	}
	source := newCredentialSource(nil, env)
	source.SetAPIKeyFile(selected)
	done := make(chan struct{}, 5)
	for range 2 {
		for _, kind := range []SecretKind{SearchAPIKeySecret, MCPBearerSecret} {
			go func() {
				key, err := source.Lookup(kind, "exa")
				want := "search-key"
				if kind == MCPBearerSecret {
					want = "mcp-key"
				}
				if err != nil || key != want {
					t.Error("concurrent lookup lost namespace or selected snapshot")
				}
				done <- struct{}{}
			}()
		}
	}
	select {
	case path := <-reads:
		if path != selected {
			t.Error("lookup acquired an unselected file")
		}
	case <-deadline.C:
		t.Fatal("selected-file acquisition did not start")
	}
	go func() {
		source.SetAPIKeyFile("/must-not-reselect/auth.yaml")
		done <- struct{}{}
	}()
	close(release)
	for range 5 {
		select {
		case <-done:
		case <-deadline.C:
			t.Fatal("concurrent credential operations did not complete")
		}
	}
	if len(reads) != 0 {
		t.Error("concurrent lookup reacquired the selected file")
	}
	if path, _ := source.AuthFilePath(); path != selected {
		t.Error("lookup allowed reselection after finalizing")
	}
}

func TestAcquisitionWarningSurvivesProviderSanitization(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "providers:\n  openai-codex:\n    api_key: ignored\nmcp:\n  github:\n    api_key: valid\n")
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
