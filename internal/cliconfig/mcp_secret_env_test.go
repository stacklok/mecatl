package cliconfig

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestDirectPreregisteredSecretEnvShippedConfiguration(t *testing.T) {
	const fixture = `mcp:
  mode: global
  servers:
    - name: direct
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: work
          principal: local-user
          issuer: https://issuer.example
          client:
            mode: preregistered
            preregistered: {id: direct-client, secret_env: MECATL_CLIENT_SECRET}
          scopes: [read]
          credentials:
            mode: environment
            environment: {credential_env: MECATL_CREDENTIAL}
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`
	var cfg permconfig.Config
	if err := yaml.Unmarshal([]byte(fixture), &cfg); err != nil {
		t.Fatal(err)
	}
	const secret = "fixture-client-secret"
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "present", false: "missing"}[present], func(t *testing.T) {
			authority, err := ResolveMCPAuthority(MCPAuthorityOptions{
				Operator: cfg.MCP, DefaultMode: mcpauthority.Global,
				LookupEnv: func(name string) (string, bool) {
					switch name {
					case "MECATL_CLIENT_SECRET":
						return secret, present
					case "MECATL_CREDENTIAL":
						return base64.StdEncoding.EncodeToString([]byte("fixture-credential-record")), true
					default:
						t.Fatalf("unexpected environment lookup %q", name)
						return "", false
					}
				},
			})
			if !present {
				if !errors.Is(err, ErrMCPProfileSecret) || strings.Contains(err.Error(), secret) {
					t.Fatalf("missing client secret did not fail safely: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			servers, lifecycle, ok := authority.Global()
			if !ok || len(servers) != 1 {
				t.Fatal("direct configuration changed authority")
			}
			defer lifecycle.Close()
			client := servers[0].OAuth.Client.Preregistered
			if client == nil || client.ClientID != "direct-client" || client.ClientSecretAuth == nil || client.ClientSecretAuth.ClientSecret != secret {
				t.Fatal("shipped environment client secret was not passed to direct OAuth")
			}
		})
	}
	for name, replacement := range map[string]string{
		"mixed sources":     "secret_env: MECATL_CLIENT_SECRET, secret_file: /mounted/client-secret",
		"invalid reference": "secret_env: OTHER_CLIENT_SECRET",
		"empty reference":   "secret_env: ''",
	} {
		t.Run(name, func(t *testing.T) {
			var invalid permconfig.Config
			if err := yaml.Unmarshal([]byte(strings.Replace(fixture, "secret_env: MECATL_CLIENT_SECRET", replacement, 1)), &invalid); err == nil {
				t.Fatal("invalid client secret reference accepted")
			}
		})
	}
}
