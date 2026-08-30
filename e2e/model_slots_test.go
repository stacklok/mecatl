//go:build e2e

package e2e_test

import (
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
)

// modelSlotSpecs covers ADR 0030 (per-slot models, Phase 1+2) LIVE: a mecated
// spawned with `--model-slot compaction=cheap --model-alias cheap=<cheap-model>`
// and `--compaction cascade` must, on a real mid-run compaction, run the tier-4
// SUMMARY call on the SLOT model — not the session model. The offline twin lives in
// internal/app (TestCompactionSlotE2ESummaryModel, which reads the summary call's
// Model off a mock observer); THIS spec adds what only a live provider can show: the
// slot model actually serves the compaction summary against the real backend without
// wedging the call.
//
// OWN SPAWN: like the compaction spec it owns its OWN mecated (NOT the shared suite
// target) because it needs --context-window-override to force a small, cheap
// compaction window plus the slot flags. Local-only by construction.
//
// HOW THE ASSERTION WORKS. The harness cannot observe an internal compaction
// call's model on the wire, so the spec asserts the two OBSERVABLE facts that
// together prove the routing: (A) the build-once "model slot ACTIVE" INFO names the
// compaction slot AND the resolved cheap model (logSlotConfigFacts) — i.e. the slot
// was wired to the cheap model, not silently ignored; and (B) a real compaction
// FIRED (EvCompaction) and the run completed cleanly — i.e. the tier-4 summary call
// (which rides the slot model) actually ran against the live provider and did not
// wedge. A slot that failed to resolve would WARN+degrade (no ACTIVE line); a slot
// model the provider rejected would fail the compaction and surface an error.
func modelSlotSpecs() {
	ginkgo.Describe("model slots", func() {
		ginkgo.It("runs the compaction summary on the slot model across a live compaction",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(8*time.Minute),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with --model-slot / --context-window-override")
				}

				// The cheap slot model: a real, cheap model on the OpenRouter lane. The
				// session runs on DefaultModel(); the compaction summary must route here.
				cheap := envOrDefault("MECATL_E2E_SLOT_CHEAP_MODEL", "openai/gpt-4.1-mini")
				window := envOrDefault("MECATL_E2E_COMPACTION_WINDOW", "2000")

				spawn, err := harness.NewLocalWith(
					"--context-window-override", window,
					"--compaction", "cascade",
					"--model-alias", "cheap="+cheap,
					"--model-slot", "compaction=cheap",
					"--max-run-tokens", envOrDefault("MECATL_E2E_COMPACTION_MAX_RUN_TOKENS", "300000"),
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with --model-slot")
				defer func() { _ = spawn.Close() }()

				drv := harness.NewDriver(spawn)
				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(8192) }

				// (A) The build-once slot fact must name the compaction slot + cheap model.
				// It is emitted at startup, so it is already in the log by the time the
				// server is ready. (Assert on the resolved cheap id, the load-bearing value.)
				startLog := spawn.LogTail(8192)
				gomega.Expect(startLog).To(gomega.ContainSubstring("model slot ACTIVE"),
					"the build-once slot fact must narrate the active slot"+logTail())
				gomega.Expect(startLog).To(gomega.ContainSubstring(cheap),
					"the slot fact must name the resolved cheap model "+cheap+logTail())

				// (B) Drive enough deterministic padding turns to grow compactible history
				// past the complete-request threshold and yield a reducing tier-4 summary,
				// then assert a compaction fired and the runs ended cleanly — the tier-4
				// summary (slot model) ran live.
				var runs []*harness.RunResult
				runTurn := func(scenario, prompt string) *harness.RunResult {
					ginkgo.GinkgoHelper()
					var sessionID string
					if len(runs) > 0 {
						sessionID = runs[0].SessionID
					}
					res, runErr := drv.Run(ctx, harness.RunOpts{
						Scenario: scenario, Timeout: 90 * time.Second, SessionID: sessionID,
					}, prompt)
					gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "turn "+scenario+" transport error"+logTail())
					gomega.Expect(res.Result).NotTo(gomega.BeNil(), "turn "+scenario+" produced no terminal result"+logTail())
					runs = append(runs, res)
					return res
				}

				runTurn("slot-0-framing", `You are helping me. Reply with the single word ready.`)
				// Grow persisted history directly with deterministic user text. Depending on
				// repeated Read calls made this test model-behaviour-dependent: after the
				// first Read, a model can legitimately reuse the prior result. Seven turns
				// put several large messages outside the cascade's preserved tail.
				paddingPrompt := strings.Repeat("deterministic compaction padding ", 240) +
					"\nReply with the single word ok."
				for i := 1; i <= 7; i++ {
					runTurn("slot-padding-"+strconv.Itoa(i), paddingPrompt)
				}
				last := runTurn("slot-trigger", `Reply with the single word done.`)

				totalCompactions := 0
				for _, r := range runs {
					totalCompactions += len(r.Compactions())
				}
				gomega.Expect(totalCompactions).To(gomega.BeNumerically(">=", 1),
					"no EvCompaction observed — the slot's tier-4 summary call never ran; the assertion would be vacuous"+logTail())
				gomega.Expect(last.Stop()).To(gomega.Equal("end_turn"),
					"the final turn did not end cleanly (stop="+last.Stop()+") — the slot model may have rejected the compaction summary call"+logTail())
				gomega.Expect(strings.TrimSpace(last.Result.Error)).To(gomega.BeEmpty(),
					"a clean run carries no error"+logTail())
			})
	})
}
