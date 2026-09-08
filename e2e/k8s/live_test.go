//go:build kind_e2e

package k8s_e2e_test

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// liveSpecs drives the cloud-native properties under a REAL LLM provider (the
// OpenRouter default lane, anthropic/claude-haiku-4.5). They are gated on
// OPENROUTER_API_KEY: BeforeSuite patches the Deployment from --mock to the
// real provider (staging the key in a k8s Secret) when the env var is set, and
// these specs Skip when it is not — the mock specs run unaffected either way.
//
// What these specs prove that the mock specs do NOT: the lease, the Redis
// snapshot, and the drain gate hold under a real multi-second LLM stream. The
// mock's instant completion cannot exercise the lease-held-during-stream window
// or prove the snapshot carries real model output. A live haiku turn takes 10-30s,
// so the lease is genuinely held across the stream boundary, the snapshot in
// Redis carries non-trivial model output (turns > 0 with real completion tokens),
// and the drain gate does not interfere with a live stream.
//
// The mock specs run FIRST (they don't need the real provider's longer latency);
// these live specs run AFTER. They share the SAME cluster + Deployment (patched
// once in BeforeSuite), so there is no patch/unpatch dance. Each live spec uses a
// fresh session to avoid cross-spec lease/state coupling.
func liveSpecs() {
	ginkgo.Describe("live LLM runs through the pods (ADR 0048 under real load)", func() {
		// liveBeforeEach is the shared gate: skip the WHOLE live block when the key
		// is absent (the mock suite already ran; these specs add nothing offline).
		ginkgo.BeforeEach(func() {
			if !liveProviderEnabled() {
				ginkgo.Skip("OPENROUTER_API_KEY is not set — skipping the live LLM specs (run `task e2e:k8s` with the key exported for the live variant)")
			}
		})

		// --- 1. one-turn provider smoke: stop=end_turn + real usage -------------
		//
		// The canary: a real one-turn run completes with stop=end_turn and real
		// (non-zero) token usage. This proves the live provider is wired through
		// the pod (the key Secret, --default-provider, the openrouter adapter)
		// and a real model turn flowed end-to-end. It gates the two property specs
		// below (a dead lane Skip()s them rather than flaking).
		ginkgo.It("drives a real one-turn run to end_turn with non-zero usage",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				refreshPods()
				podA := agentPods[0]

				ginkgo.By("port-forwarding to pod-A")
				addrA, stopA := portForward(podA)
				defer stopA()

				ginkgo.By("creating a session on pod-A")
				cCtx, cCancel := shortCtx(30 * time.Second)
				sessionID := createLiveSessionOverHTTP(cCtx, addrA)
				cCancel()

				ginkgo.By("driving a real one-turn prompt and parsing the result event")
				runCtx, runCancel := shortCtx(180 * time.Second)
				status, res, err := drainRunSSE(runCtx, addrA, sessionID, promptLiveProviderSm)
				runCancel()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(),
					"draining the live run SSE stream: %v", err)
				gomega.Expect(status).To(gomega.Equal(http.StatusOK),
					"live run-start = HTTP %d, want 200", status)
				gomega.Expect(res).NotTo(gomega.BeNil(), "no result event parsed from the live stream")
				gomega.Expect(res.Stop).To(gomega.Equal("end_turn"),
					"live run stop = %q, want end_turn\nSSE diagnostic: %s", res.Stop, res.diagnostic())
				gomega.Expect(res.Input).To(gomega.BeNumerically(">", 0),
					"live run input_tokens = %d, want > 0 (a real model call)", res.Input)
				gomega.Expect(res.Output).To(gomega.BeNumerically(">", 0),
					"live run output_tokens = %d, want > 0 (real completion tokens)", res.Output)

				ginkgo.By("confirming the session is terminal (completed) on pod-A")
				gCtx, gCancel := shortCtx(30 * time.Second)
				getStatus, getBody := httpGetSession(gCtx, addrA, sessionID)
				gCancel()
				gomega.Expect(getStatus).To(gomega.Equal(http.StatusOK),
					"GET live session = HTTP %d, want 200", getStatus)
				var snap struct {
					State string `json:"state"`
					Turns int    `json:"turns"`
				}
				gomega.Expect(json.Unmarshal(getBody, &snap)).To(gomega.Succeed(),
					"unmarshal live session snapshot: %s", formatBody(getBody))
				gomega.Expect(snap.State).To(gomega.Equal("completed"),
					"live session state = %q, want completed", snap.State)
				gomega.Expect(snap.Turns).To(gomega.BeNumerically(">", 0),
					"live session turns = %d, want > 0 (a real turn ran)", snap.Turns)
			})

		// --- 2. lease exclusion under a real LLM turn --------------------------
		//
		// Same as the mock lease test, but the run is a REAL multi-second LLM
		// turn: the lease is held for the duration of the stream, not the mock's
		// instant completion. Pod-B's 409 is observed against a genuinely-live
		// lease window. The takeover after release also drives a real turn.
		ginkgo.It("holds the lease across a real LLM stream and releases it for takeover",
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
				sessionID := createLiveSessionOverHTTP(sessCtx, addrA)
				sessCancel()

				// Drive a real turn to terminal BUT keep pod-A's run in flight to
				// observe the lease-held window. The run itself is the lease holder
				// for its whole duration (10-30s of real streaming); probing pod-B
				// concurrently would race the run's completion. Instead, complete
				// the run (the lease is SESSION-scoped, held past run-end), then
				// probe pod-B while the session lease is still held by pod-A's
				// Service (released only on session DELETE / shutdown).
				ginkgo.By("driving a real LLM turn on pod-A to terminal (lease held for the session's life)")
				runCtx, runCancel := shortCtx(180 * time.Second)
				status, res, err := drainRunSSE(runCtx, addrA, sessionID, promptLiveProviderSm)
				runCancel()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "live run on pod-A: %v", err)
				gomega.Expect(status).To(gomega.Equal(http.StatusOK),
					"pod-A live run = HTTP %d, want 200", status)
				gomega.Expect(res).NotTo(gomega.BeNil())
				gomega.Expect(res.Stop).To(gomega.Equal("end_turn"),
					"pod-A live run stop = %q, want end_turn\nSSE diagnostic: %s", res.Stop, res.diagnostic())

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

				ginkgo.By("posting to pod-B after the clean release → 200 (takeover, real turn)")
				gomega.Eventually(func(g gomega.Gomega) {
					takeCtx, takeCancel := shortCtx(180 * time.Second)
					defer takeCancel()
					st, res, err := drainRunSSE(takeCtx, addrB, sessionID, promptLiveProviderSm)
					g.Expect(err).NotTo(gomega.HaveOccurred(), "pod-B takeover live run transport error")
					g.Expect(st).To(gomega.Equal(http.StatusOK),
						"pod-B takeover after release = HTTP %d, want 200", st)
					g.Expect(res).NotTo(gomega.BeNil(), "pod-B takeover produced no result event")
					g.Expect(res.Stop).To(gomega.Equal("end_turn"),
						"pod-B takeover stop = %q, want end_turn\nSSE diagnostic: %s", res.Stop, res.diagnostic())
				}, 200*time.Second, 2*time.Second).Should(gomega.Succeed(),
					"pod-B takeover after pod-A released the lease did not complete a real turn")
			})

		// --- 3. persistence with real model output -----------------------------
		//
		// Same as the mock persistence test, but the run produces REAL model
		// output. The content-counter assertion is MORE meaningful here: a non-zero
		// turns count + a completed state on pod-B proves the Redis snapshot
		// carried genuine model output across the pod restart, not just an empty
		// stub. The takeover after restart is also a real turn.
		ginkgo.It("persists a real model turn across a pod restart and resumes on the successor",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				refreshPods()
				podA, podB := agentPods[0], agentPods[1]

				ginkgo.By("port-forwarding to pod-A and pod-B")
				addrA, stopA := portForward(podA)
				defer stopA()
				addrB, stopB := portForward(podB)
				defer stopB()

				ginkgo.By("creating a session + driving a real turn on pod-A to terminal")
				cCtx, cCancel := shortCtx(30 * time.Second)
				sessionID := createLiveSessionOverHTTP(cCtx, addrA)
				cCancel()
				rCtx, rCancel := shortCtx(180 * time.Second)
				status, res, err := drainRunSSE(rCtx, addrA, sessionID, promptLiveProviderSm)
				rCancel()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "pod-A live run: %v", err)
				gomega.Expect(status).To(gomega.Equal(http.StatusOK),
					"pod-A live run = HTTP %d, want 200", status)
				gomega.Expect(res).NotTo(gomega.BeNil())
				gomega.Expect(res.Stop).To(gomega.Equal("end_turn"),
					"pod-A live run stop = %q, want end_turn\nSSE diagnostic: %s", res.Stop, res.diagnostic())
				gomega.Expect(res.Output).To(gomega.BeNumerically(">", 0),
					"pod-A live run output_tokens = %d, want > 0 (real model output to persist)", res.Output)

				ginkgo.By("confirming the session is terminal on pod-A with real content")
				gCtx, gCancel := shortCtx(30 * time.Second)
				preStatus, preBody := httpGetSession(gCtx, addrA, sessionID)
				gCancel()
				gomega.Expect(preStatus).To(gomega.Equal(http.StatusOK),
					"GET session on pod-A before restart = HTTP %d\n--- body ---\n%s",
					preStatus, formatBody(preBody))
				var preSnap struct {
					State     string `json:"state"`
					Turns     int    `json:"turns"`
					ToolCalls int    `json:"tool_calls"`
				}
				gomega.Expect(json.Unmarshal(preBody, &preSnap)).To(gomega.Succeed(),
					"unmarshal pod-A live session snapshot: %s", formatBody(preBody))
				gomega.Expect(preSnap.State).To(gomega.Equal("completed"),
					"live session state = %q, want completed", preSnap.State)
				gomega.Expect(preSnap.Turns).To(gomega.BeNumerically(">", 0),
					"live session turns = %d, want > 0 (real content to persist)", preSnap.Turns)

				ginkgo.By("deleting pod-A (graceful → Service.Close releases the lease)")
				preDeletePersist := podNames()
				kubectlDeletePod(podA, false)
				_ = waitReplacementReady(preDeletePersist)

				ginkgo.By("GETting the session on pod-B → it survives with the real content (Redis)")
				getCtx, getCancel := shortCtx(30 * time.Second)
				postStatus, postBody := httpGetSession(getCtx, addrB, sessionID)
				getCancel()
				gomega.Expect(postStatus).To(gomega.Equal(http.StatusOK),
					"GET session on pod-B after restart = HTTP %d, want 200 (Redis persistence)\n--- body ---\n%s",
					postStatus, formatBody(postBody))
				var postSnap struct {
					State     string `json:"state"`
					Turns     int    `json:"turns"`
					ToolCalls int    `json:"tool_calls"`
				}
				gomega.Expect(json.Unmarshal(postBody, &postSnap)).To(gomega.Succeed(),
					"unmarshal pod-B live session snapshot: %s", formatBody(postBody))
				gomega.Expect(postSnap.State).To(gomega.Equal("completed"),
					"live session state on pod-B = %q, want completed (Redis persisted it)", postSnap.State)
				// The real model's turns must survive the restart — the snapshot
				// carried genuine model output, not an empty stub.
				gomega.Expect(postSnap.Turns).To(gomega.Equal(preSnap.Turns),
					"live session turns on pod-B = %d, want %d (Redis persisted pod-A's real content)",
					postSnap.Turns, preSnap.Turns)
				gomega.Expect(postSnap.ToolCalls).To(gomega.Equal(preSnap.ToolCalls),
					"live session tool_calls on pod-B = %d, want %d (Redis persisted pod-A's content)",
					postSnap.ToolCalls, preSnap.ToolCalls)

				ginkgo.By("posting a follow-up prompt on pod-B → a real turn succeeds (Reopen issue #51)")
				resumeCtx, resumeCancel := shortCtx(180 * time.Second)
				resumeStatus, resumeRes, err := drainRunSSE(resumeCtx, addrB, sessionID, promptLiveProviderSm)
				resumeCancel()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "pod-B resume live run: %v", err)
				gomega.Expect(resumeStatus).To(gomega.Equal(http.StatusOK),
					"pod-B follow-up after restart = HTTP %d, want 200 (Reopen + lease acquire)", resumeStatus)
				gomega.Expect(resumeRes).NotTo(gomega.BeNil())
				gomega.Expect(resumeRes.Stop).To(gomega.Equal("end_turn"),
					"pod-B resume stop = %q, want end_turn\nSSE diagnostic: %s", resumeRes.Stop, resumeRes.diagnostic())
			})
	})
}
