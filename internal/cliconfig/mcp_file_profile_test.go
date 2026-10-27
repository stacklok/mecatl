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

func TestMCPFileSourceSelection(t *testing.T) {
	for _, tc := range []struct {
		name, ref, env, file, want string
		missing                    bool
	}{
		{name: "file ignores both environments", ref: "Exact", env: "env-token", file: " file-token ", want: " file-token "},
		{name: "environment ignores file", env: "env-token", file: "file-token", want: "env-token"},
		{name: "environment has no file fallback", file: "file-token", missing: true},
		{name: "file has no environment fallback", ref: "Exact", env: "env-token", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fileBearerProfile("server", tc.ref)
			if tc.ref == "" {
				p.Auth.StaticBearer.TokenEnv = "MECATL_SELECTED_TOKEN"
			}
			fileCalls, envCalls := 0, 0
			profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{
				Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p}},
				LookupFileToken: func(ref string) (string, error) {
					fileCalls++
					if ref != "Exact" {
						t.Fatal("wrong file reference")
					}
					return tc.file, nil
				},
				LookupEnv: func(name string) (string, bool) {
					envCalls++
					if name != "MECATL_SELECTED_TOKEN" {
						t.Fatal("legacy environment read by explicit profile")
					}
					return tc.env, tc.env != ""
				},
			})
			if tc.missing {
				if !errors.Is(err, ErrMCPProfileSecret) {
					t.Fatalf("expected secret error, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer profiles.Close()
				if profiles.Servers[0].Headers["Authorization"] != "Bearer "+tc.want {
					t.Fatal("incorrect or normalized bearer")
				}
			}
			if tc.ref != "" && (fileCalls != 1 || envCalls != 0) || tc.ref == "" && (fileCalls != 0 || envCalls != 1) {
				t.Fatal("credential sources crossed")
			}
		})
	}
}

func TestMCPSelectedFileWarningsFailClosed(t *testing.T) {
	for _, state := range []string{"missing", "malformed", "loose", "semantic warning", "empty", "missing entry"} {
		t.Run(state, func(t *testing.T) {
			clearProviderEnv(t)
			path := filepath.Join(t.TempDir(), "auth.yaml")
			body := "mcp:\n  Exact:\n    token: private-token-sentinel\n"
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
			profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "Exact")}}, LookupFileToken: flags.MCPToken, LookupEnv: func(string) (string, bool) { return "fallback-must-not-apply", true }})
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
				if _, _, err := NewProviderCredentialResolver(flags, keys).Load(nil); err != nil {
					t.Fatal("provider-only permissive lifecycle changed")
				}
			}
		})
	}
}

func TestMCPFileHeaderSafetyAndLookupErrors(t *testing.T) {
	for _, token := range []string{"sentinel\rvalue", "sentinel\nvalue", "sentinel\x00value", "sentinel\x01value", "sentinel\x1fvalue", "sentinel\x7fvalue", " \t "} {
		profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "Exact")}}, LookupFileToken: func(string) (string, error) { return token, nil }})
		if profiles != nil || !errors.Is(err, ErrMCPProfileSecret) || strings.Contains(err.Error(), "sentinel") {
			t.Fatal("unsafe header value accepted or disclosed")
		}
	}
	for _, sourceErr := range []error{errors.New("private-source-sentinel"), ErrMCPProfileStore} {
		_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "Exact")}}, LookupFileToken: func(string) (string, error) { return "private-token-sentinel", sourceErr }})
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatal("source error was not redacted")
		}
	}
}

func TestMCPFileInvalidReferenceDoesNotLookup(t *testing.T) {
	for _, ref := range []string{"../private-reference-sentinel", "Exact"} {
		p := fileBearerProfile("server", ref)
		if ref == "Exact" {
			p.Auth.StaticBearer.TokenEnv = "MECATL_TOKEN"
		}
		_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p}}, LookupFileToken: func(string) (string, error) { t.Fatal("invalid reference looked up"); return "", nil }, LookupEnv: func(string) (string, bool) { t.Fatal("invalid reference fell back to environment"); return "", false }})
		if !errors.Is(err, ErrMCPProfileSecret) || strings.Contains(err.Error(), "private-reference-sentinel") {
			t.Fatal("invalid reference was accepted or disclosed")
		}
	}
	_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "Exact")}}})
	if !errors.Is(err, ErrMCPProfileStore) {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestMCPFileLookupAfterSelectionAndOverride(t *testing.T) {
	operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{fileBearerProfile("discarded", "unavailable"), {Name: "selected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	lookup := func(string) (string, error) { t.Fatal("discarded file reference looked up"); return "", nil }
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, SelectedName: "selected", LookupFileToken: lookup})
	if err != nil {
		t.Fatal(err)
	}
	profiles.Close()
	legacy := &MCPServerList{}
	if err := legacy.Set("discarded=https://legacy.example/mcp"); err != nil {
		t.Fatal(err)
	}
	profiles, err = LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, Legacy: legacy, LookupFileToken: lookup, LookupEnv: func(name string) (string, bool) {
		if name != "MCP_DISCARDED_TOKEN" {
			t.Fatal("wrong legacy env")
		}
		return "legacy-token", true
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if profiles.Servers[0].URL != "https://legacy.example/mcp" || profiles.Servers[0].Headers["Authorization"] != "Bearer legacy-token" {
		t.Fatal("legacy whole-entry replacement changed")
	}
	operator.Mode = "broker"
	_, err = ResolveMCPAuthority(MCPAuthorityOptions{Operator: operator, DefaultMode: mcpauthority.Global, BrokerSupported: true, LookupFileToken: lookup})
	if !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("broker static bearer accepted: %v", err)
	}
}

func TestMCPFinalSelectedPathAndSnapshot(t *testing.T) {
	for _, source := range []string{"conventional", "settings", "CLI overrides settings"} {
		t.Run(source, func(t *testing.T) {
			clearProviderEnv(t)
			conventional := writeAuthFile(t, "mcp: [poisoned-conventional-file\n")
			path := filepath.Join(t.TempDir(), "selected.yaml")
			if source == "conventional" {
				path = conventional
			}
			body := "providers:\n  openai:\n    api_key: selected-provider\nmcp:\n  Exact:\n    token: selected-mcp\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := &ProviderFlags{}
			if source == "CLI overrides settings" {
				flags.authFile = &path
			}
			initial := flags.resolve(xdgconfig.OSEnv, time.Now())
			provider := NewProviderCredentialResolver(flags, initial)
			resolver := NewMCPProfileResolver(nil, func(string) (string, bool) { t.Fatal("file profile read environment"); return "", false }, flags.MCPToken)
			selected := path
			if source == "CLI overrides settings" {
				selected = filepath.Join(t.TempDir(), "must-not-read")
			}
			if source != "conventional" {
				provider.SetAPIKeyFile(selected)
			}
			keys, _, err := provider.Load(nil)
			if err != nil {
				t.Fatal(err)
			}
			if keys.OpenAIKey != "selected-provider" {
				t.Fatal("provider used preliminary source")
			}
			if err := os.WriteFile(path, []byte("mcp:\n  Exact:\n    token: rotated-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{fileBearerProfile("server", "Exact")}}
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
