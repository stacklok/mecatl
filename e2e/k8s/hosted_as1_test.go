//go:build kind_e2e

// The hosted AS-1 journey signs mecatui's real login code in at the AS-1 a
// second mecak8s release hosts, in front of an HTTPS Dex at a private
// in-cluster address, and drives every request from the test process through
// port-forwards. Its assertions are facts observed in a live cluster.

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
	)

	ginkgo.BeforeAll(func() {
		ctx := ginkgoSuiteCtx()
		ginkgo.By("creating the " + as1Namespace + " namespace (PSS restricted)")
		nsYAML := runCmd(ctx, "kubectl", "create", "namespace", as1Namespace, "--dry-run=client", "-o", "yaml")
		kubectlApplyStdin(ctx, []byte(nsYAML))
		runCmd(ctx, "kubectl", "label", "namespace", as1Namespace,
			"pod-security.kubernetes.io/enforce=restricted", "pod-security.kubernetes.io/warn=restricted", "--overwrite")

		ginkgo.By("generating the test CA, server certificates, AS-1 signing key, and Dex client secret")
		pki := newAS1PKI()
		dexCert, dexKey := pki.serverPair(as1DexName, as1DexName+"."+as1Namespace, as1DexName+"."+as1Namespace+".svc", as1DexHost)
		as1Cert, as1Key := pki.serverPair(as1Release, as1Release+"."+as1Namespace, as1Release+"."+as1Namespace+".svc", as1Host)
		signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		signingDER, err := x509.MarshalECPrivateKey(signingKey)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientSecret := rand.Text()

		applySecretIn(ctx, as1Namespace, as1DexName, map[string][]byte{
			"config.yaml": []byte(as1DexConfig(clientSecret)), "tls.crt": dexCert, "tls.key": dexKey,
		})
		applySecretIn(ctx, as1Namespace, as1Release+"-tls", map[string][]byte{"tls.crt": as1Cert, "tls.key": as1Key})
		applySecretIn(ctx, as1Namespace, as1Release+"-signing", map[string][]byte{"signing-key.pem": pemBlock("EC PRIVATE KEY", signingDER)})
		applySecretIn(ctx, as1Namespace, as1Release+"-upstream", map[string][]byte{"client-secret": []byte(clientSecret)})
		applySecretIn(ctx, as1Namespace, as1Release+"-upstream-ca", map[string][]byte{"ca.crt": pki.caPEM})

		ginkgo.By("deploying Dex over HTTPS at " + as1DexIssuer)
		kubectlApplyStdin(ctx, []byte(as1DexManifests))
		runCmd(ctx, "kubectl", "rollout", "status", "deployment/"+as1DexName, "-n", as1Namespace, "--timeout=180s")

		ginkgo.By("installing the " + as1Release + " release with hosted AS-1 upstream of that Dex")
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

	ginkgo.It("signs the TUI in at AS-1 through Dex's login and issues an at+jwt access token naming the Dex identity", func() {
		ctx, cancel := shortCtx(90 * time.Second)
		defer cancel()
		browser := newAS1Browser(client, aliceEmail, dexPass)
		presenter := clientauth.PresenterFunc(func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
			back, err := browser.authorize(ctx, authorizationURL)
			if err != nil {
				return oauthlogin.Result{}, err
			}
			if back.Get("error") != "" {
				return oauthlogin.Result{}, errors.New("authorization refused: " + back.Get("error"))
			}
			return oauthlogin.Result{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
		})
		login = clientauth.LoginConfig{Identity: identity, Presenter: presenter, HTTPClient: client, IdentityBrokerSignIn: true}
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
		status, sessionID := as1CreateSession(ctx, client, signedIn.AccessToken)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated), "session create with the AS-1 access token")
		expectAS1Owner(ctx, client, signedIn.AccessToken, sessionID, subID)
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

	ginkgo.It("accepts a pre-restart access token after the pod restarts and refuses the pre-restart refresh token", func() {
		ctx := ginkgoSuiteCtx()
		reqCtx, cancel := shortCtx(30 * time.Second)
		beforeRestart, err := source.Refresh(reqCtx)
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
		status, sessionID := as1CreateSession(reqCtx, client, beforeRestart)
		gomega.Expect(status).To(gomega.Equal(http.StatusCreated),
			"an access token AS-1 issued before the restart was refused after it; the signing key comes from the Secret")
		expectAS1Owner(reqCtx, client, beforeRestart, sessionID, subID)

		status, raw := as1Do(reqCtx, client, http.MethodPost, as1Issuer+"/oauth/token", "", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refreshBeforeRestart},
			"client_id": {as1TUIClient}, "resource": {as1Audience},
		})
		ginkgo.GinkgoWriter.Printf("pre-restart refresh after restart: %d %s\n", status, formatBody(raw))
		var refusal struct {
			Error string `json:"error"`
		}
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), "pre-restart refresh token: %s", formatBody(raw))
		gomega.Expect(json.Unmarshal(raw, &refusal)).To(gomega.Succeed(), "decode %s", formatBody(raw))
		gomega.Expect(refusal.Error).To(gomega.Equal("invalid_grant"))
	})
})

// installAS1Release installs the second, single-replica mecak8s release with
// TLS, the identity broker, and hosted AS-1 upstream of the HTTPS Dex.
func installAS1Release(ctx context.Context) {
	ginkgo.GinkgoHelper()
	chartDir := filepath.Join(repoRoot(), "deploy", "helm", "mecak8s")
	args := []string{"upgrade", "--install", as1Release, chartDir, "--namespace", as1Namespace,
		"--values", filepath.Join(chartDir, "values-kind.yaml"), "--wait", "--timeout=4m"}
	for _, value := range []string{
		"image.repository=ko.local/mecak8s", "image.tag=e2e", "fullnameOverride=" + as1Release,
		"extraArgs[0]=--no-shell",
		"tls.secretName=" + as1Release + "-tls",
		"oidc.ownerIdentity=sub_id", "oidc.audience=" + as1Audience, "oidc.resource=" + as1Resource,
		"identityBroker.subjectClaim=sub",
		"identityBroker.as1.issuer=" + as1Issuer, "identityBroker.as1.tuiClientID=" + as1TUIClient,
		"identityBroker.as1.signingKey.secretName=" + as1Release + "-signing", "identityBroker.as1.signingKey.key=signing-key.pem",
		"identityBroker.as1.upstream.issuer=" + as1DexIssuer, "identityBroker.as1.upstream.clientID=" + as1DexClientID,
		"identityBroker.as1.upstream.clientSecret.secretName=" + as1Release + "-upstream", "identityBroker.as1.upstream.clientSecret.key=client-secret",
		"identityBroker.as1.upstream.caSecret.secretName=" + as1Release + "-upstream-ca", "identityBroker.as1.upstream.caSecret.key=ca.crt",
	} {
		args = append(args, "--set-string", value)
	}
	args = append(args, "--set", "replicaCount=1,tls.enabled=true,oidc.enabled=true,identityBroker.enabled=true,identityBroker.as1.upstream.allowPrivateAddresses=true")
	out, err := boundedCommandOutput(ctx, 1<<20, "helm", args...)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("--- %s logs ---\n%s\n", as1Release,
			runCmdQuiet("kubectl", "logs", "-n", as1Namespace, "deployment/"+as1Release, "--tail=100"))
	}
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "helm install %s\n%s", as1Release, out)
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
