//go:build kind_e2e

package k8s_e2e_test

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// persistenceSpecs proves session persistence across an agent pod restart
// (ADR 0048 §4h test 3). The agent pods are STORAGE-FREE — every session
// snapshot + the durable event log lives in Redis (a managed service), not pod
// memory or disk. So killing an agent pod loses ZERO session state: a successor
// pod reloads the snapshot from Redis and continues.
//
// The flow: create a session + run on pod-A, drive it to terminal (the mock
// provider completes — the session reaches `completed`). Delete pod-A. Wait for
// the replacement. GET the session on pod-B → it survives (Redis-backed, state
// `completed`). POST a follow-up prompt on pod-B → succeeds: pod-B reopens the
// completed session (issue #51 — Reopen drives `completed → idle`, so a resumed
// run re-enters the loop cleanly) and acquires the lease pod-A released on
// shutdown. This is the cloud-native disposability property: the pod is
// disposable, the session is not.
func persistenceSpecs() {
	ginkgo.Describe("session persistence across pod restart (Redis)", func() {
		ginkgo.It("a session survives its pod's restart and is resumable on a successor",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				refreshPods()
				podA, podB := agentPods[0], agentPods[1]

				ginkgo.By("port-forwarding to pod-A and pod-B")
				addrA, stopA := portForward(podA)
				defer stopA()
				addrB, stopB := portForward(podB)
				defer stopB()

				ginkgo.By("creating a session + run on pod-A and driving it to terminal")
				cCtx, cCancel := shortCtx(30 * time.Second)
				sessionID := createSessionOverHTTP(cCtx, addrA)
				cCancel()
				rCtx, rCancel := shortCtx(60 * time.Second)
				status, drainErr := drainRun(rCtx, addrA, sessionID, "persist me across restart")
				rCancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(), "drain pod-A run (persistence case)")
				gomega.Expect(status).To(gomega.Equal(http.StatusOK), "pod-A run-start (persistence case)")

				// Sanity: the session is now terminal on pod-A (completed). The snapshot
				// is in Redis.
				ginkgo.By("confirming the session is terminal on pod-A before the restart")
				gCtx, gCancel := shortCtx(30 * time.Second)
				preStatus, preBody := httpGetSession(gCtx, addrA, sessionID)
				gCancel()
				gomega.Expect(preStatus).To(gomega.Equal(http.StatusOK),
					"GET session on pod-A before restart = HTTP %d\n--- body ---\n%s",
					preStatus, formatBody(preBody))
				// Capture the session's CONTENT counters (turns + tool_calls) BEFORE
				// the restart. A fresh empty session or a stub would also return 200
				// + state=completed, so asserting the SAME non-zero counters survive
				// the restart is what actually proves Redis-backed persistence — not
				// just a 200. The GET response carries these as top-level JSON fields
				// (internal/adapter/server/http.go sessionResp: turns / tool_calls).
				var preSnap struct {
					State     string `json:"state"`
					Turns     int    `json:"turns"`
					ToolCalls int    `json:"tool_calls"`
				}
				gomega.Expect(json.Unmarshal(preBody, &preSnap)).To(gomega.Succeed(),
					"unmarshal pod-A session snapshot: %s", formatBody(preBody))
				gomega.Expect(preSnap.State).To(gomega.Equal("completed"),
					"session should be terminal (completed) after the run, got state %q", preSnap.State)
				gomega.Expect(preSnap.Turns).To(gomega.BeNumerically(">", 0),
					"session turns should be non-zero after a run (proves content exists to persist), got %d", preSnap.Turns)

				ginkgo.By("deleting pod-A (graceful → Service.Close releases the lease)")
				preDeletePersist := podNames()
				kubectlDeletePod(podA, false)
				// Wait for the replacement so the Deployment is stable before probing pod-B.
				_ = waitReplacementReady(preDeletePersist)

				ginkgo.By("GETting the session on pod-B → it survives (Redis-backed)")
				// pod-B never held this session in memory; it reads the snapshot from
				// Redis. A 200 with state=completed proves persistence.
				getCtx, getCancel := shortCtx(30 * time.Second)
				postStatus, postBody := httpGetSession(getCtx, addrB, sessionID)
				getCancel()
				gomega.Expect(postStatus).To(gomega.Equal(http.StatusOK),
					"GET session on pod-B after pod-A restart = HTTP %d, want 200 (Redis persistence)\n--- body ---\n%s",
					postStatus, formatBody(postBody))
				var postSnap struct {
					State     string `json:"state"`
					Turns     int    `json:"turns"`
					ToolCalls int    `json:"tool_calls"`
				}
				gomega.Expect(json.Unmarshal(postBody, &postSnap)).To(gomega.Succeed(),
					"unmarshal pod-B session snapshot: %s", formatBody(postBody))
				gomega.Expect(postSnap.State).To(gomega.Equal("completed"),
					"session state on pod-B after restart = %q, want completed (Redis persisted it)", postSnap.State)
				// The CONTENT counters must match what pod-A wrote — same non-zero
				// turns + tool_calls — proving the snapshot (not just an empty stub)
				// survived the pod restart via Redis.
				gomega.Expect(postSnap.Turns).To(gomega.Equal(preSnap.Turns),
					"session turns on pod-B after restart = %d, want %d (Redis persisted pod-A's content)",
					postSnap.Turns, preSnap.Turns)
				gomega.Expect(postSnap.ToolCalls).To(gomega.Equal(preSnap.ToolCalls),
					"session tool_calls on pod-B after restart = %d, want %d (Redis persisted pod-A's content)",
					postSnap.ToolCalls, preSnap.ToolCalls)

				ginkgo.By("posting a follow-up prompt on pod-B → succeeds (Reopen issue #51)")
				// pod-B reopens the completed session (Reopen → idle) and drives a new
				// run. pod-A released the lease on shutdown, so pod-B acquires it.
				resumeCtx, resumeCancel := shortCtx(60 * time.Second)
				resumeStatus, drainErr := drainRun(resumeCtx, addrB, sessionID, "resume after restart")
				resumeCancel()
				gomega.Expect(drainErr).NotTo(gomega.HaveOccurred(), "drain pod-B resumed run")
				gomega.Expect(resumeStatus).To(gomega.Equal(http.StatusOK),
					"pod-B follow-up prompt after pod-A restart = HTTP %d, want 200 (Reopen + lease acquire)",
					resumeStatus)
			})
	})
}
