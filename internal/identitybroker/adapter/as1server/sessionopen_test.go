package as1server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// countingTransport counts the requests it forwards to next.
type countingTransport struct {
	next http.RoundTripper
	n    atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

// TestSessionOpenWithATokenForAnotherAudience drives the broker's session
// open against AS-1 with access tokens for otherAudience, which only this
// package's tests can have AS-1 issue.
func TestSessionOpenWithATokenForAnotherAudience(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	keyFile := h.WriteSigningKey(t, t.TempDir())
	f, signer, _ := startDelegateAS1(t, h, func(c *Config, _ *DelegateClient) { c.SigningKeyFile = keyFile })
	transport := &countingTransport{next: f.server.InProcessClient().Transport}
	as1, err := authserverclient.New(authserverclient.Config{
		Issuer: f.server.Issuer(), TokenEndpoint: f.server.TokenEndpoint(), Assertions: signer,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("authserverclient.New: %v", err)
	}
	brokerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, nil, brokerKey, identitybroker.WithAuthServer(as1))
	if err != nil {
		t.Fatalf("identitybroker.New: %v", err)
	}
	workload := identitybroker.Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	alice := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice}
	// open opens host for Alice with token and returns whether the session it
	// leaves holds a refresh token, and the open's error.
	open := func(t *testing.T, host string, token identitybroker.Token) (bool, error) {
		t.Helper()
		ctx := context.Background()
		_, openErr := b.OpenSession(ctx, workload, identitybroker.OpenSessionRequest{
			HostSessionID: host, Incarnation: "inc_abcdefghijklmnopqrstuvwxya", Owner: alice, AccessToken: token,
		})
		s, ok, err := b.FindSession(ctx, workload, identitybroker.FindSessionRequest{RootHostSessionID: host})
		if err != nil || !ok {
			t.Fatalf("FindSession = %v, %v; want the paired session", ok, err)
		}
		return s.HoldsRefreshToken, openErr
	}
	wantError := func(t *testing.T, err error, code identitybroker.Code, reason identitybroker.Reason, token identitybroker.Token) {
		t.Helper()
		var e *identitybroker.Error
		if !errors.As(err, &e) || e.Code != code || e.Reason != reason {
			t.Fatalf("error = %v, want %s %q", err, code, reason)
		}
		if strings.Contains(err.Error(), token.Value()) {
			t.Fatal("the error discloses the access token")
		}
	}

	t.Run("as AS-1 issues it, without sub_id: the broker refuses it before AS-1", func(t *testing.T) {
		before := transport.n.Load()
		token := identitybroker.NewToken(f.signIn(t, otherAudience).AccessToken)
		holds, err := open(t, "hostS42", token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSubjectInvalid, token)
		if holds || transport.n.Load() != before {
			t.Fatalf("holds a refresh token %v after %d requests to AS-1; want none of either", holds, transport.n.Load()-before)
		}
	})
	t.Run("with Alice's sub_id: subject_token_audience is a configuration fault", func(t *testing.T) {
		before := transport.n.Load()
		token := withSubID(t, keyFile, f.signIn(t, otherAudience).AccessToken, alice)
		holds, err := open(t, "hostS43", token)
		wantError(t, err, identitybroker.CodeInternal, "", token)
		// The detail names AS-1's reason, so the refusal is AS-1's own.
		if !strings.Contains(err.Error(), "subject_token_audience") {
			t.Fatalf("error = %v, want AS-1's subject_token_audience", err)
		}
		if holds || transport.n.Load() != before+1 {
			t.Fatalf("holds a refresh token %v after %d requests to AS-1; want none and one", holds, transport.n.Load()-before)
		}
	})
}

// withSubID returns access, an access token AS-1 issued, with owner's sub_id
// added and signed again with AS-1's signing key in keyFile, so AS-1
// verifies it and judges it by its other claims.
func withSubID(t *testing.T, keyFile, access string, owner identitybroker.Owner) identitybroker.Token {
	t.Helper()
	return identitybroker.NewToken(identityharness.ResignAccessToken(t, keyFile, access, func(claims jwt.MapClaims) {
		if _, ok := claims["sub_id"]; ok {
			t.Fatal("AS-1 issued the token with a sub_id")
		}
		claims["sub_id"] = map[string]string{"format": "iss_sub", "iss": owner.Issuer, "sub": owner.Subject}
	}))
}
