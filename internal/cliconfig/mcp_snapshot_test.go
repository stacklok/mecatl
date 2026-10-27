package cliconfig

import (
	"errors"
	"os"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestStaticSnapshotEnvironmentFirstThenFileReference(t *testing.T) {
	for _, initial := range []string{"valid", "missing", "broken"} {
		for _, first := range []string{"lookup", "direct", "authority", "legacy direct", "legacy authority"} {
			t.Run(initial+"/"+first, func(t *testing.T) {
				reads := 0
				body := "mcp:\n  later:\n    token: original-file\n"
				if initial == "broken" {
					body = "mcp: [broken"
				}
				missing := initial == "missing"
				env := envWithAuth("/config/mecatl/auth.yaml", "")
				env.ReadFile = func(string) ([]byte, error) {
					reads++
					if missing {
						return nil, os.ErrNotExist
					}
					return []byte(body), nil
				}
				source := newCredentialSource(nil, env, []string{"MECATL_MCP_TOKEN_FIRST=original-env", "MECATL_EXACT_LEGACY=original-env"})
				resolver := NewMCPProfileResolver(nil, nil, source)
				profile := fileBearerProfile("server", "first")
				if first == "legacy direct" || first == "legacy authority" {
					profile = legacyBearerProfile("server")
				}
				operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{profile}}
				load := func(authority bool) ([]mcp.ServerConfig, error) {
					if authority {
						result, err := resolver.LoadAuthority(operator, mcpauthority.Global, true)
						if err != nil {
							return nil, err
						}
						servers, closer, _ := result.Global()
						defer closer.Close()
						return servers, nil
					}
					servers, closer, err := resolver.Load(operator)
					if closer != nil {
						defer closer.Close()
					}
					return servers, err
				}
				if first == "lookup" {
					if token, err := source.Lookup(MCPBearerSecret, "first"); err != nil || token != "original-env" {
						t.Fatal("environment winner required usable fallback")
					}
				} else {
					servers, err := load(first == "authority" || first == "legacy authority")
					if err != nil || servers[0].Headers["Authorization"] != "Bearer original-env" {
						t.Fatal("environment winner required usable fallback")
					}
				}
				if reads != 1 {
					t.Fatalf("initial acquisitions = %d, want 1", reads)
				}
				body, missing = "mcp:\n  later:\n    token: rotated-file\n", false
				t.Setenv("MECATL_MCP_TOKEN_FIRST", "rotated-env")
				t.Setenv("MECATL_EXACT_LEGACY", "rotated-env")
				t.Setenv("MECATL_MCP_TOKEN_LATER", "newly-present-env")
				for _, authority := range []bool{false, true} {
					// Refresh declarations to a different ref whose environment was absent.
					operator.Servers[0] = fileBearerProfile("server", "later")
					servers, err := load(authority)
					if initial == "valid" {
						if err != nil || servers[0].Headers["Authorization"] != "Bearer original-file" {
							t.Fatal("refresh did not preserve original file and environment absence")
						}
					} else if !errors.Is(err, ErrMCPProfileStore) {
						t.Fatalf("refresh lost original source failure: %v", err)
					}
					operator.Servers[0] = profile
					servers, err = load(authority)
					if err != nil || servers[0].Headers["Authorization"] != "Bearer original-env" {
						t.Fatal("refresh changed captured environment winner")
					}
				}
				if reads != 1 {
					t.Fatalf("refresh reread fallback: %d acquisitions", reads)
				}
			})
		}
	}
}
