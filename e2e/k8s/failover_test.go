//go:build kind_e2e

package k8s_e2e_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// failoverSpecs proves graceful failover releases the session lease BEFORE the
// TTL (ADR 0048 §4h test 2). The contrast is the control case:
//
//   - GRACEFUL: `kubectl delete pod` pod-A → preStop /drain + SIGTERM →
//     boundedShutdown → Service.Close → releaseLease for every held lease. The
//     k8s Lease object is DELETED immediately, so a survivor acquires the
//     session's lease on its FIRST run-entry — well inside the 30s TTL.
//
//   - FORCE (control): hard-stop pod-B's application container through the
//     kind node CRI, then force-delete its Pod object → NO graceful shutdown →
//     NO releaseLease. The k8s Lease object REMAINS (holder = dead pod's owner)
//     until its TTL lapses. A survivor's run-entry is REFUSED with 409
//     immediately after the replacement is Ready, proving the LEASE (not mere
//     pod absence) is what gates takeover — the force-killed pod is gone, yet
//     its lease still blocks.
//
// Together: graceful = immediate 200 (lease released), force = 409 until TTL
// (lease survives pod death). This is the honest, meaningful cloud-native
// failover property — the pod is disposable, the lease is the coordination
// primitive, and a clean shutdown hands off without waiting for the TTL.
func failoverSpecs() {
	ginkgo.Describe("graceful failover releases the lease before TTL", func() {
		ginkgo.It("graceful pod delete lets a survivor take over immediately; force delete blocks until TTL",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				refreshPods()
				podA, podB := agentPods[0], agentPods[1]

				// --- GRACEFUL case: lease released before TTL. ---
				ginkgo.By("port-forwarding to pod-A and pod-B")
				addrA, stopA := portForward(podA)
				defer stopA()
				addrB, stopB := portForward(podB)
				defer stopB()

				ginkgo.By("creating a session + run on pod-A (lease held by pod-A)")
				cCtx, cCancel := shortCtx(30 * time.Second)
				gracefulSession := createSessionOverHTTP(cCtx, addrA)
				cCancel()
				rCtx, rCancel := shortCtx(60 * time.Second)
				status, drainErr := drainRun(rCtx, addrA, gracefulSession, "graceful-case holder")
				rCancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(), "drain pod-A run (graceful case)")
				gomega.Expect(status).To(gomega.Equal(http.StatusOK), "pod-A run-start (graceful case)")

				ginkgo.By("gracefully deleting pod-A (preStop /drain + SIGTERM → releaseLease)")
				// Snapshot the pod roster BEFORE the delete so waitReplacementReady
				// waits for the genuinely-NEW replacement pod, not the survivor pod-B
				// (which was Ready all along and would satisfy a name!=deletedName
				// check on its first poll).
				preDeleteGraceful := podNames()
				kubectlDeletePod(podA, false)
				// The port-forward to the dead pod is now stale; the stopA defer will
				// cancel it harmlessly. Wait for a fresh replacement.
				replacementA := waitReplacementReady(preDeleteGraceful)

				ginkgo.By("posting the session to pod-B after graceful delete → immediate 200 (before TTL)")
				// Measure takeover latency: graceful release means it succeeds FAST
				// (well under the 30s TTL). A wall-clock bound proves "before TTL".
				start := time.Now()
				takeCtx, takeCancel := shortCtx(60 * time.Second)
				takeStatus, drainErr := drainRun(takeCtx, addrB, gracefulSession, "graceful takeover")
				takeCancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(), "drain pod-B graceful takeover")
				took := time.Since(start)
				gomega.Expect(takeStatus).To(gomega.Equal(http.StatusOK),
					"pod-B run-start after graceful pod-A delete = HTTP %d, want 200 (lease should be released)\n--- took %s ---",
					takeStatus, took)
				gomega.Expect(took).To(gomega.BeNumerically("<", leaseTTL),
					"graceful failover takeover took %s, want < lease TTL %s (the clean release must beat the TTL)",
					took, leaseTTL)

				// --- FORCE control case: lease survives pod death until TTL. ---
				ginkgo.By("creating a fresh session + run on pod-B (lease held by pod-B)")
				c2Ctx, c2Cancel := shortCtx(30 * time.Second)
				forceSession := createSessionOverHTTP(c2Ctx, addrB)
				c2Cancel()
				r2Ctx, r2Cancel := shortCtx(60 * time.Second)
				status, drainErr = drainRun(r2Ctx, addrB, forceSession, "force-case holder")
				r2Cancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(), "drain pod-B run (force case)")
				gomega.Expect(status).To(gomega.Equal(http.StatusOK), "pod-B run-start (force case)")
				// acquiredAt is the moment pod-B's holding run completed with the
				// lease held. The force-delete control case must observe its 409
				// BEFORE the lease TTL lapses (the k8s Lease object expires at
				// acquiredAt+leaseTTL); if waitReplacementReady eats that whole
				// window the 409 is no longer observable and the leg is skipped
				// rather than flaked.
				acquiredAt := time.Now()

				ginkgo.By("force-deleting pod-B (NO graceful shutdown → NO releaseLease)")
				// Snapshot BEFORE the delete: waitReplacementReady must return pod-B's
				// genuinely-new replacement, not pod-A's replacement (replacementA),
				// which is a survivor relative to this delete's roster.
				preDeleteForce := podNames()
				kubectlDeletePod(podB, true)
				// Wait for pod-B's replacement too, so the Deployment is stable and the
				// survivor we probe (replacementA) is not the only live pod skewing
				// readiness.
				_ = waitReplacementReady(preDeleteForce)

				ginkgo.By("port-forwarding to pod-A's replacement (the survivor)")
				addrA2, stopA2 := portForward(replacementA)
				defer stopA2()

				// If the replacement readiness consumed the lease TTL window, the
				// k8s Lease has already expired and the survivor would correctly
				// return 200 — observing a 409 here is no longer possible, so skip
				// this leg with a clear message instead of flaking.
				if elapsed := time.Since(acquiredAt); elapsed >= leaseTTL {
					ginkgo.Skip(fmt.Sprintf(
						"replacement took longer than lease TTL (elapsed %s >= TTL %s); cannot assert pre-TTL exclusion",
						elapsed, leaseTTL))
				}

				ginkgo.By("immediately probing the force-case session on the survivor → 409 (lease survives pod death)")
				// The force-killed pod-B is GONE, yet its k8s Lease object remains
				// (holder = dead pod-B's owner) until the TTL. The survivor's run-entry
				// is REFUSED — proving the LEASE, not pod presence, gates takeover. The
				// 409 body must carry the lease-elsewhere signal (not an unrelated
				// ErrNoActiveRun 409), and it must be observed BEFORE the TTL.
				probeCtx, probeCancel := shortCtx(15 * time.Second)
				fStatus, fBody := httpPrompt(probeCtx, addrA2, forceSession, "force-case survivor")
				probeCancel()
				expectLeaseConflict(fStatus, fBody)
				gomega.Expect(time.Since(acquiredAt)).To(gomega.BeNumerically("<", leaseTTL),
					"force-delete 409 observed at %s after lease acquire, want < lease TTL %s (the lease must still be live)",
					time.Since(acquiredAt), leaseTTL)
			})
	})
}
