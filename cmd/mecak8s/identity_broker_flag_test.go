package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// TestIdentityBrokerRequiresOwnerFromSubID pins fail-closed startup: with
// caller identity on, the broker must not start unless owners come from the
// AS-1 access token's sub_id.
func TestIdentityBrokerRequiresOwnerFromSubID(t *testing.T) {
	oidc := []string{"--mock", "--oidc-issuer=https://as1.example", "--oidc-audience=mecatl"}
	owner := []string{"--oidc-owner-from-sub-id", "--oidc-accepted-client-ids=mecatui,studio"}
	join := slices.Concat[[]string]
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{name: "broker with OIDC and owner from sub_id", args: join([]string{"--enable-identity-broker"}, oidc, owner)},
		{name: "broker without OIDC", args: []string{"--mock", "--enable-identity-broker"}},
		{name: "OIDC without broker", args: oidc},
		{name: "broker with OIDC but not owner from sub_id", args: join([]string{"--enable-identity-broker"}, oidc), want: "requires --oidc-owner-from-sub-id"},
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

// TestOwnerFromSubIDMisconfigurationRefusesStartup pins that a misconfigured
// owner-from-sub_id setting stops mecak8s before it serves anything.
func TestOwnerFromSubIDMisconfigurationRefusesStartup(t *testing.T) {
	oidc := []string{"--mock", "--oidc-issuer=https://as1.example.invalid", "--oidc-audience=mecatl"}
	for name, args := range map[string][]string{
		"without client IDs":            slices.Concat(oidc, []string{"--oidc-owner-from-sub-id"}),
		"client IDs without the option": slices.Concat(oidc, []string{"--oidc-accepted-client-ids=mecatui"}),
		"without issuer":                {"--mock", "--oidc-owner-from-sub-id", "--oidc-accepted-client-ids=mecatui"},
		"advertised client ID not accepted": slices.Concat(oidc, []string{
			"--oidc-owner-from-sub-id", "--oidc-accepted-client-ids=studio",
			"--oidc-resource=https://mecatl.example", "--oidc-client-id=mecatui",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseFlags(args)
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", args, err)
			}
			if auth, err := newAuthenticator(t.Context(), cfg); auth != nil || !errors.Is(err, cliconfig.ErrOIDCMisconfigured) {
				t.Fatalf("newAuthenticator(%v) = (%v, %v), want ErrOIDCMisconfigured", args, auth, err)
			}
		})
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
		if got := appConfig(cfg, nil, observability{}).IdentityBroker; got != c.want {
			t.Errorf("parseFlags(%v): IdentityBroker = %v, want %v", c.args, got, c.want)
		}
	}
}
