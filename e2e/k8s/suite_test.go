//go:build kind_e2e

package k8s_e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// suiteCtx is the suite-lifetime context cancelled in AfterSuite. It parents
// every kubectl/kind/ko command (via ginkgoSuiteCtx) so a suite abort tears
// down long-running port-forwards and waits.
var suiteCtx, suiteCancel = context.WithCancel(context.Background())

// agentPods holds the two agent pod names captured in BeforeSuite, addressed as
// pod-A / pod-B across the suite. They are refreshed after any delete by the
// individual specs that delete pods.
var agentPods []string

// TestK8sE2E is the ginkgo entry point. It is gated behind the kind_e2e build
// tag, so `task test` (no tag) never compiles or runs it.
func TestK8sE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "mecatl kind k8s e2e suite (ADR 0048)")
}

// BeforeSuite runs the cluster lifecycle (MECAK8S-PLAN §4h):
//  1. require kind/ko/helm/kubectl (skip gracefully if missing)
//  2. kind create cluster
//  3. ko build compiles the mecak8s image into the local container daemon
//     under a deterministic ref (ko.local/mecak8s:e2e)
//  4. save the image to a tarball + kind load image-archive into the cluster node
//  5. helm upgrade --install the deploy/helm/mecak8s chart with the Kind values
//     profile, pointed at that image (namespace created + PSS-labelled first)
//  6. kubectl wait for all part-of=mecak8s pods Ready (agent replicas + Redis)
//  7. capture the two agent pod names
//
// AfterSuite deletes the cluster. The cluster is created ONCE per suite run and
// shared across the three specs (Serial + Ordered), so the ~30s kind bring-up
// cost is paid once.
var _ = ginkgo.BeforeSuite(func() {
	requireTools()

	ginkgo.By("creating the kind cluster")
	kindCreateCluster()

	ginkgo.By("building the mecak8s image with ko")
	koBuildMecak8sImage()

	ginkgo.By("saving the image to a tarball + loading it into the kind node")
	saveAndLoadImage(e2eImageRef)

	ginkgo.By("installing the deploy/helm/mecak8s chart (values-kind.yaml)")
	helmInstallMecak8sChart()

	ginkgo.By("waiting for all mecak8s pods to be Ready")
	waitPodsReady()

	ginkgo.By("capturing the agent pod names")
	agentPods = podNames()
	ginkgo.GinkgoWriter.Printf("agent pods: pod-A=%s pod-B=%s\n", agentPods[0], agentPods[1])

	// LIVE PROVIDER: capture the key once, then remove it from this process before
	// any kubectl invocation can inherit it through a credential plugin. The local
	// variable is needed only to stage the Secret below; liveProviderConfigured
	// retains the non-secret state used by the later live-spec gate.
	key := os.Getenv("OPENROUTER_API_KEY")
	if key != "" {
		if err := os.Unsetenv("OPENROUTER_API_KEY"); err != nil {
			ginkgo.Fail("remove OPENROUTER_API_KEY from the Kind test process: "+err.Error(), 1)
			return
		}
		liveProviderConfigured = true
		ginkgo.By("OPENROUTER_API_KEY is set — enabling the live provider")
		enableLiveProvider(key)
	} else {
		ginkgo.By("OPENROUTER_API_KEY is not set — running the mock suite only (live specs will skip)")
	}
})

// AfterSuite tears the cluster down unconditionally (even on failure) so a
// failed run does not leak a kind cluster. suiteCancel also stops any in-flight
// port-forward / kubectl command goroutines. It mirrors the existing e2e
// suite's AfterSuite teardown pattern.
var _ = ginkgo.AfterSuite(func() {
	suiteCancel()
	ginkgo.By("deleting the kind cluster")
	kindDeleteCluster()
})

// The suite is Serial + Ordered: the three specs share ONE cluster and manipulate
// the SAME pod set (deleting pods changes the roster), so they cannot run
// concurrently. ContinueOnFailure keeps a mid-suite failure from skipping the
// teardown (the AfterSuite cluster delete always runs).
var _ = ginkgo.Describe("mecak8s cloud-native properties (ADR 0048)", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	leaseExclusionSpecs()
	drainIsolationSpecs()
	failoverSpecs()
	persistenceSpecs()
	liveSpecs()
})

// refreshPods re-reads the two agent pod names. Called by specs that delete a
// pod, so the suite's pod-A/pod-B addressing stays accurate after a replacement.
func refreshPods() {
	ginkgo.GinkgoHelper()
	agentPods = podNames()
}

// shortCtx returns a context with a per-call timeout, parented at the suite
// context. It is the per-spec command/HTTP timeout helper.
func shortCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(suiteCtx, timeout)
}
