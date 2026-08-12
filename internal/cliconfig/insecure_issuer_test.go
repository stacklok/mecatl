// Tests the opt-in private-issuer relaxation using the validator construction path.
// The relaxation is for controlled test IdPs and is off by default.

package cliconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jwksOnly serves just enough of an IdP to get past construction: the validator
// fetches the pinned JWKS during NewValidator, so the URL has to answer.
func jwksOnly(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

// --- AC3.3 -------------------------------------------------------------------

// TestCallerIdentityE2E_Scenario3_PrivateIssuerRefusedByDefault pins AC3.3: the
// flag is OFF by default, and with it off BOTH relaxations stay in force — an
// `http://` issuer is refused on the scheme, and an https issuer resolving to
// loopback is refused on the address. Turning it on is what accepts them.
//
// The two refusals come from checks at DIFFERENT layers (a Config-level scheme
// check, and an address check at dial time), which is why one case each is not
// redundant: a change that relaxed only one would leave the other subtest green.
func TestCallerIdentityE2E_Scenario3_PrivateIssuerRefusedByDefault(t *testing.T) {
	// The zero value must be the safe one — nothing else in this test matters if
	// a fresh OIDCConfig arrives with the relaxation already on.
	if (OIDCConfig{}).InsecureAllowPrivateIssuer {
		t.Fatal("InsecureAllowPrivateIssuer defaults to TRUE — the SSRF defences are off unless an operator opts back in")
	}

	build := func(t *testing.T, srv *httptest.Server, insecure bool) error {
		t.Helper()
		// No httpClient override: this asserts what the validator's OWN client
		// does, which is the only thing a real deployment has.
		_, err := OIDCValidator(context.Background(), OIDCConfig{
			Issuer:                     srv.URL,
			Audience:                   "mecatl",
			JWKSURI:                    srv.URL + "/",
			NewValidator:               defaultNewValidator,
			InsecureAllowPrivateIssuer: insecure,
		})
		return err
	}

	t.Run("off: an http:// issuer is refused on the scheme", func(t *testing.T) {
		if err := build(t, jwksOnly(t, false), false); err == nil {
			t.Fatal("a plaintext http:// issuer was accepted with the flag off")
		}
	})

	t.Run("off: an https issuer at a loopback address is refused", func(t *testing.T) {
		if err := build(t, jwksOnly(t, true), false); err == nil {
			t.Fatal("a loopback issuer was accepted with the flag off — the check that blocks a jwks_uri aimed at 169.254.169.254 is not in force")
		}
	})

	t.Run("on: the same http:// loopback issuer is accepted", func(t *testing.T) {
		// Proves the flag does BOTH relaxations. A plain-http loopback server needs
		// the scheme check AND the address check lifted; if the flag only moved one,
		// this stays red.
		if err := build(t, jwksOnly(t, false), true); err != nil {
			t.Fatalf("the flag did not relax both checks: %v", err)
		}
	})
}

// --- AC3.4 -------------------------------------------------------------------

// TestCallerIdentityE2E_Scenario3_InsecureIssuerFlagWarns pins AC3.4: enabling
// the flag produces an operator-facing warning that names it and says it is
// test-only, and NOT enabling it produces silence.
//
// The warning is the only signal an operator gets that they have disabled an SSRF
// defence — a flag that relaxes security quietly is how a test fixture becomes a
// production vulnerability.
func TestCallerIdentityE2E_Scenario3_InsecureIssuerFlagWarns(t *testing.T) {
	on := OIDCConfig{Issuer: "https://idp.example.com", Audience: "mecatl", InsecureAllowPrivateIssuer: true}

	w := on.InsecureIssuerWarning()
	if w == "" {
		t.Fatal("enabling the flag produced no warning: an operator gets no signal that an SSRF defence is off")
	}
	for _, want := range []string{"oidc-insecure-allow-private-issuer", "169.254.169.254", "NOT"} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning does not mention %q, so it does not tell the operator what was turned off or that it is test-only:\n%s", want, w)
		}
	}

	t.Run("silent when the flag is off", func(t *testing.T) {
		off := on
		off.InsecureAllowPrivateIssuer = false
		if got := off.InsecureIssuerWarning(); got != "" {
			t.Fatalf("warned with the flag off: %q", got)
		}
	})

	t.Run("silent when identity is off entirely", func(t *testing.T) {
		// The flag is meaningless without an issuer; warning then would train
		// operators to ignore the message.
		noIdentity := OIDCConfig{InsecureAllowPrivateIssuer: true}
		if got := noIdentity.InsecureIssuerWarning(); got != "" {
			t.Fatalf("warned with caller identity disabled: %q", got)
		}
	})
}
