//go:build e2e

package e2e_test

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// snapshotFidelitySpecs is the cloud-native Phase 1 LIVE scenario: a session's
// persisted selector AND its cumulative token spend survive a REAL SIGKILL +
// restart over a shared store, so the loop-level token budget continues across
// the process death.
//
// WHAT IT PROVES: drive one short turn on local #1 that consumes PART of a small
// --max-run-tokens budget but completes cleanly (the budget is sized BELOW that
// one turn's real spend, so after turn 1 the persisted cumulative usage already
// exceeds the ceiling — yet turn 1 itself completes because the boundary check
// runs BEFORE each turn, and turn 1's only boundary saw zero spend). SIGKILL #1,
// restart #2 sharing the store, resume the session and drive turn 2: the FIRST
// turn-boundary check on #2 reads the REHYDRATED session's cumulative usage
// (already over the ceiling) and ends the run with stop=budget BEFORE any model
// call — the StopBudget oracle.
//
// WHY LIVE, NOT OFFLINE: the offline two-Build gate
// (internal/app TestSelectorSessionSurvivesRestartE2E) feeds the budget with
// mockllm usage CHUNKS — synthetic token counts. This spec proves the REAL
// provider's token accounting (the live adapter's usage frames) flows into the
// persisted session.Usage, that Usage survives a real process death on the JSONL
// store, and that the rehydrated per-session engine honours the SAME budget
// against the SAME persisted spend. A fresh-budget process (no persisted usage)
// would see zero cumulative at turn 2's boundary and NOT trip — so the StopBudget
// terminal on #2 is the persisted-usage signal that only a live cross-process run
// can confirm.
//
// LANE: hard-pinned to haikuLane (see restart_helpers_test.go) — the lane is
// fixed here so the per-turn spend (and therefore the budget sizing) tracks a
// known tool-calling model, not an env-overridable one.
func snapshotFidelitySpecs() {
	ginkgo.Describe("snapshot fidelity (cloud-native Phase 1)", func() {
		ginkgo.It("continues the run token budget across a SIGKILL+restart on the persisted selector",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				// Local-only by construction: a remote target cannot be SIGKILLed +
				// restarted by the harness (it owns its own process pair, like
				// approve-after-kill).
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot SIGKILL + restart the server process")
				}

				// BUDGET SIZING (the StopBudget oracle, deterministic): a single turn's
				// input with the full catalog on this lane is ~5-6k tokens (verified —
				// see the harness --max-run-tokens note in local.go). A 3000-token
				// ceiling is comfortably BELOW one turn's spend yet > 0, so:
				//   - Turn 1 (boundary at cumulative=0 < 3000) PROCEEDS and completes
				//     cleanly; afterwards the persisted cumulative usage is ~5-6k >= 3000.
				//   - Turn 2 on the restarted #2 (boundary at the rehydrated cumulative
				//     ~5-6k >= 3000) trips StopBudget BEFORE any model call.
				// The margin (3000 vs ~5500) absorbs live token-count jitter; if a future
				// catalog shrinks turn-1 spend below the ceiling the spec fails loudly at
				// the turn-2 stop assertion rather than flaking silently.
				const budgetTokens = "3000"

				// --- Local #1: one short turn UNDER the budget, then SIGKILL. ---
				// The small budget is appended last, so it WINS over the harness default
				// (Go's flag pkg: last wins — see NewLocalWith / local.go).
				local1, err := harness.NewLocalWith("--max-run-tokens", budgetTokens)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #1")
				killed := false
				defer func() {
					if !killed {
						_ = local1.Close()
					}
				}()

				cli1 := local1.Client()
				// Explicit haiku selector: the session persists this selector, and the
				// restart leg must rebuild the SAME engine from it (rehydrateSession).
				sessionID, _, resolved1, err := cli1.CreateSession(ctx,
					client.ModeFromString("default"),
					client.ModelSelection{ProviderID: harness.ProviderID, ModelID: haikuLane})
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create session on local #1")
				// Confirm the selector pinned haiku from turn zero (the resolved model is
				// the composition single source — Service.ResolvedModel). The cross-restart
				// same-model guarantee then rides the StopBudget oracle below: a default-
				// provider fallback would NOT carry this session's persisted spend, so a
				// continued budget is itself evidence the persisted selector rebuilt.
				gomega.Expect(resolved1.ModelID).To(gomega.Equal(haikuLane),
					"local #1 did not resolve the pinned haiku selector\n--- mecated log tail ---\n"+local1.LogTail(4096))

				stream1, err := cli1.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on local #1")

				// A one-word, no-tool reply: cheap, and its single turn boundary (before
				// the turn, at cumulative=0) is under the budget, so it completes.
				const turn1Prompt = "Reply with exactly the single word: ok. Do not call any tools."
				gomega.Expect(stream1.SendPrompt(sessionID, turn1Prompt, nil)).To(gomega.Succeed(), "send turn 1 on local #1")

				res1, ok := driveToResult(ctx, stream1, 90*time.Second)
				gomega.Expect(ok).To(gomega.BeTrue(),
					"turn 1 on local #1 never reached a terminal result\n--- mecated log tail ---\n"+local1.LogTail(4096))
				// Turn 1 completed cleanly (NOT budget) and recorded REAL provider usage —
				// that real spend is what must persist and carry the budget across the
				// restart. (end_turn is the benign clean terminal for a no-tool turn.)
				gomega.Expect(res1.Stop).To(gomega.Equal("end_turn"),
					"turn 1 should complete cleanly under the budget, not trip it\n--- mecated log tail ---\n"+local1.LogTail(4096))
				gomega.Expect(res1.Usage.InputTokens).To(gomega.BeNumerically(">", 0),
					"turn 1 recorded no real input usage to persist\n--- mecated log tail ---\n"+local1.LogTail(4096))

				// SIGKILL local #1 WITHOUT cleanup: the persisted session (with turn 1's
				// cumulative usage + the haiku selector) is the last durable write.
				gomega.Expect(local1.Kill()).To(gomega.Succeed(), "SIGKILL local #1")
				killed = true

				// --- Local #2: restart over the SAME store with the SAME small budget. ---
				// The budget is a per-PROCESS flag, so #2 must be spawned with it too; the
				// persisted spend it reads from the shared store is what trips it.
				local2, err := harness.NewLocalSharingStore(local1, "--max-run-tokens", budgetTokens)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #2 sharing local #1 store")
				defer func() { _ = local2.Close() }()

				cli2 := local2.Client()
				stream2, err := cli2.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on local #2")

				// Resume the SAME session id and drive turn 2. StartRunContent routes a
				// prompt for a not-in-this-process session through loadAndReopen ->
				// rehydrateSession, rebuilding the per-session engine from the persisted
				// selector and reloading the session WITH its prior cumulative usage. The
				// turn-2 boundary check then sees that persisted spend.
				const turn2Prompt = "Reply with exactly the single word: ok. Do not call any tools."
				gomega.Expect(stream2.SendPrompt(sessionID, turn2Prompt, nil)).To(gomega.Succeed(), "resume + send turn 2 on local #2")

				res2, ok := driveToResult(ctx, stream2, 90*time.Second)
				gomega.Expect(ok).To(gomega.BeTrue(),
					"turn 2 on local #2 never reached a terminal result\n--- mecated log tail ---\n"+local2.LogTail(4096))

				// THE ORACLE: the resumed run ends with stop=budget. This holds ONLY if
				// the rehydrated session carried turn 1's persisted cumulative usage (the
				// boundary check reads sess.Usage, not the per-run delta) over the real
				// process death. A fresh-budget process would start turn 2 at zero
				// cumulative and end end_turn instead — so stop=budget is the
				// persisted-usage + same-engine-rebuilt signal in one event-layer
				// assertion (never model prose).
				gomega.Expect(res2.Stop).To(gomega.Equal("budget"),
					"the resumed run did not trip the persisted token budget (stop=budget); "+
						"the cumulative usage did not survive the restart, or the budget did not continue\n"+
						"--- mecated log tail ---\n"+local2.LogTail(4096))
			})
	})
}
