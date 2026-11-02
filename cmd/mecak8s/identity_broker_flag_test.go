package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

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
	join := slices.Concat[[]string]
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{name: "broker with OIDC and owner identity sub_id", args: join([]string{"--enable-identity-broker"}, oidc, owner)},
		{name: "broker without OIDC", args: []string{"--mock", "--enable-identity-broker"}},
		{name: "OIDC without broker", args: oidc},
		{name: "broker with OIDC but no owner identity", args: join([]string{"--enable-identity-broker"}, oidc), want: "requires --oidc-owner-identity=sub_id"},
		{name: "broker with OIDC and empty owner identity", args: join([]string{"--enable-identity-broker", "--oidc-owner-identity="}, oidc), want: "requires --oidc-owner-identity=sub_id"},
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

// as1Args host AS-1 with every required companion flag.
var as1Args = []string{
	"--mock", "--enable-identity-broker",
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
			"--identity-broker-subject-claim=oid", "--identity-broker-as1-signing-key-file=/keys/as1.pem",
			"--identity-broker-as1-upstream-client-secret-file=/secrets/as1")},
		{name: "signing key without issuer", args: []string{"--mock", "--identity-broker-as1-signing-key-file=/keys/as1.pem"}, want: "require --identity-broker-as1-issuer"},
		{name: "TUI client without issuer", args: []string{"--mock", "--identity-broker-as1-tui-client-id=mecatui"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream issuer without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-issuer=https://idp.example"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream client without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-client-id=x"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream secret without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-client-secret-file=/s"}, want: "require --identity-broker-as1-issuer"},
		{name: "subject claim without issuer", args: []string{"--mock", "--identity-broker-subject-claim=oid"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream CA without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-ca-file=/ca.pem"}, want: "require --identity-broker-as1-issuer"},
		{name: "upstream private addresses without issuer", args: []string{"--mock", "--identity-broker-as1-upstream-allow-private-addresses"}, want: "require --identity-broker-as1-issuer"},
		{name: "without the broker", args: without("--enable-identity-broker"), want: "requires --enable-identity-broker"},
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
	if got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1; got.SubjectClaim != "sub" || got.SigningKeyFile != "" || got.UpstreamClientSecretFile != "" {
		t.Fatalf("IdentityBrokerHostedAS1 defaults = %+v, want the explicit subject claim sub and no files", got)
	}
	cfg, err = parseFlags(with("--identity-broker-subject-claim=oid", "--identity-broker-as1-signing-key-file=/keys/as1.pem", "--identity-broker-as1-upstream-client-secret-file=/secrets/as1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := appConfig(cfg, nil, observability{}).IdentityBrokerHostedAS1; got.SubjectClaim != "oid" || got.SigningKeyFile != "/keys/as1.pem" || got.UpstreamClientSecretFile != "/secrets/as1" {
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

func TestIdentityBrokerFlagMapsToConfig(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--mock"}, false},
		{[]string{"--mock", "--enable-identity-broker"}, true},
		{[]string{"--mock", "--enable-identity-broker=false"}, false},
	} {
		cfg, err := parseFlags(c.args)
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", c.args, err)
		}
		if got := appConfig(cfg, nil, observability{}); got.IdentityBroker != c.want || got.IdentityBrokerHostedAS1 != nil {
			t.Errorf("parseFlags(%v): IdentityBroker = %v, IdentityBrokerHostedAS1 = %+v; want %v and none", c.args, got.IdentityBroker, got.IdentityBrokerHostedAS1, c.want)
		}
	}
}
