package app

import (
	"crypto/ecdsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file build with the identity broker's gateway entries and
// the embedded gateway's signing key. None is parallel: a protected route
// reads an environment variable.

// entryBuild is a build configuration with the identity broker on, the
// protected calendar route with an embedded gateway signing key, and opts'
// configuration file.
func entryBuild(t *testing.T, opts brokerconfigtest.Options) Config {
	t.Helper()
	return Config{
		UseMock: true, NoSoul: true, NoShell: true,
		IdentityBroker: true, MCPAuthority: protectedBrokerAuthorityWithKey(t),
		IdentityBrokerConfigFile: brokerconfigtest.Write(t, opts).Path,
	}
}

// otherAS2Trust is an AS-2 trust in an issuer other than any AS-1 here, so a
// gateway entry may route servers.
func otherAS2Trust(t *testing.T) as2Trust {
	t.Helper()
	return mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
		Issuer: "https://untrusted-as1.mecatl.test", JWKSURL: "https://untrusted-as1.mecatl.test/jwks",
		CAFile: identityharness.New(t, identityharness.Options{}).WriteCACert(t, t.TempDir()), Subjects: []string{"nobody"},
	})
}

// wantBuildRefused fails t unless Build refuses cfg with an error wrapping
// want, when set, and containing detail.
func wantBuildRefused(t *testing.T, cfg Config, want error, detail string) {
	t.Helper()
	built, err := buildIsolated(t, t.Context(), cfg)
	if err == nil {
		built.Close()
		t.Fatalf("Build succeeded, want %v", want)
	}
	if (want != nil && !errors.Is(err, want)) || !strings.Contains(err.Error(), detail) {
		t.Fatalf("Build error = %v, want %v containing %q", err, want, detail)
	}
}

// TestBuildRefusesADisagreeingGatewayEntry: an embedded gateway entry must
// name the AS-2 the embedded gateway derives from mcp.broker.callback_url,
// byte for byte, route only the MCP broker's OAuth servers, and have an
// embedded gateway to describe.
func TestBuildRefusesADisagreeingGatewayEntry(t *testing.T) {
	entry := func(edit func(*identitybroker.GatewayEntry)) []identitybroker.GatewayEntry {
		e := embeddedEntry()
		edit(&e[0])
		return e
	}
	for name, tc := range map[string]struct {
		gateways []identitybroker.GatewayEntry
		edit     func(*Config)
		want     error
		detail   string
	}{
		"another issuer": {
			gateways: entry(func(e *identitybroker.GatewayEntry) { e.AS2Issuer = "https://broker.example/v1/mcp/other" }),
			want:     errEmbeddedEntryDisagrees, detail: "as2_issuer",
		},
		"an issuer with a trailing slash": {
			gateways: entry(func(e *identitybroker.GatewayEntry) { e.AS2Issuer += "/" }),
			want:     errEmbeddedEntryDisagrees, detail: "as2_issuer",
		},
		"another token endpoint": {
			gateways: entry(func(e *identitybroker.GatewayEntry) { e.TokenEndpoint = as2TestIssuer + "/token" }),
			want:     errEmbeddedEntryDisagrees, detail: "token_endpoint",
		},
		"another resource": {
			gateways: entry(func(e *identitybroker.GatewayEntry) { e.Resource = as2TestIssuer }),
			want:     errEmbeddedEntryDisagrees, detail: "resource",
		},
		"a server the MCP broker does not route": {
			gateways: embeddedEntry("github"), want: errEmbeddedEntryServerIsNotARoute, detail: `"github"`,
		},
		"a server in another case": {
			gateways: embeddedEntry("Calendar"), want: errEmbeddedEntryServerIsNotARoute, detail: `"Calendar"`,
		},
		"an anonymous server": {
			gateways: embeddedEntry("docs"), want: errEmbeddedEntryServerIsNotARoute, detail: `"docs"`,
			edit: func(cfg *Config) {
				declaration, _ := cfg.MCPAuthority.Broker()
				declaration.Routes = append(declaration.Routes, permconfig.MCPServerProfile{Name: "docs", URL: "https://docs.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}})
				cfg.MCPAuthority = mcpauthority.NewBroker(declaration)
			},
		},
		"no MCP broker": {
			gateways: embeddedEntry(), want: errEmbeddedEntryWithoutEmbeddedGateway,
			edit: func(cfg *Config) { cfg.MCPAuthority = nil },
		},
		"no OAuth route": {
			gateways: embeddedEntry(), want: errEmbeddedEntryWithoutEmbeddedGateway,
			edit: func(cfg *Config) { cfg.MCPAuthority = mcpauthority.NewBroker(mcpauthority.BrokerConfig{}) },
		},
		"a configured MCP broker": {
			gateways: embeddedEntry(), want: errEmbeddedEntryWithoutEmbeddedGateway,
			edit: func(cfg *Config) {
				cfg.MCPAuthority = mcpauthority.NewBroker(mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL})
				cfg.MCPBrokerCaller = unusedBrokerCaller
			},
		},
		"a non-https callback": {
			gateways: embeddedEntry(), want: errEmbeddedEntryWithoutEmbeddedGateway,
			edit: func(cfg *Config) {
				declaration, _ := cfg.MCPAuthority.Broker()
				declaration.CallbackURL = "http://broker.example/oauth/callback"
				cfg.MCPAuthority = mcpauthority.NewBroker(declaration)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := entryBuild(t, brokerconfigtest.Options{Gateways: tc.gateways})
			cfg.IdentityBrokerAS1 = unusedAS1("https://as1.example")
			cfg.IdentityBrokerAS2Trust = otherAS2Trust(t)
			if tc.edit != nil {
				tc.edit(&cfg)
			}
			wantBuildRefused(t, cfg, tc.want, tc.detail)
		})
	}
}

// TestBuildRefusesGatewayServersBeforeAS2TrustsAS1: production's AS-2
// trusts no ID-JAG issuer, so an entry routing servers is refused, naming
// #2533; routing them also needs AS-1.
func TestBuildRefusesGatewayServersBeforeAS2TrustsAS1(t *testing.T) {
	cfg := entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry(gatewayTestServer)})
	cfg.IdentityBrokerAS1 = unusedAS1("https://as1.example")
	wantBuildRefused(t, cfg, errGatewayServersBeforeAS2TrustsAS1, "#2533")

	cfg = entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry(gatewayTestServer)})
	cfg.IdentityBrokerAS2Trust = otherAS2Trust(t)
	wantBuildRefused(t, cfg, errGatewayServersWithoutAS1, "")
}

// TestBuildGatewaySigningKeyRules: the embedded gateway's signing key is
// required with an embedded gateway entry and refused without one, including
// with the identity broker off.
func TestBuildGatewaySigningKeyRules(t *testing.T) {
	t.Run("missing with an entry", func(t *testing.T) {
		cfg := entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry()})
		cfg.MCPAuthority = protectedBrokerAuthority()
		wantBuildRefused(t, cfg, errNoAS2SigningKey, "")
	})
	t.Run("without an entry", func(t *testing.T) {
		wantBuildRefused(t, entryBuild(t, brokerconfigtest.Options{}), errAS2SigningKeyWithoutEntry, "")
	})
	t.Run("with the identity broker off", func(t *testing.T) {
		wantBuildRefused(t, Config{UseMock: true, NoSoul: true, NoShell: true, MCPAuthority: protectedBrokerAuthorityWithKey(t)}, errAS2SigningKeyWithoutEntry, "")
	})
	t.Run("with an injected core", func(t *testing.T) {
		cfg := Config{UseMock: true, NoSoul: true, NoShell: true, IdentityBroker: true, identityBrokerCore: newClosableCore(t), MCPAuthority: protectedBrokerAuthorityWithKey(t)}
		wantBuildRefused(t, cfg, errAS2SigningKeyWithoutEntry, "")
	})
	t.Run("a gateway token lifetime with an entry routing no servers", func(t *testing.T) {
		// The gateway issues no gateway token, so the setting would be inert.
		cfg := entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry()})
		declaration, _ := cfg.MCPAuthority.Broker()
		declaration.GatewayTokenLifetime = 10 * time.Second
		cfg.MCPAuthority = mcpauthority.NewBroker(declaration)
		wantBuildRefused(t, cfg, errGatewayTokenLifetimeWithoutServers, "")
	})
	t.Run("unreadable", func(t *testing.T) {
		cfg := entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry()})
		declaration, _ := cfg.MCPAuthority.Broker()
		declaration.SigningKeyFile = t.TempDir() + "/absent.pem"
		cfg.MCPAuthority = mcpauthority.NewBroker(declaration)
		wantBuildRefused(t, cfg, nil, "the embedded gateway's signing key")
	})
}

// TestBuildRefusesAGatewaySigningKeyThatIsABrokerKey compares the public
// halves of the embedded gateway's signing key and its fallbacks with every
// identity broker key.
func TestBuildRefusesAGatewaySigningKeyThatIsABrokerKey(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	agentFallback := brokerconfigtest.NewKey(t)
	for name, tc := range map[string]struct {
		hostedAS1 bool
		// key and fallback pick the embedded gateway's signing key and a
		// fallback from the written file; nil picks a new key.
		key, fallback func(brokerconfigtest.Files) string
	}{
		"the agent issuer key":     {key: func(f brokerconfigtest.Files) string { return writeAS2Key(t, f.AgentIssuerKey) }},
		"an agent issuer fallback": {key: func(brokerconfigtest.Files) string { return writeAS2Key(t, agentFallback) }},
		"the broker client key":    {key: func(f brokerconfigtest.Files) string { return writeAS2Key(t, f.BrokerClientKey) }},
		"a fallback that is the broker client key": {
			fallback: func(f brokerconfigtest.Files) string { return writeAS2Key(t, f.BrokerClientKey) },
		},
		"AS-1's signing key":   {hostedAS1: true, key: func(f brokerconfigtest.Files) string { return f.AS1SigningKeyFile }},
		"an AS-1 fallback key": {hostedAS1: true, fallback: func(f brokerconfigtest.Files) string { return f.AS1FallbackKeyFiles[0] }},
	} {
		t.Run(name, func(t *testing.T) {
			opts := brokerconfigtest.Options{Gateways: embeddedEntry(), AgentIssuerFallbackKeys: []*ecdsa.PrivateKey{agentFallback}, HostedAS1: tc.hostedAS1}
			if tc.hostedAS1 {
				opts.AS1Fallbacks = 1
			}
			files := brokerconfigtest.Write(t, opts)
			declaration, _ := protectedBrokerAuthority().Broker()
			declaration.SigningKeyFile = writeAS2Key(t, brokerconfigtest.NewKey(t))
			if tc.key != nil {
				declaration.SigningKeyFile = tc.key(files)
			}
			if tc.fallback != nil {
				declaration.FallbackKeyFiles = []string{tc.fallback(files)}
			}
			cfg := Config{
				UseMock: true, NoSoul: true, NoShell: true, IdentityBroker: true,
				MCPAuthority: mcpauthority.NewBroker(declaration), IdentityBrokerConfigFile: files.Path,
			}
			if tc.hostedAS1 {
				cfg.IdentityBrokerHostedAS1 = hostedAS1Config(t, h, "https://as1.mecatl.test/as1")
			}
			wantBuildRefused(t, cfg, errAS2SigningKeyIsABrokerKey, "")
		})
	}
	// Positive control: a key of its own, with a fallback of its own, starts.
	cfg := entryBuild(t, brokerconfigtest.Options{Gateways: embeddedEntry(), AgentIssuerFallbackKeys: []*ecdsa.PrivateKey{agentFallback}})
	declaration, _ := cfg.MCPAuthority.Broker()
	declaration.FallbackKeyFiles = []string{writeAS2Key(t, brokerconfigtest.NewKey(t))}
	cfg.MCPAuthority = mcpauthority.NewBroker(declaration)
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatalf("Build with the embedded gateway's own keys: %v", err)
	}
	built.Close()
}
