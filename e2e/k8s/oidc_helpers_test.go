//go:build kind_e2e

// The fixtures use the production toolhive-core/authn validator from the tagged
// module dependency. The canonical task runs with GOWORK=off so no local module
// overlay can affect the image under test.

package k8s_e2e_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A REAL OIDC provider, not a static JWKS file. Dex is a single Go binary with
// static config and in-memory storage, so it costs one ConfigMap + Deployment +
// Service — and unlike a hand-rolled JWKS it ISSUES tokens and serves DISCOVERY.
//
// That difference is the point. With a static JWKS the suite could only prove "a
// signature over a key we handed you verifies"; the `.well-known` fetch that every
// real deployment performs had ZERO executions anywhere in this repo.
const (
	dexName      = "dex"
	dexPort      = 5556
	dexIssuer    = "http://dex.mecatl.svc.cluster.local:5556"
	dexClient    = "mecatl"
	dexSecret    = "mecatl-secret"
	dexPass      = "password"
	jwksProxyURI = "http://jwks-proxy.mecatl.svc.cluster.local:8080/keys"
	aliceEmail   = "alice@example.com"
	bobEmail     = "bob@example.com"
)

// idp is the in-cluster identity provider plus a local tunnel to it, so the test
// can obtain REAL tokens the way a caller would.
type idp struct {
	addr string
	stop func()
}

// deployDex brings up Dex and forwards its port.
//
// PSS `restricted` is ENFORCED on this namespace, so the pod must be runAsNonRoot
// with dropped capabilities and RuntimeDefault seccomp or the API server rejects
// it. Dex needs no writable path with in-memory storage.
func deployDex(ctx context.Context) *idp {
	ginkgo.GinkgoHelper()

	ginkgo.By("deploying Dex (a real OIDC provider) + its NetworkPolicies")
	kubectlApplyStdin(ctx, []byte(dexManifests))

	waitOut, err := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Ready",
		"pod", "-l", "app.kubernetes.io/name="+dexName, "-n", k8sNamespace,
		"--timeout=180s").CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"Dex never became Ready\n--- output ---\n%s", waitOut)

	pod := strings.TrimSpace(runCmdQuiet("kubectl", "get", "pods",
		"-l", "app.kubernetes.io/name="+dexName, "-n", k8sNamespace,
		"-o", "jsonpath={.items[0].metadata.name}"))
	gomega.ExpectWithOffset(1, pod).NotTo(gomega.BeEmpty(), "no Dex pod found")

	port := freeLocalPort()
	fwdCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(fwdCtx, "kubectl", "port-forward", "-n", k8sNamespace,
		"pod/"+pod, fmt.Sprintf("%d:%d", port, dexPort))
	gomega.ExpectWithOffset(1, cmd.Start()).To(gomega.Succeed(), "port-forward to Dex")

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	gomega.EventuallyWithOffset(1, func() bool {
		c, derr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 30*time.Second, 250*time.Millisecond).Should(gomega.BeTrue(),
		"the Dex port-forward never accepted on %s", addr)

	return &idp{addr: addr, stop: func() { cancel(); _ = cmd.Wait() }}
}

// deployJWKSProxy installs a disposable reverse proxy in front of Dex's key
// endpoint. Taking the proxy pod down closes existing keep-alive connections,
// while Dex itself remains alive with the same signing key.
func deployJWKSProxy(ctx context.Context) {
	ginkgo.GinkgoHelper()
	kubectlApplyStdin(ctx, []byte(jwksProxyManifests))
	setJWKSProxyReplicas(ctx, 1)
}

func setJWKSProxyReplicas(ctx context.Context, replicas int) {
	ginkgo.GinkgoHelper()
	out, err := exec.CommandContext(ctx, "kubectl", "scale", "deployment/jwks-proxy",
		"-n", k8sNamespace, fmt.Sprintf("--replicas=%d", replicas)).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"scale JWKS proxy to %d replicas\n--- output ---\n%s", replicas, out)
	if replicas == 0 {
		gomega.EventuallyWithOffset(1, func() string {
			return strings.TrimSpace(runCmdQuiet("kubectl", "get", "pods", "-n", k8sNamespace,
				"-l", "app.kubernetes.io/name=jwks-proxy", "-o", "name"))
		}, 60*time.Second, time.Second).Should(gomega.BeEmpty(), "JWKS proxy pod remained after scale-to-zero")
		return
	}
	rollout, err := exec.CommandContext(ctx, "kubectl", "rollout", "status",
		"deployment/jwks-proxy", "-n", k8sNamespace, "--timeout=180s").CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"JWKS proxy did not become ready\n--- output ---\n%s", rollout)
}

// token obtains a REAL id_token via Dex's password grant.
//
// The password grant is deliberate: it yields a genuine IdP-signed token with one
// HTTP call, no browser and no redirect dance, which is what makes a real IdP
// usable in an automated suite at all.
func (d *idp) token(ctx context.Context, email string) string {
	ginkgo.GinkgoHelper()
	form := url.Values{
		"grant_type": {"password"},
		"username":   {email},
		"password":   {dexPass},
		"scope":      {"openid email profile"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+d.addr+"/token", strings.NewReader(form.Encode()))
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	req.SetBasicAuth(dexClient, dexSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST Dex /token")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusOK),
		"Dex refused a token for %s: %s", email, raw)

	var out struct {
		IDToken string `json:"id_token"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &out)).To(gomega.Succeed(), "unmarshal %s", raw)
	gomega.ExpectWithOffset(1, out.IDToken).NotTo(gomega.BeEmpty(), "empty id_token")
	return out.IDToken
}

// forgedToken mints a token carrying one of Dex's REAL key ids, signed with a key
// Dex never published.
//
// Reusing the real `kid` is what makes this a SIGNATURE test rather than an
// unknown-key test: the validator finds the named key, verifies against it, and
// must reject. A random kid would also 401, but for the weaker reason "no such
// key" — which an edge that skipped signature verification entirely would produce
// too.
func (d *idp) forgedToken(ctx context.Context, sub string) string {
	ginkgo.GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+d.addr+"/keys", nil)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "GET Dex /keys")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &jwks)).To(gomega.Succeed(), "unmarshal JWKS %s", raw)
	gomega.ExpectWithOffset(1, jwks.Keys).NotTo(gomega.BeEmpty(), "Dex published no keys")

	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "generate the rogue key")

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": dexIssuer, "aud": dexClient, "sub": sub,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	tok.Header["kid"] = jwks.Keys[0].Kid
	s, err := tok.SignedString(rogue)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "sign the forged token")
	return s
}

// currentAgentArgs reads the live Deployment args so temporary OIDC rollouts
// preserve the provider, model, Redis plaintext opt-in, and any future chart flags.
// The Deployment can already have been switched from mock to OpenRouter by the
// suite-level live-provider patch; rebuilding args from a static baseline would
// silently undo that switch.
func currentAgentArgs(ctx context.Context) []string {
	ginkgo.GinkgoHelper()
	deploymentJSON, err := exec.CommandContext(ctx, "kubectl", "get",
		"deployment/mecak8s-agent", "-n", k8sNamespace, "-o", "json").Output()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "read agent Deployment args")

	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string   `json:"name"`
						Args []string `json:"args"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(deploymentJSON, &deployment)).To(gomega.Succeed(),
		"decode agent Deployment args")
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == agentComponent {
			return container.Args
		}
	}
	ginkgo.Fail("agent Deployment has no "+agentComponent+" container", 1)
	return nil
}

var oidcArgNames = map[string]struct{}{
	"--oidc-issuer": {}, "--oidc-audience": {}, "--oidc-jwks-uri": {},
	"--oidc-insecure-allow-private-issuer": {}, "--oidc-max-jwks-staleness": {},
}

func withoutOIDCArgs(current []string) []string {
	args := make([]string, 0, len(current))
	for i := 0; i < len(current); i++ {
		arg := current[i]
		name, _, hasValue := strings.Cut(arg, "=")
		if _, ok := oidcArgNames[name]; !ok {
			args = append(args, arg)
			continue
		}
		if !hasValue && i+1 < len(current) && !strings.HasPrefix(current[i+1], "-") {
			i++
		}
	}
	return args
}

// patchAgentToOIDC turns caller identity on and waits out the rollout.
//
// It passes `--oidc-issuer` and NO `--oidc-jwks-uri`, so the agent performs real
// OIDC DISCOVERY against Dex. That is the shape a real deployment uses; pinning
// the JWKS URI is the air-gap hook, and pinning it everywhere is what left
// discovery unexercised.
//
// `--oidc-insecure-allow-private-issuer` is required and is this flag's justified
// use: Dex is http:// at an in-cluster address, which the validator refuses by
// default (the same check that blocks a jwks_uri aimed at 169.254.169.254).
// `task deploy:check` fails if the flag ever appears under deploy/.
func patchAgentToOIDC(ctx context.Context) {
	patchAgentToOIDCWithCachePolicy(ctx, "", 0)
}

// patchAgentToOIDCWithCachePolicy enables caller identity with optional JWKS URI
// and cache-staleness overrides. Empty/zero leave production defaults in force.
func patchAgentToOIDCWithCachePolicy(ctx context.Context, jwksURI string, maxStaleness time.Duration) {
	ginkgo.GinkgoHelper()

	args := append(withoutOIDCArgs(currentAgentArgs(ctx)),
		"--oidc-issuer="+dexIssuer,
		"--oidc-audience="+dexClient,
		"--oidc-insecure-allow-private-issuer",
	)
	if jwksURI != "" {
		args = append(args, "--oidc-jwks-uri="+jwksURI)
	}
	if maxStaleness > 0 {
		args = append(args, "--oidc-max-jwks-staleness="+maxStaleness.String())
	}
	argsJSON, err := json.Marshal(args)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "marshal the OIDC args")

	mode := "discovery, no pinned JWKS"
	if jwksURI != "" {
		mode = "pinned JWKS endpoint"
	}
	ginkgo.By("patching mecak8s-agent to enable caller identity (" + mode + ")")
	patch := fmt.Sprintf(
		`[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":%s}]`, argsJSON)
	patchOut, err := exec.CommandContext(ctx, "kubectl", "patch",
		"deployment/mecak8s-agent", "-n", k8sNamespace,
		"--type=json", "-p", patch).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl patch deployment to OIDC failed\n--- output ---\n%s", patchOut)

	ginkgo.By("waiting for the OIDC rollout")
	rolloutOut, err := exec.CommandContext(ctx, "kubectl", "rollout", "status",
		"deployment/mecak8s-agent", "-n", k8sNamespace, "--timeout=240s").CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"the OIDC rollout never completed. A pod that cannot reach the IdP exits at "+
			"startup by design (fail-closed) — check the agent→Dex egress NetworkPolicy, "+
			"which IS enforced in kind\n--- output ---\n%s", rolloutOut)

	ginkgo.By("waiting for all mecak8s pods to be Ready (after the OIDC patch)")
	waitPodsReady()
	agentPods = podNames()
}

// restoreAgentFromOIDC puts the Deployment back to the unauthenticated baseline
// and removes the IdP.
//
// NOT optional hygiene. The Deployment is shared state for the whole suite:
// leaving identity on makes every later spec's unauthenticated request 401, which
// is exactly how an earlier run of these specs turned three passing specs red.
func restoreAgentFromOIDC(ctx context.Context) {
	ginkgo.GinkgoHelper()
	argsJSON, err := json.Marshal(withoutOIDCArgs(currentAgentArgs(ctx)))
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "marshal the base args")

	ginkgo.By("restoring mecak8s-agent to the unauthenticated baseline")
	patch := fmt.Sprintf(
		`[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":%s}]`, argsJSON)
	out, err := exec.CommandContext(ctx, "kubectl", "patch",
		"deployment/mecak8s-agent", "-n", k8sNamespace,
		"--type=json", "-p", patch).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"restore patch failed\n--- output ---\n%s", out)

	rollout, err := exec.CommandContext(ctx, "kubectl", "rollout", "status",
		"deployment/mecak8s-agent", "-n", k8sNamespace, "--timeout=240s").CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"restore rollout never completed\n--- output ---\n%s", rollout)
	waitPodsReady()
	agentPods = podNames()

	deleteOut, err := exec.CommandContext(ctx, "kubectl", "delete", "-n", k8sNamespace,
		"deployment/dex", "service/dex", "configmap/dex-config",
		"deployment/jwks-proxy", "service/jwks-proxy", "configmap/jwks-proxy-config",
		"networkpolicy/dex-allow-agent-ingress",
		"networkpolicy/mecak8s-agent-allow-dex-egress",
		"networkpolicy/mecak8s-agent-allow-jwks-proxy-egress",
		"networkpolicy/jwks-proxy-allow-agent-ingress",
		"networkpolicy/jwks-proxy-allow-dex-egress",
		"networkpolicy/dex-allow-jwks-proxy-ingress",
		"--ignore-not-found").CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"remove Dex fixture resources\n--- output ---\n%s", deleteOut)
	gomega.EventuallyWithOffset(1, func() bool {
		out, getErr := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", k8sNamespace,
			"-l", "app.kubernetes.io/name="+dexName, "-o", "name").Output()
		return getErr == nil && strings.TrimSpace(string(out)) == ""
	}, 60*time.Second, time.Second).Should(gomega.BeTrue(),
		"Dex pods did not terminate before the next caller-identity journey")
}

// kubectlApplyStdin applies a manifest from stdin.
func kubectlApplyStdin(ctx context.Context, manifest []byte) {
	ginkgo.GinkgoHelper()
	apply := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
	out, err := apply.CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl apply (stdin) failed\n--- output ---\n%s", out)
}

// jwksProxyManifests is a disposable transport fault boundary in front of Dex's
// JWKS endpoint. Scaling it to zero closes active connections without restarting
// Dex or changing the signing key used by the token under test.
const jwksProxyManifests = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: jwks-proxy-config
  namespace: mecatl
data:
  default.conf: |
    server {
      listen 8080;
      location /keys {
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_pass http://dex.mecatl.svc.cluster.local:5556/keys;
      }
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jwks-proxy
  namespace: mecatl
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: jwks-proxy
  template:
    metadata:
      labels:
        app.kubernetes.io/name: jwks-proxy
    spec:
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: nginx
          image: nginxinc/nginx-unprivileged:1.29-alpine
          ports:
            - containerPort: 8080
          readinessProbe:
            httpGet:
              path: /keys
              port: 8080
            periodSeconds: 2
          volumeMounts:
            - name: config
              mountPath: /etc/nginx/conf.d
              readOnly: true
            - name: tmp
              mountPath: /tmp
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
      volumes:
        - name: config
          configMap:
            name: jwks-proxy-config
        - name: tmp
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: jwks-proxy
  namespace: mecatl
spec:
  selector:
    app.kubernetes.io/name: jwks-proxy
  ports:
    - port: 8080
      targetPort: 8080
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: mecak8s-agent-allow-jwks-proxy-egress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mecak8s
      app.kubernetes.io/component: agent
  policyTypes: ["Egress"]
  egress:
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: jwks-proxy
      ports:
        - protocol: TCP
          port: 8080
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: jwks-proxy-allow-agent-ingress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: jwks-proxy
  policyTypes: ["Ingress"]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: mecak8s
              app.kubernetes.io/component: agent
      ports:
        - protocol: TCP
          port: 8080
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: jwks-proxy-allow-dex-egress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: jwks-proxy
  policyTypes: ["Egress"]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: dex
      ports:
        - protocol: TCP
          port: 5556
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: dex-allow-jwks-proxy-ingress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: dex
  policyTypes: ["Ingress"]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: jwks-proxy
      ports:
        - protocol: TCP
          port: 5556
`

// dexManifests is the IdP: config, workload, Service, and the two NetworkPolicies
// its traffic needs. The bcrypt hash is of the literal "password" — throwaway
// credentials for an ephemeral in-cluster IdP with in-memory storage.
const dexManifests = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: dex-config
  namespace: mecatl
data:
  config.yaml: |
    issuer: http://dex.mecatl.svc.cluster.local:5556
    storage:
      type: memory
    web:
      http: 0.0.0.0:5556
    oauth2:
      passwordConnector: local
      skipApprovalScreen: true
    staticClients:
      - id: mecatl
        name: mecatl
        secret: mecatl-secret
        public: false
        redirectURIs: ["http://localhost:5555/callback"]
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
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dex
  namespace: mecatl
  labels:
    app.kubernetes.io/name: dex
    app.kubernetes.io/part-of: mecak8s
    app.kubernetes.io/component: idp
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: dex
  template:
    metadata:
      labels:
        app.kubernetes.io/name: dex
        app.kubernetes.io/part-of: mecak8s
        app.kubernetes.io/component: idp
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: dex
          image: ghcr.io/dexidp/dex:v2.44.0
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/dex", "serve", "/etc/dex/config.yaml"]
          ports:
            - containerPort: 5556
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
              port: 5556
            initialDelaySeconds: 2
            periodSeconds: 2
      volumes:
        - name: cfg
          configMap:
            name: dex-config
---
apiVersion: v1
kind: Service
metadata:
  name: dex
  namespace: mecatl
  labels:
    app.kubernetes.io/name: dex
spec:
  selector:
    app.kubernetes.io/name: dex
  ports:
    - name: http
      port: 5556
      targetPort: 5556
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: dex-allow-agent-ingress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: dex
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: mecatl
          podSelector:
            matchLabels:
              app.kubernetes.io/name: mecak8s
              app.kubernetes.io/component: agent
      ports:
        - protocol: TCP
          port: 5556
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: mecak8s-agent-allow-dex-egress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mecak8s
      app.kubernetes.io/component: agent
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: dex
      ports:
        - protocol: TCP
          port: 5556
`
