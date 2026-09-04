package permconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

const validMCPYAML = `
mcp:
  servers:
    - name: public
      url: http://public.example/mcp
      auth:
        mode: none
    - name: static_api
      url: https://static.example/mcp
      auth:
        mode: static_bearer
        static_bearer:
          token_env: MECATL_MCP_STATIC_TOKEN
    - name: oauth_local
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: work
          principal: alice@example.com
          issuer: https://id.example
          client:
            mode: preregistered
            preregistered:
              id: mecatl-local
              secret_env: MECATL_MCP_CLIENT_SECRET
          scopes: [mcp.read, mcp.write]
          request_refresh_token: true
          credentials:
            mode: local
            local:
              root: /var/lib/mecatl/credentials
              key_env: MECATL_MCP_CREDENTIAL_KEY
          network:
            additional_origins: [https://tokens.example]
            private_origins: [https://mcp.example]
            max_redirects: 2
    - name: oauth_env
      url: https://cluster.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: cluster
          principal: service-account:mecatl
          issuer: https://issuer.example
          client:
            mode: cimd
            cimd:
              document_url: https://client.example/mecatl.json
          scopes: [mcp.read]
          credentials:
            mode: environment
            environment:
              credential_env: MECATL_MCP_CLUSTER_CREDENTIAL
              allow_process_local_refresh: false
          network:
            additional_origins: [https://client.example]
            private_origins: []
            max_redirects: 0
`

func TestMCPValidTaggedUnionVariants(t *testing.T) {
	cfg, err := parseYAML([]byte(validMCPYAML))
	if err != nil {
		t.Fatalf("parse valid MCP settings: %v", err)
	}
	if cfg.MCP == nil || len(cfg.MCP.Servers) != 4 {
		t.Fatalf("MCP servers = %#v, want four", cfg.MCP)
	}
	if got := cfg.MCP.Servers[1].Auth.StaticBearer.TokenEnv; got != "MECATL_MCP_STATIC_TOKEN" {
		t.Fatalf("static token reference = %q", got)
	}
	if cfg.MCP.Servers[2].Auth.OAuth.Client.Preregistered == nil || cfg.MCP.Servers[3].Auth.OAuth.Client.CIMD == nil {
		t.Fatal("OAuth client variants were not preserved")
	}
	if cfg.MCP.Servers[2].Auth.OAuth.Credentials.Local == nil || cfg.MCP.Servers[3].Auth.OAuth.Credentials.Environment == nil {
		t.Fatal("OAuth credential variants were not preserved")
	}
}

func TestMCPStaticProtectedToolsAreStrictTrustedDeclarations(t *testing.T) {
	const config = `mcp:
  servers:
    - name: protected
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          issuer: https://issuer.example
          client:
            mode: preregistered
            preregistered: {id: client, secret_env: MECATL_CLIENT_SECRET}
          scopes: [read]
          credentials:
            mode: local
            local: {root: /credentials, key_env: MECATL_KEY}
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
          tools:
            - name: reviewed
              description: statically admitted
              input_schema: {type: object}
              read_only: true
`
	cfg, err := parseYAML([]byte(config))
	if err != nil {
		t.Fatalf("parse protected static tool: %v", err)
	}
	tool := cfg.MCP.Servers[0].Auth.OAuth.Tools[0]
	if tool.Name != "reviewed" || string(tool.InputSchema) != `{"type":"object"}` || !tool.ReadOnly {
		t.Fatalf("static tool declaration = %#v", tool)
	}
	if _, err := parseYAML([]byte(strings.Replace(config, "read_only: true", "unexpected: value", 1))); err == nil {
		t.Fatal("unknown static-tool field parsed successfully")
	}
}

func TestMCPAuthoritySyntaxIsLosslessAndStrict(t *testing.T) {
	cfg, err := parseYAML([]byte(`mcp:
  mode: broker
  broker:
    callback_url: https://agent.example/callback
  servers:
    - name: protected
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          upstream:
            mode: oauth2
            oauth2:
              authorization_endpoint: https://auth.example/authorize
              token_endpoint: https://auth.example/token
          client:
            mode: cimd
            cimd: {document_url: https://auth.example/client.json}
          scopes: [read]
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`))
	if err != nil {
		t.Fatalf("parse broker declaration: %v", err)
	}
	if cfg.MCP.Mode != "broker" || cfg.MCP.Broker.CallbackURL != "https://agent.example/callback" || cfg.MCP.Servers[0].Auth.OAuth.Upstream.OAuth2.TokenEndpoint != "https://auth.example/token" {
		t.Fatalf("lossless broker declaration = %#v", cfg.MCP)
	}
	for _, body := range []string{
		`mcp: {mode: broker, broker: {callback: https://agent.example/callback}, servers: []}`,
		`mcp: {mode: broker, servers: [{name: x, url: https://x.example/mcp, auth: {mode: oauth, oauth: {upstream: {mode: oauth2, oauth2: {authorization_endpoint: http://auth.example/authorize, token_endpoint: https://auth.example/token}}, client: {mode: cimd, cimd: {document_url: https://auth.example/client.json}}, scopes: [read], network: {additional_origins: [], private_origins: [], max_redirects: 0}}}}]}`,
	} {
		if _, err := parseYAML([]byte(body)); err == nil {
			t.Fatal("invalid authority syntax parsed successfully")
		}
	}
}

func TestTokenEndpointRejectsQueryString(t *testing.T) {
	base := `mcp:
  mode: broker
  broker:
    callback_url: https://agent.example/callback
  servers:
    - name: protected
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          upstream:
            mode: oauth2
            oauth2:
              authorization_endpoint: https://auth.example/authorize
              token_endpoint: %s
          client:
            mode: cimd
            cimd: {document_url: https://auth.example/client.json}
          scopes: [read]
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`
	if _, err := parseYAML([]byte(fmt.Sprintf(base, "https://auth.example/token?tenant=1"))); err == nil {
		t.Fatal("token_endpoint with a query string parsed successfully")
	}
	if _, err := parseYAML([]byte(fmt.Sprintf(base, "https://auth.example/token"))); err != nil {
		t.Fatalf("token_endpoint without a query string failed to parse: %v", err)
	}
	// mcp.servers[].url legitimately carries a query string; this validator
	// must stay scoped to the token endpoint only.
	withServerQuery := strings.Replace(fmt.Sprintf(base, "https://auth.example/token"),
		"url: https://mcp.example/mcp", "url: https://mcp.example/mcp?workspace=1", 1)
	if _, err := parseYAML([]byte(withServerQuery)); err != nil {
		t.Fatalf("mcp.servers[].url with a query string failed to parse: %v", err)
	}
}

func TestMCPStrictValidation(t *testing.T) {
	minimalOAuth := `
mcp:
  servers:
    - name: svc
      url: https://mcp.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: work
          principal: alice
          issuer: https://issuer.example
          client:
            mode: preregistered
            preregistered: {id: client, secret_env: MECATL_CLIENT_SECRET}
          scopes: [read]
          credentials:
            mode: local
            local: {root: /credentials, key_env: MECATL_KEY}
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`
	replace := func(old, replacement string) string { return strings.Replace(minimalOAuth, old, replacement, 1) }
	cases := map[string]string{
		"unknown mcp key":                    replace("  servers:", "  serverz:"),
		"unknown server key":                 replace("      url:", "      endpoint:"),
		"auth mapping omitted":               replace("      auth:\n        mode: oauth\n        oauth:\n", ""),
		"missing auth mode":                  replace("        mode: oauth\n", ""),
		"unknown auth mode":                  replace("mode: oauth", "mode: dcr"),
		"cross auth variant":                 replace("        oauth:\n", "        static_bearer: {token_env: MECATL_TOKEN}\n        oauth:\n"),
		"none with null payload":             `mcp: {servers: [{name: svc, url: https://mcp.example/mcp, auth: {mode: none, oauth: null}}]}`,
		"unknown oauth key":                  replace("          profile:", "          profil:"),
		"client mapping omitted":             replace("          client:\n            mode: preregistered\n            preregistered: {id: client, secret_env: MECATL_CLIENT_SECRET}\n", ""),
		"dcr client":                         replace("mode: preregistered", "mode: dcr"),
		"cross client variant":               replace("            preregistered:", "            cimd: {document_url: https://client.example/cimd.json}\n            preregistered:"),
		"client with null cross variant":     replace("            preregistered:", "            cimd: null\n            preregistered:"),
		"unknown credentials":                replace("mode: local", "mode: vault"),
		"cross credential variant":           replace("            local:", "            environment: {credential_env: MECATL_CREDENTIAL}\n            local:"),
		"credential with null cross variant": replace("            local:", "            environment: null\n            local:"),
		"missing scopes":                     replace("          scopes: [read]\n", ""),
		"invalid env reference":              replace("MECATL_CLIENT_SECRET", "CLIENT_SECRET"),
		"secret value not reference":         replace("MECATL_CLIENT_SECRET", "actual-secret-value"),
		"relative root":                      replace("root: /credentials", "root: credentials"),
		"missing network":                    replace("          network: {additional_origins: [], private_origins: [], max_redirects: 0}\n", ""),
		"redirect negative":                  replace("max_redirects: 0", "max_redirects: -1"),
		"redirect too high":                  replace("max_redirects: 0", "max_redirects: 6"),
		"relative URL":                       replace("https://mcp.example/mcp", "/mcp"),
		"URL userinfo":                       replace("https://mcp.example/mcp", "https://user@mcp.example/mcp"),
		"URL fragment":                       replace("https://mcp.example/mcp", "https://mcp.example/mcp#secret"),
		"authenticated external HTTP":        replace("https://mcp.example/mcp", "http://mcp.example/mcp"),
		"issuer path":                        replace("https://issuer.example", "https://issuer.example/oauth"),
		"noncanonical issuer":                replace("https://issuer.example", "https://ISSUER.example:443/"),
		"CIMD replaced by unsupported shape": replace("preregistered: {id: client, secret_env: MECATL_CLIENT_SECRET}", "cimd: {document_url: http://client.example/cimd.json}"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseYAML([]byte(body)); err == nil {
				t.Fatal("parse succeeded; want strict validation error")
			}
		})
	}
}

func TestMCPRejectsDuplicateNamesAndInvalidOrigins(t *testing.T) {
	for name, body := range map[string]string{
		"case collision":      `mcp: {servers: [{name: GitHub, url: https://one.example/mcp, auth: {mode: none}}, {name: github, url: https://two.example/mcp, auth: {mode: none}}]}`,
		"double underscore":   `mcp: {servers: [{name: bad__name, url: https://one.example/mcp, auth: {mode: none}}]}`,
		"origin path":         strings.Replace(validMCPYAML, "https://tokens.example]", "https://tokens.example/path]", 1),
		"origin query":        strings.Replace(validMCPYAML, "https://tokens.example]", "https://tokens.example?x=1]", 1),
		"origin wildcard":     strings.Replace(validMCPYAML, "https://tokens.example]", "https://*.example]", 1),
		"private not allowed": strings.Replace(validMCPYAML, "private_origins: [https://mcp.example]", "private_origins: [https://private.example]", 1),
		"CIMD origin absent":  strings.Replace(validMCPYAML, "additional_origins: [https://client.example]", "additional_origins: []", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseYAML([]byte(body)); err == nil {
				t.Fatal("parse succeeded; want validation error")
			}
		})
	}
}

func TestOperatorMCPWholeBlockPrecedence(t *testing.T) {
	cli := `mcp: {servers: [{name: cli, url: https://cli.example/mcp, auth: {mode: none}}]}`
	user := `mcp: {servers: [{name: user, url: https://user.example/mcp, auth: {mode: static_bearer, static_bearer: {token_env: MECATL_USER_TOKEN}}}]}`
	env := xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return "/cfg"
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(path string) ([]byte, error) {
			switch path {
			case "/cli/settings.yaml":
				return []byte(cli), nil
			case "/cfg/mecatl/settings.yaml":
				return []byte(user), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{"/cli/settings.yaml"}}, env)
	got := r.OperatorMCP()
	if got == nil || len(got.Servers) != 1 || got.Servers[0].Name != "cli" {
		t.Fatalf("operator MCP = %#v; want complete CLI block only", got)
	}
	if ((*Resolver)(nil)).OperatorMCP() != nil {
		t.Fatal("nil resolver accessor must be nil")
	}
}

func TestMCPMetadataCaptureDoesNotResolveSecretReferences(t *testing.T) {
	const settings = `mcp: {servers: [{name: static_api, url: https://mcp.example/mcp, auth: {mode: static_bearer, static_bearer: {token_env: MECATL_MCP_TOKEN}}}]}`
	env := xdgconfig.ResolveEnv{
		Getenv: func(name string) string {
			t.Fatalf("metadata capture looked up environment variable %q", name)
			return ""
		},
		UserHomeDir: func() (string, error) {
			t.Fatal("metadata capture inspected the user home")
			return "", errors.New("unexpected call")
		},
		ReadFile: func(path string) ([]byte, error) {
			if path != "/operator/settings.yaml" {
				t.Fatalf("unexpected file read %q", path)
			}
			return []byte(settings), nil
		},
	}
	r := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, env)
	got := r.OperatorMCP()
	if got == nil || len(got.Servers) != 1 || got.Servers[0].Auth.StaticBearer.TokenEnv != "MECATL_MCP_TOKEN" {
		t.Fatalf("captured MCP metadata = %#v", got)
	}
}

func TestMalformedProjectMCPFailsSoftWithValueFreeWarning(t *testing.T) {
	const malformed = `mcp:
  servers:
    - name: canary
      url: https://url-canary.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: profile-canary
          principal: principal-canary
          issuer: https://issuer-canary.example
          client:
            mode: preregistered
            preregistered: {id: client, secret_env: MECATL_SECRET_CANARY}
          scopes: [read]
          credentials:
            mode: environment
            environment: {credential_env: MECATL_CREDENTIAL_CANARY}
          network: {additional_origins: [], private_origins: [], max_redirects: 99}
`
	var log bytes.Buffer
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, malformed)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: slogdiag.New(&log, false, port.LevelDebug)}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)
	if r.OperatorMCP() != nil {
		t.Fatal("malformed project MCP block became operator configuration")
	}
	got := log.String()
	if !strings.Contains(got, "project YAML invalid; skipping") {
		t.Fatalf("missing fail-soft warning: %s", got)
	}
	for _, canary := range []string{"url-canary", "issuer-canary", "profile-canary", "principal-canary", "MECATL_SECRET_CANARY", "MECATL_CREDENTIAL_CANARY"} {
		if strings.Contains(got, canary) {
			t.Errorf("warning leaked project MCP value %q: %s", canary, got)
		}
	}
}

func TestProjectMCPIgnoredWithValueFreeWarning(t *testing.T) {
	const canary = `mcp:
  servers:
    - name: canary
      url: https://url-canary.example/mcp
      auth:
        mode: oauth
        oauth:
          profile: profile-canary
          principal: principal-canary
          issuer: https://issuer-canary.example
          client:
            mode: preregistered
            preregistered: {id: client, secret_env: MECATL_SECRET_CANARY}
          scopes: [read]
          credentials:
            mode: environment
            environment: {credential_env: MECATL_CREDENTIAL_CANARY}
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`
	var log bytes.Buffer
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, canary)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: slogdiag.New(&log, false, port.LevelDebug)}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)
	if r.OperatorMCP() != nil {
		t.Fatal("project MCP block became operator configuration")
	}
	got := log.String()
	if !strings.Contains(got, "IGNORING a project-tier mcp") {
		t.Fatalf("missing generic warning: %s", got)
	}
	for _, secret := range []string{"url-canary", "issuer-canary", "profile-canary", "principal-canary", "MECATL_SECRET_CANARY", "MECATL_CREDENTIAL_CANARY"} {
		if strings.Contains(got, secret) {
			t.Errorf("warning leaked project MCP value %q: %s", secret, got)
		}
	}
}
