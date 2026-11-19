package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// TestIdentityBrokerRequiresOwnerIdentitySubID pins fail-closed startup: with
// caller identity on, the broker must not start unless owners come from the
// AS-1 access token's sub_id.
func TestIdentityBrokerRequiresOwnerIdentitySubID(t *testing.T) {
	oidc := []string{"--mock", "--oidc-issuer=https://as1.example", "--oidc-audience=mecatl"}
	owner := []string{"--oidc-owner-identity=sub_id", "--oidc-accepted-client-ids=mecatui,studio"}
	broker := brokerArgs
	join := slices.Concat[[]string]
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{name: "broker with OIDC and owner identity sub_id", args: join(broker, oidc, owner)},
		{name: "broker without OIDC", args: join([]string{"--mock"}, broker)},
		{name: "OIDC without broker", args: oidc},
		{name: "broker with OIDC but no owner identity", args: join(broker, oidc), want: "requires --oidc-owner-identity=sub_id"},
		{name: "broker with OIDC and empty owner identity", args: join(broker, []string{"--oidc-owner-identity="}, oidc), want: "requires --oidc-owner-identity=sub_id"},
		{name: "unknown owner identity", args: join(oidc, []string{"--oidc-owner-identity=upstream_subject"}), want: "accepted values: sub_id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseFlags(c.args)
			if c.want == "" {
				if err != nil {
					t.Fatalf("parseFlags(%v): %v", c.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("parseFlags(%v) = %v, want error containing %q", c.args, err, c.want)
			}
		})
	}
}

// TestOwnerIdentityMisconfigurationRefusesStartup pins that a misconfigured
// owner identity setting stops mecak8s before it serves anything.
func TestOwnerIdentityMisconfigurationRefusesStartup(t *testing.T) {
	oidc := []string{"--mock", "--oidc-issuer=https://as1.example.invalid", "--oidc-audience=mecatl"}
	for name, args := range map[string][]string{
		"without client IDs":            slices.Concat(oidc, []string{"--oidc-owner-identity=sub_id"}),
		"client IDs without the option": slices.Concat(oidc, []string{"--oidc-accepted-client-ids=mecatui"}),
		"without issuer":                {"--mock", "--oidc-owner-identity=sub_id", "--oidc-accepted-client-ids=mecatui"},
		"advertised client ID not accepted": slices.Concat(oidc, []string{
			"--oidc-owner-identity=sub_id", "--oidc-accepted-client-ids=studio",
			"--oidc-resource=https://mecatl.example", "--oidc-client-id=mecatui",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseFlags(args)
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", args, err)
			}
			if auth, err := newAuthenticator(t.Context(), cfg, nil); auth != nil || !errors.Is(err, cliconfig.ErrOIDCMisconfigured) {
				t.Fatalf("newAuthenticator(%v) = (%v, %v), want ErrOIDCMisconfigured", args, auth, err)
			}
		})
	}
}

// brokerArgs enable the identity broker with its configuration file, which
// flag parsing does not read.
var brokerArgs = []string{"--enable-identity-broker", "--identity-broker-config=/etc/mecatl/identity-broker.json"}

// as1Args host AS-1 with every required companion flag.
var as1Args = []string{
	"--mock", "--enable-identity-broker", "--identity-broker-config=/etc/mecatl/identity-broker.json",
	"--identity-broker-as1-issuer=https://mecatl.example/as1",
	"--identity-broker-as1-tui-client-id=mecatui",
	"--identity-broker-as1-upstream-issuer=https://idp.example",
	"--identity-broker-as1-upstream-client-id=mecatl-as1",
	"--identity-broker-subject-claim=sub",
	"--oidc-audience=https://mecatl.example/api",
	"--oidc-resource=https://mecatl.example",
	"--oidc-owner-identity=sub_id",
}

// TestIdentityBrokerAS1FlagRules pins which hosted AS-1 configurations
// mecak8s starts with, and that caller identity is derived from AS-1's.
func TestIdentityBrokerAS1FlagRules(t *testing.T) {
	without := func(drop ...string) []string {
		return slices.DeleteFunc(slices.Clone(as1Args), func(arg string) bool {
			return slices.ContainsFunc(drop, func(prefix string) bool { return strings.HasPrefix(arg, prefix) })
		})
	}
	with := func(extra ...string) []string { return slices.Concat(as1Args, extra) }
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{name: "all companions", args: as1Args},
		{name: "matching explicit caller identity", args: with(
			"--oidc-issuer=https://mecatl.example/as1", "--oidc-client-id=mecatui",
			"--oidc-accepted-client-ids=studio,mecatui",
			"--identity-broker-subject-claim=oid",
			"--identity-broker-as1-upstream-client-secret-file=/secrets/as1")},
		{name: "TUI client without issuer", args: []string{"--mock", "--identity-broker-as1-tui-client-id=mecatui"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream issuer without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-issuer=https://idp.example"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream client without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-client-id=x"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream secret without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-client-secret-file=/s"}, want: "require --identity-broker-as1-issuer"},
		{name: "subject claim without issuer", args: []string{"--mock", "--identity-broker-subject-claim=oid"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream CA without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-ca-file=/ca.pem"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream private addresses without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-allow-private-addresses"}, want: "require --identity-broker-as1-issuer"},
		{name: "without the broker", args: without("--enable-identity-broker", "--identity-broker-config"), want: "requires --enable-identity-broker"},
		{name: "without the broker configuration file", args: without("--identity-broker-config"), want: "--enable-identity-broker and --identity-broker-config require each other"},
		{name: "the removed AS-1 signing key flag", args: with("--identity-broker-as1-signing-key-file=/keys/as1.pem"), want: "--identity-broker-as1-signing-key-file was removed: set AS-1's signing key (keys.as1_signing) in the --identity-broker-config file"},
		{name: "the removed AS-1 signing key flag, empty", args: with("--identity-broker-as1-signing-key-file="), want: "--identity-broker-as1-signing-key-file was removed"},
		{name: "the removed AS-1 signing key flag without the broker", args: []string{"--mock", "--identity-broker-as1-signing-key-file", "/keys/as1.pem"}, want: "--identity-broker-as1-signing-key-file was removed"},
		{name: "without the TUI client", args: without("--identity-broker-as1-tui-client-id"), want: "requires --identity-broker-as1-tui-client-id"},
		{name: "without the upstream issuer", args: without("--identity-broker-as1-upstream-issuer"), want: "requires --identity-broker-as1-tui-client-id"},
		{name: "without the upstream client", args: without("--identity-broker-as1-upstream-client-id"), want: "requires --identity-broker-as1-tui-client-id"},
		{name: "without the subject claim", args: without("--identity-broker-subject-claim"), want: "requires --identity-broker-subject-claim: oid for Entra ID, whose sub differs per application, or sub for an identity provider whose sub is the same across applications"},
		{name: "empty subject claim", args: with("--identity-broker-subject-claim="), want: "requires --identity-broker-subject-claim"},
		{name: "http issuer", args: with("--identity-broker-as1-issuer=http://mecatl.example/as1"), want: "--identity-broker-as1-issuer must be an https URL"},
		{name: "issuer with a query", args: with("--identity-broker-as1-issuer=https://mecatl.example/as1?x=1"), want: "--identity-broker-as1-issuer must be an https URL"},
		{name: "http upstream issuer", args: with("--identity-broker-as1-upstream-issuer=http://idp.example"), want: "--identity-broker-as1-upstream-issuer must be an https URL"},
		{name: "upstream private addresses without the upstream CA", args: with("--identity-broker-as1-upstream-allow-private-addresses"), want: "--identity-broker-as1-upstream-allow-private-addresses requires --identity-broker-as1-upstream-ca-file"},
		{name: "without the audience", args: without("--oidc-audience"), want: "requires --oidc-audience"},
		{name: "plain-word audience", args: with("--oidc-audience=mecatl"), want: "--oidc-audience must be an absolute http or https URL"},
		{name: "audience without a host", args: with("--oidc-audience=https:///api"), want: "--oidc-audience must be an absolute http or https URL"},
		{name: "non-http audience", args: with("--oidc-audience=urn:mecatl:api"), want: "--oidc-audience must be an absolute http or https URL"},
		{name: "audience with a fragment", args: with("--oidc-audience=https://mecatl.example/api#x"), want: "--oidc-audience must be an absolute http or https URL"},
		{name: "without the resource", args: without("--oidc-resource"), want: "requires --oidc-resource"},
		{name: "private HTTPS issuer admission", args: with("--oidc-allow-private-https-issuer", "--oidc-ca-cert-file=/ca.pem"), want: "--oidc-allow-private-https-issuer cannot be used"},
		{name: "another JWKS URI", args: with("--oidc-jwks-uri=https://idp.example/jwks"), want: "--oidc-jwks-uri cannot be used with --identity-broker-as1-issuer"},
		{name: "AS-1's own JWKS URI", args: with("--oidc-jwks-uri=https://mecatl.example/as1/.well-known/jwks.json"), want: "--oidc-jwks-uri cannot be used with --identity-broker-as1-issuer"},
		{name: "another OIDC issuer", args: with("--oidc-issuer=https://idp.example"), want: "--oidc-issuer must be unset or equal"},
		{name: "another client ID", args: with("--oidc-client-id=studio"), want: "--oidc-client-id must be unset or equal"},
		{name: "accepted client IDs without the TUI", args: with("--oidc-accepted-client-ids=studio"), want: "--oidc-accepted-client-ids must include"},
		{name: "without owner identity sub_id", args: without("--oidc-owner-identity"), want: "requires --oidc-owner-identity=sub_id"},
		{name: "scopes without offline_access", args: with("--oidc-scopes=openid,profile"), want: "--oidc-scopes must include offline_access"},
		{name: "scopes with offline_access", args: with("--oidc-scopes=openid,offline_access")},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parseFlags(c.args)
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("parseFlags(%v) = %v, want error containing %q", c.args, err, c.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", c.args, err)
			}
			if cfg.oidc.Issuer != "https://mecatl.example/as1" || cfg.oidc.ClientID != "mecatui" || !slices.Contains(cfg.oidc.AcceptedClientIDs, "mecatui") {
				t.Fatalf("caller identity = issuer %q, client %q, accepted %v; want AS-1's", cfg.oidc.Issuer, cfg.oidc.ClientID, cfg.oidc.AcceptedClientIDs)
			}
			got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1
			if got == nil || got.Issuer != "https://mecatl.example/as1" || got.Audience != "https://mecatl.example/api" || len(got.PublicClients) != 1 || got.PublicClients[0].ID != "mecatui" ||
				!slices.Equal(got.PublicClients[0].RedirectURIs, []string{oauthlogin.ExactRedirectURL}) || got.UpstreamIssuer != "https://idp.example" ||
				got.UpstreamClientID != "mecatl-as1" || got.UpstreamCAFile != "" || got.UpstreamAllowPrivateAddresses {
				t.Fatalf("IdentityBrokerHostedAS1 = %+v", got)
			}
		})
	}
	cfg, err := parseFlags(as1Args)
	if err != nil {
		t.Fatal(err)
	}
	if got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1; got.SubjectClaim != "sub" || got.UpstreamClientSecretFile != "" {
		t.Fatalf("IdentityBrokerHostedAS1 defaults = %+v, want the explicit subject claim sub and no files", got)
	}
	cfg, err = parseFlags(with("--identity-broker-subject-claim=oid", "--identity-broker-as1-upstream-client-secret-file=/secrets/as1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1; got.SubjectClaim != "oid" || got.UpstreamClientSecretFile != "/secrets/as1" {
		t.Fatalf("IdentityBrokerHostedAS1 = %+v, want the configured subject claim and files", got)
	}
	for _, c := range []struct {
		args        []string
		wantCA      string
		wantPrivate bool
	}{
		{args: []string{"--identity-broker-as1-upstream-ca-file=/ca/idp.pem"}, wantCA: "/ca/idp.pem"},
		{args: []string{"--identity-broker-as1-upstream-ca-file=/ca/idp.pem", "--identity-broker-as1-upstream-allow-private-addresses"}, wantCA: "/ca/idp.pem", wantPrivate: true},
	} {
		cfg, err = parseFlags(with(c.args...))
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", c.args, err)
		}
		if got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1; got.UpstreamCAFile != c.wantCA || got.UpstreamAllowPrivateAddresses != c.wantPrivate {
			t.Fatalf("%v: upstream CA %q, private addresses %v; want %q, %v", c.args, got.UpstreamCAFile, got.UpstreamAllowPrivateAddresses, c.wantCA, c.wantPrivate)
		}
	}
}

// TestHostedAS1EnablesExecutionWithoutOIDCIssuer pins that the caller identity
// a hosted AS-1 derives satisfies remote execution's ownership requirement, as
// --identity-broker-as1-issuer documents for an unset --oidc-issuer.
func TestHostedAS1EnablesExecutionWithoutOIDCIssuer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := parseFlags(slices.Concat(as1Args, []string{
		"--execution-enabled", "--execution-endpoint=127.0.0.1:1", "--execution-template-id=go",
		"--execution-template-revision=v1-" + strings.Repeat("a", 64),
		"--execution-tls-ca=unused", "--execution-tls-cert=unused", "--execution-tls-key=unused",
		"--execution-allowed-principal=https://idp.example|alice",
	}))
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.oidc.Issuer != "https://mecatl.example/as1" {
		t.Fatalf("oidc issuer = %q, want AS-1's", cfg.oidc.Issuer)
	}
	built := appConfig(cfg, nil, observability{})
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	if !built.ExecutionOwnerAllowed(alice) || !built.ExecutionTemplateAllowed(alice, "go", cfg.executionTemplateRevision) {
		t.Fatal("allowlisted principal refused")
	}
}

// TestNewAuthenticatorFetchesThroughTheHostedAS1 pins that the validator
// fetches through the passed hosted AS-1's in-process client, and through no
// AS-1 client when none is passed. TestHostedAS1SignsTheTUIIn proves the
// in-process fetch end to end.
func TestNewAuthenticatorFetchesThroughTheHostedAS1(t *testing.T) {
	cfg, err := parseFlags(as1Args)
	if err != nil {
		t.Fatal(err)
	}
	errStop := errors.New("validator construction stopped")
	var got *http.Client
	cfg.oidc.NewValidator = func(_ context.Context, c cliconfig.OIDCConfig) (server.PrincipalValidator, error) {
		got = c.HTTPClient
		return nil, errStop
	}
	hosted := &app.IdentityBrokerAS1Server{Issuer: cfg.oidc.Issuer, HTTPClient: &http.Client{}}
	if _, err := newAuthenticator(t.Context(), cfg, hosted); !errors.Is(err, errStop) || got != hosted.HTTPClient {
		t.Fatalf("newAuthenticator with the hosted AS-1: err %v, client %p; want the AS-1's client %p", err, got, hosted.HTTPClient)
	}
	got = &http.Client{}
	if _, err := newAuthenticator(t.Context(), cfg, nil); !errors.Is(err, errStop) || got != nil {
		t.Fatalf("newAuthenticator without a hosted AS-1: err %v, client %p; want none", err, got)
	}
}

// TestIdentityBrokerRefusesALocalWorkspace pins that the broker, whose keys
// are files in this process's filesystem, never starts beside a workspace
// sessions' file tools could read them from.
func TestIdentityBrokerRefusesALocalWorkspace(t *testing.T) {
	broker := slices.Concat([]string{"--mock"}, brokerArgs)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{name: "broker with a workspace", args: slices.Concat(broker, []string{"--workspace=/srv/work"}), want: "--enable-identity-broker cannot be used with --workspace"},
		{name: "broker without a workspace", args: broker},
		{name: "broker with the Redis filesystem", args: slices.Concat(broker, []string{"--redis-filesystem", "--redis-url=redis://redis:6379"})},
		{name: "workspace without the broker", args: []string{"--mock", "--workspace=/srv/work"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseFlags(c.args)
			if c.want == "" {
				if err != nil {
					t.Fatalf("parseFlags(%v): %v", c.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("parseFlags(%v) = %v, want error containing %q", c.args, err, c.want)
			}
			for _, alternative := range []string{"--redis-filesystem", "Kubernetes execution"} {
				if !strings.Contains(err.Error(), alternative) {
					t.Errorf("parseFlags(%v) = %v, want it to name %s", c.args, err, alternative)
				}
			}
		})
	}
}

func TestIdentityBrokerFlagMapsToConfig(t *testing.T) {
	const file = "/etc/mecatl/identity-broker.json"
	for _, c := range []struct {
		args     []string
		want     bool
		wantFile string
	}{
		{[]string{"--mock"}, false, ""},
		{[]string{"--mock", "--enable-identity-broker", "--identity-broker-config=" + file}, true, file},
		{[]string{"--mock", "--enable-identity-broker=false"}, false, ""},
	} {
		cfg, err := parseFlags(c.args)
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", c.args, err)
		}
		if got := appConfig(cfg, nil, observability{}); got.IdentityBroker != c.want || got.IdentityBrokerConfigFile != c.wantFile || got.IdentityBrokerHostedAS1 != nil {
			t.Errorf("parseFlags(%v): IdentityBroker = %v, IdentityBrokerConfigFile = %q, IdentityBrokerHostedAS1 = %+v; want %v, %q, and none",
				c.args, got.IdentityBroker, got.IdentityBrokerConfigFile, got.IdentityBrokerHostedAS1, c.want, c.wantFile)
		}
	}
	for _, args := range [][]string{
		{"--mock", "--enable-identity-broker"},
		{"--mock", "--identity-broker-config=" + file},
		{"--mock", "--enable-identity-broker", "--identity-broker-config="},
	} {
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--enable-identity-broker and --identity-broker-config require each other") {
			t.Errorf("parseFlags(%v) = %v, want the two flags required together", args, err)
		}
	}
}
