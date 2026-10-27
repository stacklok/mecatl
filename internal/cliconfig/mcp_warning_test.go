package cliconfig

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func legacyBearerProfile(name string) permconfig.MCPServerProfile {
	p := fileBearerProfile(name, "")
	p.Auth.StaticBearer.TokenEnv = "MECATL_EXACT_LEGACY"
	return p
}

func TestMCPLegacyWarningSharedLifetimeAndCapturedEnvironment(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing secret still warns", true: "valid secret"}[present], func(t *testing.T) {
			var environ []string
			if present {
				environ = []string{"MECATL_EXACT_LEGACY=private-legacy-sentinel"}
			}
			env := envWithAuth("/config/mecatl/auth.yaml", "")
			reads := 0
			env.ReadFile = func(string) ([]byte, error) { reads++; return []byte("mcp: [poisoned-unused-fallback"), nil }
			source := newCredentialSource(nil, env, environ)
			resolver := NewMCPProfileResolver(nil, func(string) (string, bool) { t.Fatal("static secret used live OAuth lookup"); return "", false }, source)
			var logs bytes.Buffer
			resolver.SetDiagnostics(slogdiag.NewText(&logs))
			operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{legacyBearerProfile("server")}}
			for range 2 {
				configs, closer, err := resolver.Load(operator)
				if present {
					if err != nil {
						t.Fatal(err)
					}
					if configs[0].Headers["Authorization"] != "Bearer private-legacy-sentinel" {
						t.Fatal("legacy token changed")
					}
					closer.Close()
				} else if !errors.Is(err, ErrMCPProfileSecret) {
					t.Fatalf("missing secret error = %v", err)
				}
				// A refresh can reselect declarations, but not ambient credentials.
				t.Setenv("MECATL_EXACT_LEGACY", "rotated-private-sentinel")
				authority, err := resolver.LoadAuthority(operator, mcpauthority.Global, true)
				if present {
					if err != nil {
						t.Fatal(err)
					}
					_, closer, _ := authority.Global()
					closer.Close()
				} else if !errors.Is(err, ErrMCPProfileSecret) {
					t.Fatalf("authority missing secret error = %v", err)
				}
			}
			if reads != 1 {
				t.Fatalf("fallback acquisitions = %d, want 1", reads)
			}
			if strings.Count(logs.String(), "deprecated") != 1 {
				t.Fatal("warning missing or repeated across loader paths")
			}
			if strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "MECATL_EXACT_LEGACY") {
				t.Fatal("warning exposed credential/reference")
			}
			fresh := NewMCPProfileResolver(nil, nil, source)
			fresh.SetDiagnostics(slogdiag.NewText(&logs))
			_, closer, _ := fresh.Load(operator)
			if closer != nil {
				closer.Close()
			}
			if strings.Count(logs.String(), "deprecated") != 2 {
				t.Fatal("new loader lifetime did not warn")
			}
		})
	}
}

func TestMCPLegacyWarningsPrecedeAllSecretResolution(t *testing.T) {
	for _, first := range []string{"missing token_env", "failing token_ref"} {
		t.Run(first, func(t *testing.T) {
			env := envWithAuth("/config/mecatl/auth.yaml", "mcp: [broken-unused-fallback")
			source := newCredentialSource(nil, env, []string{"MECATL_EXACT_LEGACY=valid-later-token"})
			resolver := NewMCPProfileResolver(nil, nil, source)
			var logs bytes.Buffer
			resolver.SetDiagnostics(slogdiag.NewText(&logs))
			leading := legacyBearerProfile("first")
			leading.Auth.StaticBearer.TokenEnv = "MECATL_MISSING_LEGACY"
			wantWarnings := 3
			wantError := ErrMCPProfileSecret
			if first == "failing token_ref" {
				leading = fileBearerProfile("first", "missing")
				wantWarnings = 2
				wantError = ErrMCPProfileStore
			}
			operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{leading, legacyBearerProfile("second"), legacyBearerProfile("third")}}
			for range 2 {
				if _, _, err := resolver.Load(operator); !errors.Is(err, wantError) {
					t.Fatalf("direct error = %v", err)
				}
				if _, err := resolver.LoadAuthority(operator, mcpauthority.Global, true); !errors.Is(err, wantError) {
					t.Fatalf("authority error = %v", err)
				}
			}
			if strings.Count(logs.String(), "deprecated") != wantWarnings {
				t.Fatal("later effective servers lost warnings or refresh repeated them")
			}
			for _, name := range []string{"second", "third"} {
				if strings.Count(logs.String(), "server="+name) != 1 {
					t.Fatal("warning was not delivered for each later server")
				}
			}
			if strings.Contains(logs.String(), "valid-later-token") || strings.Contains(logs.String(), "MECATL_EXACT_LEGACY") {
				t.Fatal("warning exposed credential/reference")
			}
		})
	}
}

func TestMCPLegacyWarningOnlyEffectiveValidGlobalSelectors(t *testing.T) {
	for _, scenario := range []string{"unselected", "overridden", "invalid union", "invalid environment", "invalid reference", "unknown mode", "broker"} {
		t.Run(scenario, func(t *testing.T) {
			p := legacyBearerProfile("discarded")
			operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{p, {Name: "selected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
			var logs bytes.Buffer
			opts := MCPProfileLoadOptions{Operator: operator, Diagnostics: slogdiag.NewText(&logs), LookupEnv: func(string) (string, bool) { t.Fatal("discarded/invalid selector read secret"); return "", false }}
			switch scenario {
			case "unselected":
				opts.SelectedName = "selected"
			case "overridden":
				opts.Legacy = &MCPServerList{}
				if err := opts.Legacy.Set("discarded=https://legacy.example/mcp"); err != nil {
					t.Fatal(err)
				}
				opts.LookupEnv = func(name string) (string, bool) {
					if name != "MCP_DISCARDED_TOKEN" {
						t.Fatal("overridden selector read secret")
					}
					return "", false
				}
			case "invalid union":
				operator.Servers[0].Auth.StaticBearer.TokenRef = "github"
			case "invalid environment":
				operator.Servers[0].Auth.StaticBearer.TokenEnv = "PRIVATE_UNSAFE_NAME"
			case "invalid reference":
				operator.Servers[0] = fileBearerProfile("discarded", "Invalid")
			case "unknown mode":
				operator.Servers[0].Auth.Mode = "unknown"
			case "broker":
				operator.Mode = "broker"
			}
			profiles, err := LoadMCPProfiles(opts)
			if scenario == "unselected" || scenario == "overridden" {
				if err != nil {
					t.Fatal(err)
				}
				profiles.Close()
			} else if !errors.Is(err, ErrMCPProfileInvalid) {
				t.Fatalf("invalid selector error = %v", err)
			}
			if logs.Len() != 0 {
				t.Fatal("discarded/invalid selector warned")
			}
			if scenario == "broker" {
				_, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: operator, DefaultMode: mcpauthority.Global, BrokerSupported: true, LookupEnv: opts.LookupEnv, Diagnostics: opts.Diagnostics})
				if !errors.Is(err, ErrMCPProfileInvalid) || logs.Len() != 0 {
					t.Fatal("broker accessed or warned about static credential")
				}
			}
		})
	}
}
