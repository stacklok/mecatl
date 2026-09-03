//go:build kind_e2e

package k8s_e2e_test

import (
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// leaseExclusionSpecs is THE cloud-native PROOF: cross-replica single-writer
// enforcement via the per-session coordination.k8s.io Lease (ADR 0048 §4h test
// 1). Two storage-free agent replicas share one Redis store + one k8s lease
// namespace. Replica A acquires the session lease on run-entry and HOLDS it for
// the session's life (the lease is session-scoped — released by CloseSession /
// shutdown, never per-run; see internal/adapter/server/service.go acquireLease).
// Replica B, live over the same Redis + lease namespace, tries to start a run on
// the SAME session and is REFUSED with 409 Conflict (ErrSessionLeasedElsewhere)
// while A holds the lease. After A explicitly closes the session (DELETE
// /v1/sessions/{id} → releaseLease), B's run-start succeeds — proving both the
// exclusion AND the release-enables-takeover half of the property.
//
// It is the live-cluster counterpart of the offline two-Build gate
// (internal/app/lease_exclusion_test.go), which exercises the identical
// acquire-at-run-entry path through composition. This spec adds what only two
// real pods over a real k8s API server can confirm: a coordination.k8s.io Lease
// held by one pod's Service genuinely blocks a SECOND pod's run-entry, and a
// clean release lets the survivor take over.
func leaseExclusionSpecs() {
	ginkgo.Describe("cross-replica lease exclusion (THE proof)", func() {
		ginkgo.It("refuses a second replica's run while the lease is held, and lets it take over after a clean release",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				refreshPods()
				podA, podB := agentPods[0], agentPods[1]

				ginkgo.By("port-forwarding to each agent pod directly")
				addrA, stopA := portForward(podA)
				defer stopA()
				addrB, stopB := portForward(podB)
				defer stopB()

				ginkgo.By("creating a session on pod-A (persisted to Redis)")
				sessCtx, sessCancel := shortCtx(30 * time.Second)
				sessionID := createSessionOverHTTP(sessCtx, addrA)
				sessCancel()

				ginkgo.By("starting a run on pod-A and driving it to terminal (lease held for the session's life)")
				runCtx, runCancel := shortCtx(60 * time.Second)
				status, drainErr := drainRun(runCtx, addrA, sessionID, "hello from the lease holder")
				runCancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(),
					"pod-A holder run stream must reach EOF cleanly")
				gomega.Expect(status).To(gomega.Equal(http.StatusOK),
					"pod-A run-start should succeed (it holds the lease), got status %d", status)

				ginkgo.By("posting the SAME session to pod-B while A holds the lease → 409")
				probeCtx, probeCancel := shortCtx(30 * time.Second)
				bStatus, bBody := httpPrompt(probeCtx, addrB, sessionID, "second writer")
				probeCancel()
				expectLeaseConflict(bStatus, bBody)

				ginkgo.By("closing the session on pod-A (DELETE → releaseLease)")
				delCtx, delCancel := shortCtx(30 * time.Second)
				delStatus := httpDeleteSession(delCtx, addrA, sessionID)
				delCancel()
				gomega.Expect(delStatus).To(gomega.Equal(http.StatusNoContent),
					"pod-A DELETE session = HTTP %d, want 204", delStatus)

				ginkgo.By("posting to pod-B after the clean release → 200 (takeover)")
				gomega.Eventually(func(g gomega.Gomega) {
					takeCtx, takeCancel := shortCtx(30 * time.Second)
					defer takeCancel()
					st, ok := drainRunSoft(takeCtx, addrB, sessionID, "takeover after release")
					g.Expect(ok).To(gomega.BeTrue(), "pod-B takeover probe transport error")
					g.Expect(st).To(gomega.Equal(http.StatusOK),
						"pod-B run-start after release = HTTP %d, want 200", st)
				}, 60*time.Second, 2*time.Second).Should(gomega.Succeed(),
					"pod-B run-start after pod-A released the lease did not succeed (want 200)")
			})
	})
}
