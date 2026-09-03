//go:build kind_e2e

// Package k8s_e2e_test is the kind-based Kubernetes cloud-native PROOF for
// mecak8s (ADR 0048, MECAK8S-PLAN §4h). It spins a real kind cluster with a
// Redis StatefulSet + two storage-free agent replicas and asserts three
// cloud-native properties over the HTTP API:
//
//  1. Lease exclusion across replicas (the single-writer proof).
//  2. Graceful failover releases the session lease before the TTL.
//  3. Session persistence across an agent pod restart (Redis-backed).
//
// It is GATED behind the `kind_e2e` build tag so `task test` never compiles
// it; it runs via `task e2e:k8s` (needs Docker + kind + ko + helm + kubectl).
// The suite skips gracefully (ginkgo.Skip, not a failure) when a tool is
// missing, so a bare `go test -tags kind_e2e` without the toolchain does not
// hard-fail.
//
// Unlike e2e_test (which spawns local mecated processes), this suite drives
// pods via kubectl/port-forward — it imports NO engine/ code and shares none of
// the e2e_test harness.
package k8s_e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Cluster + manifest constants (ADR 0048 §4h, deploy/helm/mecak8s/).
const (
	kindClusterName  = "mecatl-e2e"
	kindNodeImage    = "kindest/node:v1.34.3"
	k8sNamespace     = "mecatl"
	agentComponent   = "agent" // app.kubernetes.io/component label value
	agentGRPCPort    = 8080    // the gRPC listener (--grpc-addr default in the pod)
	agentPodPort     = 8081    // the HTTP/SSE listener (--http-addr default in the pod)
	agentServiceName = "mecak8s-agent"

	// liveProviderSecret is the k8s Secret holding OPENROUTER_API_KEY for the live
	// specs. The key is staged from the test process's environment into a Secret —
	// never into a pod arg, a manifest, or a log — so it cannot leak.
	liveProviderSecret = "openrouter-key"
	// liveProviderModel is the default-lane model the live specs run on (the same
	// verified-cheap lane as e2e/harness/target.go's DefaultModel).
	liveProviderModel = "anthropic/claude-haiku-4.5"
	// liveProviderID is the provider id the live deployment is patched to.
	liveProviderID = "openrouter"
)

// liveProviderConfigured records that BeforeSuite received and scrubbed an
// OpenRouter key before any live-provider command ran.
var liveProviderConfigured bool

// liveProviderEnabled reports whether BeforeSuite enabled the live-provider
// variant. The key itself is removed from the process environment before the
// fixture invokes kubectl or any credential plugin.
func liveProviderEnabled() bool {
	return liveProviderConfigured
}

// --- tool availability / cluster lifecycle -----------------------------------

// requireTools skips the suite (ginkgo.Skip) if a container runtime (Docker OR
// podman), kind, ko, helm, or kubectl is not on PATH. This is a tool-availability
// gate, not a failure — the Taskfile also has preconditions, but the Go code
// skips too so a bare `go test -tags kind_e2e` does not hard-fail on a machine
// without the toolchain. The suite header says "Needs Docker + kind + ko + helm +
// kubectl", but podman is an ACCEPTABLE ALTERNATIVE container runtime: when
// KIND_EXPERIMENTAL_PROVIDER=podman is set, ko loads into podman's store and
// `podman save` feeds the kind `image-archive` load. So EITHER docker OR podman
// satisfies the runtime requirement (the other tools are mandatory);
// without a runtime those commands hit an Expect and fail the spec instead of
// skipping.
func requireTools() {
	ginkgo.GinkgoHelper()
	// Docker OR podman (the chosen runtime saves the image to a tarball that
	// `kind load image-archive` reads, sidestepping the snapshotter bridge).
	_, dockerErr := exec.LookPath("docker")
	_, podmanErr := exec.LookPath("podman")
	if dockerErr != nil && podmanErr != nil {
		ginkgo.Skip("neither docker nor podman is on PATH — skipping the kind k8s e2e suite")
	}
	for _, tool := range []string{"kind", "ko", "helm", "kubectl"} {
		if _, err := exec.LookPath(tool); err != nil {
			ginkgo.Skip(fmt.Sprintf("%q is not on PATH — skipping the kind k8s e2e suite (needs container runtime + kind + ko + helm + kubectl)", tool))
		}
	}
}

// kindCreateCluster creates a fresh kind cluster. It is idempotent-ish: if a
// cluster with the same name already exists (a prior run left it), it is
// deleted first so the suite starts from a clean slate. The kind node image is
// pulled by kind itself.
func kindCreateCluster() {
	ginkgo.GinkgoHelper()
	// Best-effort delete of a leftover cluster (ignore errors — it may not exist).
	_ = exec.Command("kind", "delete", "cluster", "--name", kindClusterName).Run()
	runCmd(ginkgoSuiteCtx(), "kind", "create", "cluster",
		"--name", kindClusterName, "--image", kindNodeImage)
}

// kindDeleteCluster tears the cluster down. Called in AfterSuite / DeferCleanup.
func kindDeleteCluster() {
	_ = exec.Command("kind", "delete", "cluster", "--name", kindClusterName).Run()
}

// e2eImageRef is the deterministic local image ref koBuildMecak8sImage produces:
// KO_DOCKER_REPO pins the repo path, --bare --tags=e2e make the tag reproducible.
// ko also prints a sha-tagged ref on its own stdout, but --tags adds a SECOND,
// deterministic tag to that SAME image — deploy/mecak8s-vmcp/Taskfile.yml's
// image-build-load task uses the identical pattern, so there is no ref to parse
// out of ko's output.
const e2eImageRef = "ko.local/mecak8s:e2e"

// koBuildMecak8sImage builds the mecak8s image with ko into the local container
// daemon under e2eImageRef. Unlike the old one-shot kustomize-era `ko resolve`
// (which built the image AND rendered manifests with the ko:// placeholder
// substituted), the Helm chart takes explicit image.repository/image.tag values
// instead of a placeholder, so building and installing are two separate steps —
// see helmInstallMecak8sChart.
func koBuildMecak8sImage() {
	ginkgo.GinkgoHelper()
	ctx := ginkgoSuiteCtx()
	build := exec.CommandContext(ctx, "ko", "build", "--local", "--bare", "--tags=e2e", "./cmd/mecak8s")
	build.Dir = repoRoot()
	build.Env = append(build.Environ(), "KO_DOCKER_REPO=ko.local/mecak8s")
	out, err := build.CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"ko build --local --bare --tags=e2e ./cmd/mecak8s failed\n--- output ---\n%s", out)
}

// containerRuntime is the local daemon ko loaded the image into: podman when
// KIND_EXPERIMENTAL_PROVIDER=podman is set (ko routes there in that case),
// otherwise docker (ko's default). It is the runtime whose store holds the
// resolved image, so it is the runtime that must `save` it to a tarball.
func containerRuntime() string {
	if os.Getenv("KIND_EXPERIMENTAL_PROVIDER") == "podman" {
		return "podman"
	}
	return "docker"
}

// saveAndLoadImage loads a local image into the kind cluster's node via a
// tarball: `<runtime> save <ref> -o <tar>` then `kind load image-archive <tar>`.
// This bypasses `kind load docker-image`'s containerd-snapshotter detection
// entirely — the snapshotter bridge is a known failure on some Docker daemons
// (the GitHub runner's overlayfs), and `image-archive` reads the tarball
// straight into the kind node's containerd regardless of the host's snapshotter.
// It works against EITHER docker OR podman (whichever holds the image, per
// containerRuntime). The tarball is a transient runtime artifact, cleaned up on
// return.
func saveAndLoadImage(imageRef string) {
	ginkgo.GinkgoHelper()
	ctx := ginkgoSuiteCtx()
	runtime := containerRuntime()

	tmp, err := os.CreateTemp("", "mecak8s-e2e-*.tar")
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "create image tarball")
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	// `<runtime> save` writes the image (under the EXACT resolved ref the pod
	// references) to a tarball. CombinedOutput captures save progress on stderr.
	saveOut, err := exec.CommandContext(ctx, runtime, "save", imageRef, "-o", tmpPath).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"%s save %s -o %s failed\n--- output ---\n%s", runtime, imageRef, tmpPath, saveOut)

	// `kind load image-archive` loads the tarball into the kind node's containerd
	// without touching the host snapshotter — the portable path that works on
	// Docker (CI) and podman (local) alike.
	loadOut, err := exec.CommandContext(ctx, "kind", "load", "image-archive", tmpPath,
		"--name", kindClusterName).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kind load image-archive %s failed\n--- output ---\n%s", tmpPath, loadOut)
}

// helmInstallMecak8sChart creates + labels the mecatl namespace (PSS restricted,
// mirroring deploy/mecak8s-vmcp/Taskfile.yml's chart-apply task) and installs the
// deploy/helm/mecak8s chart with the disposable Kind values profile
// (values-kind.yaml — mockProvider + an in-chart plaintext Redis fixture),
// pointed at the image koBuildMecak8sImage already loaded into the kind node.
//
// fullnameOverride pins the Deployment's name to mecak8s-agent, matching what
// oidc_helpers_test.go's kubectl patch functions and the rest of this suite
// already reference by that literal name. The pod-selection labels
// (podNames/waitPodsReady select on app.kubernetes.io/part-of=mecak8s and
// app.kubernetes.io/component=agent) are unaffected by fullnameOverride — the
// chart's _helpers.tpl renders those labels identically regardless of the
// resource name.
func helmInstallMecak8sChart() {
	ginkgo.GinkgoHelper()
	ctx := ginkgoSuiteCtx()
	chartDir := filepath.Join(repoRoot(), "deploy", "helm", "mecak8s")

	nsYAML := runCmd(ctx, "kubectl", "create", "namespace", k8sNamespace, "--dry-run=client", "-o", "yaml")
	apply := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	apply.Stdin = strings.NewReader(nsYAML)
	applyOut, err := apply.CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl apply namespace failed\n--- output ---\n%s", applyOut)
	runCmd(ctx, "kubectl", "label", "namespace", k8sNamespace,
		"pod-security.kubernetes.io/enforce=restricted",
		"pod-security.kubernetes.io/audit=restricted",
		"pod-security.kubernetes.io/warn=restricted",
		"--overwrite")

	installOut, err := boundedCommandOutput(ctx, 1<<20, "helm", "upgrade", "--install", "mecak8s", chartDir,
		"--namespace", k8sNamespace,
		"--values", filepath.Join(chartDir, "values-kind.yaml"),
		"--set", "image.repository=ko.local/mecak8s",
		"--set", "image.tag=e2e",
		"--set", "fullnameOverride=mecak8s-agent",
		"--wait", "--timeout=4m")
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"helm upgrade --install mecak8s failed\n--- output ---\n%s", installOut)

	kubectlApplyStdin(ctx, []byte(agentBaselineEgressNetworkPolicy))
}

// agentBaselineEgressNetworkPolicy re-homes, into the e2e fixture, the three
// egress rules deploy/mecak8s/networkpolicy.yaml used to provide before the
// kustomize→Helm convergence (the chart itself now ships no NetworkPolicy by
// design — network isolation is left to the cluster). It is REQUIRED here for
// a structural reason, not belt-and-suspenders: the OIDC specs' own
// mecak8s-agent-allow-dex-egress / -jwks-proxy-egress policies (in
// oidc_helpers_test.go) select the agent pod with an Egress policyType, and
// Kubernetes NetworkPolicy semantics mean the FIRST policy of a given
// policyType that selects a pod flips that pod from unrestricted to
// deny-except-explicitly-listed for that direction — additively unioned
// across every policy that also selects it. Without this baseline, applying
// the Dex fixture's policies leaves the agent pod able to reach ONLY Dex and
// the JWKS proxy, and loses DNS, the k8s API (Lease coordination), and Redis
// — which is exactly what broke when the agent pod's labels were corrected
// from the stale kustomize-era `mecatl` to the chart's actual `mecak8s` (see
// the fix in oidc_helpers_test.go): the label fix made those two Egress
// policies start matching the real agent pod, which then had no baseline
// allow-rule to union with.
//
// Applied unconditionally in helmInstallMecak8sChart — even before any OIDC
// spec runs — because it must be in place BEFORE the Dex fixture's own
// policies are applied for a later spec to have any chance of correct union
// semantics, and applying it early costs nothing (the same DNS/API/Redis
// egress every spec, OIDC or not, already needs).
const agentBaselineEgressNetworkPolicy = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: mecak8s-agent-baseline-egress
  namespace: mecatl
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mecak8s
      app.kubernetes.io/component: agent
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
    # No 'to' selector = any destination IP on 443: the k8s API server
    # (Lease coordination) and, when the live provider is enabled, the
    # external LLM endpoint. Same rationale as the deleted kustomize policy.
    - ports:
        - protocol: TCP
          port: 443
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: redis
      ports:
        - protocol: TCP
          port: 6379
`

// waitPodsReady waits for both agent replicas AND the Redis pod to be Ready.
// The two waits are SEPARATE label selectors, not one part-of=mecak8s query:
// the chart's agent pods carry only name/instance/component
// (mecak8s.selectorLabels) — part-of is a resource-level label
// (mecak8s.labels), never applied to the Pod template — so a single
// part-of=mecak8s selector matches only the Redis pod (redis-local.yaml sets
// part-of inline on its own pod template) and silently misses both agent
// replicas; kubectl wait on a selector matching zero resources returns success
// immediately rather than erroring, so that gap was invisible until Dex's own
// NetworkPolicies (keyed on the agent pod's actual labels) surfaced it. The
// Redis image is pulled from Docker Hub on first apply, so the timeout must
// allow for an image pull.
func waitPodsReady() {
	ginkgo.GinkgoHelper()
	runCmd(ginkgoSuiteCtx(), "kubectl", "wait",
		"--for=condition=Ready", "pod",
		"-n", k8sNamespace,
		"-l", "app.kubernetes.io/component="+agentComponent,
		"--timeout=180s")
	runCmd(ginkgoSuiteCtx(), "kubectl", "wait",
		"--for=condition=Ready", "pod",
		"-n", k8sNamespace,
		"-l", "app.kubernetes.io/name=redis",
		"--timeout=180s")
}

// podNames returns the names of the agent pods (component=agent) that are
// Running and Ready, in stable sorted order so pod-A / pod-B are addressable
// across the suite. There are exactly two (the Deployment replicas:2); the
// suite asserts that. Filtering to Ready pods excludes terminating pods during
// a rolling update (the old pods leave the Ready set when /readyz flips to 503
// via the drain gate, but they remain in the pod list during the
// terminationGracePeriodSeconds window).
//
// This is an Eventually because a RollingUpdate with maxSurge:1 has a transient
// 3-pod window: the new pods surge Ready before the old pod's preStop /drain
// hook flips its readiness to not-ready. The rollout is "complete" (kubectl
// rollout status returns) when the new ReplicaSet reaches replicas, NOT when the
// old pod is gone. Waiting for exactly 2 Ready pods is the correct semantic —
// it resolves the transient state naturally via the readiness probe, not via a
// pod-count guess.
func podNames() []string {
	ginkgo.GinkgoHelper()
	var names []string
	gomega.Eventually(func(g gomega.Gomega) {
		out := runCmdQuiet("kubectl", "get", "pods",
			"-n", k8sNamespace,
			"-l", "app.kubernetes.io/component="+agentComponent,
			"--field-selector=status.phase==Running",
			"-o", "jsonpath={.items[?(@.status.containerStatuses[0].ready==true)].metadata.name}")
		names = strings.Fields(strings.TrimSpace(out))
		g.Expect(names).To(gomega.HaveLen(2),
			"expected exactly two Ready agent pods (replicas:2), got %d: %v", len(names), names)
	}, 120*time.Second, 2*time.Second).Should(gomega.Succeed(),
		"did not converge on exactly two Ready agent pods")
	return names
}

func serviceReadyEndpointCount() int {
	ginkgo.GinkgoHelper()
	out := runCmdQuiet("kubectl", "get", "endpointslice", "-n", k8sNamespace,
		"-l", "kubernetes.io/service-name="+agentServiceName,
		"-o", "jsonpath={.items[*].endpoints[?(@.conditions.ready==true)].addresses[*]}")
	return len(strings.Fields(out))
}

type servicePort struct {
	Name       string `json:"name"`
	Port       int32  `json:"port"`
	TargetPort string `json:"targetPort"`
}

func agentServicePorts() []servicePort {
	ginkgo.GinkgoHelper()
	raw := runCmd(ginkgoSuiteCtx(), "kubectl", "get", "service", agentServiceName, "-n", k8sNamespace, "-o", "json")
	var service struct {
		Spec struct {
			Ports []servicePort `json:"ports"`
		} `json:"spec"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal([]byte(raw), &service)).To(gomega.Succeed(), "unmarshal Service %s", agentServiceName)
	return service.Spec.Ports
}

// kubectlDeletePod deletes a pod. Graceful (the default) lets the preStop /drain
// hook + SIGTERM fire, so the pod's Service.Close releases its held leases
// before the TTL. force=true passes --force --grace-period=0, which skips the
// graceful shutdown — NO releaseLease, so the k8s Lease object remains until its
// TTL lapses. The contrast is the failover control case.
func kubectlDeletePod(podName string, force bool) {
	ginkgo.GinkgoHelper()
	args := []string{"delete", "pod", podName, "-n", k8sNamespace}
	if force {
		args = append(args, "--force", "--grace-period=0")
	}
	runCmd(ginkgoSuiteCtx(), "kubectl", args...)
}

// waitReplacementReady waits for a NEW Ready agent pod — one whose name is NOT in
// the preDelete snapshot of pod names that existed before the delete. With
// replicas:2, after deleting pod-A there are briefly two candidates for "a pod
// that is not deletedName": pod-B (the survivor, Ready all along) and the
// genuinely-new replacement. Taking a pre-delete roster and excluding ALL of it
// ensures this returns the NEW replacement pod, never the survivor — which
// matters for the force-delete control case, where the survivor pod-B may have
// just been force-killed and must not be mistaken for its own replacement.
//
// Callers MUST capture the pod names BEFORE calling kubectlDeletePod and pass
// them as preDelete.
func waitReplacementReady(preDelete []string) string {
	ginkgo.GinkgoHelper()
	old := make(map[string]struct{}, len(preDelete))
	for _, n := range preDelete {
		old[n] = struct{}{}
	}
	isNewReady := func(name string) bool {
		if _, hit := old[name]; hit {
			return false // a survivor from the pre-delete roster, not the replacement
		}
		ready := runCmdQuiet("kubectl", "get", "pod", name, "-n", k8sNamespace,
			"-o", "jsonpath={.status.containerStatuses[0].ready}")
		return ready == "true"
	}
	gomega.Eventually(func(g gomega.Gomega) string {
		out := runCmdQuiet("kubectl", "get", "pods",
			"-n", k8sNamespace,
			"-l", "app.kubernetes.io/component="+agentComponent,
			"-o", "jsonpath={.items[*].metadata.name}")
		for _, n := range strings.Fields(strings.TrimSpace(out)) {
			if isNewReady(n) {
				return n
			}
		}
		return ""
	}, 120*time.Second, 2*time.Second).ShouldNot(gomega.BeEmpty(),
		"no NEW (pre-delete-roster-excluded) replacement pod became Ready (pre-delete roster: %v)", preDelete)

	// Re-query to return the stable name (the Eventually closure's return is
	// discarded to keep the matcher simple; re-read once it is known ready).
	for _, n := range strings.Fields(runCmdQuiet("kubectl", "get", "pods",
		"-n", k8sNamespace, "-l", "app.kubernetes.io/component="+agentComponent,
		"-o", "jsonpath={.items[*].metadata.name}")) {
		if isNewReady(n) {
			return n
		}
	}
	ginkgo.Fail("replacement pod became Ready then vanished — race in waitReplacementReady")
	return ""
}

// --- port-forward ------------------------------------------------------------

// portForward starts a port-forward from a free local port to the agent pod's HTTP
// listener and returns the local "host:port" address plus a stop function.
func portForward(podName string) (addr string, stop func()) {
	return portForwardPort(podName, agentPodPort)
}

// portForwardGRPC is the gRPC sibling of portForward. It shares the same
// lifecycle and readiness behavior while targeting mecak8s's gRPC listener.
func portForwardGRPC(podName string) (addr string, stop func()) {
	return portForwardPort(podName, agentGRPCPort)
}

// portForwardService reaches the normal HTTP Service port rather than a selected
// Pod, so it proves the Service cannot invoke a pod-only lifecycle endpoint.
func portForwardService() (addr string, stop func()) {
	return portForwardResource("service/"+agentServiceName, agentPodPort)
}

func portForwardPort(podName string, targetPort int) (addr string, stop func()) {
	return portForwardResource("pod/"+podName, targetPort)
}

// portForwardResource starts `kubectl port-forward` from a free local port to a
// Kubernetes resource port and returns the local "host:port" address plus a stop
// function. The forward runs for the lifetime of the returned context-cancellation
// / stop call.
func portForwardResource(resource string, targetPort int) (addr string, stop func()) {
	ginkgo.GinkgoHelper()
	port := freeLocalPort()
	ctx, cancel := context.WithCancel(ginkgoSuiteCtx())
	cmd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"-n", k8sNamespace,
		resource,
		fmt.Sprintf("%d:%d", port, targetPort))
	// port-forward writes progress to stderr; capture it for failure diagnosis.
	var buf ginkgoWriter
	cmd.Stderr = &buf
	gomega.ExpectWithOffset(1, cmd.Start()).To(gomega.Succeed(),
		"start kubectl port-forward for %s", resource)

	addr = fmt.Sprintf("127.0.0.1:%d", port)
	// Wait for the forward to be accepting connections before returning, so the
	// caller's first HTTP request does not race the tunnel setup.
	gomega.Eventually(func() bool {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 30*time.Second, 500*time.Millisecond).Should(gomega.BeTrue(),
		"port-forward to pod %s never accepted on %s\n--- port-forward stderr ---\n%s",
		resource, addr, buf.String())

	return addr, func() {
		cancel()
		_ = cmd.Wait()
	}
}

// ginkgoWriter is a thread-safe bytes.Buffer suitable for an exec Cmd's Stderr
// capture (port-forward writes from its own goroutine).
type ginkgoWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (g *ginkgoWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.Write(p)
}

func (g *ginkgoWriter) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.String()
}

// freeLocalPort returns a free loopback TCP port. It binds :0, reads the
// assigned port, and closes — the standard ephemeral-port probe. The bind→close
// →hand-to-port-forward window is racy in principle against other processes
// (loopback-private in practice; FlakeAttempts covers the rare collision).
func freeLocalPort() int {
	ginkgo.GinkgoHelper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "bind free local port")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// --- HTTP API helpers --------------------------------------------------------

// createLiveSessionOverHTTP creates a session on the explicit OpenRouter/Haiku
// selector path. The live lane intentionally does not rely on deployment defaults:
// it verifies the CreateSession resolved-model echo before starting the real stream.
func createLiveSessionOverHTTP(ctx context.Context, addr string) string {
	ginkgo.GinkgoHelper()
	body, err := json.Marshal(map[string]any{
		"mode":        "default",
		"provider_id": liveProviderID,
		"model_id":    liveProviderModel,
	})
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "marshal live session create body")
	url := fmt.Sprintf("http://%s/v1/sessions", addr)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST %s", url)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusCreated),
		"create live session: status %d, body %s", resp.StatusCode, string(raw))
	var respBody struct {
		SessionID     string `json:"session_id"`
		ResolvedModel *struct {
			ProviderID string `json:"provider_id"`
			ModelID    string `json:"model_id"`
		} `json:"resolved_model"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &respBody)).To(gomega.Succeed(),
		"create live session: unmarshal %s", string(raw))
	gomega.ExpectWithOffset(1, respBody.SessionID).NotTo(gomega.BeEmpty(), "empty live session_id")
	gomega.ExpectWithOffset(1, respBody.ResolvedModel).NotTo(gomega.BeNil(),
		"live session response omitted resolved_model: %s", string(raw))
	gomega.ExpectWithOffset(1, respBody.ResolvedModel.ProviderID).To(gomega.Equal(liveProviderID),
		"live session resolved provider_id = %q, want explicit selector %q (response: %s)",
		respBody.ResolvedModel.ProviderID, liveProviderID, string(raw))
	gomega.ExpectWithOffset(1, respBody.ResolvedModel.ModelID).To(gomega.Equal(liveProviderModel),
		"live session resolved model_id = %q, want explicit selector %q (response: %s)",
		respBody.ResolvedModel.ModelID, liveProviderModel, string(raw))
	return respBody.SessionID
}

// createSessionOverHTTP creates a session via POST /v1/sessions and returns the
// session id. The request is path-free: mecak8s composition binds the configured
// deployment workspace.
func createSessionOverHTTP(ctx context.Context, addr string) string {
	ginkgo.GinkgoHelper()
	body := defaultSessionCreateBody()
	url := fmt.Sprintf("http://%s/v1/sessions", addr)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST %s", url)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusCreated),
		"create session: status %d, body %s", resp.StatusCode, string(raw))
	var respBody struct {
		SessionID string `json:"session_id"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(raw, &respBody)).To(gomega.Succeed(),
		"create session: unmarshal %s", string(raw))
	gomega.ExpectWithOffset(1, respBody.SessionID).NotTo(gomega.BeEmpty(), "empty session_id")
	return respBody.SessionID
}

// httpPrompt POSTs a prompt to /v1/sessions/{id}/prompt and returns the HTTP
// status + a bounded slice of the body. A 2xx is an SSE stream; this reads a
// bounded slice (enough to capture a 409 refusal message) — mirroring
// e2e/harness/approve.go's PromptOverHTTP. To drive a run to terminal, use
// drainRun instead.
func httpPrompt(ctx context.Context, addr, sessionID, text string) (status int, body []byte) {
	ginkgo.GinkgoHelper()
	reqBody, _ := json.Marshal(map[string]any{"text": text})
	url := fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "POST %s", url)
	defer func() { _ = resp.Body.Close() }()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, body
}

// drainRun starts a prompt run and drains the SSE stream to completion, returning
// the HTTP status. It is the "drive the run to terminal" helper: the mock
// provider completes a run quickly, and draining ensures the run has fully ended
// (the session reaches a terminal state) before subsequent assertions. A 409
// (lease held elsewhere) returns immediately — there is no stream to drain.
func drainRun(ctx context.Context, addr, sessionID, text string) (int, error) {
	ginkgo.GinkgoHelper()
	reqBody, _ := json.Marshal(map[string]any{"text": text})
	url := fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain the SSE stream fully so the run reaches terminal server-side. A 409
	// has a tiny body (the error JSON) and closes immediately. A 2xx streams
	// events until the run ends; read to EOF.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return resp.StatusCode, fmt.Errorf("drain POST %s response: %w", url, err)
	}
	return resp.StatusCode, nil
}

// httpGetSession GETs /v1/sessions/{id} and returns (status, body). Used to
// prove session persistence across a pod restart (the snapshot lives in Redis,
// so pod-B reads what pod-A wrote).
func httpGetSession(ctx context.Context, addr, sessionID string) (status int, body []byte) {
	ginkgo.GinkgoHelper()
	url := fmt.Sprintf("http://%s/v1/sessions/%s", addr, sessionID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "GET %s", url)
	defer func() { _ = resp.Body.Close() }()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, body
}

// httpDeleteSession DELETEs /v1/sessions/{id} (CloseSession → releaseLease).
// It is the explicit lease-release lever: a session-scoped lease is held for
// the session's life, and this is what ends it in-process (vs a pod restart,
// which ends it via Service.Close). Returns the HTTP status.
func httpDeleteSession(ctx context.Context, addr, sessionID string) int {
	ginkgo.GinkgoHelper()
	url := fmt.Sprintf("http://%s/v1/sessions/%s", addr, sessionID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "DELETE %s", url)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// drainRunSoft is drainRun without the hard failure on a transport error: it
// returns the status and ok=false on a dial/HTTP error. It is the Eventually-
// safe probe for takeover assertions where a transient connection blip (the
// survivor mid-acquire) should RETRY, not abort the attempt.
func drainRunSoft(ctx context.Context, addr, sessionID, text string) (status int, ok bool) {
	ginkgo.GinkgoHelper()
	status, err := drainRun(ctx, addr, sessionID, text)
	return status, err == nil
}

// --- live-provider patching (the live LLM e2e path) ------------------------

// liveProviderPatch returns a JSON Patch that preserves the chart-rendered
// container configuration while switching only the provider selectors and key
// reference. Pointer fields distinguish absent args/env (JSON Patch add) from
// present fields (replace).
func liveProviderPatch(deploymentJSON []byte) ([]byte, error) {
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string            `json:"name"`
						Args *[]string         `json:"args"`
						Env  *[]map[string]any `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(deploymentJSON, &deployment); err != nil {
		return nil, fmt.Errorf("decode agent Deployment: %w", err)
	}

	containerIndex := -1
	for i, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == agentComponent {
			containerIndex = i
			break
		}
	}
	if containerIndex < 0 {
		return nil, fmt.Errorf("agent Deployment has no %q container", agentComponent)
	}
	container := deployment.Spec.Template.Spec.Containers[containerIndex]

	var currentArgs []string
	if container.Args != nil {
		currentArgs = *container.Args
	}
	args := liveProviderArgs(currentArgs)

	keyRef := map[string]any{
		"name": "OPENROUTER_API_KEY",
		"valueFrom": map[string]any{"secretKeyRef": map[string]any{
			"name": liveProviderSecret,
			"key":  "OPENROUTER_API_KEY",
		}},
	}

	type patchOp struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	fieldOp := func(present bool) string {
		if present {
			return "replace"
		}
		return "add"
	}
	base := fmt.Sprintf("/spec/template/spec/containers/%d", containerIndex)
	patch := []patchOp{{Op: fieldOp(container.Args != nil), Path: base + "/args", Value: args}}
	if container.Env == nil {
		patch = append(patch, patchOp{Op: "add", Path: base + "/env", Value: []map[string]any{keyRef}})
	} else {
		envPath := base + "/env/-"
		envOp := "add"
		for i, item := range *container.Env {
			if item["name"] == "OPENROUTER_API_KEY" {
				envPath = fmt.Sprintf("%s/env/%d", base, i)
				envOp = "replace"
				break
			}
		}
		patch = append(patch, patchOp{Op: envOp, Path: envPath, Value: keyRef})
	}
	return json.Marshal(patch)
}

func liveProviderArgs(current []string) []string {
	args := make([]string, 0, len(current)+2)
	for i := 0; i < len(current); i++ {
		arg := current[i]
		name := arg
		if before, _, ok := strings.Cut(arg, "="); ok {
			name = before
		}
		switch name {
		case "--mock", "--model", "--default-provider", "--default-model":
			if arg == name && i+1 < len(current) && !strings.HasPrefix(current[i+1], "-") {
				i++
			}
			continue
		default:
			args = append(args, arg)
		}
	}
	return append(args,
		"--default-provider="+liveProviderID,
		"--default-model="+liveProviderModel,
	)
}

type boundedDrainWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (w *boundedDrainWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.buf.Len()
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		_, _ = w.buf.Write(p[:remaining])
	}
	return len(p), nil // keep draining the child even after the capture fills.
}

func (w *boundedDrainWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func liveRolloutDiagnostics(parent context.Context, key string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	defer cancel()

	var report strings.Builder
	run := func(title string, args ...string) {
		captured := &boundedDrainWriter{limit: 16 * 1024}
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		cmd.Stdout = captured
		cmd.Stderr = captured
		err := cmd.Run()
		fmt.Fprintf(&report, "\n--- %s ---\n", title)
		if err != nil {
			fmt.Fprintf(&report, "command failed: %v\n", err)
		}
		report.WriteString(boundedRedacted(captured.String(), key, 16*1024))
	}
	run("deployment replicas", "get", "deployment/mecak8s-agent", "-n", k8sNamespace,
		"-o", "custom-columns=NAME:.metadata.name,DESIRED:.spec.replicas,CURRENT:.status.replicas,UPDATED:.status.updatedReplicas,READY:.status.readyReplicas,AVAILABLE:.status.availableReplicas,UNAVAILABLE:.status.unavailableReplicas")
	run("agent pod status", "get", "pods", "-n", k8sNamespace,
		"-l", "app.kubernetes.io/component="+agentComponent,
		"-o", `custom-columns=NAME:.metadata.name,PHASE:.status.phase,READY:.status.containerStatuses[*].ready,RESTARTS:.status.containerStatuses[*].restartCount,WAITING:.status.containerStatuses[*].state.waiting.reason`)

	podOut, _ := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", k8sNamespace,
		"-l", "app.kubernetes.io/component="+agentComponent, "-o", "name").Output()
	for _, pod := range strings.Fields(string(podOut)) {
		run(pod+" agent logs (current)", "logs", "-n", k8sNamespace, pod,
			"-c", agentComponent, "--tail=80", "--limit-bytes=16384")
		run(pod+" agent logs (previous)", "logs", "-n", k8sNamespace, pod,
			"-c", agentComponent, "--previous", "--tail=80", "--limit-bytes=16384")
	}
	run("recent warning events", "get", "events", "-n", k8sNamespace,
		"--field-selector=type=Warning", "--sort-by=.lastTimestamp",
		"-o", "custom-columns=LAST:.lastTimestamp,REASON:.reason,OBJECT:.involvedObject.name,MESSAGE:.message")
	return boundedRedacted(report.String(), key, 64*1024)
}

func boundedRedacted(value, secret string, limit int) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[diagnostics truncated]...\n"
}

// enableLiveProvider swaps the agent Deployment from --mock to the real
// OpenRouter provider + the default-lane model, staged via a k8s Secret so the
// OPENROUTER_API_KEY never reaches a pod arg, a manifest, or a log. It is called
// ONCE in BeforeSuite (after the mock pods are Ready) when OPENROUTER_API_KEY is
// set in the test process's environment. The mock specs then run on the real-
// provider pods (their assertions are provider-agnostic), and the live specs run
// after. No un-patching — kind delete cluster (AfterSuite) destroys everything.
//
// SECURITY: key is supplied by BeforeSuite after it reads and removes
// OPENROUTER_API_KEY from the Go test process environment. It is written to a
// Secret via a `kubectl apply -f -` of a `stringData` JSON manifest (kubectl
// carries the value to the API server over its stdin; it is never echoed to
// stdout/stderr, never a bare argv token, never in a file). The Secret NAME is
// the only identifier surfaced in logs — the value never is. The patch then
// consumes it via an envFrom secretKeyRef, so the key reaches the pod ONLY
// through the Secret, never a pod arg or a Deployment spec field.
func enableLiveProvider(key string) {
	ginkgo.GinkgoHelper()
	ctx := ginkgoSuiteCtx()
	gomega.ExpectWithOffset(1, key).NotTo(gomega.BeEmpty(),
		"enableLiveProvider called without OPENROUTER_API_KEY")

	// 1. Stage the key as a k8s Secret. `kubectl apply -f -` over a stringData
	//    JSON manifest is idempotent (create-or-replace) without a prior delete.
	ginkgo.By("creating the openrouter-key Secret (key staged from env, never logged)")
	// stringData keeps the key as a plaintext field inside the manifest (kubectl
	// converts it to base64 data server-side); the manifest rides stdin, never a
	// bare argv token, so it never appears in a shell history or ps listing.
	envSecretApply := fmt.Sprintf(
		`{"apiVersion":"v1","kind":"Secret","metadata":{"name":%q,"namespace":%q},"type":"Opaque","stringData":{"OPENROUTER_API_KEY":%q}}`,
		liveProviderSecret, k8sNamespace, key)
	applySecret := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applySecret.Stdin = strings.NewReader(envSecretApply)
	secretOutput := &boundedDrainWriter{limit: 16 * 1024}
	applySecret.Stdout = secretOutput
	applySecret.Stderr = secretOutput
	err := applySecret.Run()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl apply secret %s failed\n--- output ---\n%s", liveProviderSecret,
		boundedRedacted(secretOutput.String(), key, 16*1024))

	// 2. Patch the Deployment: preserve every chart-rendered flag and env entry,
	//    dropping only --mock and conflicting provider/model selectors before
	//    appending one OpenRouter selector pair and upserting the Secret key ref.
	ginkgo.By("patching mecak8s-agent to the real OpenRouter provider + model")
	deploymentJSON, err := exec.CommandContext(ctx, "kubectl", "get",
		"deployment/mecak8s-agent", "-n", k8sNamespace, "-o", "json").Output()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl get deployment before live-provider patch failed")
	patch, err := liveProviderPatch(deploymentJSON)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"build live-provider Deployment patch")
	patchOut, err := exec.CommandContext(ctx, "kubectl", "patch",
		"deployment/mecak8s-agent", "-n", k8sNamespace,
		"--type=json", "-p", string(patch)).CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"kubectl patch deployment to live provider failed\n--- output ---\n%s", patchOut)

	// 3. Wait for the rollout: the RollingUpdate (maxSurge:1, maxUnavailable:0)
	//    spins a new pod first, so readiness gates on the live provider's startup
	//    (the openrouter adapter is construction-time only; no network at startup,
	//    but the startupProbe still must clear).
	ginkgo.By("waiting for the live-provider rollout to complete")
	rolloutCtx, rolloutCancel := context.WithTimeout(ctx, 300*time.Second)
	defer rolloutCancel()
	rolloutOut, err := boundedCommandOutput(rolloutCtx, 16*1024, "kubectl", "rollout", "status",
		"deployment/mecak8s-agent", "-n", k8sNamespace,
		"--timeout=290s")
	if err != nil {
		diagnostics := liveRolloutDiagnostics(ctx, key)
		ginkgo.Fail(fmt.Sprintf(
			"kubectl rollout status (live provider) failed: %v\n--- output ---\n%s%s",
			err, boundedRedacted(string(rolloutOut), key, 16*1024), diagnostics), 1)
	}

	ginkgo.By("waiting for all mecak8s pods to be Ready (after the live-provider patch)")
	waitPodsReady()

	// Refresh the captured pod names: the rollout replaced both pods. podNames()
	// filters to Ready pods only, so the terminating old pods (still in the pod
	// list during terminationGracePeriodSeconds) are excluded automatically.
	agentPods = podNames()
	ginkgo.GinkgoWriter.Printf("live provider enabled; agent pods after rollout: pod-A=%s pod-B=%s\n",
		agentPods[0], agentPods[1])
}

// --- SSE result parsing ------------------------------------------------------

// sseResult captures the terminal `result` event from a prompt SSE stream. The
// HTTP relay frames each Event as one `data: <json>\n\n` line; the terminal
// event carries type="result" with a nested result{stop,text,usage}. This parses
// the stream incrementally (a live model turn can emit many events over 10-30s)
// and returns the first result event seen, or an error if the stream ended
// without one. It is the "drive a real run to terminal + assert end_turn + real
// usage" helper for the live specs — distinct from drainRun (which discards the
// body, fine for the mock's instant completion but blind to a real run's stop).
type sseResult struct {
	Stop            string   `json:"stop"`
	Text            string   `json:"text"`
	Input           int64    `json:"input_tokens"`
	Output          int64    `json:"output_tokens"`
	EventTypes      []string `json:"-"`
	NoProgressTexts []string `json:"-"`
}

func (r *sseResult) diagnostic() string {
	return fmt.Sprintf("events=%v result_text=%q no_progress=%q", r.EventTypes, r.Text, r.NoProgressTexts)
}

// sseDiagnosticText bounds model- or server-produced text included in a failed
// live assertion. The live lane never sends credentials in prompts, but diagnostics
// must not turn an unexpected verbose event into unbounded test output.
func sseDiagnosticText(text string) string {
	const limit = 512
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "...[truncated]"
}

const maxSSEDiagnosticEvents = 12

// drainRunSSE starts a prompt run, drains the SSE stream to terminal, and parses
// the terminal `result` event (stop + usage). It is the live-spec counterpart of
// drainRun: instead of discarding the body, it scans the stream for the result
// event so the live specs can assert stop=end_turn + real (non-zero) usage — the
// difference between "the run completed" and "a real model produced output".
//
// A non-2xx status returns (status, nil, nil) immediately (the caller asserts on
// the status — e.g. the 409 lease-conflict path). On a 2xx stream that ends
// without a result event, it returns the status + a non-nil error.
func drainRunSSE(ctx context.Context, addr, sessionID, text string) (status int, res *sseResult, err error) {
	ginkgo.GinkgoHelper()
	reqBody, _ := json.Marshal(map[string]any{"text": text})
	url := fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	if status != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return status, nil, nil
	}
	// Scan the SSE stream for the `data:` line carrying type:"result". Each frame
	// is `data: <json>\n\n`; a bufio.Scanner over lines is sufficient.
	scanner := bufio.NewScanner(resp.Body)
	// A single event JSON is small, but a reasoning turn's text can be long; raise
	// the per-line budget so a large result text is not truncated.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var eventTypes, noProgressTexts []string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" {
			continue
		}
		var ev struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Result *struct {
				Stop  string `json:"stop"`
				Text  string `json:"text"`
				Usage *struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"result"`
		}
		if jerr := json.Unmarshal([]byte(payload), &ev); jerr != nil {
			continue // not a JSON event frame (e.g. a keep-alive comment); skip
		}
		if len(eventTypes) < maxSSEDiagnosticEvents {
			eventTypes = append(eventTypes, ev.Type)
		}
		if ev.Type == "no_progress" && len(noProgressTexts) < maxSSEDiagnosticEvents {
			noProgressTexts = append(noProgressTexts, sseDiagnosticText(ev.Text))
		}
		if ev.Type == "result" && ev.Result != nil {
			res = &sseResult{
				Stop:            ev.Result.Stop,
				Text:            sseDiagnosticText(ev.Result.Text),
				EventTypes:      eventTypes,
				NoProgressTexts: noProgressTexts,
			}
			if ev.Result.Usage != nil {
				res.Input = ev.Result.Usage.InputTokens
				res.Output = ev.Result.Usage.OutputTokens
			}
			return status, res, nil
		}
	}
	if serr := scanner.Err(); serr != nil {
		return status, nil, fmt.Errorf("scanning SSE stream: %w", serr)
	}
	return status, nil, fmt.Errorf("SSE stream ended without a result event")
}

// promptLiveProviderSm is the canary prompt the live specs drive: a cheap
// single-turn run that completes with stop=end_turn and real (non-zero) usage
// without invoking any tools. It mirrors e2e/provider_test.go's default-lane
// canary so the same verified-cheap lane + phrasing is exercised through the pod.
const promptLiveProviderSm = "Reply with exactly the single word: ok. Do not call any tools."

// ginkgoSuiteCtx returns a context cancelled when the ginkgo suite exits. It is
// the parent for all kubectl/kind/ko commands so a suite abort tears them down.
func ginkgoSuiteCtx() context.Context { return suiteCtx }

func boundedCommandOutput(ctx context.Context, limit int, name string, args ...string) ([]byte, error) {
	captured := &boundedDrainWriter{limit: limit}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = captured
	cmd.Stderr = captured
	err := cmd.Run()
	return []byte(captured.String()), err
}

// runCmd runs a command under the suite context and fails the spec on a non-zero
// exit, attaching combined output. It is the loud variant for commands whose
// failure is fatal to the spec.
func runCmd(ctx context.Context, name string, args ...string) string {
	ginkgo.GinkgoHelper()
	out, err := boundedCommandOutput(ctx, 1<<20, name, args...)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"%s %s failed\n--- output ---\n%s", name, strings.Join(args, " "), out)
	return string(out)
}

// runCmdQuiet runs a command and returns its combined output WITHOUT failing on
// a non-zero exit. It is the probe variant — used in Eventually loops where a
// transient failure (pod not yet ready) is expected and retried.
func runCmdQuiet(name string, args ...string) string {
	out, _ := boundedCommandOutput(ginkgoSuiteCtx(), 1<<20, name, args...)
	return string(out)
}

// repoRoot returns the repository root directory so ko/kubectl commands that
// reference relative paths (./cmd/mecak8s, deploy/helm/mecak8s/) resolve correctly
// regardless of the Go test's working directory.
func repoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "." // fallback: the test's CWD (usually the repo root anyway)
	}
	return strings.TrimSpace(string(out))
}

// formatBody clamps a response body for an assertion message.
func formatBody(b []byte) string {
	if len(b) > 1024 {
		return string(b[:1024]) + "…(truncated)"
	}
	return string(b)
}

// expectLeaseConflict asserts an HTTP 409 response is the LEASE-ELSEWHERE
// refusal, not an unrelated 409. HTTP 409 is returned for two distinct
// conditions — ErrSessionLeasedElsewhere ("server: session is leased by another
// process") AND ErrNoActiveRun — so a bare status==409 check would pass for the
// wrong reason. The handler writes err.Error() as the body, so this verifies the
// body carries the lease signal. Used at every 409 assertion that is meant to
// PROVE lease exclusion (lease_test.go pod-B refusal, failover_test.go force-
// delete control case).
func expectLeaseConflict(status int, body []byte) {
	ginkgo.GinkgoHelper()
	gomega.ExpectWithOffset(1, status).To(gomega.Equal(http.StatusConflict),
		"want HTTP 409 Conflict (lease held elsewhere), got %d\n--- body ---\n%s",
		status, formatBody(body))
	gomega.ExpectWithOffset(1, strings.Contains(string(body), "leased by another process")).
		To(gomega.BeTrue(),
			"409 body does not carry the lease-elsewhere signal (want %q in body)\n--- body ---\n%s",
			"leased by another process", formatBody(body))
}

// leaseTTL is the configured session-lease TTL (30s; the pods run with the
// default --session-lease-ttl, and internal/adapter/k8slease defaults to 30s).
// It bounds how long a force-killed holder's lease blocks a survivor and is the
// timing window the failover control case must observe its 409 WITHIN.
const leaseTTL = 30 * time.Second
