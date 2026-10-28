package cliconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func fileBearerProfile(name, ref string) permconfig.MCPServerProfile {
	return permconfig.MCPServerProfile{Name: name, URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenRef: ref}}}
}

type testSecretLookup func(SecretKind, string) (string, error)

func (f testSecretLookup) Lookup(kind SecretKind, ref string) (string, error) { return f(kind, ref) }

func TestMCPStaticSourceSelection(t *testing.T) {
	for _, explicitTokenEnv := range []bool{false, true} {
		p := fileBearerProfile("server", "exact")
		if explicitTokenEnv {
			p.Auth.StaticBearer = &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_SELECTED_TOKEN"}
		}
		secretCalls, envCalls := 0, 0
		profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{
			Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p}},
			Secrets: testSecretLookup(func(kind SecretKind, ref string) (string, error) {
				secretCalls++
				if kind != MCPBearerSecret || ref != "exact" {
					t.Fatal("wrong logical reference")
				}
				return " logical\ttoken", nil
			}),
			LookupEnv: func(name string) (string, bool) {
				envCalls++
				if name != "MECATL_SELECTED_TOKEN" {
					t.Fatal("wrong explicit token environment variable")
				}
				return "explicit-token", true
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer profiles.Close()
		want := "Bearer  logical\ttoken"
		if explicitTokenEnv {
			want = "Bearer explicit-token"
		}
		if profiles.Servers[0].Headers["Authorization"] != want {
			t.Fatal("incorrect or normalized bearer")
		}
		if explicitTokenEnv && (envCalls != 1 || secretCalls != 0) || !explicitTokenEnv && (envCalls != 0 || secretCalls != 1) {
			t.Fatal("credential sources crossed")
		}
	}
}

func TestMCPSelectedFileWarningsFailClosed(t *testing.T) {
	for _, state := range []string{"missing", "malformed", "loose", "semantic warning", "empty", "missing entry"} {
		t.Run(state, func(t *testing.T) {
			clearProviderEnv(t)
			t.Setenv("MECATL_MCP_TOKEN_EXACT", "")
			if err := os.Unsetenv("MECATL_MCP_TOKEN_EXACT"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "auth.yaml")
			body := "mcp:\n  exact:\n    token: private-token-sentinel\n"
			switch state {
			case "malformed":
				body = "mcp: [private-token-sentinel"
			case "semantic warning":
				body += "providers:\n  openai-codex:\n    api_key: ignored-private-sentinel\n"
			case "empty":
				body = ""
			case "missing entry":
				body = "providers: {}\n"
			}
			if state != "missing" {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "loose" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			flags := &ProviderFlags{authFile: &path}
			keys := flags.Resolve()
			source := NewCredentialSource(flags, keys)
			profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "exact")}}, Secrets: source})
			if profiles != nil {
				profiles.Close()
			}
			want := ErrMCPProfileStore
			if state == "empty" || state == "missing entry" {
				want = ErrMCPProfileSecret
			}
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), path) {
				t.Fatal("file error exposed credential material or underlying diagnostics")
			}
			if state == "malformed" || state == "missing" {
				if _, _, err := NewProviderCredentialResolver(source, keys).Load(nil); err != nil {
					t.Fatal("provider-only permissive lifecycle changed")
				}
			}
		})
	}
}

func TestMCPStaticHeaderSafetyAndLookupErrors(t *testing.T) {
	for _, token := range []string{"sentinel\rvalue", "sentinel\nvalue", "sentinel\x00value", "sentinel\x01value", "sentinel\x1fvalue", "sentinel\x7fvalue", " \t ", "sentinel ", "sentinel\t"} {
		for _, explicitTokenEnv := range []bool{false, true} {
			p := fileBearerProfile("server", "exact")
			if explicitTokenEnv {
				p.Auth.StaticBearer = &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_TOKEN"}
			}
			profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p}}, Secrets: testSecretLookup(func(SecretKind, string) (string, error) { return token, nil }), LookupEnv: func(string) (string, bool) { return token, true }})
			if profiles != nil || !errors.Is(err, ErrMCPProfileSecret) || strings.Contains(err.Error(), "sentinel") {
				t.Fatal("unsafe header value accepted or disclosed")
			}
		}
	}
	for _, sourceErr := range []error{errors.New("private-source-sentinel"), ErrSecretSource} {
		_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "exact")}}, Secrets: testSecretLookup(func(SecretKind, string) (string, error) { return "private-token-sentinel", sourceErr })})
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatal("source error was not redacted")
		}
	}
}

func TestMCPInvalidReferenceDoesNotLookup(t *testing.T) {
	for _, ref := range []string{"../private-reference-sentinel", "Exact", "both"} {
		p := fileBearerProfile("server", ref)
		if ref == "both" {
			p.Auth.StaticBearer.TokenEnv = "MECATL_TOKEN"
		}
		_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p}}, Secrets: testSecretLookup(func(SecretKind, string) (string, error) { t.Fatal("invalid reference looked up"); return "", nil }), LookupEnv: func(string) (string, bool) { t.Fatal("invalid reference fell back to environment"); return "", false }})
		if !errors.Is(err, ErrMCPProfileInvalid) || strings.Contains(err.Error(), "private-reference-sentinel") {
			t.Fatal("invalid reference was accepted or disclosed")
		}
	}
	_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "exact")}}})
	if !errors.Is(err, ErrMCPProfileStore) {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestMCPLookupAfterSelectionAndOverride(t *testing.T) {
	operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("discarded", "unavailable"), {Name: "selected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	lookup := testSecretLookup(func(SecretKind, string) (string, error) { t.Fatal("discarded reference looked up"); return "", nil })
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, SelectedName: "selected", Secrets: lookup})
	if err != nil {
		t.Fatal(err)
	}
	profiles.Close()
	cliServers := &MCPServerList{}
	if err := cliServers.Set("discarded=https://cli.example/mcp"); err != nil {
		t.Fatal(err)
	}
	profiles, err = LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, CLIServers: cliServers, Secrets: lookup, LookupEnv: func(name string) (string, bool) {
		if name != "MCP_DISCARDED_TOKEN" {
			t.Fatal("wrong CLI environment variable")
		}
		return "cli-token", true
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if profiles.Servers[0].URL != "https://cli.example/mcp" || profiles.Servers[0].Headers["Authorization"] != "Bearer cli-token" {
		t.Fatal("CLI whole-entry replacement changed")
	}
	operator.Mode = "broker"
	_, err = ResolveMCPAuthority(MCPAuthorityOptions{Operator: operator, DefaultMode: mcpauthority.Global, BrokerSupported: true, Secrets: lookup})
	if !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("broker static bearer accepted: %v", err)
	}
}

func TestMCPFinalSelectedPathAndSnapshot(t *testing.T) {
	for _, sourceName := range []string{"conventional", "settings", "CLI overrides settings"} {
		t.Run(sourceName, func(t *testing.T) {
			clearProviderEnv(t)
			t.Setenv("MECATL_MCP_TOKEN_EXACT", "")
			if err := os.Unsetenv("MECATL_MCP_TOKEN_EXACT"); err != nil {
				t.Fatal(err)
			}
			conventional := writeAuthFile(t, "mcp: [poisoned-conventional-file\n")
			path := filepath.Join(t.TempDir(), "selected.yaml")
			if sourceName == "conventional" {
				path = conventional
			}
			body := "providers:\n  openai:\n    api_key: selected-provider\nmcp:\n  exact:\n    token: selected-mcp\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := &ProviderFlags{}
			if sourceName == "CLI overrides settings" {
				flags.authFile = &path
			}
			initial := flags.resolve(xdgconfig.OSEnv, time.Now())
			source := NewCredentialSource(flags, initial)
			provider := NewProviderCredentialResolver(source, initial)
			resolver := NewMCPProfileResolver(nil, nil, source)
			selected := path
			if sourceName == "CLI overrides settings" {
				selected = filepath.Join(t.TempDir(), "must-not-read")
			}
			if sourceName != "conventional" {
				provider.SetAPIKeyFile(selected)
			}
			keys, _, err := provider.Load(nil)
			if err != nil {
				t.Fatal(err)
			}
			if keys.OpenAIKey != "selected-provider" {
				t.Fatal("provider used preliminary source")
			}
			if err := os.WriteFile(path, []byte("mcp:\n  exact:\n    token: rotated-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "exact")}}
			configs, lifecycle, err := resolver.Load(operator)
			if err != nil {
				t.Fatal(err)
			}
			defer lifecycle.Close()
			if configs[0].Headers["Authorization"] != "Bearer selected-mcp" {
				t.Fatal("direct loader reread file or captured preliminary source")
			}
			configs[0].Headers["Authorization"] = "mutated-projection"
			authority, err := resolver.LoadAuthority(operator, mcpauthority.Global, true)
			if err != nil {
				t.Fatal(err)
			}
			servers, lifecycle, ok := authority.Global()
			if !ok {
				t.Fatal("expected global authority")
			}
			defer lifecycle.Close()
			if servers[0].Headers["Authorization"] != "Bearer selected-mcp" {
				t.Fatal("authority reread file or reused mutable projection")
			}
		})
	}
}
