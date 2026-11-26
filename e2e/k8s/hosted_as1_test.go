//go:build kind_e2e

// The hosted AS-1 journey signs mecatui's real login code in at the AS-1 a
// second mecak8s release hosts, in front of an HTTPS Dex at a private
// in-cluster address, and drives every request from the test process through
// port-forwards. Its assertions are facts observed in a live cluster.
//
// The release is also the deployable identity broker of #2815: the chart
// delivers its operator keys, settings, and embedded gateway entry, and the
// spec restarts it, rotates the broker client key and AS-1's signing key, and
// has it refuse missing and shared keys and a rotation in the wrong order.

package k8s_e2e_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/net/html"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// The second release lives in its own namespace so its agent pod, its Redis,
// and its NetworkPolicy-free Dex never match the suite's label selectors,
// redis-0, or Dex policies in the mecatl namespace.
const (
	as1Namespace  = "mecatl-as1"
	as1Release    = "mecak8s-as1"
	as1Host       = as1Release + "." + as1Namespace + ".svc.cluster.local"
	as1HTTPAddr   = as1Host + ":8081"
	as1GRPCTarget = as1Host + ":8080"
	as1Resource   = "https://" + as1HTTPAddr
	as1Issuer     = as1Resource + "/as1"
	as1Audience   = as1Resource + "/api"
	as1TUIClient  = "mecatui"

	as1DexName     = "dex-as1"
	as1DexHost     = as1DexName + "." + as1Namespace + ".svc.cluster.local"
	as1DexPort     = 5554
	as1DexAddr     = as1DexHost + ":5554"
	as1DexIssuer   = "https://" + as1DexAddr
	as1DexClientID = "mecak8s-as1"

	// The embedded gateway derives AS-2's issuer from the callback URL's
	// scheme and host.
	as1CallbackURL = as1Resource + "/v1/mcp/authorization/callback"
	as2Issuer      = as1Resource + "/v1/mcp/broker"
)

var _ = ginkgo.Describe("hosted AS-1 signs mecatui in through Dex", ginkgo.Serial, ginkgo.Ordered, func() {
	var (
		tunnels  *as1Tunnels
		client   *http.Client
		stops    []func()
		identity clientauth.Identity
		login    clientauth.LoginConfig
		signedIn clientauth.Token
		creds    *clientauth.Credentials
		registry *clientauth.Registry
		source   *clientauth.RefreshSource
		subID    as1SubjectID
		// signingKey is AS-1's signing key; the test generates it, as it does
		// the agent issuer, broker client, and AS-2 signing keys.
		signingKey      *ecdsa.PrivateKey
		agentIssuerKey  *ecdsa.PrivateKey
		brokerClientKey *ecdsa.PrivateKey
		as2SigningKey   *ecdsa.PrivateKey
		aliceSession    string
	)

	// signIn signs alice in at AS-1 through Dex and returns her access token.
	signIn := func(step string) string {
		ginkgo.GinkgoHelper()
		ctx, cancel := shortCtx(90 * time.Second)
		defer cancel()
		browser := newAS1Browser(client, aliceEmail, dexPass)
		tok, err := clientauth.Login(ctx, clientauth.LoginConfig{Identity: identity, Presenter: browser.presenter(), HTTPClient: client, IdentityBrokerSignIn: true})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: sign in at AS-1", step)
		return tok.AccessToken
	}

	// signInAndOpenSession signs alice in and opens a session, which converts
	// her sign-in at AS-1 with the broker client's assertion, so the open
	// fails if AS-1 refuses the broker client's key. It returns the access
	// token, which the open consumed.
	signInAndOpenSession := func(step string) string {
		ginkgo.GinkgoHelper()
		accessToken := signIn(step)
		ctx, cancel := shortCtx(30 * time.Second)
		defer cancel()
		status, sessionID := as1CreateSession(ctx, client, accessToken)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "%s: session open, which authenticates the broker client at AS-1", step)
		expectAS1Owner(ctx, client, accessToken, sessionID, subID)
		return accessToken
	}

	// upgradeAndRestart upgrades the release with overrides, waits for the
	// pod that replaces the running one, and forwards to it.
	upgradeAndRestart := func(ctx context.Context, overrides ...string) {
		ginkgo.GinkgoHelper()
		old := as1AgentPod()
		installAS1Release(ctx, overrides...)
		replacement := as1AgentPod()
		gomega.Expect(replacement).NotTo(gomega.Equal(old), "helm upgrade with %v did not restart %s", overrides, old)
		stops = append(stops, tunnels.forwardAS1())
	}

	ginkgo.BeforeAll(func() {
		ctx := ginkgoSuiteCtx()
		ginkgo.By("creating the " + as1Namespace + " namespace (PSS restricted)")
		nsYAML := runCmd(ctx, "kubectl", "create", "namespace", as1Namespace, "--dry-run=client", "-o", "yaml")
		kubectlApplyStdin(ctx, []byte(nsYAML))
		runCmd(ctx, "kubectl", "label", "namespace", as1Namespace,
			"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/warn=restricted", "--overwrite")

		ginkgo.By("generating the test CA, server certificates, identity broker keys, and Dex client secret")
		pki := newAS1PKI()
		dexCert, dexKey := pki.serverPair(as1DexName, as1DexName+"."+as1Namespace, as1DexName+"."+as1Namespace+".svc", as1DexHost)
		as1Cert, as1Key := pki.serverPair(as1Release, as1Release+"."+as1Namespace, as1Release+"."+as1Namespace+".svc", as1Host)
		var err error
		signingKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		agentIssuerKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		brokerClientKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		as2SigningKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientSecret := rand.Text()

		applySecretIn(ctx, as1Namespace, as1DexName, map[string][]byte{
			"config.yaml": []byte(as1DexConfig(clientSecret)), "tls.crt": dexCert, "tls.key": dexKey,
		})
		applySecretIn(ctx, as1Namespace, as1Release+"-tls", map[string][]byte{"tls.crt": as1Cert, "tls.key": as1Key})
		applySecretIn(ctx, as1Namespace, as1Release+"-signing", map[string][]byte{"signing-key.pem": ecKeyPEM(signingKey)})
		applySecretIn(ctx, as1Namespace, as1Release+"-agent-issuer", map[string][]byte{"key.pem": ecKeyPEM(agentIssuerKey)})
		applySecretIn(ctx, as1Namespace, as1Release+"-broker-client", map[string][]byte{"key.pem": ecKeyPEM(brokerClientKey), "jwks.json": brokerClientKeySet(brokerClientKey)})
		applySecretIn(ctx, as1Namespace, as1Release+"-upstream", map[string][]byte{"client-secret": []byte(clientSecret)})
		applySecretIn(ctx, as1Namespace, as1Release+"-upstream-ca", map[string][]byte{"ca.crt": pki.caPEM})
		applySecretIn(ctx, as1Namespace, as1Release+"-mcp-signing", map[string][]byte{"key.pem": ecKeyPEM(as2SigningKey)})
		// The embedded gateway's OAuth MCP server is never called, so its
		// client secret only has to exist.
		applySecretIn(ctx, as1Namespace, as1Release+"-mcp-oauth", map[string][]byte{"client-secret": []byte(rand.Text())})

		ginkgo.By("deploying Dex over HTTPS at " + as1DexIssuer)
		kubectlApplyStdin(ctx, []byte(as1DexManifests))
		runCmd(ctx, "kubectl", "rollout", "status", "deployment/"+as1DexName, "-n", as1Namespace, "--timeout=180s")

		ginkgo.By("installing the " + as1Release + " release with hosted AS-1 upstream of that Dex, the identity broker's settings, and an embedded gateway entry")
		installAS1Release(ctx)

		ginkgo.By("forwarding Dex and the AS-1 release to the test process")
		tunnels = &as1Tunnels{addrs: map[string]string{}}
		dexAddr, stopDex := portForwardResourceIn(as1Namespace, "deployment/"+as1DexName, as1DexPort)
		stops = append(stops, stopDex)
		tunnels.set(as1DexAddr, dexAddr)
		stops = append(stops, tunnels.forwardAS1())
		client = tunnels.client(pki.pool)

		// The refresh source's target lock lives here until the container ends.
		registry, err = clientauth.OpenRegistry(ginkgo.GinkgoT().TempDir())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		identity = clientauth.Identity{
			Target: as1GRPCTarget, Issuer: as1Issuer, ClientID: as1TUIClient, Audience: as1Audience,
			RedirectURI: oauthlogin.ExactRedirectURL, Scopes: []string{"openid", "offline_access"},
		}
	})

	ginkgo.AfterAll(func() {
		if source != nil {
			_ = source.Close()
		}
		if ginkgo.CurrentSpecReport().Failed() {
			ginkgo.GinkgoWriter.Printf("--- %s logs ---\n%s\n--- %s logs ---\n%s\n",
				as1Release, runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "deployment/"+as1Release, "--tail=200"),
				as1DexName, runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "deployment/"+as1DexName, "--tail=200"))
		}
		for _, stop := range stops {
			stop()
		}
		ginkgo.By("removing the " + as1Release + " release, its Dex, and the " + as1Namespace + " namespace")
		runCmdQuiet("helm", "uninstall", as1Release, "-n", as1Namespace, "--wait", "--timeout=2m", "--ignore-not-found")
		ctx, cancel := shortCtx(4 * time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "kubectl", "delete", "namespace", as1Namespace, "--ignore-not-found", "--wait", "--timeout=3m").CombinedOutput()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "delete namespace %s\n%s", as1Namespace, out)
	})

	ginkgo.It("names AS-1 as the only authorization server, for the TUI client, in the protected-resource metadata", func() {
		ctx, cancel := shortCtx(30 * time.Second)
		defer cancel()
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			Audience             string   `json:"com.stacklok.mecatl.audience"`
			ClientID             string   `json:"com.stacklok.mecatl.client_id"`
			IdentityBrokerSignIn bool     `json:"com.stacklok.mecatl.identity_broker_sign_in"`
		}
		status, raw := as1Do(ctx, client, http.MethodGet, as1Resource+"/.well-known/oauth-protected-resource", "", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), "protected-resource metadata: %s", formatBody(raw))
		gomega.Expect(json.Unmarshal(raw, &doc)).To(gomega.Succeed(), "decode %s", formatBody(raw))
		gomega.Expect(doc.AuthorizationServers).To(gomega.Equal([]string{as1Issuer}))
		gomega.Expect(doc.ClientID).To(gomega.Equal(as1TUIClient))
		gomega.Expect(doc.Audience).To(gomega.Equal(as1Audience))
		gomega.Expect(doc.Resource).To(gomega.Equal(as1Resource))
		gomega.Expect(doc.IdentityBrokerSignIn).To(gomega.BeTrue())
	})

	ginkgo.It("starts from the identity broker settings and embedded gateway entry the chart delivers, and AS-2 signs with the operator's key", func() {
		ctx, cancel := shortCtx(30 * time.Second)
		defer cancel()
		raw := runCmd(ctx, "kubectl", "get", "configmap", as1Release+"-identity-broker", "-n", as1Namespace,
			"-o", `jsonpath={.data.identity-broker\.json}`)
		var file struct {
			IDJAGMaxLifetime        string           `json:"id_jag_max_lifetime"`
			IDJAGLifetime           string           `json:"id_jag_lifetime"`
			CredentialLifetime      string           `json:"credential_lifetime"`
			ClientAssertionLifetime string           `json:"client_assertion_lifetime"`
			ClockSkew               string           `json:"clock_skew"`
			Gateways                []map[string]any `json:"gateways"`
		}
		gomega.Expect(json.Unmarshal([]byte(raw), &file)).To(gomega.Succeed(), "decode the identity broker configuration file: %s", raw)
		gomega.Expect([]string{file.IDJAGMaxLifetime, file.IDJAGLifetime, file.CredentialLifetime, file.ClientAssertionLifetime, file.ClockSkew}).
			To(gomega.Equal([]string{"8h", "2m", "30m", "45s", "10s"}))
		gomega.Expect(file.Gateways).To(gomega.Equal([]map[string]any{{
			"kind": "embedded", "as2_issuer": as2Issuer, "token_endpoint": as2Issuer + "/oauth/token",
			"resource": as2Issuer + "/mcp", "servers": []any{},
		}}))
		// The refusal spec below shows mecak8s loads the 10s clock skew the
		// chart delivered. AS-2 publishing the operator's key shows it took
		// the signing key, which it accepts only with the gateway entry.
		jwksKeyID(as1JWKS(ctx, client, as2Issuer+"/.well-known/jwks.json"), &as2SigningKey.PublicKey, "AS-2")
	})

	ginkgo.It("signs the TUI in at AS-1 through Dex's login and issues an at+jwt access token naming the Dex identity", func() {
		ctx, cancel := shortCtx(90 * time.Second)
		defer cancel()
		browser := newAS1Browser(client, aliceEmail, dexPass)
		login = clientauth.LoginConfig{Identity: identity, Presenter: browser.presenter(), HTTPClient: client, IdentityBrokerSignIn: true}
		var err error
		signedIn, err = clientauth.Login(ctx, login)
		ginkgo.GinkgoWriter.Printf("browser steps: %s\n", strings.Join(browser.steps, " -> "))
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "clientauth.Login through hosted AS-1")
		gomega.Expect(signedIn.RefreshToken).NotTo(gomega.BeEmpty(), "AS-1 issued no refresh token to the TUI")

		// AS-1 sends the browser to Dex asking for offline_access, the scope Dex
		// issues refresh tokens for, with prompt=consent. Dex itself does not log
		// the refresh token it issues. Observed with Dex v2.44: AS-1's consent
		// page comes first, then Dex's password form, and skipApprovalScreen
		// suppresses Dex's approval page even with prompt=consent; the browser
		// still approves one if a Dex version shows it.
		gomega.Expect(browser.upstreamAuthorize).NotTo(gomega.BeNil(), "the sign-in never reached Dex's authorization endpoint")
		upstream := browser.upstreamAuthorize.Query()
		gomega.Expect(upstream.Get("client_id")).To(gomega.Equal(as1DexClientID))
		gomega.Expect(upstream.Get("redirect_uri")).To(gomega.Equal(as1Issuer + "/oauth/callback"))
		gomega.Expect(strings.Fields(upstream.Get("scope"))).To(gomega.ContainElements("openid", "offline_access"))
		gomega.Expect(upstream.Get("prompt")).To(gomega.Equal("consent"))
		gomega.Expect(browser.steps).To(gomega.ContainElements("dex login", "as1 consent"))

		subID = expectAS1AccessToken(signedIn.AccessToken)
	})

	ginkgo.It("opens a session with the AS-1 access token, owned by the Dex identity in sub_id", func() {
		ctx, cancel := shortCtx(60 * time.Second)
		defer cancel()
		var status int
		status, aliceSession = as1CreateSession(ctx, client, signedIn.AccessToken)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "session create with the AS-1 access token")
		expectAS1Owner(ctx, client, signedIn.AccessToken, aliceSession, subID)
	})

	ginkgo.It("converts the sign-in at AS-1 when the session opens: a second session with the same access token is refused", func() {
		ctx, cancel := shortCtx(60 * time.Second)
		defer cancel()
		// AS-1 converts each access token once (its jti is single use), so the
		// refusal proves the first open exchanged the token at the real AS-1.
		expectAS1SessionOpenRefused(ctx, client, signedIn.AccessToken, "bearer_rejected")
		gomega.Expect(as1ListSessions(ctx, client, signedIn.AccessToken)).To(gomega.ContainElement(aliceSession),
			"the refused open must leave the first session in place")
	})

	ginkgo.It("keeps bob, signed in through AS-1 and Dex, out of alice's session", func() {
		ctx, cancel := shortCtx(90 * time.Second)
		defer cancel()
		browser := newAS1Browser(client, bobEmail, dexPass)
		bob, err := clientauth.Login(ctx, clientauth.LoginConfig{Identity: identity, Presenter: browser.presenter(), HTTPClient: client, IdentityBrokerSignIn: true})
		ginkgo.GinkgoWriter.Printf("bob's browser steps: %s\n", strings.Join(browser.steps, " -> "))
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "clientauth.Login as bob through hosted AS-1")
		bobSubID := expectAS1AccessToken(bob.AccessToken)
		gomega.Expect(bobSubID).NotTo(gomega.Equal(subID), "bob's token names alice's identity")

		status, bobSession := as1CreateSession(ctx, client, bob.AccessToken)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "session create as bob")
		expectAS1Owner(ctx, client, bob.AccessToken, bobSession, bobSubID)
		gomega.Expect(as1ListSessions(ctx, client, bob.AccessToken)).NotTo(gomega.ContainElement(aliceSession), "bob's list shows alice's session")

		status, raw := as1Do(ctx, client, http.MethodGet, as1Resource+"/v1/sessions/"+url.PathEscape(aliceSession), bob.AccessToken, nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusNotFound), "bob opening alice's session: %s", formatBody(raw))

		expectAS1Owner(ctx, client, signedIn.AccessToken, aliceSession, subID)
		gomega.Expect(as1ListSessions(ctx, client, signedIn.AccessToken)).NotTo(gomega.ContainElement(bobSession), "alice's list shows bob's session")
	})

	ginkgo.It("refuses AS-1-signed access tokens with the wrong audience or without sub_id, and accepts a correctly minted one", func() {
		ctx, cancel := shortCtx(60 * time.Second)
		defer cancel()
		kid := as1JWKSKeyID(ctx, client, &signingKey.PublicKey)
		// Re-sign the claims AS-1 itself issued, so only the changed claim differs.
		issued := jwt.MapClaims{}
		_, _, err := jwt.NewParser().ParseUnverified(signedIn.AccessToken, issued)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		mint := func(edit func(jwt.MapClaims)) string {
			claims := jwt.MapClaims{}
			for k, v := range issued {
				claims[k] = v
			}
			now := time.Now()
			claims["jti"], claims["iat"], claims["nbf"], claims["exp"] = rand.Text(), now.Unix(), now.Unix(), now.Add(5*time.Minute).Unix()
			edit(claims)
			tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
			tok.Header["typ"], tok.Header["kid"] = "at+jwt", kid
			signed, err := tok.SignedString(signingKey)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return signed
		}
		for _, c := range []struct {
			name string
			edit func(jwt.MapClaims)
			want int
		}{
			{"correct claims (positive control)", func(c jwt.MapClaims) {
				aud, err := c.GetAudience()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect([]string(aud)).To(gomega.ContainElement(as1Audience))
				gomega.Expect(c).To(gomega.HaveKey("sub_id"))
				gomega.Expect(c["client_id"]).To(gomega.Equal(as1TUIClient))
			}, http.StatusOK},
			{"wrong audience", func(c jwt.MapClaims) { c["aud"] = []string{"https://other.example/api"} }, http.StatusUnauthorized},
			{"no sub_id", func(c jwt.MapClaims) { delete(c, "sub_id") }, http.StatusUnauthorized},
		} {
			status, raw := as1Do(ctx, client, http.MethodGet, as1Resource+"/v1/sessions", mint(c.edit), nil)
			gomega.Expect(status).To(gomega.Equal(c.want), "%s: GET /v1/sessions: %s", c.name, formatBody(raw))
		}
	})

	ginkgo.It("refreshes the sign-in at AS-1 and opens a session with the refreshed token", func() {
		ctx, cancel := shortCtx(60 * time.Second)
		defer cancel()
		store, err := credentialstore.NewMemoryBackend().Open("hosted-as1-kind")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		creds, err = clientauth.NewCredentials(store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = creds.Save(ctx, identity, signedIn, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refreshing := login
		refreshing.Presenter, refreshing.Registry = nil, registry
		// The source's token validator lives as long as the context it is built
		// with, so it outlives this spec on the suite context.
		source, err = clientauth.NewRefreshSource(ginkgoSuiteCtx(), creds, refreshing)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "NewRefreshSource at hosted AS-1")

		refreshed, err := source.Refresh(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "refresh at hosted AS-1")
		gomega.Expect(refreshed).NotTo(gomega.Equal(signedIn.AccessToken))
		gomega.Expect(expectAS1AccessToken(refreshed)).To(gomega.Equal(subID), "the refreshed token names another identity")
		rec, err := creds.Load(ctx, identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rec.Token.RefreshToken).NotTo(gomega.Equal(signedIn.RefreshToken), "AS-1 did not rotate the refresh token")

		status, sessionID := as1CreateSession(ctx, client, refreshed)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "session create with the refreshed AS-1 access token")
		expectAS1Owner(ctx, client, refreshed, sessionID, subID)
	})

	ginkgo.It("keeps AS-1's and AS-2's keys across a pod restart, accepts a pre-restart access token after it, and refuses the pre-restart refresh token (in-memory AS-1 state until #2563)", func() {
		ctx := ginkgoSuiteCtx()
		reqCtx, cancel := shortCtx(30 * time.Second)
		beforeRestart, err := source.Refresh(reqCtx)
		as1Keys := as1JWKS(reqCtx, client, as1Issuer+"/.well-known/jwks.json")
		as2Keys := as1JWKS(reqCtx, client, as2Issuer+"/.well-known/jwks.json")
		cancel()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "refresh before the restart")
		rec, err := creds.Load(ctx, identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refreshBeforeRestart := rec.Token.RefreshToken
		gomega.Expect(refreshBeforeRestart).NotTo(gomega.BeEmpty())

		old := as1AgentPod()
		ginkgo.By("deleting the AS-1 pod " + old)
		runCmd(ctx, "kubectl", "delete", "pod", old, "-n", as1Namespace, "--wait=false")
		var replacement string
		gomega.Eventually(func(g gomega.Gomega) {
			replacement = as1AgentPod()
			g.Expect(replacement).NotTo(gomega.Equal(old))
		}, 4*time.Minute, 2*time.Second).Should(gomega.Succeed(), "no Ready replacement for %s", old)
		ginkgo.GinkgoWriter.Printf("AS-1 pod %s replaced by %s\n", old, replacement)
		stops = append(stops, tunnels.forwardAS1())

		reqCtx, cancel = shortCtx(60 * time.Second)
		defer cancel()
		gomega.Expect(as1JWKS(reqCtx, client, as1Issuer+"/.well-known/jwks.json")).To(gomega.ConsistOf(as1Keys), "AS-1's key set changed across the restart")
		gomega.Expect(as1JWKS(reqCtx, client, as2Issuer+"/.well-known/jwks.json")).To(gomega.ConsistOf(as2Keys), "AS-2's key set changed across the restart")
		status, sessionID := as1CreateSession(reqCtx, client, beforeRestart)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated),
			"an access token AS-1 issued before the restart was refused after it; the signing key comes from the Secret")
		expectAS1Owner(reqCtx, client, beforeRestart, sessionID, subID)
		// The restart dropped AS-1's state, so that open converted at the new
		// AS-1, with the broker client key from its Secret; a second open with
		// the same token proves it did.
		expectAS1SessionOpenRefused(reqCtx, client, beforeRestart, "bearer_rejected")

		status, raw := as1Do(reqCtx, client, http.MethodPost, as1Issuer+"/oauth/token", "", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refreshBeforeRestart},
			"client_id": {as1TUIClient}, "resource": {as1Audience},
		})
		ginkgo.GinkgoWriter.Printf("pre-restart refresh after restart: %d %s\n", status, formatBody(raw))
		var refusal struct {
			Error string `json:"error"`
		}
		// #2563 persists AS-1's state, which flips this invalid_grant assertion.
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), "pre-restart refresh token: %s", formatBody(raw))
		gomega.Expect(json.Unmarshal(raw, &refusal)).To(gomega.Succeed(), "decode %s", formatBody(raw))
		gomega.Expect(refusal.Error).To(gomega.Equal("invalid_grant"))
	})

	ginkgo.It("refuses a release without a required key, refuses to start with one key for two roles, a key set without the broker client key, or a setting out of bounds, and starts again once fixed", func() {
		ctx := ginkgoSuiteCtx()
		ginkgo.By("rendering the chart without AS-1's signing key or the embedded gateway's signing key")
		for want, overrides := range map[string][]string{
			"identityBroker.keys.as1Signing is required when identityBroker.as1.issuer is set": {
				"identityBroker.keys.as1Signing.secretName=", "identityBroker.keys.as1Signing.key=",
			},
			"identityBroker.gateways requires mcp.broker.signingKey": {
				"mcp.broker.signingKey.secretName=", "mcp.broker.signingKey.key=",
			},
		} {
			out, err := boundedCommandOutput(ctx, 1<<20, "helm", append([]string{"template"}, as1ReleaseArgs(overrides...)...)...)
			gomega.Expect(err).To(gomega.HaveOccurred(), "helm template without a required key rendered:\n%.2000s", out)
			gomega.Expect(string(out)).To(gomega.ContainSubstring(want))
		}

		// The chart refuses one Secret key for two roles; the same key in
		// two Secrets reaches mecak8s, which compares the keys themselves.
		other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		applySecretIn(ctx, as1Namespace, as1Release+"-agent-issuer-copy", map[string][]byte{"key.pem": ecKeyPEM(agentIssuerKey)})
		applySecretIn(ctx, as1Namespace, as1Release+"-other-key-set", map[string][]byte{"jwks.json": brokerClientKeySet(other)})
		for _, c := range []struct {
			name, want string
			overrides  []string
		}{
			{
				name: "the agent issuer key, copied to another Secret, as AS-1's signing key",
				want: "the agent issuer key is also the AS-1 signing key; each role needs its own key",
				overrides: []string{
					"identityBroker.keys.as1Signing.secretName=" + as1Release + "-agent-issuer-copy", "identityBroker.keys.as1Signing.key=key.pem",
				},
			},
			{
				name: "the agent issuer key, copied to another Secret, as the embedded gateway's signing key",
				want: "is also one of the identity broker's keys; each role needs its own key",
				overrides: []string{
					"mcp.broker.signingKey.secretName=" + as1Release + "-agent-issuer-copy", "mcp.broker.signingKey.key=key.pem",
				},
			},
			{
				name: "a broker client key set holding another key",
				want: "does not hold the broker client key's public key",
				overrides: []string{
					"identityBroker.keys.brokerClient.keySet.secretName=" + as1Release + "-other-key-set", "identityBroker.keys.brokerClient.keySet.key=jwks.json",
				},
			},
			{
				// The message names the 10s skew the chart delivered, not
				// mecak8s's 30s default.
				name:      "an ID-JAG maximum lifetime under ten clock skews",
				want:      "the clock skew 10s must be at most a tenth of the ID-JAG grant maximum lifetime 1m30s",
				overrides: []string{"identityBroker.settings.idJAGMaxLifetime=90s"},
			},
		} {
			ginkgo.By("upgrading to " + c.name)
			upgradeAS1ReleaseNoWait(ctx, c.overrides...)
			expectAS1StartupRefused(c.want)
		}

		ginkgo.By("restoring the release")
		installAS1Release(ctx)
		as1AgentPod()
		stops = append(stops, tunnels.forwardAS1())
		signInAndOpenSession("restored release")
	})

	ginkgo.It("rotates the broker client key with helm upgrade: each step of the operator guide starts and serves sign-in, and the wrong order is refused at startup", func() {
		ctx := ginkgoSuiteCtx()
		next, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// The operator guide's steps, each pointing the chart at a new Secret
		// key, which rolls the pod. The Secret holds every step's key up front;
		// the chart projects only the keys it names.
		applySecretIn(ctx, as1Namespace, as1Release+"-broker-client", map[string][]byte{
			"key.pem": ecKeyPEM(brokerClientKey), "jwks.json": brokerClientKeySet(brokerClientKey),
			"jwks-both.json": mergeKeySets(brokerClientKeySet(brokerClientKey), brokerClientKeySet(next)),
			"key-next.pem":   ecKeyPEM(next), "jwks-next.json": brokerClientKeySet(next),
		})

		ginkgo.By("signing with the next key before the key set holds it, the wrong order")
		upgradeAS1ReleaseNoWait(ctx, "identityBroker.keys.brokerClient.key=key-next.pem")
		expectAS1StartupRefused("does not hold the broker client key's public key")

		for i, step := range []struct {
			name      string
			overrides []string
		}{
			{"1: the old and next keys in the key set, signing with the old key", []string{
				"identityBroker.keys.brokerClient.keySet.key=jwks-both.json",
			}},
			{"2: signing with the next key", []string{
				"identityBroker.keys.brokerClient.keySet.key=jwks-both.json", "identityBroker.keys.brokerClient.key=key-next.pem",
			}},
			{"3: only the next key in the key set", []string{
				"identityBroker.keys.brokerClient.keySet.key=jwks-next.json", "identityBroker.keys.brokerClient.key=key-next.pem",
			}},
		} {
			ginkgo.By("rotation step " + step.name)
			if i == 0 {
				// The refused release left no Ready pod to replace.
				installAS1Release(ctx, step.overrides...)
				as1AgentPod()
				stops = append(stops, tunnels.forwardAS1())
			} else {
				upgradeAndRestart(ctx, step.overrides...)
			}
			signInAndOpenSession("rotation step " + step.name)
		}
	})

	// Last, so no later spec expects the baseline AS-1 signing key. The next
	// key joins the release's own Secret, which the namespace deletion removes.
	ginkgo.It("rotates AS-1's signing key with a fallback: tokens signed by the old key open sessions while it is a fallback and are refused once it is removed", func() {
		ctx := ginkgoSuiteCtx()
		next, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		applySecretIn(ctx, as1Namespace, as1Release+"-signing", map[string][]byte{
			"signing-key.pem": ecKeyPEM(signingKey), "signing-key-next.pem": ecKeyPEM(next),
		})
		reqCtx, cancel := shortCtx(30 * time.Second)
		oldKid := jwksKeyID(as1JWKS(reqCtx, client, as1Issuer+"/.well-known/jwks.json"), &signingKey.PublicKey, "AS-1")
		cancel()
		beforeRotation := signIn("before the rotation")
		gomega.Expect(as1TokenKeyID(beforeRotation)).To(gomega.Equal(oldKid))

		ginkgo.By("signing with the next key, the old key a fallback")
		upgradeAndRestart(ctx, "identityBroker.keys.as1Signing.key=signing-key-next.pem",
			"identityBroker.keys.as1Signing.fallbacks[0].secretName="+as1Release+"-signing",
			"identityBroker.keys.as1Signing.fallbacks[0].key=signing-key.pem")
		reqCtx, cancel = shortCtx(30 * time.Second)
		keys := as1JWKS(reqCtx, client, as1Issuer+"/.well-known/jwks.json")
		gomega.Expect(keys).To(gomega.HaveLen(2), "AS-1 publishes %v", keys)
		gomega.Expect(jwksKeyID(keys, &signingKey.PublicKey, "AS-1")).To(gomega.Equal(oldKid))
		newKid := jwksKeyID(keys, &next.PublicKey, "AS-1")
		status, sessionID := as1CreateSession(reqCtx, client, beforeRotation)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "session open with a token the fallback key signed")
		expectAS1Owner(reqCtx, client, beforeRotation, sessionID, subID)
		cancel()
		gomega.Expect(as1TokenKeyID(signInAndOpenSession("signing with the next key"))).To(gomega.Equal(newKid))

		ginkgo.By("removing the fallback")
		upgradeAndRestart(ctx, "identityBroker.keys.as1Signing.key=signing-key-next.pem")
		reqCtx, cancel = shortCtx(30 * time.Second)
		defer cancel()
		keys = as1JWKS(reqCtx, client, as1Issuer+"/.well-known/jwks.json")
		gomega.Expect(keys).To(gomega.HaveLen(1), "AS-1 publishes %v", keys)
		gomega.Expect(jwksKeyID(keys, &next.PublicKey, "AS-1")).To(gomega.Equal(newKid))
		// Listing converts nothing at AS-1, so its refusal is mecak8s's
		// validation against AS-1's key set.
		status, raw := as1Do(reqCtx, client, http.MethodGet, as1Resource+"/v1/sessions", beforeRotation, nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusUnauthorized), "list sessions with a token the removed key signed: %s", formatBody(raw))
		expectAS1SessionOpenRefused(reqCtx, client, beforeRotation, "bearer_rejected")
		gomega.Expect(as1TokenKeyID(signInAndOpenSession("after removing the fallback"))).To(gomega.Equal(newKid))
	})
})

// as1TokenKeyID returns the kid in an AS-1 token's header.
func as1TokenKeyID(raw string) string {
	ginkgo.GinkgoHelper()
	tok, _, err := jwt.NewParser().ParseUnverified(raw, jwt.MapClaims{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "AS-1 token is not a JWT")
	kid, _ := tok.Header["kid"].(string)
	return kid
}

// installAS1Release installs or upgrades the second, single-replica mecak8s
// release with TLS, the identity broker, hosted AS-1 upstream of the HTTPS
// Dex, and an embedded gateway entry, then the --set-string overrides, and
// waits for it.
func installAS1Release(ctx context.Context, overrides ...string) {
	ginkgo.GinkgoHelper()
	args := append(append([]string{"upgrade", "--install"}, as1ReleaseArgs(overrides...)...), "--wait", "--timeout=4m")
	out, err := boundedCommandOutput(ctx, 1<<20, "helm", args...)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("--- %s logs ---\n%s\n", as1Release,
			runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "deployment/"+as1Release, "--tail=100"))
	}
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "helm install %s\n%s", as1Release, out)
}

// upgradeAS1ReleaseNoWait upgrades the release with overrides without waiting
// for its pod, which the Recreate strategy replaces.
func upgradeAS1ReleaseNoWait(ctx context.Context, overrides ...string) {
	ginkgo.GinkgoHelper()
	runCmd(ctx, "helm", append([]string{"upgrade"}, as1ReleaseArgs(overrides...)...)...)
}

// expectAS1StartupRefused waits until a release pod's mecak8s logs want, its
// startup refusal, and asserts that no release pod is Ready.
func expectAS1StartupRefused(want string) {
	ginkgo.GinkgoHelper()
	selector := "app.kubernetes.io/instance=" + as1Release + ",app.kubernetes.io/component=" + agentComponent
	gomega.Eventually(func(g gomega.Gomega) {
		// A crash-looping container's refusal is in the current or the
		// previous container's log.
		logs := runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "-l", selector, "--tail=50") +
			runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "-l", selector, "--tail=50", "--previous")
		g.Expect(logs).To(gomega.ContainSubstring(want))
	}, 3*time.Minute, 2*time.Second).Should(gomega.Succeed(), "mecak8s did not refuse to start with %q", want)
	ready := runCmdQuiet("kubectl", "get", "pods", "-n", as1Namespace, "-l", selector,
		"-o", "jsonpath={.items[?(@.status.containerStatuses[0].ready==true)].metadata.name}")
	gomega.Expect(strings.Fields(ready)).To(gomega.BeEmpty(), "a pod is Ready although mecak8s refused to start")
}

// as1ReleaseArgs are the release's helm arguments after the subcommand, with
// overrides as the last --set-string values.
func as1ReleaseArgs(overrides ...string) []string {
	ginkgo.GinkgoHelper()
	chartDir := filepath.Join(repoRoot(), "deploy", "helm", "mecak8s")
	values := filepath.Join(ginkgo.GinkgoT().TempDir(), "as1-values.yaml")
	gomega.Expect(os.WriteFile(values, []byte(as1ReleaseValues()), 0o600)).To(gomega.Succeed())
	args := []string{as1Release, chartDir, "--namespace", as1Namespace,
		"--values", filepath.Join(chartDir, "values-kind.yaml"), "--values", values}
	for _, value := range append([]string{
		"image.repository=ko.local/mecak8s", "image.tag=e2e", "fullnameOverride=" + as1Release,
		"extraArgs[0]=--no-shell",
		"tls.secretName=" + as1Release + "-tls",
		"oidc.ownerIdentity=sub_id", "oidc.audience=" + as1Audience, "oidc.resource=" + as1Resource,
		// The identity broker refuses values-kind.yaml's workspace beside its
		// key files.
		"workspace=",
		"identityBroker.trustDomain=agents.mecatl-e2e.test", "identityBroker.audience=" + as1Resource + "/identity-broker",
		"identityBroker.keys.agentIssuer.secretName=" + as1Release + "-agent-issuer", "identityBroker.keys.agentIssuer.key=key.pem",
		"identityBroker.keys.brokerClient.secretName=" + as1Release + "-broker-client", "identityBroker.keys.brokerClient.key=key.pem",
		"identityBroker.keys.brokerClient.keySet.secretName=" + as1Release + "-broker-client", "identityBroker.keys.brokerClient.keySet.key=jwks.json",
		"identityBroker.keys.as1Signing.secretName=" + as1Release + "-signing", "identityBroker.keys.as1Signing.key=signing-key.pem",
		"identityBroker.subjectClaim=sub",
		"identityBroker.as1.issuer=" + as1Issuer, "identityBroker.as1.tuiClientID=" + as1TUIClient,
		"identityBroker.as1.upstream.issuer=" + as1DexIssuer, "identityBroker.as1.upstream.clientID=" + as1DexClientID,
		"identityBroker.as1.upstream.clientSecret.secretName=" + as1Release + "-upstream", "identityBroker.as1.upstream.clientSecret.key=client-secret",
		"identityBroker.as1.upstream.caSecret.secretName=" + as1Release + "-upstream-ca", "identityBroker.as1.upstream.caSecret.key=ca.crt",
	}, overrides...) {
		args = append(args, "--set-string", value)
	}
	return append(args, "--set", "replicaCount=1,tls.enabled=true,oidc.enabled=true,identityBroker.enabled=true,identityBroker.as1.upstream.allowPrivateAddresses=true")
}

// as1ReleaseValues are the identity broker's settings and its embedded
// gateway entry, with the callback URL, signing key, and OAuth MCP server the
// embedded gateway needs. mecak8s starts that server's OAuth2 upstream
// without contacting it, and no session enrolls, so its host need not exist.
func as1ReleaseValues() string {
	return fmt.Sprintf(`identityBroker:
  settings:
    idJAGMaxLifetime: 8h
    idJAGLifetime: 2m
    credentialLifetime: 30m
    clientAssertionLifetime: 45s
    clockSkew: 10s
  gateways:
    - kind: embedded
      as2Issuer: %[1]s
      tokenEndpoint: %[1]s/oauth/token
      resource: %[1]s/mcp
      servers: []
mcp:
  broker:
    callbackURL: %[2]s
    signingKey:
      secretName: %[3]s-mcp-signing
      key: key.pem
  servers:
    - name: calendar
      url: https://calendar.mecatl-e2e.test/mcp
      auth:
        mode: oauth
        oauth:
          upstream:
            mode: oauth2
            oauth2:
              authorizationEndpoint: https://calendar.mecatl-e2e.test/authorize
              tokenEndpoint: https://calendar.mecatl-e2e.test/token
          client:
            mode: preregistered
            preregistered:
              id: mecatl-e2e
              secretKeyRef: {name: %[3]s-mcp-oauth, key: client-secret}
          scopes: [calendar.read]
          network: {additionalOrigins: [], privateOrigins: [], maxRedirects: 0}
`, as2Issuer, as1CallbackURL, as1Release)
}

// brokerClientKeySet is key's public JWK Set, named by its RFC 7638
// thumbprint as the broker's own signer names it.
func brokerClientKeySet(key *ecdsa.PrivateKey) []byte {
	ginkgo.GinkgoHelper()
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	set, err := json.Marshal(signer.JWKS())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return set
}

// mergeKeySets is one JWK Set holding every key of sets.
func mergeKeySets(sets ...[]byte) []byte {
	ginkgo.GinkgoHelper()
	var keys []json.RawMessage
	for _, raw := range sets {
		var set struct {
			Keys []json.RawMessage `json:"keys"`
		}
		gomega.Expect(json.Unmarshal(raw, &set)).To(gomega.Succeed())
		keys = append(keys, set.Keys...)
	}
	merged, err := json.Marshal(map[string]any{"keys": keys})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return merged
}

// as1AgentPod returns the release's one Ready agent pod.
func as1AgentPod() string {
	ginkgo.GinkgoHelper()
	var names []string
	gomega.Eventually(func(g gomega.Gomega) {
		out := runCmdQuiet("kubectl", "get", "pods", "-n", as1Namespace,
			"-l", "app.kubernetes.io/instance="+as1Release+",app.kubernetes.io/component="+agentComponent,
			"--field-selector=status.phase==Running",
			"-o", "jsonpath={.items[?(@.status.containerStatuses[0].ready==true)].metadata.name}")
		names = strings.Fields(out)
		g.Expect(names).To(gomega.HaveLen(1))
	}, 4*time.Minute, 2*time.Second).Should(gomega.Succeed(), "no single Ready %s pod", as1Release)
	return names[0]
}

// as1SubjectID is an RFC 9493 iss_sub subject identifier.
type as1SubjectID struct {
	Format string `json:"format"`
	Iss    string `json:"iss"`
	Sub    string `json:"sub"`
}

// expectAS1AccessToken checks an AS-1 access token's RFC 9068 shape and
// returns its sub_id, which must name the Dex identity.
func expectAS1AccessToken(raw string) as1SubjectID {
	ginkgo.GinkgoHelper()
	claims := jwt.MapClaims{}
	tok, _, err := jwt.NewParser().ParseUnverified(raw, claims)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "AS-1 access token is not a JWT")
	gomega.Expect(tok.Header["typ"]).To(gomega.Equal("at+jwt"))
	gomega.Expect(claims["iss"]).To(gomega.Equal(as1Issuer))
	gomega.Expect(claims["client_id"]).To(gomega.Equal(as1TUIClient))
	aud, err := claims.GetAudience()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect([]string(aud)).To(gomega.ContainElement(as1Audience))
	encoded, err := json.Marshal(claims["sub_id"])
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var subID as1SubjectID
	gomega.Expect(json.Unmarshal(encoded, &subID)).To(gomega.Succeed(), "sub_id = %s", encoded)
	gomega.Expect(subID.Format).To(gomega.Equal("iss_sub"))
	gomega.Expect(subID.Iss).To(gomega.Equal(as1DexIssuer))
	gomega.Expect(subID.Sub).NotTo(gomega.BeEmpty())
	return subID
}

// expectAS1SessionOpenRefused asserts that opening a session with bearer is
// refused with 401 and the problem code want.
func expectAS1SessionOpenRefused(ctx context.Context, client *http.Client, bearer, want string) {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodPost, as1Resource+"/v1/sessions", bearer, defaultSessionCreateBody())
	gomega.Expect(status).To(gomega.Equal(http.StatusUnauthorized), "POST /v1/sessions: %s", formatBody(raw))
	var problem struct {
		Code string `json:"code"`
	}
	gomega.Expect(json.Unmarshal(raw, &problem)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	gomega.Expect(problem.Code).To(gomega.Equal(want), "POST /v1/sessions: %s", formatBody(raw))
}

// as1CreateSession creates a session over HTTPS with bearer.
func as1CreateSession(ctx context.Context, client *http.Client, bearer string) (int, string) {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodPost, as1Resource+"/v1/sessions", bearer, defaultSessionCreateBody())
	if status != http.StatusCreated {
		ginkgo.GinkgoWriter.Printf("POST /v1/sessions: %d %s\n", status, formatBody(raw))
		return status, ""
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	gomega.Expect(json.Unmarshal(raw, &out)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	return status, out.SessionID
}

// expectAS1Owner checks the listed owner of sessionID is the Dex identity.
func expectAS1Owner(ctx context.Context, client *http.Client, bearer, sessionID string, want as1SubjectID) {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodGet, as1Resource+"/v1/sessions", bearer, nil)
	gomega.Expect(status).To(gomega.Equal(http.StatusOK), "list sessions: %s", formatBody(raw))
	type owner struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	}
	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Owner     *owner `json:"owner"`
		} `json:"sessions"`
	}
	gomega.Expect(json.Unmarshal(raw, &out)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	var got *owner
	listed := false
	for _, s := range out.Sessions {
		if s.SessionID == sessionID {
			got, listed = s.Owner, true
		}
	}
	gomega.Expect(listed).To(gomega.BeTrue(), "session %s not listed: %s", sessionID, formatBody(raw))
	gomega.Expect(got).NotTo(gomega.BeNil(), "session %s has no owner", sessionID)
	gomega.Expect(got.Issuer).To(gomega.Equal(want.Iss))
	gomega.Expect(got.Subject).To(gomega.Equal(want.Sub))
}

// as1ListSessions returns the session IDs bearer's session list shows.
func as1ListSessions(ctx context.Context, client *http.Client, bearer string) []string {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodGet, as1Resource+"/v1/sessions", bearer, nil)
	gomega.Expect(status).To(gomega.Equal(http.StatusOK), "list sessions: %s", formatBody(raw))
	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	gomega.Expect(json.Unmarshal(raw, &out)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	ids := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		ids = append(ids, s.SessionID)
	}
	return ids
}

// as1JWKSKeyID returns the kid AS-1 publishes for pub, found through its
// discovery document. ToolHive derives the kid as pub's RFC 7638 thumbprint.
func as1JWKSKeyID(ctx context.Context, client *http.Client, pub *ecdsa.PublicKey) string {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodGet, as1Issuer+"/.well-known/openid-configuration", "", nil)
	gomega.Expect(status).To(gomega.Equal(http.StatusOK), "AS-1 discovery: %s", formatBody(raw))
	var discovery struct {
		JWKSURI string `json:"jwks_uri"`
	}
	gomega.Expect(json.Unmarshal(raw, &discovery)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	return jwksKeyID(as1JWKS(ctx, client, discovery.JWKSURI), pub, "AS-1")
}

// as1JWKS returns the keys of the non-empty JWK Set at jwksURL.
func as1JWKS(ctx context.Context, client *http.Client, jwksURL string) []map[string]any {
	ginkgo.GinkgoHelper()
	status, raw := as1Do(ctx, client, http.MethodGet, jwksURL, "", nil)
	gomega.Expect(status).To(gomega.Equal(http.StatusOK), "GET %s: %s", jwksURL, formatBody(raw))
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	gomega.Expect(json.Unmarshal(raw, &jwks)).To(gomega.Succeed(), "decode %s", formatBody(raw))
	gomega.Expect(jwks.Keys).NotTo(gomega.BeEmpty(), "%s publishes no keys", jwksURL)
	return jwks.Keys
}

// jwksKeyID returns the kid of pub among the keys server publishes.
func jwksKeyID(keys []map[string]any, pub *ecdsa.PublicKey, server string) string {
	ginkgo.GinkgoHelper()
	point, err := pub.Bytes()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	x, y := base64.RawURLEncoding.EncodeToString(point[1:33]), base64.RawURLEncoding.EncodeToString(point[33:])
	for _, k := range keys {
		if k["crv"] == "P-256" && k["x"] == x && k["y"] == y {
			kid, _ := k["kid"].(string)
			gomega.Expect(kid).NotTo(gomega.BeEmpty())
			return kid
		}
	}
	ginkgo.Fail(fmt.Sprintf("%s's JWKS does not publish the test's signing key: %v", server, keys))
	return ""
}

// as1Do sends one request: body is JSON, or a form when it is url.Values.
func as1Do(ctx context.Context, client *http.Client, method, target, bearer string, body any) (int, []byte) {
	ginkgo.GinkgoHelper()
	var reader io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		reader, contentType = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	case []byte:
		reader, contentType = bytes.NewReader(b), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s %s", method, target)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw
}

// as1Tunnels maps in-cluster host:port addresses to local port-forwards, so
// the test process reaches the cluster under the names the certificates and
// issuers carry, without editing /etc/hosts.
type as1Tunnels struct {
	mu    sync.Mutex
	addrs map[string]string
}

func (t *as1Tunnels) set(clusterAddr, localAddr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.addrs[clusterAddr] = localAddr
}

// forwardAS1 forwards the release's Service HTTP port, replacing any earlier
// forward, and returns the forward's stop function.
func (t *as1Tunnels) forwardAS1() func() {
	ginkgo.GinkgoHelper()
	addr, stop := portForwardResourceIn(as1Namespace, "service/"+as1Release, agentPodPort)
	t.set(as1HTTPAddr, addr)
	return stop
}

func (t *as1Tunnels) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	t.mu.Lock()
	local, ok := t.addrs[addr]
	t.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no port-forward for %s", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, local)
}

// client is an HTTPS client that dials through the tunnels and trusts only
// the test CA.
func (t *as1Tunnels) client(roots *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext:       t.dial,
			TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		},
	}
}

// as1Browser plays the user's browser for one sign-in: it keeps cookies,
// follows redirects, fills in Dex's password form, and approves Dex's
// approval page and AS-1's consent page, posting each form with its page's
// Origin as a browser does. steps records the pages it acted on.
type as1Browser struct {
	client            *http.Client
	login, password   string
	steps             []string
	upstreamAuthorize *url.URL
}

func newAS1Browser(base *http.Client, login, password string) *as1Browser {
	jar, err := cookiejar.New(nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	c := *base
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &as1Browser{client: &c, login: login, password: password}
}

// presenter answers clientauth's authorization URL with this browser.
func (b *as1Browser) presenter() clientauth.PresenterFunc {
	return func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
		back, err := b.authorize(ctx, authorizationURL)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		if back.Get("error") != "" {
			return oauthlogin.Result{}, errors.New("authorization refused: " + back.Get("error"))
		}
		return oauthlogin.Result{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
	}
}

// authorize follows authorizationURL until the redirect to its redirect_uri,
// whose query it returns without contacting the redirect URI.
func (b *as1Browser) authorize(ctx context.Context, authorizationURL string) (url.Values, error) {
	start, err := url.Parse(authorizationURL)
	if err != nil {
		return nil, err
	}
	redirectURI := start.Query().Get("redirect_uri")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL, nil)
	if err != nil {
		return nil, err
	}
	for range 20 {
		if req.URL.Host == as1DexAddr && strings.HasSuffix(req.URL.Path, "/auth") {
			b.upstreamAuthorize = req.URL
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusFound, http.StatusSeeOther:
			location, err := resp.Location()
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(location.String(), redirectURI+"?") {
				return location.Query(), nil
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
			if err != nil {
				return nil, err
			}
		case http.StatusOK:
			if req, err = b.submit(ctx, req.URL, body); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%s %s%s: status %d: %.300s", req.Method, req.URL.Host, req.URL.Path, resp.StatusCode, body)
		}
	}
	return nil, errors.New("sign-in did not return to the redirect URI within 20 requests")
}

// submit answers the page at page: Dex's password form, Dex's approval
// page, or AS-1's consent page.
func (b *as1Browser) submit(ctx context.Context, page *url.URL, body []byte) (*http.Request, error) {
	forms, err := parseForms(body)
	if err != nil {
		return nil, err
	}
	var (
		chosen *pageForm
		values url.Values
		step   string
	)
	for _, f := range forms {
		switch {
		case f.fields["password"] != nil:
			chosen, step, values = f, "dex login", f.hidden()
			values.Set("login", b.login)
			values.Set("password", b.password)
		// ToolHive's consentPageTemplate (pkg/authserver/server/handlers/consent.go)
		// posts a hidden handle with a decision=approve|deny button.
		case f.hiddenValues.Has("handle"):
			chosen, step, values = f, "as1 consent", f.hidden()
			values.Set("decision", "approve")
		case slices.Contains(f.submits, [2]string{"approval", "approve"}):
			chosen, step, values = f, "dex approval", f.hidden()
			values.Set("approval", "approve")
		default:
			continue
		}
		break
	}
	if chosen == nil {
		return nil, fmt.Errorf("GET %s%s: status 200 without a login, approval, or consent form: %.300s", page.Host, page.Path, body)
	}
	b.steps = append(b.steps, step)
	target, err := page.Parse(chosen.action)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", page.Scheme+"://"+page.Host)
	return req, nil
}

// pageForm is one HTML form: its action (empty posts to the page), its
// hidden inputs, the names of its other inputs, and its submit name/values.
type pageForm struct {
	action       string
	hiddenValues url.Values
	fields       map[string][]string
	submits      [][2]string
}

func (f *pageForm) hidden() url.Values {
	out := url.Values{}
	for k, v := range f.hiddenValues {
		out[k] = slices.Clone(v)
	}
	return out
}

func parseForms(body []byte) ([]*pageForm, error) {
	var forms []*pageForm
	var current *pageForm
	tokens := html.NewTokenizer(bytes.NewReader(body))
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			if err := tokens.Err(); !errors.Is(err, io.EOF) {
				return nil, err
			}
			return forms, nil
		case html.EndTagToken:
			if tok := tokens.Token(); tok.Data == "form" {
				current = nil
			}
			continue
		case html.StartTagToken, html.SelfClosingTagToken:
		default:
			continue
		}
		tok := tokens.Token()
		attrs := map[string]string{}
		for _, a := range tok.Attr {
			attrs[a.Key] = a.Val
		}
		switch tok.Data {
		case "form":
			current = &pageForm{action: attrs["action"], hiddenValues: url.Values{}, fields: map[string][]string{}}
			forms = append(forms, current)
		case "input", "button":
			if current == nil || attrs["name"] == "" {
				continue
			}
			typ := strings.ToLower(attrs["type"])
			switch {
			case typ == "hidden":
				current.hiddenValues.Add(attrs["name"], attrs["value"])
			case typ == "submit" || (tok.Data == "button" && typ == ""):
				current.submits = append(current.submits, [2]string{attrs["name"], attrs["value"]})
			default:
				current.fields[attrs["name"]] = append(current.fields[attrs["name"]], attrs["value"])
			}
		}
	}
}

// as1PKI is the test CA that issues Dex's and the release's certificates.
type as1PKI struct {
	caPEM []byte
	pool  *x509.CertPool
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
}

func newAS1PKI() *as1PKI {
	ginkgo.GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: as1Serial(), Subject: pkix.Name{CommonName: "mecatl e2e AS-1 test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &as1PKI{caPEM: pemBlock("CERTIFICATE", der), pool: pool, cert: cert, key: key}
}

// serverPair issues a server certificate for dnsNames and returns it and its
// key in PEM.
func (p *as1PKI) serverPair(dnsNames ...string) (certPEM, keyPEM []byte) {
	ginkgo.GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: as1Serial(), Subject: pkix.Name{CommonName: dnsNames[0]}, DNSNames: dnsNames,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, p.cert, &key.PublicKey, p.key)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return pemBlock("CERTIFICATE", der), pemBlock("PRIVATE KEY", keyDER)
}

func as1Serial() *big.Int {
	ginkgo.GinkgoHelper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return serial
}

// applySecretIn applies an Opaque Secret in namespace without putting its
// values on a command line.
func applySecretIn(ctx context.Context, namespace, name string, data map[string][]byte) {
	ginkgo.GinkgoHelper()
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]string{"name": name, "namespace": namespace}, "data": data,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "apply Secret %s/%s: %s", namespace, name, out)
}

// as1DexConfig is Dex's configuration: HTTPS, the static alice/bob password
// users of the suite's other Dex, and AS-1's confidential client. The
// bcrypt hash is of the literal "password".
func as1DexConfig(clientSecret string) string {
	return fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  https: 0.0.0.0:%d
  tlsCert: /etc/dex/tls.crt
  tlsKey: /etc/dex/tls.key
logger:
  level: debug
oauth2:
  skipApprovalScreen: true
staticClients:
  - id: %s
    name: mecak8s AS-1
    secret: %q
    redirectURIs: [%q]
enablePasswordDB: true
staticPasswords:
  - email: alice@example.com
    hash: "$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W"
    username: alice
    userID: alice-uid
  - email: bob@example.com
    hash: "$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W"
    username: bob
    userID: bob-uid
`, as1DexIssuer, as1DexPort, as1DexClientID, clientSecret, as1Issuer+"/oauth/callback")
}

// as1DexManifests run Dex from the dex-as1 Secret, which holds its config and
// TLS pair. The namespace has no NetworkPolicy, so no policy is needed.
const as1DexManifests = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dex-as1
  namespace: mecatl-as1
  labels:
    app.kubernetes.io/name: dex-as1
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: dex-as1
  template:
    metadata:
      labels:
        app.kubernetes.io/name: dex-as1
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: dex
          image: ghcr.io/dexidp/dex:v2.44.0
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/dex", "serve", "/etc/dex/config.yaml"]
          ports:
            - containerPort: 5554
          volumeMounts:
            - name: cfg
              mountPath: /etc/dex
              readOnly: true
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          readinessProbe:
            httpGet:
              path: /healthz
              port: 5554
              scheme: HTTPS
            initialDelaySeconds: 2
            periodSeconds: 2
      volumes:
        - name: cfg
          secret:
            secretName: dex-as1
            defaultMode: 0440
---
apiVersion: v1
kind: Service
metadata:
  name: dex-as1
  namespace: mecatl-as1
spec:
  selector:
    app.kubernetes.io/name: dex-as1
  ports:
    - name: https
      port: 5554
      targetPort: 5554
`

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// ecKeyPEM is key as a SEC 1 PEM private key, a form the identity broker and
// AS-1 read.
func ecKeyPEM(key *ecdsa.PrivateKey) []byte {
	ginkgo.GinkgoHelper()
	der, err := x509.MarshalECPrivateKey(key)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return pemBlock("EC PRIVATE KEY", der)
}
