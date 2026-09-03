//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// leaseExclusionSpecs is the cloud-native Phase 4 LIVE scenario: cross-process
// single-writer enforcement via a SHARED single-host flock session lease. TWO
// real mecated processes share one --store-dir AND one --session-lease-dir
// (modelling two replicas over one store). Replica A drives a session to a parked
// Write ask, so it HOLDS the session lease. Replica B, live and sharing the same
// store+lease dir, tries to start a run on the SAME session over HTTP and is
// REFUSED with 409 Conflict (ErrSessionLeasedElsewhere) while A holds the lease.
// After A is SIGKILLed, the generation flock auto-releases (the single-host
// crash-recovery property), and B resumes the parked run through the approval API.
//
// It is the live counterpart of the offline two-Build gate
// TestCrossProcessLeaseExclusion (internal/app/), which exercises the identical
// acquire-at-run-entry path through the composition (Build #1 holds → Build #2
// StartRun → ErrSessionLeasedElsewhere → release → success). The two-Build gate
// stays the CI-green proof; THIS spec adds what only two real OS processes over a
// shared flock can confirm: a flock held by one process genuinely blocks a SECOND
// live process, and the flock auto-releases on the holder's abrupt death so the
// survivor takes over.
//
// LANE: HARD-PINNED to the haiku lane (the shared haikuLane constant in
// restart_helpers_test.go) — this scenario REQUIRES a real Write tool call to
// raise the ask that parks the run and holds the lease, and the OpenAI-family lane
// content-filters tool-bearing mecatl-shaped requests (finding F2; e2e/README.md).
func leaseExclusionSpecs() {
	ginkgo.Describe("cross-process session leasing (cloud-native Phase 4)", func() {
		ginkgo.It("refuses a second live replica's run while the lease is held, and lets it take over after the holder dies",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn + SIGKILL a second local process")
				}

				// --- Replica A: spawn, then respawn with a flock session lease. ---
				// NewLocalWith creates the state tree (incl. the lease dir); we read the
				// lease-dir path from it, close it, and respawn A AND B both pointing
				// --session-lease-dir at that SAME shared dir so the flock is genuinely
				// contended across the two processes.
				bootstrap, err := harness.NewLocalWith()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn replica A (bootstrap for the shared lease dir)")
				leaseDir := bootstrap.StateDir(harness.StateLease)
				_ = bootstrap.Close()

				const leaseTTL = 5 * time.Minute
				localA, err := harness.NewLocalSharingStore(bootstrap,
					"--session-lease-dir", leaseDir, "--session-lease-ttl", leaseTTL.String())
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn replica A with the shared lease dir")
				killedA := false
				defer func() {
					if !killedA {
						_ = localA.Close()
					}
				}()

				cliA := localA.Client()
				sessionID, _, _, err := cliA.CreateSession(ctx,
					client.ModeFromString("default"),
					client.ModelSelection{ProviderID: harness.ProviderID, ModelID: haikuLane})
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create session on replica A")

				streamA, err := cliA.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on replica A")

				const prompt = `Use the Write tool to create a file named note.txt in the workspace ` +
					`with exactly this content and nothing else: leased. Call no other tool.`
				gomega.Expect(streamA.SendPrompt(sessionID, prompt, nil)).To(gomega.Succeed(), "send prompt on replica A")

				// Drive to the Write ask: the run parks AWAITING, so replica A holds the
				// session lease for the duration. DO NOT approve.
				askID, _, driveErr := driveToWriteAsk(ctx, streamA, 90*time.Second)
				gomega.Expect(driveErr).NotTo(gomega.HaveOccurred(),
					"drive replica A to the Write permission ask\n--- mecated log tail ---\n"+localA.LogTail(4096))
				expectNonEmpty(askID, "a Write permission ask on replica A", localA.LogTail(4096))

				// --- Replica B: a SECOND live mecated sharing A's store + lease dir. ---
				// (Two live processes over one store is normally a write-race; the lease
				// is exactly what makes it safe — B is refused, never writes.)
				localB, err := harness.NewLocalSharingStore(localA,
					"--session-lease-dir", leaseDir, "--session-lease-ttl", leaseTTL.String())
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn replica B sharing A's store + lease")
				defer func() { _ = localB.Close() }()

				// B tries to start a run on the SAME session while A holds the lease:
				// 409 Conflict (ErrSessionLeasedElsewhere).
				probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				status, body, err := harness.PromptOverHTTP(probeCtx, localB.HTTPAddr(), string(sessionID), "second writer")
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "B run-start probe over HTTP")
				gomega.Expect(status).To(gomega.Equal(http.StatusConflict),
					"replica B run-start while A holds the lease = HTTP %d, want 409 Conflict\n--- body ---\n%s\n--- B log ---\n%s",
					status, string(body), localB.LogTail(4096))
				gomega.Expect(strings.Contains(string(body), "leased by another process")).To(gomega.BeTrue(),
					"409 was not lease-specific\n--- body ---\n%s", string(body))

				// --- Kill A: the generation flock auto-releases on process death. ---
				killedAt := time.Now()
				gomega.Expect(localA.Kill()).To(gomega.Succeed(), "SIGKILL replica A")
				killedA = true

				// B can now take over the parked session through the restart-safe approval
				// path. Denial avoids mutating the workspace; a successful SSE drain proves
				// the lease was acquired and the resumed run reached terminal state.
				gomega.Eventually(func() bool {
					takeoverCtx, c := context.WithTimeout(ctx, 30*time.Second)
					defer c()
					stream, aerr := harness.ApproveOverHTTP(takeoverCtx, localB.HTTPAddr(), string(sessionID), askID, "deny")
					if aerr != nil {
						return false
					}
					_, derr := harness.DrainSSE(stream)
					return derr == nil
				}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue(),
					"replica B could not resume the parked run after A died (generation flock should auto-release on death)\n--- B log ---\n"+localB.LogTail(4096))
				gomega.Expect(time.Since(killedAt)).To(gomega.BeNumerically("<", leaseTTL/4),
					"crash takeover did not occur materially before the configured lease TTL %s", leaseTTL)
			})
	})
}
