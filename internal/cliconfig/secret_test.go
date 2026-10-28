package cliconfig

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/app"
)

func TestStaticSecretGenericMapping(t *testing.T) {
	for _, ref := range []string{"exa", "brave", "websearch", "custom_42"} {
		for _, kind := range []SecretKind{SearchAPIKeySecret, MCPBearerSecret} {
			for _, tc := range []struct {
				name, value, want string
				present           bool
				wantErr           error
			}{
				{name: "file", want: "search-file"},
				{name: "canonical", present: true, value: "canonical-key", want: "canonical-key"},
				{name: "empty canonical", present: true},
				{name: "opaque canonical", present: true, value: "key\rvalue ", want: "key\rvalue "},
			} {
				t.Run(string(kind)+"/"+ref+"/"+tc.name, func(t *testing.T) {
					want, wantErr := tc.want, tc.wantErr
					if kind == MCPBearerSecret {
						if !tc.present {
							want = "mcp-file"
						}
						if tc.name == "empty canonical" || tc.name == "opaque canonical" {
							want, wantErr = "", ErrSecretUnavailable
						}
					}
					env := envWithAuth("/config/mecatl/auth.yaml", "search:\n  "+ref+":\n    api_key: search-file\nmcp:\n  "+ref+":\n    api_key: mcp-file\n")
					name := secretTokenEnv(kind, ref)
					prefix := "MECATL_SEARCH_TOKEN_"
					if kind == MCPBearerSecret {
						prefix = "MECATL_MCP_TOKEN_"
					}
					if name != prefix+strings.ToUpper(ref) {
						t.Fatal("incorrect canonical name")
					}
					env.Environ = func() []string {
						if tc.present {
							return []string{name + "=" + tc.value}
						}
						return nil
					}
					source := newCredentialSource(nil, env)
					key, err := source.Lookup(kind, ref)
					if key != want || err != wantErr {
						t.Fatalf("wrong credential selection or error: %v", err)
					}
				})
			}
		}
	}
}

func TestSearchCanonicalPrecedenceAcrossProjections(t *testing.T) {
	for _, ref := range []string{"exa", "brave", "websearch"} {
		for _, tc := range []struct {
			name, alias, canonical, want string
			present                      bool
		}{
			{name: "file", want: "file-key"},
			{name: "alias", alias: "alias-key", want: "alias-key"},
			{name: "canonical beats alias", alias: "alias-key", canonical: "canonical-key", present: true, want: "canonical-key"},
			{name: "empty canonical blocks fallback", alias: "alias-key", present: true},
		} {
			t.Run(ref+"/"+tc.name, func(t *testing.T) {
				clearProviderEnv(t)
				writeAuthFile(t, "search:\n  "+ref+":\n    api_key: file-key\n")
				t.Setenv(searchAPIKeyEnv(ref), tc.alias)
				if tc.present {
					t.Setenv(secretTokenEnv(SearchAPIKeySecret, ref), tc.canonical)
				}
				check := func(keys ResolvedCredentials, want string) {
					t.Helper()
					got := map[string]string{"exa": keys.Exa, "brave": keys.Brave, "websearch": keys.WebSearch}[ref]
					if got != want || keys.AuthFileWarning != "" {
						t.Fatal("wrong projected search credential")
					}
				}
				envWant := tc.want
				if tc.name == "file" {
					envWant = ""
				}
				check(ReadProviderKeys(), envWant)
				flags := &ProviderFlags{}
				keys := flags.Resolve()
				check(keys, tc.want)
				var cfg app.Config
				flags.ApplyResolved(&cfg, keys)
				got := map[string]string{"exa": cfg.ExaAPIKey, "brave": cfg.BraveAPIKey, "websearch": cfg.WebSearchAPIKey}[ref]
				if got != tc.want {
					t.Fatal("canonical search credential did not reach app config")
				}
				source := NewCredentialSource(flags, keys)
				keys, err := source.ResolveProviders(nil)
				if err != nil {
					t.Fatal(err)
				}
				check(keys, tc.want)
				key, err := source.Lookup(SearchAPIKeySecret, ref)
				if err != nil || key != tc.want {
					t.Fatalf("generic lookup disagrees with projection: %v", err)
				}
			})
		}
	}
}

func TestSearchCanonicalSnapshot(t *testing.T) {
	for _, present := range []bool{false, true} {
		env := envWithAuth("/config/mecatl/auth.yaml", "search:\n  custom_42:\n    api_key: file-key\n")
		entries := []string(nil)
		if present {
			entries = []string{"MECATL_SEARCH_TOKEN_CUSTOM_42=original-key"}
		}
		env.Environ = func() []string { return entries }
		source := newCredentialSource(nil, env)
		entries = []string{"MECATL_SEARCH_TOKEN_CUSTOM_42=changed-key"}
		want := "file-key"
		if present {
			want = "original-key"
		}
		key, err := source.Lookup(SearchAPIKeySecret, "custom_42")
		if err != nil || key != want {
			t.Fatal("canonical search environment did not retain startup snapshot")
		}
	}
}

func TestUnknownSearchFileEntryDoesNotWarnDuringProviderResolution(t *testing.T) {
	env := envWithAuth("/config/mecatl/auth.yaml", "search:\n  custom_42:\n    api_key: unused-key\n")
	keys := (&ProviderFlags{}).resolve(env, time.Time{})
	if keys.AuthFileWarning != "" || keys.Any() {
		t.Fatal("unused search credential changed provider discovery")
	}
	if _, err := keys.source.ResolveProviders(nil); err != nil {
		t.Fatal(err)
	}
	key, err := keys.source.Lookup(SearchAPIKeySecret, "custom_42")
	if err != nil || key != "unused-key" {
		t.Fatal("unknown search credential was not available")
	}
}
